package chat

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func openTestStore(t *testing.T) *SessionStore {
	t.Helper()
	store, err := OpenSessionStore(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func TestEmptySessionListIsAnEmptyArray(t *testing.T) {
	store := openTestStore(t)
	defer store.Close()
	sessions, err := store.ListSessions(10)
	if err != nil {
		t.Fatal(err)
	}
	if sessions == nil || len(sessions) != 0 {
		t.Fatalf("empty session list = %#v; want a non-nil empty slice", sessions)
	}
}

func TestMessageJSONKeepsNullProviderData(t *testing.T) {
	encoded, err := json.Marshal(Message{Role: "user", Content: "plain text"})
	if err != nil {
		t.Fatal(err)
	}
	var message map[string]any
	if err := json.Unmarshal(encoded, &message); err != nil {
		t.Fatal(err)
	}
	value, exists := message["provider_data"]
	if !exists || value != nil {
		t.Fatalf("provider_data JSON = %#v (present=%v); want explicit null", value, exists)
	}
}

func TestSessionStoreCRUDAndNativeData(t *testing.T) {
	store := openTestStore(t)
	session, err := store.CreateSession(Session{Title: "first", Provider: "anthropic", Model: "claude-test"})
	if err != nil {
		t.Fatal(err)
	}
	if session.ID == "" || session.BusyMode != "interrupt" {
		t.Fatalf("unexpected new session: %#v", session)
	}
	_, err = store.AppendMessages(session.ID, []Message{
		{Role: "user", Content: "use the tool"},
		{Role: "assistant", Content: "", InputTokens: intPtr(7), OutputTokens: intPtr(2), ProviderData: map[string]any{
			"provider": "anthropic",
			"message":  map[string]any{"role": "assistant", "content": []any{map[string]any{"type": "tool_use", "id": "call-1"}}},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	messages, err := store.ListMessages(session.ID)
	if err != nil || len(messages) != 2 {
		t.Fatalf("ListMessages = %#v, %v", messages, err)
	}
	providerData, ok := messages[1].ProviderData.(map[string]any)
	if !ok || providerData["provider"] != "anthropic" {
		t.Fatalf("provider-native history was not preserved: %#v", messages[1].ProviderData)
	}
	if messages[1].InputTokens == nil || *messages[1].InputTokens != 7 {
		t.Fatalf("input token count not persisted: %#v", messages[1].InputTokens)
	}
	status, err := store.SetBusyMode(session.ID, "steer")
	if err != nil || status == nil || status.BusyMode != "steer" {
		t.Fatalf("SetBusyMode = %#v, %v", status, err)
	}
	renamed, err := store.RenameSession(session.ID, "renamed")
	if err != nil || renamed == nil || renamed.Title != "renamed" {
		t.Fatalf("RenameSession = %#v, %v", renamed, err)
	}
	results, err := store.SearchMessages("tool", 10)
	if err != nil || len(results) != 1 || results[0].SessionTitle != "renamed" {
		t.Fatalf("SearchMessages = %#v, %v", results, err)
	}
	latest, err := store.LatestSession()
	if err != nil || latest == nil || latest.ID != session.ID {
		t.Fatalf("LatestSession = %#v, %v", latest, err)
	}
	if _, err := store.SetBusyMode(session.ID, "invalid"); err == nil {
		t.Fatal("invalid busy mode was accepted")
	}
}

func TestCreateSessionWithMessagesIsAtomic(t *testing.T) {
	store := openTestStore(t)
	_, err := store.CreateSessionWithMessages(Session{ID: "transaction-check"}, []Message{{
		Role: "assistant", Content: "invalid provider value", ProviderData: func() {},
	}})
	if err == nil {
		t.Fatal("non-JSON provider data was accepted")
	}
	session, err := store.GetSession("transaction-check")
	if err != nil || session != nil {
		t.Fatalf("failed transaction left a session behind: %#v, %v", session, err)
	}
}

func TestSessionStoreMigratesSlice8Schema(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	legacy, err := OpenSessionStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := legacy.Close(); err != nil {
		t.Fatal(err)
	}
	// Replace the current schema with the original Slice 8 columns.
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	legacyDB, err := openLegacySchema(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := legacyDB.Close(); err != nil {
		t.Fatal(err)
	}
	store, err := OpenSessionStore(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	session, err := store.GetSession("legacy")
	if err != nil || session == nil || session.BusyMode != "interrupt" {
		t.Fatalf("legacy session migration = %#v, %v", session, err)
	}
	messages, err := store.ListMessages("legacy")
	if err != nil || len(messages) != 1 || messages[0].InputTokens != nil {
		t.Fatalf("legacy messages migration = %#v, %v", messages, err)
	}
}

func TestConcurrentSessionStoreOpenersMigrateSlice8Schema(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	legacy, err := openLegacySchema(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := legacy.Close(); err != nil {
		t.Fatal(err)
	}

	const openers = 2
	ready := make(chan struct{}, openers)
	start := make(chan struct{})
	results := make(chan error, openers)
	for range openers {
		go func() {
			ready <- struct{}{}
			<-start
			store, err := OpenSessionStore(path)
			if err == nil {
				defer store.Close()
				var session *Session
				session, err = store.GetSession("legacy")
				if err == nil && (session == nil || session.BusyMode != defaultBusyMode) {
					err = fmt.Errorf("legacy session after concurrent migration = %#v", session)
				}
			}
			results <- err
		}()
	}
	for range openers {
		select {
		case <-ready:
		case <-time.After(2 * time.Second):
			t.Fatal("migration openers did not reach the start barrier")
		}
	}
	close(start)
	for range openers {
		select {
		case err := <-results:
			if err != nil {
				t.Fatalf("concurrent Slice 8 migration failed: %v", err)
			}
		case <-time.After(7 * time.Second):
			t.Fatal("concurrent Slice 8 migration did not finish")
		}
	}
}

func openLegacySchema(path string) (*sql.DB, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	_, err = db.Exec(`CREATE TABLE sessions (
id TEXT PRIMARY KEY,title TEXT NOT NULL DEFAULT '',cwd TEXT NOT NULL DEFAULT '',
provider TEXT NOT NULL DEFAULT '',model TEXT NOT NULL DEFAULT '',created_at TEXT NOT NULL,updated_at TEXT NOT NULL
);
CREATE TABLE messages (
id TEXT PRIMARY KEY,session_id TEXT NOT NULL,role TEXT NOT NULL,content TEXT NOT NULL,created_at TEXT NOT NULL,
FOREIGN KEY(session_id) REFERENCES sessions(id) ON DELETE CASCADE
);
INSERT INTO sessions (id,title,cwd,provider,model,created_at,updated_at) VALUES ('legacy','','','anthropic','claude-test','2026-01-01T00:00:00Z','2026-01-01T00:00:00Z');
INSERT INTO messages (id,session_id,role,content,created_at) VALUES ('legacy-message','legacy','user','saved text','2026-01-01T00:00:01Z');`)
	if err != nil {
		_ = db.Close()
		return nil, err
	}
	return db, nil
}

func TestAdapterSessionHandleIsStableAndScoped(t *testing.T) {
	store := openTestStore(t)
	handle, first, err := store.BindAdapterSession("gateway", "user-a", "/v1/chat/completions", "client-session-1", Session{Provider: "openai", Model: "gpt-test"}, []Message{{Role: "user", Content: "hello"}})
	if err != nil {
		t.Fatal(err)
	}
	handle2, second, err := store.BindAdapterSession("gateway", "user-a", "/v1/chat/completions", "client-session-1", Session{Provider: "gemini", Model: "ignored"}, nil)
	if err != nil || handle2 != handle || second.ID != first.ID || second.Provider != "openai" {
		t.Fatalf("existing handle did not resolve to the original session: %q %#v %v", handle2, second, err)
	}
	other, _, err := store.BindAdapterSession("gateway", "user-b", "/v1/chat/completions", "client-session-1", Session{Provider: "openai"}, nil)
	if err != nil || other != handle {
		t.Fatalf("stable external id should be accepted in a distinct principal scope: %q, %v", other, err)
	}
	loaded, err := store.GetAdapterSession("gateway", "user-a", "/v1/chat/completions", handle)
	if err != nil || loaded == nil || loaded.ID != first.ID {
		t.Fatalf("GetAdapterSession = %#v, %v", loaded, err)
	}
	if _, _, err := store.BindAdapterSession("gateway", "user-a", "/route", "bad\x00handle", Session{}, nil); err == nil {
		t.Fatal("control character in adapter handle was accepted")
	}
	newSession, err := store.CreateSession(Session{Provider: "anthropic"})
	if err != nil {
		t.Fatal(err)
	}
	updated, err := store.RebindAdapterSession("gateway", "user-a", "/v1/chat/completions", handle, newSession.ID)
	if err != nil || !updated {
		t.Fatalf("RebindAdapterSession = %v, %v", updated, err)
	}
	loaded, err = store.GetAdapterSession("gateway", "user-a", "/v1/chat/completions", handle)
	if err != nil || loaded == nil || loaded.ID != newSession.ID {
		t.Fatalf("adapter rebind did not persist: %#v, %v", loaded, err)
	}
}

func TestDefaultStateDBPathOverride(t *testing.T) {
	path := filepath.Join(t.TempDir(), "custom-state.db")
	t.Setenv("HARNESS_STATE_DB", path)
	if actual := DefaultStateDBPath(); actual != path {
		t.Fatalf("DefaultStateDBPath() = %q; want %q", actual, path)
	}
}

func intPtr(value int64) *int64 { return &value }
