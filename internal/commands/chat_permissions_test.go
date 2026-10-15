package commands

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Koality-Assured/harness-cli/internal/chat"
	"github.com/spf13/cobra"
)

func TestPermissionsFlagValidation(t *testing.T) {
	stateDB := filepath.Join(t.TempDir(), "state.db")
	t.Setenv("HARNESS_STATE_DB", stateDB)

	command := &cobra.Command{Use: "chat-test"}
	var out, errOut strings.Builder
	command.SetOut(&out)
	command.SetErr(&errOut)

	// Invalid permissions mode should return error
	optionsInvalid := chatOptionsValue{
		query:       "test query",
		provider:    "anthropic",
		permissions: "dangerous-mode",
	}
	err := runChat(command, optionsInvalid, nil, func(string) (string, error) { return "token", nil })
	if err == nil || !strings.Contains(err.Error(), "invalid --permissions mode 'dangerous-mode'") {
		t.Fatalf("expected error for dangerous-mode, got: %v", err)
	}

	// Valid modes should be accepted
	for _, mode := range []string{"ask", "auto-edit", "yolo"} {
		transport := &fakeChatTransport{
			response: "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":2}}}\n\n" +
				"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"ok\"}}\n\n" +
				"event: message_delta\ndata: {\"type\":\"message_delta\",\"usage\":{\"output_tokens\":2}}\n\n",
		}
		client := &chat.ProviderClient{HTTP: transport}
		out.Reset()
		errOut.Reset()
		opt := chatOptionsValue{
			query:       "test query",
			provider:    "anthropic",
			permissions: mode,
		}
		err := runChat(command, opt, client, func(string) (string, error) { return "token", nil })
		if err != nil {
			t.Fatalf("mode %s failed: %v", mode, err)
		}
	}
}

func TestPermissionsSlashCommand(t *testing.T) {
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
	permPolicy := chat.NewPermissionPolicy("ask", true)
	slashOpts := &SlashOptions{
		Permissions: permPolicy,
	}

	var out, errOut strings.Builder
	cmd := &cobra.Command{}
	cmd.SetOut(&out)
	cmd.SetErr(&errOut)

	// 1. Inspect initial permissions
	handled, exit := handleChatSlash(cmd, store, &session, &model, session.Provider, "key", nil, "/permissions", nil, slashOpts)
	if !handled || exit {
		t.Fatalf("expected handled=true, exit=false; got (%v, %v)", handled, exit)
	}
	if !strings.Contains(out.String(), "permissions=ask") {
		t.Errorf("output = %q; want permissions=ask", out.String())
	}

	// 2. Set to auto-edit
	out.Reset()
	handled, _ = handleChatSlash(cmd, store, &session, &model, session.Provider, "key", nil, "/permissions auto-edit", nil, slashOpts)
	if !handled || permPolicy.Mode() != "auto-edit" {
		t.Errorf("expected auto-edit mode, got %q", permPolicy.Mode())
	}
	if !strings.Contains(out.String(), "permission mode set to auto-edit") {
		t.Errorf("output = %q; want 'permission mode set to auto-edit'", out.String())
	}

	// 3. Set to yolo
	out.Reset()
	handled, _ = handleChatSlash(cmd, store, &session, &model, session.Provider, "key", nil, "/permissions yolo", nil, slashOpts)
	if !handled || permPolicy.Mode() != "yolo" {
		t.Errorf("expected yolo mode, got %q", permPolicy.Mode())
	}
	if !strings.Contains(out.String(), "permission mode set to yolo") {
		t.Errorf("output = %q; want 'permission mode set to yolo'", out.String())
	}

	// 4. Invalid argument
	errOut.Reset()
	handled, _ = handleChatSlash(cmd, store, &session, &model, session.Provider, "key", nil, "/permissions invalid-mode", nil, slashOpts)
	if !handled || !strings.Contains(errOut.String(), "usage: /permissions [ask|auto-edit|yolo]") {
		t.Errorf("errOut = %q; want usage", errOut.String())
	}
}

func TestStatusDisplaysPermissions(t *testing.T) {
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
	permPolicy := chat.NewPermissionPolicy("auto-edit", true)
	slashOpts := &SlashOptions{
		Permissions: permPolicy,
	}

	var out, errOut strings.Builder
	cmd := &cobra.Command{}
	cmd.SetOut(&out)
	cmd.SetErr(&errOut)

	handled, exit := handleChatSlash(cmd, store, &session, &model, session.Provider, "key", nil, "/status", nil, slashOpts)
	if !handled || exit {
		t.Fatalf("expected handled=true, exit=false; got (%v, %v)", handled, exit)
	}
	if !strings.Contains(out.String(), "permissions=auto-edit") {
		t.Errorf("output = %q; want to contain 'permissions=auto-edit'", out.String())
	}
}

func TestPromptToolConfirmationDecisions(t *testing.T) {
	ctx := context.Background()

	// 1. Allow once via "1"
	var out bytes.Buffer
	in := strings.NewReader("1\n")
	dec, feedback, err := promptToolConfirmationWithReader(ctx, &out, in, nil, "run_command", map[string]any{"command": "git status"}, nil)
	if err != nil || dec != chat.DecisionAllowOnce || feedback != "" {
		t.Fatalf("choice 1: got dec=%v, fb=%q, err=%v", dec, feedback, err)
	}
	if !strings.Contains(out.String(), "[Tool Approval Required]") || !strings.Contains(out.String(), "Command: git status") {
		t.Errorf("prompt output missing tool info: %s", out.String())
	}

	// 2. Allow session via "2"
	out.Reset()
	in = strings.NewReader("2\n")
	dec, feedback, err = promptToolConfirmationWithReader(ctx, &out, in, nil, "run_command", map[string]any{"command": "git status"}, nil)
	if err != nil || dec != chat.DecisionAllowSession || feedback != "" {
		t.Fatalf("choice 2: got dec=%v, fb=%q, err=%v", dec, feedback, err)
	}

	// 3. Allow session via short code "a"
	out.Reset()
	in = strings.NewReader("a\n")
	dec, feedback, err = promptToolConfirmationWithReader(ctx, &out, in, nil, "edit_file", map[string]any{"path": "config.go"}, nil)
	if err != nil || dec != chat.DecisionAllowSession || feedback != "" {
		t.Fatalf("choice a: got dec=%v, fb=%q, err=%v", dec, feedback, err)
	}

	// 4. Deny via "3" with custom feedback
	out.Reset()
	in = strings.NewReader("3\nDon't edit production files\n")
	dec, feedback, err = promptToolConfirmationWithReader(ctx, &out, in, nil, "edit_file", map[string]any{"path": "prod.yaml"}, nil)
	if err != nil || dec != chat.DecisionDeny || feedback != "Don't edit production files" {
		t.Fatalf("choice 3 with feedback: got dec=%v, fb=%q, err=%v", dec, feedback, err)
	}

	// 5. Deny via "d" without feedback (empty Enter)
	out.Reset()
	in = strings.NewReader("d\n\n")
	dec, feedback, err = promptToolConfirmationWithReader(ctx, &out, in, nil, "run_command", map[string]any{"command": "rm -rf /"}, nil)
	if err != nil || dec != chat.DecisionDeny || feedback != "Tool execution denied by user." {
		t.Fatalf("choice d without feedback: got dec=%v, fb=%q, err=%v", dec, feedback, err)
	}
}

func TestPromptToolConfirmationDrainsKeystrokes(t *testing.T) {
	r, w := io.Pipe()
	pump := newChatLinePump(r)
	defer pump.close()
	defer w.Close()

	// Write accidental keystroke into pipe
	go func() {
		fmt.Fprintln(w, "accidental keystroke before prompt")
	}()

	// Wait briefly for the pump goroutine to read and queue the accidental line
	time.Sleep(50 * time.Millisecond)

	var out bytes.Buffer

	// Concurrently write the actual approval choice after prompt begins
	go func() {
		time.Sleep(50 * time.Millisecond)
		fmt.Fprintln(w, "1")
	}()

	var drained []string
	dec, _, err := promptToolConfirmation(context.Background(), &out, pump, "run_command", map[string]any{"command": "ls"}, func(lines []string) {
		drained = lines
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if dec != chat.DecisionAllowOnce {
		t.Fatalf("expected DecisionAllowOnce, got %v", dec)
	}
	if strings.Contains(out.String(), "Invalid choice") {
		t.Errorf("accidental keystroke was not drained before prompt: %s", out.String())
	}
	if len(drained) == 0 || drained[0] != "accidental keystroke before prompt" {
		t.Errorf("expected drained lines to contain accidental keystroke, got: %v", drained)
	}
}

func TestPromptToolConfirmationEOF(t *testing.T) {
	var out bytes.Buffer
	in := strings.NewReader("")
	dec, feedback, err := promptToolConfirmationWithReader(context.Background(), &out, in, nil, "run_command", map[string]any{"command": "ls"}, nil)
	if err != nil {
		t.Fatalf("expected err == nil on EOF, got: %v", err)
	}
	if dec != chat.DecisionDeny {
		t.Fatalf("expected DecisionDeny on EOF, got: %v", dec)
	}
	if feedback != "Tool execution cancelled by user (EOF)." {
		t.Fatalf("expected EOF feedback message, got: %q", feedback)
	}
}

func TestPromptToolConfirmationContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	var out bytes.Buffer
	in := strings.NewReader("1\n")
	dec, feedback, err := promptToolConfirmationWithReader(ctx, &out, in, nil, "run_command", map[string]any{"command": "ls"}, nil)
	if err != context.Canceled {
		t.Fatalf("expected context.Canceled, got: %v", err)
	}
	if dec != chat.DecisionDeny {
		t.Fatalf("expected DecisionDeny, got: %v", dec)
	}
	if feedback != "Tool execution cancelled (interrupted)." {
		t.Fatalf("expected interruption feedback message, got: %q", feedback)
	}
}

func TestPromptToolConfirmationQueuedPreservation(t *testing.T) {
	r, w := io.Pipe()
	pump := newChatLinePump(r)
	defer pump.close()
	defer w.Close()

	go func() {
		fmt.Fprintln(w, "queued line 1")
		fmt.Fprintln(w, "queued line 2")
	}()

	time.Sleep(50 * time.Millisecond)

	var out bytes.Buffer
	go func() {
		time.Sleep(50 * time.Millisecond)
		fmt.Fprintln(w, "1")
	}()

	var capturedDrained []string
	dec, _, err := promptToolConfirmation(context.Background(), &out, pump, "run_command", map[string]any{"command": "ls"}, func(lines []string) {
		capturedDrained = append(capturedDrained, lines...)
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if dec != chat.DecisionAllowOnce {
		t.Fatalf("expected DecisionAllowOnce, got %v", dec)
	}
	if len(capturedDrained) != 2 || capturedDrained[0] != "queued line 1" || capturedDrained[1] != "queued line 2" {
		t.Fatalf("expected queued lines preserved, got: %v", capturedDrained)
	}
}

type sequenceFakeTransport struct {
	responses []string
	idx       int
}

func (s *sequenceFakeTransport) Do(req *http.Request) (*http.Response, error) {
	if s.idx >= len(s.responses) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader("")),
		}, nil
	}
	resp := s.responses[s.idx]
	s.idx++
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(resp)),
	}, nil
}

func TestOneShotToolRejectionVsYolo(t *testing.T) {
	stateDB := filepath.Join(t.TempDir(), "state.db")
	t.Setenv("HARNESS_STATE_DB", stateDB)

	command := &cobra.Command{Use: "chat-test"}
	var out, errOut strings.Builder
	command.SetOut(&out)
	command.SetErr(&errOut)

	anthropicToolCallStream := "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":5}}}\n\n" +
		"event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"tool_use\",\"id\":\"toolu_1\",\"name\":\"run_command\",\"input\":{\"command\":\"ls\"}}}\n\n" +
		"event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\n" +
		"event: message_delta\ndata: {\"type\":\"message_delta\",\"usage\":{\"output_tokens\":2}}\n\n"

	anthropicFinalAnswer := "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":5}}}\n\n" +
		"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"done\"}}\n\n" +
		"event: message_delta\ndata: {\"type\":\"message_delta\",\"usage\":{\"output_tokens\":2}}\n\n"

	// 1. One-shot with default permissions (ask): tool call should be denied with clear message, non-blocking
	transport := &sequenceFakeTransport{responses: []string{anthropicToolCallStream, anthropicFinalAnswer}}
	client := &chat.ProviderClient{HTTP: transport}

	optAsk := chatOptionsValue{
		query:       "execute ls",
		provider:    "anthropic",
		permissions: "ask",
		noMCP:       true,
	}

	err := runChat(command, optAsk, client, func(string) (string, error) { return "token", nil })
	if err != nil {
		t.Fatalf("runChat failed: %v", err)
	}

	store, err := chat.OpenSessionStore(stateDB)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	sessions, _ := store.ListSessions(1)
	if len(sessions) == 0 {
		t.Fatal("expected session to be created")
	}
	history, err := store.ListMessages(sessions[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	var foundDenial bool
	for _, msg := range history {
		if msg.Role == "tool" && strings.Contains(msg.Content, "Tool requires interactive confirmation") {
			foundDenial = true
		}
	}
	if !foundDenial {
		t.Error("expected tool rejection message in conversation history for default ask permissions")
	}

	// 2. One-shot with yolo permissions: tool call allowed through policy
	out.Reset()
	errOut.Reset()
	transportYolo := &sequenceFakeTransport{responses: []string{anthropicToolCallStream, anthropicFinalAnswer}}
	clientYolo := &chat.ProviderClient{HTTP: transportYolo}

	optYolo := chatOptionsValue{
		query:       "execute ls again",
		provider:    "anthropic",
		permissions: "yolo",
		noMCP:       true,
	}

	err = runChat(command, optYolo, clientYolo, func(string) (string, error) { return "token", nil })
	if err != nil {
		t.Fatalf("runChat with yolo failed: %v", err)
	}

	sessionsYolo, _ := store.ListSessions(2)
	latestID := sessionsYolo[0].ID
	historyYolo, err := store.ListMessages(latestID)
	if err != nil {
		t.Fatal(err)
	}
	var foundConfirmationDenial bool
	for _, msg := range historyYolo {
		if msg.Role == "tool" && strings.Contains(msg.Content, "Tool requires interactive confirmation") {
			foundConfirmationDenial = true
		}
	}
	if foundConfirmationDenial {
		t.Error("tool should not have been denied for interactive confirmation under yolo mode")
	}
}
