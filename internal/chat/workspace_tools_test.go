package chat

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestResolveWorkspacePath(t *testing.T) {
	wsDir := t.TempDir()
	if realWs, err := filepath.EvalSymlinks(wsDir); err == nil {
		wsDir = realWs
	}
	outsideDir := t.TempDir()
	if realOutside, err := filepath.EvalSymlinks(outsideDir); err == nil {
		outsideDir = realOutside
	}

	// 1. Valid file in root
	relPath := "file.txt"
	absTarget := filepath.Join(wsDir, relPath)
	if err := os.WriteFile(absTarget, []byte("content"), 0644); err != nil {
		t.Fatal(err)
	}

	resolved, err := ResolveWorkspacePath(relPath, wsDir)
	if err != nil {
		t.Fatalf("unexpected error for valid file: %v", err)
	}
	if !strings.EqualFold(resolved, absTarget) {
		t.Errorf("ResolveWorkspacePath = %q; want %q", resolved, absTarget)
	}

	// 2. Subdirectory file
	subDir := filepath.Join(wsDir, "sub", "dir")
	if err := os.MkdirAll(subDir, 0755); err != nil {
		t.Fatal(err)
	}
	subFile := filepath.Join(subDir, "nested.txt")
	if err := os.WriteFile(subFile, []byte("nested"), 0644); err != nil {
		t.Fatal(err)
	}
	resolved, err = ResolveWorkspacePath("sub/dir/nested.txt", wsDir)
	if err != nil {
		t.Fatalf("unexpected error for nested file: %v", err)
	}
	if !strings.EqualFold(resolved, subFile) {
		t.Errorf("ResolveWorkspacePath = %q; want %q", resolved, subFile)
	}

	// 3. Traversal attacks
	traversalCases := []string{
		"../escaped.txt",
		"sub/../../escaped.txt",
		"sub/dir/../../../escaped.txt",
		filepath.Join(outsideDir, "outside.txt"),
	}
	for _, tc := range traversalCases {
		_, err := ResolveWorkspacePath(tc, wsDir)
		if err == nil {
			t.Errorf("ResolveWorkspacePath(%q) should have failed with containment error", tc)
		}
	}

	// 4. Empty path
	if _, err := ResolveWorkspacePath("", wsDir); err == nil {
		t.Error("ResolveWorkspacePath(\"\") should fail")
	}

	// 5. Non-existent path within workspace (for write_file)
	newPath := "new_folder/new_file.txt"
	expectedNew := filepath.Join(wsDir, "new_folder", "new_file.txt")
	resolved, err = ResolveWorkspacePath(newPath, wsDir)
	if err != nil {
		t.Fatalf("unexpected error for new path: %v", err)
	}
	if !strings.EqualFold(resolved, expectedNew) {
		t.Errorf("ResolveWorkspacePath = %q; want %q", resolved, expectedNew)
	}

	// 6. Symlink escape (if OS allows symlinks)
	outsideFile := filepath.Join(outsideDir, "secret.txt")
	_ = os.WriteFile(outsideFile, []byte("secret"), 0644)
	symlinkPath := filepath.Join(wsDir, "escape_link")
	if err := os.Symlink(outsideFile, symlinkPath); err == nil {
		_, err := ResolveWorkspacePath("escape_link", wsDir)
		if err == nil {
			t.Errorf("ResolveWorkspacePath should reject symlink escaping root")
		}
	}
}

func TestHandleReadFile(t *testing.T) {
	wsDir := t.TempDir()

	// Normal text file
	filePath := filepath.Join(wsDir, "test.txt")
	content := "line 1\nline 2\nline 3\nline 4\nline 5\n"
	if err := os.WriteFile(filePath, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	// 1. Read all lines
	res, err := HandleReadFile(map[string]any{"path": "test.txt"}, wsDir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	m := res.(map[string]any)
	expectedContent := "1: line 1\n2: line 2\n3: line 3\n4: line 4\n5: line 5\n"
	if m["content"] != expectedContent {
		t.Errorf("read_file full content = %q; want %q", m["content"], expectedContent)
	}
	if m["total_lines"] != 5 {
		t.Errorf("total_lines = %v; want 5", m["total_lines"])
	}

	// 2. Read line range
	res, err = HandleReadFile(map[string]any{
		"path":       "test.txt",
		"start_line": 2,
		"end_line":   4,
	}, wsDir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	m = res.(map[string]any)
	expectedRange := "2: line 2\n3: line 3\n4: line 4\n"
	if m["content"] != expectedRange {
		t.Errorf("read_file range = %q; want %q", m["content"], expectedRange)
	}

	// 3. Binary file rejection
	binPath := filepath.Join(wsDir, "binary.dat")
	if err := os.WriteFile(binPath, []byte{0x00, 0x01, 0x02, 0xFF}, 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := HandleReadFile(map[string]any{"path": "binary.dat"}, wsDir); err == nil {
		t.Error("read_file should reject binary file with null bytes")
	}

	// 4. Cap output at 2,000 lines
	var bigSb strings.Builder
	for i := 1; i <= 2500; i++ {
		bigSb.WriteString(fmt.Sprintf("generated line %d\n", i))
	}
	bigFile := filepath.Join(wsDir, "big.txt")
	if err := os.WriteFile(bigFile, []byte(bigSb.String()), 0644); err != nil {
		t.Fatal(err)
	}
	res, err = HandleReadFile(map[string]any{"path": "big.txt"}, wsDir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	m = res.(map[string]any)
	linesOut := strings.Split(strings.TrimSpace(m["content"].(string)), "\n")
	if len(linesOut) != 2000 {
		t.Errorf("read_file lines count = %d; want 2000", len(linesOut))
	}

	// 5. Traversal rejection
	if _, err := HandleReadFile(map[string]any{"path": "../escaped.txt"}, wsDir); err == nil {
		t.Error("read_file should reject path traversal")
	}
}

func TestHandleListDir(t *testing.T) {
	wsDir := t.TempDir()

	_ = os.WriteFile(filepath.Join(wsDir, "root_file.txt"), []byte("hello"), 0644)
	_ = os.WriteFile(filepath.Join(wsDir, ".hidden_file"), []byte("hidden"), 0644)
	subDir := filepath.Join(wsDir, "subdir")
	_ = os.MkdirAll(subDir, 0755)
	_ = os.WriteFile(filepath.Join(subDir, "nested.txt"), []byte("nested"), 0644)

	// 1. Default listing (depth=1, no hidden)
	res, err := HandleListDir(map[string]any{}, wsDir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	out := res.(map[string]any)["output"].(string)
	if !strings.Contains(out, "[FILE] root_file.txt") {
		t.Errorf("output missing root_file.txt: %s", out)
	}
	if !strings.Contains(out, "[DIR]  subdir") {
		t.Errorf("output missing subdir: %s", out)
	}
	if strings.Contains(out, ".hidden_file") {
		t.Errorf("output should not contain hidden file by default: %s", out)
	}
	if strings.Contains(out, "nested.txt") {
		t.Errorf("depth 1 should not contain nested.txt: %s", out)
	}

	// 2. Depth=2
	res, err = HandleListDir(map[string]any{"depth": 2}, wsDir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	out = res.(map[string]any)["output"].(string)
	if !strings.Contains(out, "[FILE] subdir/nested.txt") {
		t.Errorf("depth 2 missing subdir/nested.txt: %s", out)
	}

	// 3. include_hidden=true
	res, err = HandleListDir(map[string]any{"include_hidden": true}, wsDir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	out = res.(map[string]any)["output"].(string)
	if !strings.Contains(out, "[FILE] .hidden_file") {
		t.Errorf("include_hidden missing .hidden_file: %s", out)
	}

	// 4. Symlink loop guard
	loopDir := filepath.Join(wsDir, "loop_dir")
	_ = os.MkdirAll(loopDir, 0755)
	loopLink := filepath.Join(loopDir, "link_back")
	if err := os.Symlink(loopDir, loopLink); err == nil {
		res, err := HandleListDir(map[string]any{"path": "loop_dir", "depth": 5}, wsDir)
		if err != nil {
			t.Fatalf("list_dir failed on recursive symlink loop: %v", err)
		}
		if res == nil {
			t.Error("list_dir returned nil result")
		}
	}
}

func TestHandleGrepSearch(t *testing.T) {
	wsDir := t.TempDir()

	goFile := filepath.Join(wsDir, "main.go")
	goContent := "package main\n\nfunc main() {\n\tprintln(\"hello world\")\n}\n"
	_ = os.WriteFile(goFile, []byte(goContent), 0644)

	txtFile := filepath.Join(wsDir, "doc.txt")
	txtContent := "hello everyone\nanother line\n"
	_ = os.WriteFile(txtFile, []byte(txtContent), 0644)

	// 1. Substring search
	res, err := HandleGrepSearch(map[string]any{"pattern": "hello"}, wsDir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	m := res.(map[string]any)
	out := m["output"].(string)
	if !strings.Contains(out, "main.go:4: \tprintln(\"hello world\")") {
		t.Errorf("grep output missing main.go match: %s", out)
	}
	if !strings.Contains(out, "doc.txt:1: hello everyone") {
		t.Errorf("grep output missing doc.txt match: %s", out)
	}

	// 2. Regex search
	res, err = HandleGrepSearch(map[string]any{"pattern": `func\s+main`}, wsDir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	out = res.(map[string]any)["output"].(string)
	if !strings.Contains(out, "main.go:3: func main() {") {
		t.Errorf("grep regex missing match: %s", out)
	}

	// 3. Glob filtering
	res, err = HandleGrepSearch(map[string]any{"pattern": "hello", "glob": "*.go"}, wsDir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	out = res.(map[string]any)["output"].(string)
	if !strings.Contains(out, "main.go") {
		t.Errorf("glob *.go missing main.go: %s", out)
	}
	if strings.Contains(out, "doc.txt") {
		t.Errorf("glob *.go should have excluded doc.txt: %s", out)
	}

	// 4. Traversal rejection
	if _, err := HandleGrepSearch(map[string]any{"pattern": "test", "path": "../escaped"}, wsDir); err == nil {
		t.Error("grep_search should reject path traversal")
	}

	// 5. Out-of-bounds symlink skipping
	outsideDir := t.TempDir()
	outsideSecret := filepath.Join(outsideDir, "secret_outside.txt")
	_ = os.WriteFile(outsideSecret, []byte("hello from leaked external file\n"), 0644)
	symlinkPath := filepath.Join(wsDir, "outside_link.txt")
	if err := os.Symlink(outsideSecret, symlinkPath); err == nil {
		res, err := HandleGrepSearch(map[string]any{"pattern": "hello"}, wsDir)
		if err != nil {
			t.Fatalf("grep_search failed on workspace with symlink: %v", err)
		}
		out := res.(map[string]any)["output"].(string)
		if strings.Contains(out, "outside_link.txt") || strings.Contains(out, "secret_outside") {
			t.Errorf("grep_search followed out-of-bounds symlink: %s", out)
		}
	}
}

func TestHandleEditFile(t *testing.T) {
	wsDir := t.TempDir()

	// 1. Basic replacement and diff preview
	filePath := filepath.Join(wsDir, "code.txt")
	orig := "line 1\nline 2\nline 3\n"
	if err := os.WriteFile(filePath, []byte(orig), 0644); err != nil {
		t.Fatal(err)
	}

	res, err := HandleEditFile(map[string]any{
		"path":                "code.txt",
		"target_content":      "line 2",
		"replacement_content": "line 2.1\nline 2.2",
	}, wsDir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	m := res.(map[string]any)
	diff := m["diff"].(string)
	if !strings.Contains(diff, "--- a/code.txt") || !strings.Contains(diff, "-line 2") || !strings.Contains(diff, "+line 2.1") {
		t.Errorf("diff malformed:\n%s", diff)
	}

	updated, _ := os.ReadFile(filePath)
	expected := "line 1\nline 2.1\nline 2.2\nline 3\n"
	if string(updated) != expected {
		t.Errorf("file content = %q; want %q", string(updated), expected)
	}

	// 2. Line ending preservation (CRLF)
	crlfPath := filepath.Join(wsDir, "crlf.txt")
	crlfOrig := "line 1\r\nline 2\r\nline 3\r\n"
	if err := os.WriteFile(crlfPath, []byte(crlfOrig), 0644); err != nil {
		t.Fatal(err)
	}

	// Pass replacement with LF, verify it's converted to CRLF
	_, err = HandleEditFile(map[string]any{
		"path":                "crlf.txt",
		"target_content":      "line 2",
		"replacement_content": "line 2 replaced\nline 2.5",
	}, wsDir)
	if err != nil {
		t.Fatalf("unexpected error on CRLF edit: %v", err)
	}
	crlfUpdated, _ := os.ReadFile(crlfPath)
	expectedCRLF := "line 1\r\nline 2 replaced\r\nline 2.5\r\nline 3\r\n"
	if string(crlfUpdated) != expectedCRLF {
		t.Errorf("CRLF line endings corrupted! got %q; want %q", string(crlfUpdated), expectedCRLF)
	}

	// 3. Target content with LF against CRLF file
	_, err = HandleEditFile(map[string]any{
		"path":                "crlf.txt",
		"target_content":      "line 2 replaced\nline 2.5", // LF in target_content
		"replacement_content": "single line",
	}, wsDir)
	if err != nil {
		t.Fatalf("unexpected error matching LF target against CRLF file: %v", err)
	}

	// 4. Duplicate target_content fails
	dupPath := filepath.Join(wsDir, "dup.txt")
	_ = os.WriteFile(dupPath, []byte("repeat\nmiddle\nrepeat\n"), 0644)
	if _, err := HandleEditFile(map[string]any{
		"path":                "dup.txt",
		"target_content":      "repeat",
		"replacement_content": "new",
	}, wsDir); err == nil {
		t.Error("edit_file should fail when target_content occurs multiple times")
	}

	// 5. Not found target_content fails
	if _, err := HandleEditFile(map[string]any{
		"path":                "dup.txt",
		"target_content":      "missing_pattern",
		"replacement_content": "new",
	}, wsDir); err == nil {
		t.Error("edit_file should fail when target_content is missing")
	}
}

func TestHandleWriteFile(t *testing.T) {
	wsDir := t.TempDir()

	// 1. Create new file with parent directories
	res, err := HandleWriteFile(map[string]any{
		"path":    "a/b/c/new.txt",
		"content": "hello world",
	}, wsDir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	m := res.(map[string]any)
	if m["status"] != "success" {
		t.Errorf("status = %v; want success", m["status"])
	}
	created, err := os.ReadFile(filepath.Join(wsDir, "a", "b", "c", "new.txt"))
	if err != nil || string(created) != "hello world" {
		t.Errorf("file not written properly: %v, content=%q", err, string(created))
	}

	// 2. Existing file without overwrite or append fails
	if _, err := HandleWriteFile(map[string]any{
		"path":    "a/b/c/new.txt",
		"content": "new text",
	}, wsDir); err == nil {
		t.Error("write_file should fail on existing file when neither overwrite nor append is true")
	}

	// 3. Overwrite existing file
	_, err = HandleWriteFile(map[string]any{
		"path":      "a/b/c/new.txt",
		"content":   "overwritten text",
		"overwrite": true,
	}, wsDir)
	if err != nil {
		t.Fatalf("unexpected error with overwrite: %v", err)
	}
	overwritten, _ := os.ReadFile(filepath.Join(wsDir, "a", "b", "c", "new.txt"))
	if string(overwritten) != "overwritten text" {
		t.Errorf("content = %q; want overwritten text", string(overwritten))
	}

	// 4. Append to existing file
	_, err = HandleWriteFile(map[string]any{
		"path":    "a/b/c/new.txt",
		"content": " appended",
		"append":  true,
	}, wsDir)
	if err != nil {
		t.Fatalf("unexpected error with append: %v", err)
	}
	appended, _ := os.ReadFile(filepath.Join(wsDir, "a", "b", "c", "new.txt"))
	if string(appended) != "overwritten text appended" {
		t.Errorf("content = %q; want 'overwritten text appended'", string(appended))
	}

	// 5. Preserving CRLF on existing file
	crlfFile := filepath.Join(wsDir, "existing_crlf.txt")
	_ = os.WriteFile(crlfFile, []byte("line 1\r\n"), 0644)
	_, err = HandleWriteFile(map[string]any{
		"path":      "existing_crlf.txt",
		"content":   "line A\nline B",
		"overwrite": true,
	}, wsDir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	crlfData, _ := os.ReadFile(crlfFile)
	if string(crlfData) != "line A\r\nline B" {
		t.Errorf("write_file overwrite did not preserve CRLF: %q", string(crlfData))
	}

	// 6. Conflicting overwrite + append fails
	if _, err := HandleWriteFile(map[string]any{
		"path":      "existing_crlf.txt",
		"content":   "test",
		"overwrite": true,
		"append":    true,
	}, wsDir); err == nil {
		t.Error("write_file should fail when both overwrite and append are true")
	}
}

func TestHandleRunCommand(t *testing.T) {
	wsDir := t.TempDir()

	// 1. Basic command
	cmdStr := "echo hello"
	res, err := HandleRunCommand(map[string]any{"command": cmdStr}, wsDir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	m := res.(map[string]any)
	stdout := strings.TrimSpace(m["stdout"].(string))
	if stdout != "hello" && stdout != "\"hello\"" {
		t.Errorf("run_command stdout = %q; want hello", stdout)
	}
	if m["exit_code"].(int) != 0 {
		t.Errorf("exit_code = %v; want 0", m["exit_code"])
	}

	// 2. Scrubbed environment check
	t.Setenv("SECRET_DATABASE_URL", "postgres://user:pass@localhost/db")
	t.Setenv("MY_AUTH_TOKEN", "supersecrettoken")
	t.Setenv("API_KEY", "sensitivekey")
	t.Setenv("ADMIN_PASSWORD", "secretpass")
	t.Setenv("AWS_CREDENTIAL", "awscred")
	t.Setenv("SSH_PRIVATE_KEY", "privatekey")
	t.Setenv("GITHUB_PAT", "ghp_123456")
	t.Setenv("SESSION_COOKIE", "sesscookie")
	t.Setenv("SAFE_ENV_VAR", "safevalue")

	scrubbed := scrubEnvironment(os.Environ())
	for _, envEntry := range scrubbed {
		parts := strings.SplitN(envEntry, "=", 2)
		name := strings.ToUpper(parts[0])
		for _, pattern := range scrubPatterns {
			if pattern == "PAT" && (name == "PATH" || name == "PATHEXT" || strings.HasPrefix(name, "PATH_")) {
				continue
			}
			if strings.Contains(name, pattern) {
				t.Errorf("scrubbed environment contains sensitive var %q: %s", pattern, envEntry)
			}
		}
	}
	foundSafe := false
	foundPath := false
	for _, envEntry := range scrubbed {
		if strings.HasPrefix(envEntry, "SAFE_ENV_VAR=") {
			foundSafe = true
		}
		if strings.HasPrefix(strings.ToUpper(envEntry), "PATH=") {
			foundPath = true
		}
	}
	if !foundSafe {
		t.Error("safe environment variable was unexpectedly scrubbed")
	}
	if !foundPath {
		t.Error("PATH environment variable was unexpectedly scrubbed by PAT pattern")
	}

	// 3. Bounded output buffer check
	buf := newBoundedBuffer(50)
	largeData := []byte(strings.Repeat("A", 100))
	n, err := buf.Write(largeData)
	if err != nil || n != len(largeData) {
		t.Fatalf("unexpected write result: n=%d, err=%v", n, err)
	}
	if !strings.Contains(buf.String(), "(output truncated at 4MB)") {
		t.Errorf("expected truncation message in buffer: %s", buf.String())
	}

	// 4. Timeout enforcement and process tree termination
	var timeoutCmd string
	if runtime.GOOS == "windows" {
		timeoutCmd = "ping 127.0.0.1 -n 10 > nul"
	} else {
		timeoutCmd = "sleep 10"
	}

	start := time.Now()
	_, err = HandleRunCommand(map[string]any{
		"command":         timeoutCmd,
		"timeout_seconds": 1,
	}, wsDir)
	elapsed := time.Since(start)

	if err == nil {
		t.Error("expected command to time out, but got no error")
	}
	if elapsed > 4*time.Second {
		t.Errorf("command took %v to time out; expected ~1s", elapsed)
	}
}

func TestCleanExtendedPrefix(t *testing.T) {
	cases := []struct {
		input    string
		expected string
	}{
		{`\\?\UNC\server\share`, `\\server\share`},
		{`\\?\unc\server\share`, `\\server\share`},
		{`\\?\Unc\server\share`, `\\server\share`},
		{`\\?\C:\path\file`, `C:\path\file`},
		{`C:\normal\path`, `C:\normal\path`},
	}
	for _, tc := range cases {
		got := cleanExtendedPrefix(tc.input)
		if got != tc.expected {
			t.Errorf("cleanExtendedPrefix(%q) = %q; want %q", tc.input, got, tc.expected)
		}
	}
}

func TestBuildWorkspaceToolRegistry(t *testing.T) {
	wsDir := t.TempDir()
	reg := BuildWorkspaceToolRegistry(wsDir, false, nil)
	if reg == nil {
		t.Fatal("BuildWorkspaceToolRegistry returned nil")
	}

	expectedTools := []string{
		"read_file",
		"list_dir",
		"grep_search",
		"edit_file",
		"write_file",
		"run_command",
		"ask_question",
	}

	defs := reg.Definitions()
	if len(defs) != len(expectedTools) {
		t.Errorf("registered %d tools; want %d", len(defs), len(expectedTools))
	}

	names := make(map[string]bool)
	for _, def := range defs {
		names[def.Name] = true
	}
	for _, expected := range expectedTools {
		if !names[expected] {
			t.Errorf("missing expected tool: %s", expected)
		}
	}

	// Test executing a tool via registry
	testFile := filepath.Join(wsDir, "registry_test.txt")
	_ = os.WriteFile(testFile, []byte("sample line\n"), 0644)
	outcome := reg.Execute("read_file", map[string]any{"path": "registry_test.txt"})
	if outcome.Error != "" {
		t.Errorf("reg.Execute read_file failed: %s", outcome.Error)
	}
	resMap, ok := outcome.Result.(map[string]any)
	if !ok || !strings.Contains(resMap["content"].(string), "1: sample line") {
		t.Errorf("read_file output unexpected: %v", outcome.Result)
	}
}

func TestPermissionPolicyWorkspaceTools(t *testing.T) {
	ctx := context.Background()

	// 1. Ask mode (non-interactive)
	pAsk := NewPermissionPolicy(PermissionModeAsk, false)
	// read_only allowed
	for _, name := range []string{"read_file", "list_dir", "grep_search", "ask_question"} {
		allowed, _, err := pAsk.CheckPermission(ctx, name, nil, nil)
		if err != nil || !allowed {
			t.Errorf("ask non-interactive should allow %s", name)
		}
	}
	// edit_file denied in non-interactive
	allowed, _, _ := pAsk.CheckPermission(ctx, "edit_file", map[string]any{"path": "foo.go"}, nil)
	if allowed {
		t.Error("ask non-interactive should deny edit_file without interactive approval")
	}
	// run_command denied in non-interactive
	allowed, _, _ = pAsk.CheckPermission(ctx, "run_command", map[string]any{"command": "echo 1"}, nil)
	if allowed {
		t.Error("ask non-interactive should deny run_command without interactive approval")
	}

	// 2. Auto-edit mode (non-interactive)
	pAuto := NewPermissionPolicy(PermissionModeAutoEdit, false)
	// edit_file allowed
	allowed, _, err := pAuto.CheckPermission(ctx, "edit_file", map[string]any{"path": "foo.go"}, nil)
	if err != nil || !allowed {
		t.Errorf("auto-edit should allow edit_file without prompt, got allowed=%v, err=%v", allowed, err)
	}
	// write_file allowed
	allowed, _, err = pAuto.CheckPermission(ctx, "write_file", map[string]any{"path": "foo.go"}, nil)
	if err != nil || !allowed {
		t.Errorf("auto-edit should allow write_file without prompt, got allowed=%v, err=%v", allowed, err)
	}
	// run_command still denied in non-interactive
	allowed, _, _ = pAuto.CheckPermission(ctx, "run_command", map[string]any{"command": "echo 1"}, nil)
	if allowed {
		t.Error("auto-edit non-interactive should deny run_command")
	}

	// 3. Yolo mode (non-interactive)
	pYolo := NewPermissionPolicy(PermissionModeYolo, false)
	for _, name := range []string{"read_file", "edit_file", "write_file", "run_command"} {
		allowed, _, err := pYolo.CheckPermission(ctx, name, nil, nil)
		if err != nil || !allowed {
			t.Errorf("yolo mode should allow %s without prompt", name)
		}
	}
}
