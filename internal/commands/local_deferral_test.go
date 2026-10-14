package commands

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Koality-Assured/harness-cli/internal/chat"
	"github.com/spf13/cobra"
)

func TestLocalStatusOffline(t *testing.T) {
	t.Setenv("HARNESS_LOCAL_BASE_URL", "http://127.0.0.1:59999/v1")

	var out strings.Builder
	cmd := &cobra.Command{Use: "test"}
	cmd.SetOut(&out)

	oldJSON := JSONOutput
	JSONOutput = true
	defer func() { JSONOutput = oldJSON }()

	err := runLocalStatus(cmd, nil)
	if err == nil {
		t.Fatal("expected error when local server is offline, got nil")
	}

	var data map[string]any
	if err := json.Unmarshal([]byte(out.String()), &data); err != nil {
		t.Fatalf("failed to parse json output: %v, raw: %s", err, out.String())
	}

	if data["status"] != "offline" {
		t.Fatalf("expected status=offline, got %v", data["status"])
	}
	if data["base_url"] != "http://127.0.0.1:59999/v1" {
		t.Fatalf("expected base_url=http://127.0.0.1:59999/v1, got %v", data["base_url"])
	}
}

func TestLocalOnboardingJSON(t *testing.T) {
	t.Setenv("HARNESS_LOCAL_BASE_URL", "http://127.0.0.1:59999/v1")

	var out strings.Builder
	cmd := &cobra.Command{Use: "test"}
	cmd.SetOut(&out)

	oldJSON := JSONOutput
	JSONOutput = true
	defer func() { JSONOutput = oldJSON }()

	localApprove = true
	defer func() { localApprove = false }()

	if err := runLocalOnboarding(cmd, nil); err != nil {
		t.Fatalf("runLocalOnboarding failed: %v", err)
	}

	var data map[string]any
	if err := json.Unmarshal([]byte(out.String()), &data); err != nil {
		t.Fatalf("failed to parse json output: %v, raw: %s", err, out.String())
	}

	if data["action"] != "onboarding" {
		t.Fatalf("expected action=onboarding, got %v", data["action"])
	}
	if data["human_approved"] != true {
		t.Fatalf("expected human_approved=true, got %v", data["human_approved"])
	}
	hw, ok := data["hardware"].(map[string]any)
	if !ok || hw["tier"] == nil {
		t.Fatalf("expected hardware profile with tier, got %v", data["hardware"])
	}
}

func TestDeferralClassification(t *testing.T) {
	// 1. Operational task -> should not defer
	res1 := classifyDeferralEligibility("run pytest tests/test_harness.py", "antigravity")
	if res1.ShouldDefer {
		t.Fatalf("expected operational command not to defer, got %v", res1)
	}
	if res1.RecommendedModel != "Gemini 3.8 Flash" {
		t.Fatalf("expected Gemini 3.8 Flash, got %s", res1.RecommendedModel)
	}

	// 2. High-leverage concurrency task -> should defer to Claude
	res2 := classifyDeferralEligibility("resolve race condition in mutex hierarchy", "antigravity")
	if !res2.ShouldDefer {
		t.Fatalf("expected concurrency task to defer, got %v", res2)
	}
	if res2.Trigger == nil || *res2.Trigger != "state_and_concurrency" {
		t.Fatalf("expected state_and_concurrency trigger, got %v", res2.Trigger)
	}
	if res2.RecommendedModel != "Claude 5.5 Sonnet" {
		t.Fatalf("expected Claude 5.5 Sonnet, got %s", res2.RecommendedModel)
	}

	// 3. Non-antigravity host -> platform-native
	res3 := classifyDeferralEligibility("resolve race condition in mutex hierarchy", "cursor")
	if res3.ShouldDefer {
		t.Fatalf("expected non-antigravity not to defer, got %v", res3)
	}
	if res3.RecommendedModel != "platform-native" {
		t.Fatalf("expected platform-native, got %s", res3.RecommendedModel)
	}
}

func TestDeferralRecordAndClear429(t *testing.T) {
	tmpDir := t.TempDir()
	oldWD, _ := os.Getwd()
	_ = os.Chdir(tmpDir)
	defer func() { _ = os.Chdir(oldWD) }()

	// Record 429
	if err := recordSecondaryExhaustion(120); err != nil {
		t.Fatalf("recordSecondaryExhaustion failed: %v", err)
	}

	avail, rem := checkSecondaryAvailability()
	if avail {
		t.Fatal("expected secondary to be unavailable after recording 429")
	}
	if rem <= 0 || rem > 120 {
		t.Fatalf("expected remaining between 1 and 120, got %d", rem)
	}

	// Circuit-breaker should trigger fallback
	res := classifyDeferralEligibility("resolve race condition in mutex hierarchy", "antigravity")
	if res.ShouldDefer {
		t.Fatal("expected should_defer=false during 429 lockout")
	}
	if !res.FallbackActive {
		t.Fatal("expected fallback_active=true")
	}
	if res.RecommendedModel != "Gemini 3.8 Flash (High)" {
		t.Fatalf("expected Gemini 3.8 Flash (High), got %s", res.RecommendedModel)
	}

	// Clear 429
	if err := clearSecondaryExhaustion(); err != nil {
		t.Fatalf("clearSecondaryExhaustion failed: %v", err)
	}

	availAfter, _ := checkSecondaryAvailability()
	if !availAfter {
		t.Fatal("expected secondary to be available after clear")
	}
}

func TestChatPresetAndSystemPrompt(t *testing.T) {
	stateDB := filepath.Join(t.TempDir(), "state.db")
	t.Setenv("HARNESS_STATE_DB", stateDB)

	store, err := chat.OpenSessionStore(stateDB)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	sess, err := store.CreateSession(chat.Session{Title: "Preset Test", Provider: "local", Model: "qwen2.5-coder:14b"})
	if err != nil {
		t.Fatal(err)
	}

	// Test SetSystemPrompt with preset content
	presetContent := chatPresets["private_code"]
	if err := store.SetSystemPrompt(sess.ID, presetContent); err != nil {
		t.Fatal(err)
	}

	retrieved, ok, err := store.GetSystemPrompt(sess.ID)
	if err != nil || !ok {
		t.Fatalf("failed to retrieve system prompt: ok=%v, err=%v", ok, err)
	}
	if retrieved != presetContent {
		t.Fatalf("retrieved system prompt mismatch: %q != %q", retrieved, presetContent)
	}

	// Test clearing
	if err := store.SetSystemPrompt(sess.ID, ""); err != nil {
		t.Fatal(err)
	}
	_, ok2, _ := store.GetSystemPrompt(sess.ID)
	if ok2 {
		t.Fatal("expected system prompt to be cleared")
	}
}

func TestChatLocalProviderTurn(t *testing.T) {
	stateDB := filepath.Join(t.TempDir(), "state.db")
	t.Setenv("HARNESS_STATE_DB", stateDB)

	transport := &fakeChatTransport{
		response: "event: message\ndata: {\"choices\":[{\"delta\":{\"content\":\"local answer\"}}],\"usage\":{\"prompt_tokens\":5,\"completion_tokens\":2}}\n\nevent: message\ndata: [DONE]\n\n",
	}
	client := &chat.ProviderClient{HTTP: transport}

	var output strings.Builder
	command := &cobra.Command{Use: "chat-local-test"}
	command.SetOut(&output)

	options := chatOptionsValue{
		query:    "hello local",
		provider: "local",
		preset:   "general",
	}

	if err := runChat(command, options, client, nil); err != nil {
		t.Fatal(err)
	}

	if transport.calls != 1 {
		t.Fatalf("expected 1 call, got %d", transport.calls)
	}
	if !strings.HasPrefix(transport.endpoint, "http://127.0.0.1:11434/v1/chat/completions") {
		t.Fatalf("expected local completions endpoint, got %s", transport.endpoint)
	}
	if !strings.Contains(output.String(), "local answer") {
		t.Fatalf("expected local answer in output, got %s", output.String())
	}
}

type fakeHTTPDoerFunc func(*http.Request) (*http.Response, error)

func (f fakeHTTPDoerFunc) Do(req *http.Request) (*http.Response, error) {
	return f(req)
}

func (f *fakeChatTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	return f.Do(req)
}
