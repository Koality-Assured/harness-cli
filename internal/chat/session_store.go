package chat

import (
	"bytes"
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Koality-Assured/harness-cli/internal/config"
	_ "modernc.org/sqlite"
)

const defaultBusyMode = "interrupt"

var busyModes = map[string]struct{}{"interrupt": {}, "queue": {}, "steer": {}}

// Session is a durable conversation and its provider configuration.
type Session struct {
	ID        string `json:"id"`
	Title     string `json:"title"`
	CWD       string `json:"cwd"`
	Provider  string `json:"provider"`
	Model     string `json:"model"`
	BusyMode  string `json:"busy_mode"`
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
}

// Message stores provider-native data separately from its display text.
type Message struct {
	ID           string `json:"id"`
	SessionID    string `json:"session_id"`
	Role         string `json:"role"`
	Content      string `json:"content"`
	InputTokens  *int64 `json:"input_tokens"`
	OutputTokens *int64 `json:"output_tokens"`
	ProviderData any    `json:"provider_data"`
	CreatedAt    string `json:"created_at"`
	SessionTitle string `json:"session_title,omitempty"`
}

// SessionStore is the SQLite persistence layer shared by chat and adapters.
type SessionStore struct {
	db *sql.DB
}

// DefaultStateDBPath returns ~/.harness/state.db or the configured override.
func DefaultStateDBPath() string {
	if path := os.Getenv("HARNESS_STATE_DB"); path != "" {
		return filepath.Clean(path)
	}
	return filepath.Join(config.GetConfigDir(), "state.db")
}

// OpenSessionStore opens the state database only when a session feature is used.
func OpenSessionStore(path string) (*SessionStore, error) {
	if path == "" {
		path = DefaultStateDBPath()
	}
	path = filepath.Clean(path)
	if path != ":memory:" {
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			return nil, fmt.Errorf("create session state directory: %w", err)
		}
		config.EnforcePrivateDirPermissions(filepath.Dir(path))
		f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
		if err != nil {
			return nil, fmt.Errorf("create session state database: %w", err)
		}
		if err := f.Close(); err != nil {
			return nil, err
		}
		config.EnforcePrivatePermissions(path)
	}
	dsn := path
	if path == ":memory:" {
		dsn = "file::memory:"
	}
	db, err := sql.Open("sqlite", dsn+"?_pragma=busy_timeout%3d5000")
	if err != nil {
		return nil, fmt.Errorf("open session state database: %w", err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	if err := db.Ping(); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("connect to session state database: %w", err)
	}
	store := &SessionStore{db: db}
	if err := store.initSchema(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return store, nil
}

func (s *SessionStore) Close() error { return s.db.Close() }

func (s *SessionStore) initSchema() error {
	ctx := context.Background()
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return fmt.Errorf("connect for session schema migration: %w", err)
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, "PRAGMA busy_timeout = 5000"); err != nil {
		return fmt.Errorf("configure session schema wait: %w", err)
	}
	if _, err := conn.ExecContext(ctx, "PRAGMA foreign_keys = ON"); err != nil {
		return fmt.Errorf("enable session foreign keys: %w", err)
	}
	if _, err := conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		return fmt.Errorf("lock session schema migration: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_, _ = conn.ExecContext(context.Background(), "ROLLBACK")
		}
	}()
	_, err = conn.ExecContext(ctx, `
CREATE TABLE IF NOT EXISTS sessions (
 id TEXT PRIMARY KEY,
 title TEXT NOT NULL DEFAULT '',
 cwd TEXT NOT NULL DEFAULT '',
 provider TEXT NOT NULL DEFAULT '',
 model TEXT NOT NULL DEFAULT '',
 busy_mode TEXT NOT NULL DEFAULT 'interrupt',
 created_at TEXT NOT NULL,
 updated_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS messages (
 id TEXT PRIMARY KEY,
 session_id TEXT NOT NULL,
 role TEXT NOT NULL,
 content TEXT NOT NULL,
 input_tokens INTEGER,
 output_tokens INTEGER,
 provider_data TEXT,
 created_at TEXT NOT NULL,
 FOREIGN KEY (session_id) REFERENCES sessions(id) ON DELETE CASCADE
);
CREATE INDEX IF NOT EXISTS idx_messages_session ON messages(session_id, created_at);
CREATE TABLE IF NOT EXISTS adapter_sessions (
 adapter TEXT NOT NULL,
 principal TEXT NOT NULL,
 route TEXT NOT NULL,
 external_id TEXT NOT NULL,
 session_id TEXT NOT NULL,
 created_at TEXT NOT NULL,
 updated_at TEXT NOT NULL,
 PRIMARY KEY (adapter, principal, route, external_id),
 FOREIGN KEY (session_id) REFERENCES sessions(id) ON DELETE CASCADE
);`)
	if err != nil {
		return fmt.Errorf("initialize session schema: %w", err)
	}
	for _, migration := range []struct{ table, column, declaration string }{
		{"sessions", "busy_mode", "TEXT NOT NULL DEFAULT 'interrupt'"},
		{"messages", "input_tokens", "INTEGER"},
		{"messages", "output_tokens", "INTEGER"},
		{"messages", "provider_data", "TEXT"},
	} {
		present, err := hasColumn(ctx, conn, migration.table, migration.column)
		if err != nil {
			return fmt.Errorf("inspect session schema: %w", err)
		}
		if !present {
			if _, err := conn.ExecContext(ctx, "ALTER TABLE "+migration.table+" ADD COLUMN "+migration.column+" "+migration.declaration); err != nil {
				return fmt.Errorf("migrate session schema: %w", err)
			}
		}
	}
	if _, err := conn.ExecContext(ctx, "COMMIT"); err != nil {
		return fmt.Errorf("commit session schema migration: %w", err)
	}
	committed = true
	return nil
}

func hasColumn(ctx context.Context, conn *sql.Conn, table, column string) (bool, error) {
	rows, err := conn.QueryContext(ctx, "PRAGMA table_info("+table+")")
	if err != nil {
		return false, err
	}
	defer rows.Close()
	for rows.Next() {
		var cid int
		var name, kind string
		var notNull, primaryKey int
		var defaultValue any
		if err := rows.Scan(&cid, &name, &kind, &notNull, &defaultValue, &primaryKey); err != nil {
			return false, err
		}
		if name == column {
			return true, nil
		}
	}
	return false, rows.Err()
}

func timestamp() string { return time.Now().UTC().Format(time.RFC3339Nano) }

func newID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	encoded := hex.EncodeToString(b[:])
	return encoded[:8] + "-" + encoded[8:12] + "-" + encoded[12:16] + "-" + encoded[16:20] + "-" + encoded[20:], nil
}

func validateBusyMode(mode string) error {
	if _, ok := busyModes[mode]; !ok {
		return fmt.Errorf("busy mode must be one of: interrupt, queue, steer")
	}
	return nil
}

func jsonValue(value any) (any, error) {
	if value == nil {
		return nil, nil
	}
	b, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("encode provider data: %w", err)
	}
	decoder := json.NewDecoder(bytes.NewReader(b))
	decoder.UseNumber()
	var decoded any
	if err := decoder.Decode(&decoded); err != nil {
		return nil, err
	}
	return decoded, nil
}

func (s *SessionStore) CreateSession(session Session) (Session, error) {
	if session.BusyMode == "" {
		session.BusyMode = defaultBusyMode
	}
	if err := validateBusyMode(session.BusyMode); err != nil {
		return Session{}, err
	}
	if session.ID == "" {
		id, err := newID()
		if err != nil {
			return Session{}, err
		}
		session.ID = id
	}
	now := timestamp()
	session.CreatedAt, session.UpdatedAt = now, now
	_, err := s.db.Exec(`INSERT INTO sessions (id,title,cwd,provider,model,busy_mode,created_at,updated_at)
VALUES (?,?,?,?,?,?,?,?)`, session.ID, session.Title, session.CWD, session.Provider, session.Model, session.BusyMode, now, now)
	if err != nil {
		return Session{}, err
	}
	return session, nil
}

func (s *SessionStore) CreateSessionWithMessages(session Session, messages []Message) (Session, error) {
	if session.BusyMode == "" {
		session.BusyMode = defaultBusyMode
	}
	if err := validateBusyMode(session.BusyMode); err != nil {
		return Session{}, err
	}
	if session.ID == "" {
		id, err := newID()
		if err != nil {
			return Session{}, err
		}
		session.ID = id
	}
	now := timestamp()
	session.CreatedAt, session.UpdatedAt = now, now
	tx, err := s.db.Begin()
	if err != nil {
		return Session{}, err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`INSERT INTO sessions (id,title,cwd,provider,model,busy_mode,created_at,updated_at)
VALUES (?,?,?,?,?,?,?,?)`, session.ID, session.Title, session.CWD, session.Provider, session.Model, session.BusyMode, now, now); err != nil {
		return Session{}, err
	}
	for _, message := range messages {
		if _, err := insertMessage(tx, session.ID, message); err != nil {
			return Session{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return Session{}, err
	}
	return session, nil
}

type messageInserter interface {
	Exec(string, ...any) (sql.Result, error)
}

func insertMessage(executor messageInserter, sessionID string, message Message) (Message, error) {
	if message.ID == "" {
		id, err := newID()
		if err != nil {
			return Message{}, err
		}
		message.ID = id
	}
	providerData, err := jsonValue(message.ProviderData)
	if err != nil {
		return Message{}, err
	}
	var raw any
	if providerData != nil {
		rawBytes, err := json.Marshal(providerData)
		if err != nil {
			return Message{}, err
		}
		raw = string(rawBytes)
	}
	message.SessionID, message.CreatedAt = sessionID, timestamp()
	_, err = executor.Exec(`INSERT INTO messages (id,session_id,role,content,input_tokens,output_tokens,provider_data,created_at)
VALUES (?,?,?,?,?,?,?,?)`, message.ID, sessionID, message.Role, message.Content, message.InputTokens, message.OutputTokens, raw, message.CreatedAt)
	return message, err
}

func (s *SessionStore) AppendMessage(sessionID string, message Message) (Message, error) {
	message, err := insertMessage(s.db, sessionID, message)
	if err != nil {
		return Message{}, err
	}
	if _, err := s.db.Exec("UPDATE sessions SET updated_at = ? WHERE id = ?", timestamp(), sessionID); err != nil {
		return Message{}, err
	}
	return message, nil
}

func (s *SessionStore) AppendMessages(sessionID string, messages []Message) ([]Message, error) {
	if len(messages) == 0 {
		return []Message{}, nil
	}
	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	inserted := make([]Message, 0, len(messages))
	for _, message := range messages {
		item, err := insertMessage(tx, sessionID, message)
		if err != nil {
			return nil, err
		}
		inserted = append(inserted, item)
	}
	if _, err := tx.Exec("UPDATE sessions SET updated_at = ? WHERE id = ?", timestamp(), sessionID); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return inserted, nil
}

func (s *SessionStore) GetSession(id string) (*Session, error) {
	var session Session
	err := s.db.QueryRow(`SELECT id,title,cwd,provider,model,busy_mode,created_at,updated_at FROM sessions WHERE id=?`, id).Scan(
		&session.ID, &session.Title, &session.CWD, &session.Provider, &session.Model, &session.BusyMode, &session.CreatedAt, &session.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &session, nil
}

func (s *SessionStore) ListMessages(sessionID string) ([]Message, error) {
	rows, err := s.db.Query(`SELECT id,session_id,role,content,input_tokens,output_tokens,provider_data,created_at
FROM messages WHERE session_id=? ORDER BY created_at ASC, rowid ASC`, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var messages []Message
	for rows.Next() {
		var message Message
		var raw sql.NullString
		if err := rows.Scan(&message.ID, &message.SessionID, &message.Role, &message.Content, &message.InputTokens, &message.OutputTokens, &raw, &message.CreatedAt); err != nil {
			return nil, err
		}
		if raw.Valid {
			decoder := json.NewDecoder(strings.NewReader(raw.String))
			decoder.UseNumber()
			if err := decoder.Decode(&message.ProviderData); err != nil {
				message.ProviderData = nil
			}
		}
		messages = append(messages, message)
	}
	return messages, rows.Err()
}

func (s *SessionStore) ListSessions(limit int) ([]Session, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := s.db.Query(`SELECT id,title,cwd,provider,model,busy_mode,created_at,updated_at
FROM sessions ORDER BY updated_at DESC, rowid DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	sessions := make([]Session, 0)
	for rows.Next() {
		var session Session
		if err := rows.Scan(&session.ID, &session.Title, &session.CWD, &session.Provider, &session.Model, &session.BusyMode, &session.CreatedAt, &session.UpdatedAt); err != nil {
			return nil, err
		}
		sessions = append(sessions, session)
	}
	return sessions, rows.Err()
}

func (s *SessionStore) RenameSession(id, title string) (*Session, error) {
	result, err := s.db.Exec("UPDATE sessions SET title=?,updated_at=? WHERE id=?", title, timestamp(), id)
	if err != nil {
		return nil, err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return nil, err
	}
	if count == 0 {
		return nil, nil
	}
	return s.GetSession(id)
}

func (s *SessionStore) TouchSession(id string, provider, model *string) error {
	current, err := s.GetSession(id)
	if err != nil || current == nil {
		return err
	}
	if provider != nil {
		current.Provider = *provider
	}
	if model != nil {
		current.Model = *model
	}
	_, err = s.db.Exec("UPDATE sessions SET provider=?,model=?,updated_at=? WHERE id=?", current.Provider, current.Model, timestamp(), id)
	return err
}

func (s *SessionStore) SetBusyMode(id, mode string) (*Session, error) {
	if err := validateBusyMode(mode); err != nil {
		return nil, err
	}
	result, err := s.db.Exec("UPDATE sessions SET busy_mode=?,updated_at=? WHERE id=?", mode, timestamp(), id)
	if err != nil {
		return nil, err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return nil, err
	}
	if count == 0 {
		return nil, nil
	}
	return s.GetSession(id)
}

func (s *SessionStore) LatestSession() (*Session, error) {
	sessions, err := s.ListSessions(1)
	if err != nil || len(sessions) == 0 {
		return nil, err
	}
	return &sessions[0], nil
}

func (s *SessionStore) SearchMessages(query string, limit int) ([]Message, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := s.db.Query(`SELECT m.id,m.session_id,m.role,m.content,m.input_tokens,m.output_tokens,m.provider_data,m.created_at,s.title
FROM messages m JOIN sessions s ON s.id=m.session_id WHERE m.content LIKE ? ORDER BY m.created_at DESC LIMIT ?`, "%"+query+"%", limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var messages []Message
	for rows.Next() {
		var message Message
		var raw sql.NullString
		if err := rows.Scan(&message.ID, &message.SessionID, &message.Role, &message.Content, &message.InputTokens, &message.OutputTokens, &raw, &message.CreatedAt, &message.SessionTitle); err != nil {
			return nil, err
		}
		if raw.Valid {
			decoder := json.NewDecoder(strings.NewReader(raw.String))
			decoder.UseNumber()
			if decoder.Decode(&message.ProviderData) != nil {
				message.ProviderData = nil
			}
		}
		messages = append(messages, message)
	}
	return messages, rows.Err()
}

func (s *SessionStore) GetAdapterSession(adapter, principal, route, externalID string) (*Session, error) {
	var session Session
	err := s.db.QueryRow(`SELECT s.id,s.title,s.cwd,s.provider,s.model,s.busy_mode,s.created_at,s.updated_at
FROM adapter_sessions a JOIN sessions s ON s.id=a.session_id
WHERE a.adapter=? AND a.principal=? AND a.route=? AND a.external_id=?`, adapter, principal, route, externalID).Scan(
		&session.ID, &session.Title, &session.CWD, &session.Provider, &session.Model, &session.BusyMode, &session.CreatedAt, &session.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &session, nil
}

func (s *SessionStore) BindAdapterSession(adapter, principal, route, externalID string, initial Session, messages []Message) (string, *Session, error) {
	if initial.BusyMode == "" {
		initial.BusyMode = defaultBusyMode
	}
	if err := validateBusyMode(initial.BusyMode); err != nil {
		return "", nil, err
	}
	if externalID == "" {
		id, err := newID()
		if err != nil {
			return "", nil, err
		}
		externalID = id
	}
	if len(externalID) > 256 || strings.ContainsAny(externalID, "\x00\n\r\t") {
		return "", nil, errors.New("adapter session handle is invalid")
	}
	current, err := s.GetAdapterSession(adapter, principal, route, externalID)
	if err != nil {
		return "", nil, err
	}
	if current != nil {
		return externalID, current, nil
	}
	tx, err := s.db.Begin()
	if err != nil {
		return "", nil, err
	}
	defer tx.Rollback()
	if initial.ID == "" {
		initial.ID, err = newID()
		if err != nil {
			return "", nil, err
		}
	}
	now := timestamp()
	initial.CreatedAt, initial.UpdatedAt = now, now
	if _, err := tx.Exec(`INSERT INTO sessions (id,title,cwd,provider,model,busy_mode,created_at,updated_at) VALUES (?,?,?,?,?,?,?,?)`, initial.ID, initial.Title, initial.CWD, initial.Provider, initial.Model, initial.BusyMode, now, now); err != nil {
		return "", nil, err
	}
	if _, err := tx.Exec(`INSERT INTO adapter_sessions (adapter,principal,route,external_id,session_id,created_at,updated_at) VALUES (?,?,?,?,?,?,?)`, adapter, principal, route, externalID, initial.ID, now, now); err != nil {
		_ = tx.Rollback()
		current, lookupErr := s.GetAdapterSession(adapter, principal, route, externalID)
		if lookupErr == nil && current != nil {
			return externalID, current, nil
		}
		return "", nil, err
	}
	for _, message := range messages {
		if _, err := insertMessage(tx, initial.ID, message); err != nil {
			return "", nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return "", nil, err
	}
	return externalID, &initial, nil
}

func (s *SessionStore) RebindAdapterSession(adapter, principal, route, externalID, sessionID string) (bool, error) {
	result, err := s.db.Exec(`UPDATE adapter_sessions SET session_id=?,updated_at=? WHERE adapter=? AND principal=? AND route=? AND external_id=?`, sessionID, timestamp(), adapter, principal, route, externalID)
	if err != nil {
		return false, err
	}
	count, err := result.RowsAffected()
	return count == 1, err
}

var _ io.Closer = (*SessionStore)(nil)
