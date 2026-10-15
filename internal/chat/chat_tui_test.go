package chat

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFormatStatusBar(t *testing.T) {
	// Standard full format
	got := FormatStatusBar("anthropic", "claude-3-5-sonnet", "high", 25.0, 0.0123, 2, "ask")
	expected := "[harness] [anthropic:claude-3-5-sonnet] [thinking: high] [ctx: 25%] [$0.0123] [mcp: 2] [mode: ask]"
	if got != expected {
		t.Errorf("FormatStatusBar mismatch:\ngot:  %q\nwant: %q", got, expected)
	}

	// Fractional context percentage
	gotFractional := FormatStatusBar("openai", "gpt-4o", "medium", 12.5, 0.0050, 1, "auto-edit")
	expectedFractional := "[harness] [openai:gpt-4o] [thinking: medium] [ctx: 12.5%] [$0.0050] [mcp: 1] [mode: auto-edit]"
	if gotFractional != expectedFractional {
		t.Errorf("FormatStatusBar fractional mismatch:\ngot:  %q\nwant: %q", gotFractional, expectedFractional)
	}

	// Defaults for empty inputs
	gotDefaults := FormatStatusBar("", "", "", 0, 0, 0, "")
	expectedDefaults := "[harness] [unknown:none] [thinking: off] [ctx: 0%] [$0.0000] [mcp: 0] [mode: ask]"
	if gotDefaults != expectedDefaults {
		t.Errorf("FormatStatusBar defaults mismatch:\ngot:  %q\nwant: %q", gotDefaults, expectedDefaults)
	}
}

func TestSlashCommandAutocompletion(t *testing.T) {
	// Leading slash prefix
	matches := CompleteSlashCommand("/m")
	if len(matches) != 2 || matches[0] != "/mcp" || matches[1] != "/model" {
		t.Errorf("expected [/mcp /model], got %v", matches)
	}

	// Exact match
	exact := CompleteSlashCommand("/quit")
	if len(exact) != 1 || exact[0] != "/quit" {
		t.Errorf("expected [/quit], got %v", exact)
	}

	// All commands with "/"
	all := CompleteSlashCommand("/")
	if len(all) != len(BuiltinSlashCommands) {
		t.Errorf("expected %d commands, got %d", len(BuiltinSlashCommands), len(all))
	}

	// Without slash
	noSlash := CompleteSlashCommand("stat")
	if len(noSlash) != 1 || noSlash[0] != "status" {
		t.Errorf("expected [status], got %v", noSlash)
	}

	// Non-matching
	none := CompleteSlashCommand("/nonexistent")
	if len(none) != 0 {
		t.Errorf("expected empty matches, got %v", none)
	}
}

func TestFilePathCompletion(t *testing.T) {
	tmpDir := t.TempDir()

	// Create workspace structure
	_ = os.WriteFile(filepath.Join(tmpDir, "main.go"), []byte("package main"), 0644)
	_ = os.WriteFile(filepath.Join(tmpDir, "README.md"), []byte("# Readme"), 0644)

	chatDir := filepath.Join(tmpDir, "internal", "chat")
	_ = os.MkdirAll(chatDir, 0755)
	_ = os.WriteFile(filepath.Join(chatDir, "chat_tui.go"), []byte("package chat"), 0644)

	// Ignored directories
	gitDir := filepath.Join(tmpDir, ".git")
	_ = os.MkdirAll(gitDir, 0755)
	_ = os.WriteFile(filepath.Join(gitDir, "HEAD"), []byte("ref: refs/heads/main"), 0644)

	nodeDir := filepath.Join(tmpDir, "node_modules", "pkg")
	_ = os.MkdirAll(nodeDir, 0755)
	_ = os.WriteFile(filepath.Join(nodeDir, "index.js"), []byte("console.log()"), 0644)

	pycacheDir := filepath.Join(tmpDir, "__pycache__")
	_ = os.MkdirAll(pycacheDir, 0755)
	_ = os.WriteFile(filepath.Join(pycacheDir, "foo.pyc"), []byte("compiled"), 0644)

	completer := NewFilePathCompleter(tmpDir)

	// 1. Tokens not starting with @ must return nil
	if res := completer.Complete("main.go"); res != nil {
		t.Errorf("expected nil for token without @ prefix, got %v", res)
	}

	// 2. All files matching @
	all := completer.Complete("@")
	if len(all) != 3 {
		t.Fatalf("expected 3 files, got %d: %v", len(all), all)
	}
	expectedFiles := map[string]bool{
		"@README.md":                 true,
		"@internal/chat/chat_tui.go": true,
		"@main.go":                   true,
	}
	for _, f := range all {
		if !expectedFiles[f] {
			t.Errorf("unexpected file in completion results: %s", f)
		}
	}

	// 3. Prefix matching
	mainMatches := completer.Complete("@main")
	if len(mainMatches) != 1 || mainMatches[0] != "@main.go" {
		t.Errorf("expected [@main.go], got %v", mainMatches)
	}

	// 4. In-memory caching and invalidation
	newFile := filepath.Join(tmpDir, "new_file.txt")
	_ = os.WriteFile(newFile, []byte("new"), 0644)

	// Without invalidation, cache hit returns old list
	cachedRes := completer.Complete("@new")
	if len(cachedRes) != 0 {
		t.Errorf("expected cache hit to exclude newly added file, got %v", cachedRes)
	}

	// Invalidate cache
	completer.InvalidateCache()
	freshRes := completer.Complete("@new")
	if len(freshRes) != 1 || freshRes[0] != "@new_file.txt" {
		t.Errorf("expected [@new_file.txt] after invalidation, got %v", freshRes)
	}

	// 5. Out-of-bounds symlink exclusion
	outsideDir := t.TempDir()
	outsideFile := filepath.Join(outsideDir, "secret.txt")
	_ = os.WriteFile(outsideFile, []byte("classified"), 0644)

	symlinkPath := filepath.Join(tmpDir, "symlink_secret.txt")
	if err := os.Symlink(outsideFile, symlinkPath); err == nil {
		completer.InvalidateCache()
		symlinkMatches := completer.Complete("@")
		for _, m := range symlinkMatches {
			if strings.Contains(m, "secret.txt") {
				t.Errorf("out-of-bounds symlink was not excluded: %s", m)
			}
		}
	}
}

func TestRenderDiff(t *testing.T) {
	// Guard against nil writer
	RenderDiff("some diff", nil)

	// Guard against empty text
	var emptyBuf bytes.Buffer
	RenderDiff("", &emptyBuf)
	if emptyBuf.Len() != 0 {
		t.Errorf("expected empty buffer for empty diff, got %q", emptyBuf.String())
	}

	// Under 150 lines
	var normalBuf bytes.Buffer
	diffText := "--- a/file.go\n+++ b/file.go\n@@ -1,3 +1,3 @@\n-old line\n+new line\n context line"
	RenderDiff(diffText, &normalBuf)
	out := normalBuf.String()
	if !strings.Contains(out, "new line") || !strings.Contains(out, "old line") {
		t.Errorf("expected colorized diff lines, got %q", out)
	}
	if strings.Contains(out, "truncated") {
		t.Errorf("expected no truncation for small diff, got %q", out)
	}

	// Over 150 lines (160 lines)
	var largeDiff strings.Builder
	for i := 1; i <= 160; i++ {
		largeDiff.WriteString(fmt.Sprintf("+line %d\n", i))
	}
	var largeBuf bytes.Buffer
	RenderDiff(largeDiff.String(), &largeBuf)
	largeOut := largeBuf.String()

	if !strings.Contains(largeOut, "+line 150") {
		t.Errorf("expected line 150 to be rendered, got %q", largeOut)
	}
	if strings.Contains(largeOut, "+line 151") {
		t.Errorf("line 151 should be capped and truncated, got %q", largeOut)
	}
	expectedTrunc := "... (10 lines truncated)"
	if !strings.Contains(largeOut, expectedTrunc) {
		t.Errorf("expected %q, got %q", expectedTrunc, largeOut)
	}
}

func TestRenderToolCallAndOutcome(t *testing.T) {
	var buf bytes.Buffer
	args := map[string]any{"path": "foo.go", "content": "hello"}
	RenderToolCall("write_file", args, &buf)

	output := buf.String()
	if !strings.Contains(output, "write_file") {
		t.Errorf("expected tool name in RenderToolCall, got %q", output)
	}
	if !strings.Contains(output, "foo.go") {
		t.Errorf("expected arguments in RenderToolCall, got %q", output)
	}

	// RenderToolOutcome success
	buf.Reset()
	RenderToolOutcome("write_file", "file written successfully", false, &buf)
	successOut := buf.String()
	if !strings.Contains(successOut, "[OK]") || !strings.Contains(successOut, "write_file") {
		t.Errorf("expected [OK] badge in RenderToolOutcome, got %q", successOut)
	}

	// RenderToolOutcome failure
	buf.Reset()
	RenderToolOutcome("write_file", "permission denied", true, &buf)
	failOut := buf.String()
	if !strings.Contains(failOut, "[FAIL]") || !strings.Contains(failOut, "write_file") {
		t.Errorf("expected [FAIL] badge in RenderToolOutcome, got %q", failOut)
	}
}

func TestChatHistoryEphemeralPrivacy(t *testing.T) {
	tmpDir := t.TempDir()
	histFile := filepath.Join(tmpDir, "chat_history")

	// 1. Regular turns are appended
	if err := AppendChatHistory(histFile, "What is the status of the cluster?"); err != nil {
		t.Fatalf("AppendChatHistory failed: %v", err)
	}
	if err := AppendChatHistory(histFile, "Show me git diff"); err != nil {
		t.Fatalf("AppendChatHistory failed: %v", err)
	}

	lines, err := LoadChatHistory(histFile, 10)
	if err != nil {
		t.Fatalf("LoadChatHistory failed: %v", err)
	}
	if len(lines) != 2 {
		t.Fatalf("expected 2 lines in history, got %d", len(lines))
	}

	// 2. ChatTUISession with storeHistory: false does NOT store single digit or choice
	cfg := ChatTUISessionConfig{
		In:            strings.NewReader("1\n"),
		Out:           &bytes.Buffer{},
		HistoryPath:   histFile,
		WorkspaceRoot: tmpDir,
		PlainMode:     true,
	}
	session := NewChatTUISession(cfg)

	// Read line with storeHistory = false
	choice, err := session.ReadLine("", false)
	if err != nil {
		t.Fatalf("ReadLine failed: %v", err)
	}
	if choice != "1" {
		t.Errorf("expected choice '1', got %q", choice)
	}

	// Reload history: must STILL be 2 lines (choice '1' was not saved)
	updatedLines, err := LoadChatHistory(histFile, 10)
	if err != nil {
		t.Fatalf("LoadChatHistory failed: %v", err)
	}
	if len(updatedLines) != 2 {
		t.Errorf("ephemeral privacy violated: single digit choice was stored in chat_history: %v", updatedLines)
	}
}

func TestChatTUISessionPersistentReader(t *testing.T) {
	// Multi-line paste simulation
	multiLineInput := "first line\nsecond line\nthird line\n"
	var out bytes.Buffer
	cfg := ChatTUISessionConfig{
		In:            strings.NewReader(multiLineInput),
		Out:           &out,
		PlainMode:     true,
		WorkspaceRoot: t.TempDir(),
	}
	session := NewChatTUISession(cfg)

	l1, err := session.ReadLine("> ", false)
	if err != nil || l1 != "first line" {
		t.Fatalf("expected 'first line', got %q (err: %v)", l1, err)
	}

	l2, err := session.ReadLine("> ", false)
	if err != nil || l2 != "second line" {
		t.Fatalf("expected 'second line', got %q (err: %v)", l2, err)
	}

	l3, err := session.ReadLine("> ", false)
	if err != nil || l3 != "third line" {
		t.Fatalf("expected 'third line', got %q (err: %v)", l3, err)
	}
}

func TestStatusBarSuppressionOnModalChoice(t *testing.T) {
	var out bytes.Buffer
	cfg := ChatTUISessionConfig{
		In:            strings.NewReader("1\nuser turn\n"),
		Out:           &out,
		PlainMode:     false,
		WorkspaceRoot: t.TempDir(),
	}
	session := NewChatTUISession(cfg)
	session.UpdateStatusBar("anthropic", "claude-3-5-sonnet", "off", 10.0, 0.001, 1, "ask")

	// 1. Tool confirmation / modal choice (storeHistory: false)
	choice, err := session.ReadLine("Choice [1-3]: ", false)
	if err != nil || choice != "1" {
		t.Fatalf("expected choice '1', got %q (err: %v)", choice, err)
	}
	// Status bar must NOT be printed for modal choice
	if strings.Contains(out.String(), "[harness]") {
		t.Errorf("status bar clobbered modal choice prompt: got %q", out.String())
	}

	// 2. Main conversational turn (storeHistory: true)
	out.Reset()
	turn, err := session.ReadLine("> ", true)
	if err != nil || turn != "user turn" {
		t.Fatalf("expected 'user turn', got %q (err: %v)", turn, err)
	}
	// Status bar MUST be printed for conversational turn
	if !strings.Contains(out.String(), "[harness]") {
		t.Errorf("status bar missing from main conversational turn prompt: got %q", out.String())
	}
}

func TestRenderToolOutcomeTruncation(t *testing.T) {
	var buf bytes.Buffer
	// Create an outcome with 25 lines and 1000 characters
	var bigOutcome strings.Builder
	for i := 1; i <= 25; i++ {
		bigOutcome.WriteString(fmt.Sprintf("line %02d: this is a long outcome result containing lots of text\n", i))
	}

	RenderToolOutcome("read_file", bigOutcome.String(), false, &buf)
	result := buf.String()

	if !strings.Contains(result, "[OK]") || !strings.Contains(result, "read_file") {
		t.Fatalf("expected [OK] badge and tool name, got %q", result)
	}
	if !strings.Contains(result, "... (truncated)") {
		t.Errorf("expected truncation note for multi-line outcome, got %q", result)
	}
	// Ensure it didn't print all 25 lines
	if strings.Contains(result, "line 20") {
		t.Errorf("expected lines past 10 to be truncated, but found line 20 in: %q", result)
	}
}
