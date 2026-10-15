package commands

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Koality-Assured/harness-cli/internal/chat"
)

func TestChatPlainAndNoTUIFlags(t *testing.T) {
	// 1. Test --plain flag sets plainMode
	resetChatFlags()
	chatCmd.SetArgs([]string{"--plain"})
	if err := chatCmd.ParseFlags([]string{"--plain"}); err != nil {
		t.Fatalf("ParseFlags failed for --plain: %v", err)
	}
	if !plainMode {
		t.Errorf("expected plainMode to be true after --plain flag")
	}

	// 2. Test --no-tui alias flag sets plainMode
	resetChatFlags()
	if err := chatCmd.ParseFlags([]string{"--no-tui"}); err != nil {
		t.Fatalf("ParseFlags failed for --no-tui: %v", err)
	}
	if !plainMode {
		t.Errorf("expected plainMode to be true after --no-tui flag")
	}

	// 3. Test default (neither flag) leaves plainMode false
	resetChatFlags()
	if err := chatCmd.ParseFlags([]string{}); err != nil {
		t.Fatalf("ParseFlags failed with no flags: %v", err)
	}
	if plainMode {
		t.Errorf("expected plainMode to be false by default")
	}
}

func TestRunChatOneShotBypassesTUI(t *testing.T) {
	resetChatFlags()
	stateDB := filepath.Join(t.TempDir(), "state.db")
	t.Setenv("HARNESS_STATE_DB", stateDB)

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	chatCmd.SetOut(&stdout)
	chatCmd.SetErr(&stderr)

	transport := &fakeChatTransport{
		response: "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":5}}}\n\n" +
			"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"Hello from one-shot\"}}\n\n" +
			"event: message_delta\ndata: {\"type\":\"message_delta\",\"usage\":{\"output_tokens\":4}}\n\n",
	}
	client := &chat.ProviderClient{HTTP: transport}

	opts := chatOptionsValue{
		query:    "one-shot prompt",
		provider: "anthropic",
		model:    "claude-3-5-sonnet",
		noTools:  true,
		noMCP:    true,
	}

	err := runChat(chatCmd, opts, client, func(string) (string, error) {
		return "test-token", nil
	})
	if err != nil {
		t.Fatalf("runChat one-shot failed: %v", err)
	}

	out := stdout.String()
	if !strings.Contains(out, "Hello from one-shot") {
		t.Errorf("expected one-shot assistant reply in stdout, got %q", out)
	}
}

func TestPromptToolConfirmationEphemeralPrivacy(t *testing.T) {
	tmpDir := t.TempDir()
	histFile := filepath.Join(tmpDir, "chat_history")

	// Prepopulate history
	if err := chat.AppendChatHistory(histFile, "Initial user query"); err != nil {
		t.Fatalf("AppendChatHistory failed: %v", err)
	}

	cfg := chat.ChatTUISessionConfig{
		In:            strings.NewReader("1\n"),
		Out:           &bytes.Buffer{},
		HistoryPath:   histFile,
		WorkspaceRoot: tmpDir,
		PlainMode:     false,
	}
	tuiSession := chat.NewChatTUISession(cfg)

	var out bytes.Buffer
	decision, feedback, err := promptToolConfirmationWithSession(
		context.Background(),
		&out,
		nil,
		nil,
		tuiSession,
		"edit_file",
		map[string]any{"path": "config.go", "patch": "--- a\n+++ b\n@@ -1 +1 @@\n-old\n+new\n"},
		nil,
	)
	if err != nil {
		t.Fatalf("promptToolConfirmationWithSession failed: %v", err)
	}
	if decision != chat.DecisionAllowOnce {
		t.Errorf("expected DecisionAllowOnce, got %v (feedback: %q)", decision, feedback)
	}

	// Verify that the tool banner was rendered with visual box and diff
	outText := out.String()
	if !strings.Contains(outText, "edit_file") {
		t.Errorf("expected tool banner with tool name, got %q", outText)
	}

	// Verify history file was NOT polluted with single digit "1"
	hist, err := chat.LoadChatHistory(histFile, 100)
	if err != nil {
		t.Fatalf("LoadChatHistory failed: %v", err)
	}
	if len(hist) != 1 || hist[0] != "Initial user query" {
		t.Errorf("ephemeral privacy violated: choice '1' stored in chat_history: %v", hist)
	}
}

func TestBusyModeAdvisoryInTUIMode(t *testing.T) {
	stateDB := filepath.Join(t.TempDir(), "state.db")
	t.Setenv("HARNESS_STATE_DB", stateDB)
	store, err := chat.OpenSessionStore(stateDB)
	if err != nil {
		t.Fatalf("OpenSessionStore failed: %v", err)
	}
	defer store.Close()

	session, err := store.CreateSession(chat.Session{
		Provider: "anthropic",
		Model:    "claude-3-5-sonnet",
		Title:    "Test Session",
	})
	if err != nil {
		t.Fatalf("CreateSession failed: %v", err)
	}

	model := "claude-3-5-sonnet"
	var out bytes.Buffer
	cmd := chatCmd
	cmd.SetOut(&out)

	// 1. In rich TUI mode (plainMode = false)
	plainMode = false
	handled, _ := handleChatSlash(cmd, store, &session, &model, "anthropic", "token", nil, "/busy steer", nil)
	if !handled {
		t.Fatalf("expected /busy steer to be handled")
	}
	if !strings.Contains(out.String(), "requires --plain / --no-tui mode") {
		t.Errorf("expected advisory note in TUI mode for /busy steer, got %q", out.String())
	}

	// 2. In plain mode (plainMode = true)
	out.Reset()
	plainMode = true
	handled, _ = handleChatSlash(cmd, store, &session, &model, "anthropic", "token", nil, "/busy queue", nil)
	if !handled {
		t.Fatalf("expected /busy queue to be handled")
	}
	if strings.Contains(out.String(), "requires --plain / --no-tui mode") {
		t.Errorf("advisory note should NOT be printed when plainMode is true, got %q", out.String())
	}
}
