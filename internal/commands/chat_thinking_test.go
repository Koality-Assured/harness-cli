package commands

import (
	"encoding/json"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Koality-Assured/harness-cli/internal/chat"
	"github.com/spf13/cobra"
)

func TestThinkingSlashCommandGetAndSet(t *testing.T) {
	store, err := chat.OpenSessionStore(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	session, err := store.CreateSession(chat.Session{
		Title:    "Test Session",
		Provider: "anthropic",
		Model:    "claude-sonnet-4-5",
	})
	if err != nil {
		t.Fatal(err)
	}

	model := session.Model
	thinkingVal := ""
	effortVal := ""
	slashOpts := &SlashOptions{
		Thinking: &thinkingVal,
		Effort:   &effortVal,
	}

	// 1. Initial query: thinking=off
	var out, errOut strings.Builder
	cmd := &cobra.Command{}
	cmd.SetOut(&out)
	cmd.SetErr(&errOut)

	handled, exit := handleChatSlash(cmd, store, &session, &model, session.Provider, "key", nil, "/thinking", nil, slashOpts)
	if !handled || exit {
		t.Fatalf("expected handled=true, exit=false; got (%v, %v)", handled, exit)
	}
	if !strings.Contains(out.String(), "thinking=off") {
		t.Errorf("expected thinking=off, got %q", out.String())
	}

	// 2. Set integer budget
	out.Reset()
	handled, _ = handleChatSlash(cmd, store, &session, &model, session.Provider, "key", nil, "/thinking 4096", nil, slashOpts)
	if !handled || thinkingVal != "4096" || effortVal != "medium" {
		t.Errorf("thinkingVal = %q, effortVal = %q; want ('4096', 'medium')", thinkingVal, effortVal)
	}
	if !strings.Contains(out.String(), "thinking set to 4096") {
		t.Errorf("output = %q; want 'thinking set to 4096'", out.String())
	}

	// 3. Set effort level string
	out.Reset()
	handled, _ = handleChatSlash(cmd, store, &session, &model, session.Provider, "key", nil, "/thinking high", nil, slashOpts)
	if !handled || thinkingVal != "high" || effortVal != "high" {
		t.Errorf("thinkingVal = %q, effortVal = %q; want ('high', 'high')", thinkingVal, effortVal)
	}
	if !strings.Contains(out.String(), "thinking set to high") {
		t.Errorf("output = %q; want 'thinking set to high'", out.String())
	}

	// 4. Turn off
	out.Reset()
	handled, _ = handleChatSlash(cmd, store, &session, &model, session.Provider, "key", nil, "/thinking off", nil, slashOpts)
	if !handled || thinkingVal != "off" || effortVal != "off" {
		t.Errorf("thinkingVal = %q, effortVal = %q; want ('off', 'off')", thinkingVal, effortVal)
	}
	if !strings.Contains(out.String(), "thinking set to off") {
		t.Errorf("output = %q; want 'thinking set to off'", out.String())
	}

	// 5. Invalid argument
	errOut.Reset()
	handled, _ = handleChatSlash(cmd, store, &session, &model, session.Provider, "key", nil, "/thinking invalid", nil, slashOpts)
	if !handled || !strings.Contains(errOut.String(), "usage: /thinking off|low|medium|high|<budget>") {
		t.Errorf("errOut = %q; want usage", errOut.String())
	}
}

func TestEffortSlashCommandGetAndSet(t *testing.T) {
	store, err := chat.OpenSessionStore(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	session, err := store.CreateSession(chat.Session{
		Title:    "Test Session",
		Provider: "openai",
		Model:    "o3-mini",
	})
	if err != nil {
		t.Fatal(err)
	}

	model := session.Model
	thinkingVal := ""
	effortVal := ""
	slashOpts := &SlashOptions{
		Thinking: &thinkingVal,
		Effort:   &effortVal,
	}

	var out, errOut strings.Builder
	cmd := &cobra.Command{}
	cmd.SetOut(&out)
	cmd.SetErr(&errOut)

	// 1. Initial query: effort=(unset)
	handled, exit := handleChatSlash(cmd, store, &session, &model, session.Provider, "key", nil, "/effort", nil, slashOpts)
	if !handled || exit {
		t.Fatalf("expected handled=true, exit=false")
	}
	if !strings.Contains(out.String(), "effort=(unset)") {
		t.Errorf("expected effort=(unset), got %q", out.String())
	}

	// 2. Set low
	out.Reset()
	handled, _ = handleChatSlash(cmd, store, &session, &model, session.Provider, "key", nil, "/effort low", nil, slashOpts)
	if !handled || effortVal != "low" || thinkingVal != "2048" {
		t.Errorf("effortVal = %q, thinkingVal = %q; want ('low', '2048')", effortVal, thinkingVal)
	}
	if !strings.Contains(out.String(), "effort set to low") {
		t.Errorf("output = %q; want 'effort set to low'", out.String())
	}

	// 3. Turn off
	out.Reset()
	handled, _ = handleChatSlash(cmd, store, &session, &model, session.Provider, "key", nil, "/effort off", nil, slashOpts)
	if !handled || effortVal != "off" || thinkingVal != "off" {
		t.Errorf("effortVal = %q, thinkingVal = %q; want ('off', 'off')", effortVal, thinkingVal)
	}
	if !strings.Contains(out.String(), "effort set to off") {
		t.Errorf("output = %q; want 'effort set to off'", out.String())
	}

	// 4. Invalid
	errOut.Reset()
	handled, _ = handleChatSlash(cmd, store, &session, &model, session.Provider, "key", nil, "/effort invalid", nil, slashOpts)
	if !handled || !strings.Contains(errOut.String(), "usage: /effort low|medium|high|off") {
		t.Errorf("errOut = %q; want usage", errOut.String())
	}
}

func TestSlashThinkingAndEffortSynchronization(t *testing.T) {
	store, err := chat.OpenSessionStore(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	session, err := store.CreateSession(chat.Session{
		Title:    "Test Session",
		Provider: "anthropic",
		Model:    "claude-sonnet-4-5",
	})
	if err != nil {
		t.Fatal(err)
	}

	model := session.Model
	thinkingVal := ""
	effortVal := ""
	slashOpts := &SlashOptions{
		Thinking: &thinkingVal,
		Effort:   &effortVal,
	}

	var out strings.Builder
	cmd := &cobra.Command{}
	cmd.SetOut(&out)

	// 1. Setting budget via /thinking synchronizes effort
	handleChatSlash(cmd, store, &session, &model, session.Provider, "key", nil, "/thinking 4096", nil, slashOpts)
	if thinkingVal != "4096" || effortVal != "medium" {
		t.Errorf("thinkingVal = %q, effortVal = %q; want ('4096', 'medium')", thinkingVal, effortVal)
	}

	// 2. Setting /thinking off preserves off on both thinking and effort
	handleChatSlash(cmd, store, &session, &model, session.Provider, "key", nil, "/thinking off", nil, slashOpts)
	if thinkingVal != "off" || effortVal != "off" {
		t.Errorf("thinkingVal = %q, effortVal = %q; want ('off', 'off')", thinkingVal, effortVal)
	}

	// 3. Setting level via /effort synchronizes thinking budget
	handleChatSlash(cmd, store, &session, &model, session.Provider, "key", nil, "/effort low", nil, slashOpts)
	if effortVal != "low" || thinkingVal != "2048" {
		t.Errorf("effortVal = %q, thinkingVal = %q; want ('low', '2048')", effortVal, thinkingVal)
	}

	// 4. Setting /effort off sets both to off
	handleChatSlash(cmd, store, &session, &model, session.Provider, "key", nil, "/effort off", nil, slashOpts)
	if effortVal != "off" || thinkingVal != "off" {
		t.Errorf("effortVal = %q, thinkingVal = %q; want ('off', 'off')", effortVal, thinkingVal)
	}

	// 5. Setting named level via /thinking synchronizes effort
	handleChatSlash(cmd, store, &session, &model, session.Provider, "key", nil, "/thinking high", nil, slashOpts)
	if thinkingVal != "high" || effortVal != "high" {
		t.Errorf("thinkingVal = %q, effortVal = %q; want ('high', 'high')", thinkingVal, effortVal)
	}
}

func TestContextCommandDisplayAndAutoCompactConfig(t *testing.T) {
	store, err := chat.OpenSessionStore(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	session, err := store.CreateSession(chat.Session{
		Title:    "Context Session",
		Provider: "anthropic",
		Model:    "claude-sonnet-4-5",
	})
	if err != nil {
		t.Fatal(err)
	}

	inTokens := int64(1000)
	outTokens := int64(500)
	_, err = store.AppendMessage(session.ID, chat.Message{
		Role:         "assistant",
		Content:      "reply",
		InputTokens:  &inTokens,
		OutputTokens: &outTokens,
	})
	if err != nil {
		t.Fatal(err)
	}

	model := session.Model
	var autoCompactPct *float64
	slashOpts := &SlashOptions{
		AutoCompactPct: &autoCompactPct,
	}

	var out, errOut strings.Builder
	cmd := &cobra.Command{}
	cmd.SetOut(&out)
	cmd.SetErr(&errOut)

	// 1. Query context
	handled, _ := handleChatSlash(cmd, store, &session, &model, session.Provider, "key", nil, "/context", nil, slashOpts)
	if !handled {
		t.Fatal("expected handled=true")
	}
	output := out.String()
	if !strings.Contains(output, "active=1500 / 200000") || !strings.Contains(output, "cumulative: input=1000 output=500") || !strings.Contains(output, "cost=$") || !strings.Contains(output, "auto_compact=disabled") {
		t.Errorf("unexpected context output: %q", output)
	}

	// 2. Set auto-compact threshold
	out.Reset()
	handled, _ = handleChatSlash(cmd, store, &session, &model, session.Provider, "key", nil, "/context auto-compact 75", nil, slashOpts)
	if !handled || autoCompactPct == nil || *autoCompactPct != 75.0 {
		t.Fatalf("autoCompactPct = %v; want 75.0", autoCompactPct)
	}
	if !strings.Contains(out.String(), "auto-compact threshold set to 75.0%") {
		t.Errorf("output = %q; want 'auto-compact threshold set to 75.0%%'", out.String())
	}

	// 3. Query auto-compact setting
	out.Reset()
	handled, _ = handleChatSlash(cmd, store, &session, &model, session.Provider, "key", nil, "/context auto-compact", nil, slashOpts)
	if !handled || !strings.Contains(out.String(), "auto_compact=75.0%") {
		t.Errorf("output = %q; want 'auto_compact=75.0%%'", out.String())
	}

	// 4. Disable auto-compact
	out.Reset()
	handled, _ = handleChatSlash(cmd, store, &session, &model, session.Provider, "key", nil, "/context auto-compact off", nil, slashOpts)
	if !handled || autoCompactPct != nil {
		t.Fatalf("autoCompactPct = %v; want nil", autoCompactPct)
	}
	if !strings.Contains(out.String(), "auto-compact disabled") {
		t.Errorf("output = %q; want 'auto-compact disabled'", out.String())
	}
}

func TestContextAutoCompactArgumentValidation(t *testing.T) {
	store, err := chat.OpenSessionStore(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	session, err := store.CreateSession(chat.Session{
		Title:    "Validation Session",
		Provider: "anthropic",
		Model:    "claude-sonnet-4-5",
	})
	if err != nil {
		t.Fatal(err)
	}

	model := session.Model
	var autoCompactPct *float64
	slashOpts := &SlashOptions{
		AutoCompactPct: &autoCompactPct,
	}

	// 1. Percentage > 100 rejected
	var out, errOut strings.Builder
	cmd := &cobra.Command{}
	cmd.SetOut(&out)
	cmd.SetErr(&errOut)

	handleChatSlash(cmd, store, &session, &model, session.Provider, "key", nil, "/context auto-compact 150", nil, slashOpts)
	if !strings.Contains(errOut.String(), "usage: /context [auto-compact <pct>]") || autoCompactPct != nil {
		t.Errorf("expected usage error, got %q", errOut.String())
	}

	// 2. 0 disables auto-compact
	out.Reset()
	errOut.Reset()
	p := 80.0
	autoCompactPct = &p
	handleChatSlash(cmd, store, &session, &model, session.Provider, "key", nil, "/context auto-compact 0", nil, slashOpts)
	if !strings.Contains(out.String(), "auto-compact disabled") || autoCompactPct != nil {
		t.Errorf("expected disabled, got out=%q, pct=%v", out.String(), autoCompactPct)
	}

	// 3. 0.0 rejected
	out.Reset()
	errOut.Reset()
	handleChatSlash(cmd, store, &session, &model, session.Provider, "key", nil, "/context auto-compact 0.0", nil, slashOpts)
	if !strings.Contains(errOut.String(), "usage: /context [auto-compact <pct>]") {
		t.Errorf("expected usage error, got %q", errOut.String())
	}

	// 4. Negative rejected
	out.Reset()
	errOut.Reset()
	handleChatSlash(cmd, store, &session, &model, session.Provider, "key", nil, "/context auto-compact -25", nil, slashOpts)
	if !strings.Contains(errOut.String(), "usage: /context [auto-compact <pct>]") {
		t.Errorf("expected usage error, got %q", errOut.String())
	}

	// 5. Extra arguments rejected
	out.Reset()
	errOut.Reset()
	handleChatSlash(cmd, store, &session, &model, session.Provider, "key", nil, "/context auto-compact 80 extra", nil, slashOpts)
	if !strings.Contains(errOut.String(), "usage: /context [auto-compact <pct>]") {
		t.Errorf("expected usage error, got %q", errOut.String())
	}

	// 6. Non-numeric string rejected
	out.Reset()
	errOut.Reset()
	handleChatSlash(cmd, store, &session, &model, session.Provider, "key", nil, "/context auto-compact fast", nil, slashOpts)
	if !strings.Contains(errOut.String(), "usage: /context [auto-compact <pct>]") {
		t.Errorf("expected usage error, got %q", errOut.String())
	}
}

func TestStatusCommandEnrichedOutputAndThresholdWarning(t *testing.T) {
	store, err := chat.OpenSessionStore(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	session, err := store.CreateSession(chat.Session{
		Title:    "Status Session",
		Provider: "anthropic",
		Model:    "claude-sonnet-4-5",
	})
	if err != nil {
		t.Fatal(err)
	}

	// Append usage exceeding 50% of 200k (100,000 input, 10,000 output = 110,000 = 55.0%)
	inTokens := int64(100000)
	outTokens := int64(10000)
	_, err = store.AppendMessage(session.ID, chat.Message{
		Role:         "assistant",
		Content:      "long response",
		InputTokens:  &inTokens,
		OutputTokens: &outTokens,
	})
	if err != nil {
		t.Fatal(err)
	}

	threshold := 50.0
	autoCompactPct := &threshold
	slashOpts := &SlashOptions{
		AutoCompactPct: &autoCompactPct,
	}

	model := session.Model
	var out, errOut strings.Builder
	cmd := &cobra.Command{}
	cmd.SetOut(&out)
	cmd.SetErr(&errOut)

	handled, _ := handleChatSlash(cmd, store, &session, &model, session.Provider, "key", nil, "/status", nil, slashOpts)
	if !handled {
		t.Fatal("expected handled=true")
	}

	output := out.String()
	// Cost: 100,000 * $3.00/1M + 10,000 * $15.00/1M = $0.30 + $0.15 = $0.4500
	if !strings.Contains(output, "session=") || !strings.Contains(output, "provider=anthropic") || !strings.Contains(output, "cost=$0.4500") {
		t.Errorf("expected status format, got %q", output)
	}
	if !strings.Contains(output, "tokens: active=110000 / 200000 (55.0%) | cumulative: input=100000 output=10000 total=110000 cost=$0.4500") {
		t.Errorf("expected enriched tokens line, got %q", output)
	}
	if !strings.Contains(output, "warning: context usage (55.0%) reached auto-compact threshold (50.0%)") {
		t.Errorf("expected auto-compact threshold warning, got %q", output)
	}
}

func TestCliParserRecognizesThinkingAndEffort(t *testing.T) {
	resetChatFlags()
	defer resetChatFlags()

	if chatCmd.Flags().Lookup("thinking") == nil {
		t.Fatal("expected --thinking flag to be registered on chatCmd")
	}
	if chatCmd.Flags().Lookup("effort") == nil {
		t.Fatal("expected --effort flag to be registered on chatCmd")
	}

	if err := chatCmd.ParseFlags([]string{"--thinking", "4096", "--effort", "high"}); err != nil {
		t.Fatalf("ParseFlags error: %v", err)
	}
	if thinkingFlag != "4096" {
		t.Errorf("thinkingFlag = %q; want '4096'", thinkingFlag)
	}
	if effortFlag != "high" {
		t.Errorf("effortFlag = %q; want 'high'", effortFlag)
	}
}

type recordingChatTransport struct {
	response     string
	calls        int
	capturedBody []byte
}

func (r *recordingChatTransport) Do(request *http.Request) (*http.Response, error) {
	r.calls++
	if request.Body != nil {
		r.capturedBody, _ = io.ReadAll(request.Body)
	}
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(r.response)),
	}, nil
}

func TestOneshotWiresThinking(t *testing.T) {
	stateDB := filepath.Join(t.TempDir(), "state.db")
	t.Setenv("HARNESS_STATE_DB", stateDB)

	transport := &recordingChatTransport{
		response: "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":5}}}\n\n" +
			"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"thoughtful\"}}\n\n" +
			"event: message_delta\ndata: {\"type\":\"message_delta\",\"usage\":{\"output_tokens\":3}}\n\n",
	}
	client := &chat.ProviderClient{HTTP: transport}

	var output, errorOutput strings.Builder
	command := &cobra.Command{Use: "chat-test"}
	command.SetOut(&output)
	command.SetErr(&errorOutput)

	options := chatOptionsValue{
		query:    "think deeply",
		provider: "anthropic",
		thinking: "2048",
	}

	if err := runChat(command, options, client, func(string) (string, error) { return "test-token", nil }); err != nil {
		t.Fatal(err)
	}

	if transport.calls != 1 {
		t.Fatalf("expected 1 call, got %d", transport.calls)
	}

	var payload map[string]any
	if err := json.Unmarshal(transport.capturedBody, &payload); err != nil {
		t.Fatalf("Unmarshal error: %v", err)
	}
	thinking, ok := payload["thinking"].(map[string]any)
	if !ok || thinking["budget_tokens"] != float64(2048) {
		t.Fatalf("captured thinking = %#v; want budget_tokens=2048", payload["thinking"])
	}
}
