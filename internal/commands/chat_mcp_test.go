package commands

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Koality-Assured/harness-cli/internal/chat"
	"github.com/spf13/cobra"
)

// TestMCPFakeProcessCommands is invoked as a child process by fake MCP tests.
func TestMCPFakeProcessCommands(t *testing.T) {
	if os.Getenv("HARNESS_FAKE_MCP_CHILD") != "1" {
		return
	}
	defer os.Exit(0)
	reader := bufio.NewScanner(os.Stdin)
	reader.Buffer(make([]byte, 4096), chat.MCPMaxMessageBytes)
	for reader.Scan() {
		var request map[string]any
		decoder := json.NewDecoder(strings.NewReader(reader.Text()))
		decoder.UseNumber()
		if decoder.Decode(&request) != nil {
			return
		}
		method, _ := request["method"].(string)
		id := request["id"]
		params, _ := request["params"].(map[string]any)
		switch method {
		case "server/discover":
			writeFakeMCPResponse(os.Stdout, map[string]any{
				"jsonrpc": "2.0", "id": id,
				"result": map[string]any{
					"resultType":        "complete",
					"supportedVersions": []string{chat.MCPModernVersion},
					"capabilities":      map[string]any{"tools": map[string]any{}},
				},
			})
		case "initialize":
			writeFakeMCPResponse(os.Stdout, map[string]any{
				"jsonrpc": "2.0", "id": id,
				"result": map[string]any{
					"protocolVersion": chat.MCPLegacyVersion,
					"capabilities":    map[string]any{"tools": map[string]any{}},
				},
			})
		case "tools/list":
			writeFakeMCPResponse(os.Stdout, map[string]any{
				"jsonrpc": "2.0", "id": id,
				"result": map[string]any{
					"resultType": "complete",
					"tools": []any{
						map[string]any{
							"name":        "echo",
							"description": "return the argument",
							"inputSchema": map[string]any{
								"type": "object",
								"properties": map[string]any{
									"value": map[string]any{"type": "string"},
								},
								"required": []string{"value"},
							},
						},
					},
				},
			})
		case "tools/call":
			arguments, _ := params["arguments"].(map[string]any)
			val := ""
			if arguments != nil {
				val, _ = arguments["value"].(string)
			}
			writeFakeMCPResponse(os.Stdout, map[string]any{
				"jsonrpc": "2.0", "id": id,
				"result": map[string]any{
					"resultType": "complete",
					"content": []any{
						map[string]any{
							"type": "text",
							"text": val,
						},
					},
				},
			})
		default:
			if id != nil {
				writeFakeMCPResponse(os.Stdout, map[string]any{
					"jsonrpc": "2.0", "id": id,
					"error": map[string]any{
						"code":    -32601,
						"message": "method not found",
					},
				})
			}
		}
	}
}

func writeFakeMCPResponse(w io.Writer, resp map[string]any) {
	data, _ := json.Marshal(resp)
	_, _ = w.Write(append(data, '\n'))
}

func TestDiscoverMCPConfig(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}

	t.Run("explicit path", func(t *testing.T) {
		dir := t.TempDir()
		cfgPath := filepath.Join(dir, "custom-mcp.json")
		content := fmt.Sprintf(`{
			"mcpServers": {
				"test-srv": {
					"command": %q,
					"args": ["-test.run=^TestMCPFakeProcessCommands$"],
					"env": {"TEST_VAR": "val"}
				}
			}
		}`, executable)
		if err := os.WriteFile(cfgPath, []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
		configs, err := chat.DiscoverMCPConfig("", cfgPath)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(configs) != 1 {
			t.Fatalf("expected 1 server config, got %d", len(configs))
		}
		if configs[0].Name != "test-srv" {
			t.Errorf("expected name 'test-srv', got %q", configs[0].Name)
		}
		if configs[0].Command != filepath.Clean(executable) {
			t.Errorf("expected command %q, got %q", filepath.Clean(executable), configs[0].Command)
		}
		if configs[0].Env["TEST_VAR"] != "val" {
			t.Errorf("expected env TEST_VAR=val, got %v", configs[0].Env)
		}
	})

	t.Run("missing file", func(t *testing.T) {
		dir := t.TempDir()
		// Missing explicit file returns error
		_, err := chat.DiscoverMCPConfig("", filepath.Join(dir, "nonexistent.json"))
		if err == nil {
			t.Fatal("expected error on missing explicit file, got nil")
		}
		if !strings.Contains(err.Error(), "explicit MCP config file not found:") {
			t.Fatalf("expected error containing 'explicit MCP config file not found:', got: %v", err)
		}

		// Missing workspace root file returns empty slice without error
		configs, err := chat.DiscoverMCPConfig(dir, "")
		if err != nil {
			t.Fatalf("expected nil error on missing workspace file, got: %v", err)
		}
		if len(configs) != 0 {
			t.Fatalf("expected 0 configs, got %d", len(configs))
		}
	})

	t.Run("JSON formats", func(t *testing.T) {
		dir := t.TempDir()

		// Format 1: {"mcpServers": {<name>: ...}}
		f1 := filepath.Join(dir, "fmt1.json")
		_ = os.WriteFile(f1, []byte(fmt.Sprintf(`{"mcpServers": {"srv1": {"command": %q}}}`, executable)), 0600)
		c1, err := chat.DiscoverMCPConfig("", f1)
		if err != nil || len(c1) != 1 || c1[0].Name != "srv1" {
			t.Fatalf("fmt1 failed: err=%v, configs=%v", err, c1)
		}

		// Format 2: {"servers": {<name>: ...}}
		f2 := filepath.Join(dir, "fmt2.json")
		_ = os.WriteFile(f2, []byte(fmt.Sprintf(`{"servers": {"srv2": {"command": %q}}}`, executable)), 0600)
		c2, err := chat.DiscoverMCPConfig("", f2)
		if err != nil || len(c2) != 1 || c2[0].Name != "srv2" {
			t.Fatalf("fmt2 failed: err=%v, configs=%v", err, c2)
		}

		// Format 3: Direct map {<name>: ...}
		f3 := filepath.Join(dir, "fmt3.json")
		_ = os.WriteFile(f3, []byte(fmt.Sprintf(`{"srv3": {"command": %q}}`, executable)), 0600)
		c3, err := chat.DiscoverMCPConfig("", f3)
		if err != nil || len(c3) != 1 || c3[0].Name != "srv3" {
			t.Fatalf("fmt3 failed: err=%v, configs=%v", err, c3)
		}

		// Format 4: Array format [{"name": "srv4", "command": ...}]
		f4 := filepath.Join(dir, "fmt4.json")
		_ = os.WriteFile(f4, []byte(fmt.Sprintf(`[{"name": "srv4", "command": %q}]`, executable)), 0600)
		c4, err := chat.DiscoverMCPConfig("", f4)
		if err != nil || len(c4) != 1 || c4[0].Name != "srv4" {
			t.Fatalf("fmt4 failed: err=%v, configs=%v", err, c4)
		}
	})

	t.Run("invalid commands", func(t *testing.T) {
		dir := t.TempDir()

		// Missing command
		f1 := filepath.Join(dir, "bad1.json")
		_ = os.WriteFile(f1, []byte(`{"mcpServers": {"bad": {}}}`), 0600)
		if _, err := chat.DiscoverMCPConfig("", f1); err == nil {
			t.Error("expected error for missing command")
		}

		// Non-existent command in PATH
		f2 := filepath.Join(dir, "bad2.json")
		_ = os.WriteFile(f2, []byte(`{"mcpServers": {"bad": {"command": "definitely_not_a_valid_binary_12345"}}}`), 0600)
		if _, err := chat.DiscoverMCPConfig("", f2); err == nil {
			t.Error("expected error for nonexistent command in PATH")
		}

		// Insecure relative command with slash (CWE-426)
		f3 := filepath.Join(dir, "bad3.json")
		_ = os.WriteFile(f3, []byte(`{"mcpServers": {"bad": {"command": "./some_relative_bin"}}}`), 0600)
		if _, err := chat.DiscoverMCPConfig("", f3); err == nil {
			t.Error("expected error for relative command (CWE-426)")
		}

		// Invalid JSON
		f4 := filepath.Join(dir, "bad4.json")
		_ = os.WriteFile(f4, []byte(`{not valid json}`), 0600)
		if _, err := chat.DiscoverMCPConfig("", f4); err == nil {
			t.Error("expected error for malformed JSON")
		}
	})
}

func TestChatMCPSlashCommands(t *testing.T) {
	stateDB := filepath.Join(t.TempDir(), "state.db")
	t.Setenv("HARNESS_STATE_DB", stateDB)

	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}

	cfg := chat.MCPServerConfig{
		Name:    "fake-mcp",
		Command: executable,
		Args:    []string{"-test.run=^TestMCPFakeProcessCommands$"},
		Env:     map[string]string{"HARNESS_FAKE_MCP_CHILD": "1"},
	}

	session, err := chat.NewACPSessionMCP([]chat.MCPServerConfig{cfg}, t.TempDir())
	if err != nil {
		t.Fatalf("failed to start fake MCP session: %v", err)
	}
	defer session.Close()

	tools := session.ToolRegistry(chat.NewToolRegistry())

	store, err := chat.OpenSessionStore(stateDB)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	sess, err := store.CreateSession(chat.Session{Title: "test-session", Provider: "local", Model: "test-model"})
	if err != nil {
		t.Fatal(err)
	}

	model := "test-model"
	mcpState := &chatMCPState{
		session: session,
		tools:   tools,
	}

	t.Run("/mcp and /mcp list", func(t *testing.T) {
		var out strings.Builder
		cmd := &cobra.Command{}
		cmd.SetOut(&out)

		handled, exit := handleChatSlash(cmd, store, &sess, &model, "local", "", nil, "/mcp", mcpState)
		if !handled || exit {
			t.Fatalf("expected handled=true, exit=false; got handled=%v, exit=%v", handled, exit)
		}
		output := out.String()
		if !strings.Contains(output, "fake-mcp") || !strings.Contains(output, "connected") {
			t.Fatalf("expected /mcp output to contain server name and connected status, got: %s", output)
		}

		out.Reset()
		handled, exit = handleChatSlash(cmd, store, &sess, &model, "local", "", nil, "/mcp list", mcpState)
		if !handled || exit {
			t.Fatalf("expected handled=true, exit=false")
		}
		if !strings.Contains(out.String(), "fake-mcp") || !strings.Contains(out.String(), "connected") {
			t.Fatalf("expected /mcp list output to contain server name and connected status, got: %s", out.String())
		}
	})

	t.Run("/mcp tools", func(t *testing.T) {
		var out strings.Builder
		cmd := &cobra.Command{}
		cmd.SetOut(&out)

		handled, exit := handleChatSlash(cmd, store, &sess, &model, "local", "", nil, "/mcp tools", mcpState)
		if !handled || exit {
			t.Fatalf("expected handled=true, exit=false")
		}
		output := out.String()
		if !strings.Contains(output, "echo") || !strings.Contains(output, "return the argument") {
			t.Fatalf("expected /mcp tools output to list active tool and description, got: %s", output)
		}
	})

	t.Run("/mcp reload", func(t *testing.T) {
		dir := t.TempDir()
		cfgPath := filepath.Join(dir, "reload-mcp.json")
		content := fmt.Sprintf(`{
			"mcpServers": {
				"reloaded-srv": {
					"command": %q,
					"args": ["-test.run=^TestMCPFakeProcessCommands$"],
					"env": {"HARNESS_FAKE_MCP_CHILD": "1"}
				}
			}
		}`, executable)
		if err := os.WriteFile(cfgPath, []byte(content), 0600); err != nil {
			t.Fatal(err)
		}

		mcpState.configPath = cfgPath

		var out strings.Builder
		cmd := &cobra.Command{}
		cmd.SetOut(&out)

		handled, exit := handleChatSlash(cmd, store, &sess, &model, "local", "", nil, "/mcp reload", mcpState)
		if !handled || exit {
			t.Fatalf("expected handled=true, exit=false")
		}
		if !strings.Contains(out.String(), "MCP reloaded") || !strings.Contains(out.String(), "1 server(s) active") {
			t.Fatalf("expected reload confirmation, got: %s", out.String())
		}
		if mcpState.session == nil {
			t.Fatal("expected reloaded session to be non-nil")
		}
		statuses := mcpState.session.ServerStatuses()
		if len(statuses) != 1 || statuses[0].Name != "reloaded-srv" {
			t.Fatalf("expected reloaded server name 'reloaded-srv', got: %v", statuses)
		}
	})

	t.Run("/mcp reload failure preserves existing session", func(t *testing.T) {
		origSession := mcpState.session
		origTools := mcpState.tools

		dir := t.TempDir()
		badCfgPath := filepath.Join(dir, "bad-reload-mcp.json")
		_ = os.WriteFile(badCfgPath, []byte(`{not valid json}`), 0600)
		mcpState.configPath = badCfgPath

		var out, errOut strings.Builder
		cmd := &cobra.Command{}
		cmd.SetOut(&out)
		cmd.SetErr(&errOut)

		handled, exit := handleChatSlash(cmd, store, &sess, &model, "local", "", nil, "/mcp reload", mcpState)
		if !handled || exit {
			t.Fatalf("expected handled=true, exit=false")
		}
		if !strings.Contains(errOut.String(), "MCP reload discovery failed") {
			t.Fatalf("expected error output on reload failure, got: %s", errOut.String())
		}
		if mcpState.session != origSession {
			t.Fatal("expected original session to be preserved after failed reload")
		}
		if mcpState.tools != origTools {
			t.Fatal("expected original tools to be preserved after failed reload")
		}
	})

	t.Run("/mcp reload workspace denied preserves existing session", func(t *testing.T) {
		origSession := mcpState.session

		wsDir := t.TempDir()
		wsMCP := filepath.Join(wsDir, ".mcp.json")
		_ = os.WriteFile(wsMCP, []byte(fmt.Sprintf(`{"mcpServers": {"ws-srv": {"command": %q, "args": ["-test.run=^TestMCPFakeProcessCommands$"], "env": {"HARNESS_FAKE_MCP_CHILD": "1"}}}}`, executable)), 0600)

		oldWd, _ := os.Getwd()
		_ = os.Chdir(wsDir)
		t.Cleanup(func() {
			_ = os.Chdir(oldWd)
		})

		mcpState.configPath = "" // trigger workspace discovery

		var out, errOut strings.Builder
		cmd := &cobra.Command{}
		cmd.SetOut(&out)
		cmd.SetErr(&errOut)
		cmd.SetIn(strings.NewReader("n\n")) // Deny trust!

		handled, exit := handleChatSlash(cmd, store, &sess, &model, "local", "", nil, "/mcp reload", mcpState)
		if !handled || exit {
			t.Fatalf("expected handled=true, exit=false")
		}
		if !strings.Contains(out.String(), "Workspace MCP configuration untrusted") {
			t.Fatalf("expected untrusted notice, got: %s", out.String())
		}
		if mcpState.session != origSession {
			t.Fatal("expected original session to be preserved after denied reload")
		}
	})

	t.Run("/mcp reload workspace approved updates session", func(t *testing.T) {
		wsDir := t.TempDir()
		wsMCP := filepath.Join(wsDir, ".mcp.json")
		_ = os.WriteFile(wsMCP, []byte(fmt.Sprintf(`{"mcpServers": {"ws-approved": {"command": %q, "args": ["-test.run=^TestMCPFakeProcessCommands$"], "env": {"HARNESS_FAKE_MCP_CHILD": "1"}}}}`, executable)), 0600)

		oldWd, _ := os.Getwd()
		_ = os.Chdir(wsDir)
		t.Cleanup(func() {
			_ = os.Chdir(oldWd)
			if mcpState.session != nil {
				mcpState.session.Close()
				mcpState.session = nil
			}
		})

		mcpState.configPath = ""

		var out, errOut strings.Builder
		cmd := &cobra.Command{}
		cmd.SetOut(&out)
		cmd.SetErr(&errOut)
		cmd.SetIn(strings.NewReader("y\n")) // Approve trust!

		handled, exit := handleChatSlash(cmd, store, &sess, &model, "local", "", nil, "/mcp reload", mcpState)
		if !handled || exit {
			t.Fatalf("expected handled=true, exit=false")
		}
		if !strings.Contains(out.String(), "MCP reloaded") {
			t.Fatalf("expected reload confirmation, got: %s", out.String())
		}
		if mcpState.session == nil || len(mcpState.session.ServerStatuses()) == 0 || mcpState.session.ServerStatuses()[0].Name != "ws-approved" {
			t.Fatalf("expected ws-approved server, got: %v", mcpState.session.ServerStatuses())
		}
	})
}

func TestChatOneShotWithMCPClientCleanup(t *testing.T) {
	t.Cleanup(resetChatFlags)
	resetChatFlags()

	stateDB := filepath.Join(t.TempDir(), "state.db")
	t.Setenv("HARNESS_STATE_DB", stateDB)

	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}

	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "oneshot-mcp.json")
	content := fmt.Sprintf(`{
		"mcpServers": {
			"oneshot-srv": {
				"command": %q,
				"args": ["-test.run=^TestMCPFakeProcessCommands$"],
				"env": {"HARNESS_FAKE_MCP_CHILD": "1"}
			}
		}
	}`, executable)
	if err := os.WriteFile(cfgPath, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}

	transport := &fakeChatTransport{
		response: "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":5}}}\n\n" +
			"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"mcp reply\"}}\n\n" +
			"event: message_delta\ndata: {\"type\":\"message_delta\",\"usage\":{\"output_tokens\":2}}\n\n",
	}
	client := &chat.ProviderClient{HTTP: transport}

	var output, errorOutput strings.Builder
	command := &cobra.Command{Use: "chat-mcp-test"}
	command.SetOut(&output)
	command.SetErr(&errorOutput)

	options := chatOptionsValue{
		query:     "test query with mcp",
		provider:  "anthropic",
		mcpConfig: cfgPath,
	}

	err = runChat(command, options, client, func(string) (string, error) { return "fake-token", nil })
	if err != nil {
		t.Fatalf("runChat failed: %v", err)
	}

	if !strings.Contains(output.String(), "mcp reply") {
		t.Fatalf("expected output to contain 'mcp reply', got: %s", output.String())
	}

	t.Run("no-mcp flag disables discovery", func(t *testing.T) {
		optionsNoMCP := chatOptionsValue{
			query:     "test query with no-mcp",
			provider:  "anthropic",
			mcpConfig: cfgPath,
			noMCP:     true,
		}
		var out strings.Builder
		cmd := &cobra.Command{}
		cmd.SetOut(&out)
		err := runChat(cmd, optionsNoMCP, client, func(string) (string, error) { return "fake-token", nil })
		if err != nil {
			t.Fatalf("runChat failed: %v", err)
		}
	})

	t.Run("one-shot ignores workspace .mcp.json without explicit --mcp-config", func(t *testing.T) {
		wsDir := t.TempDir()
		wsMCP := filepath.Join(wsDir, ".mcp.json")
		_ = os.WriteFile(wsMCP, []byte(fmt.Sprintf(`{"mcpServers": {"ws-srv": {"command": %q, "args": ["-test.run=^TestMCPFakeProcessCommands$"], "env": {"HARNESS_FAKE_MCP_CHILD": "1"}}}}`, executable)), 0600)

		oldWd, _ := os.Getwd()
		_ = os.Chdir(wsDir)
		t.Cleanup(func() {
			_ = os.Chdir(oldWd)
		})

		resetChatFlags()
		t.Cleanup(resetChatFlags)

		var out strings.Builder
		cmd := &cobra.Command{}
		cmd.SetOut(&out)

		opts := chatOptionsValue{
			query:    "test query ignoring workspace mcp",
			provider: "anthropic",
		}
		err := runChat(cmd, opts, client, func(string) (string, error) { return "fake-token", nil })
		if err != nil {
			t.Fatalf("runChat failed: %v", err)
		}
		if !strings.Contains(out.String(), "mcp reply") {
			t.Fatalf("expected output, got: %s", out.String())
		}
	})

	t.Run("one-shot loads workspace .mcp.json when explicitly passed in --mcp-config", func(t *testing.T) {
		wsDir := t.TempDir()
		wsMCP := filepath.Join(wsDir, ".mcp.json")
		_ = os.WriteFile(wsMCP, []byte(fmt.Sprintf(`{"mcpServers": {"ws-explicit": {"command": %q, "args": ["-test.run=^TestMCPFakeProcessCommands$"], "env": {"HARNESS_FAKE_MCP_CHILD": "1"}}}}`, executable)), 0600)

		resetChatFlags()
		defer resetChatFlags()

		var out strings.Builder
		cmd := &cobra.Command{}
		cmd.SetOut(&out)

		opts := chatOptionsValue{
			query:     "test query with explicit mcp-config",
			provider:  "anthropic",
			mcpConfig: wsMCP,
		}
		err := runChat(cmd, opts, client, func(string) (string, error) { return "fake-token", nil })
		if err != nil {
			t.Fatalf("runChat failed: %v", err)
		}
		if !strings.Contains(out.String(), "mcp reply") {
			t.Fatalf("expected output, got: %s", out.String())
		}
	})
}

func TestPromptWorkspaceMCPTrust(t *testing.T) {
	tests := []struct {
		input    string
		expected bool
	}{
		{"y\n", true},
		{"Y\n", true},
		{"yes\n", true},
		{"YES\n", true},
		{"n\n", false},
		{"no\n", false},
		{"\n", false},
		{"anything else\n", false},
		{"y", true},
	}
	for _, tc := range tests {
		var out strings.Builder
		res := promptWorkspaceMCPTrust(strings.NewReader(tc.input), &out, "/test/path/.mcp.json")
		if res != tc.expected {
			t.Errorf("promptWorkspaceMCPTrust(%q) = %v, expected %v", tc.input, res, tc.expected)
		}
		if !strings.Contains(out.String(), "Trust workspace MCP configuration at /test/path/.mcp.json? [y/N]: ") {
			t.Errorf("prompt output missing expected prompt string, got: %q", out.String())
		}
	}
}
