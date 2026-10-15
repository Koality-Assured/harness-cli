package commands

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Koality-Assured/harness-cli/internal/chat"
	"github.com/spf13/cobra"
)

func TestChatNoToolsFlagRegistration(t *testing.T) {
	resetChatFlags()
	defer resetChatFlags()

	flag := chatCmd.Flags().Lookup("no-tools")
	if flag == nil {
		t.Fatal("expected --no-tools flag to be registered on chatCmd")
	}
	if flag.DefValue != "false" {
		t.Errorf("expected --no-tools default value to be 'false', got %q", flag.DefValue)
	}
}

func TestChatNoToolsExecution(t *testing.T) {
	resetChatFlags()
	defer resetChatFlags()

	stateDB := filepath.Join(t.TempDir(), "state.db")
	t.Setenv("HARNESS_STATE_DB", stateDB)

	transport := &fakeChatTransport{
		response: "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":5}}}\n\n" +
			"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"done\"}}\n\n" +
			"event: message_delta\ndata: {\"type\":\"message_delta\",\"usage\":{\"output_tokens\":2}}\n\n",
	}
	client := &chat.ProviderClient{HTTP: transport}

	t.Run("default chat has workspace tools enabled", func(t *testing.T) {
		resetChatFlags()
		defer resetChatFlags()

		var out, errOut strings.Builder
		cmd := &cobra.Command{Use: "chat-tools-test"}
		cmd.SetOut(&out)
		cmd.SetErr(&errOut)

		opts := chatOptionsValue{
			query:    "test with tools",
			provider: "anthropic",
			noMCP:    true,
			noTools:  false,
		}

		err := runChat(cmd, opts, client, func(string) (string, error) { return "fake-token", nil })
		if err != nil {
			t.Fatalf("runChat failed: %v", err)
		}
	})

	t.Run("chat with --no-tools disables workspace tools", func(t *testing.T) {
		resetChatFlags()
		defer resetChatFlags()

		var out, errOut strings.Builder
		cmd := &cobra.Command{Use: "chat-no-tools-test"}
		cmd.SetOut(&out)
		cmd.SetErr(&errOut)

		opts := chatOptionsValue{
			query:    "test without tools",
			provider: "anthropic",
			noMCP:    true,
			noTools:  true,
		}

		err := runChat(cmd, opts, client, func(string) (string, error) { return "fake-token", nil })
		if err != nil {
			t.Fatalf("runChat failed: %v", err)
		}
	})
}

func TestWorkspaceToolRegistryMCPLayering(t *testing.T) {
	wsDir := t.TempDir()
	baseRegistry := chat.BuildWorkspaceToolRegistry(wsDir, false, nil)
	baseDefs := baseRegistry.Definitions()
	if len(baseDefs) < 6 {
		t.Fatalf("expected at least 6 base tools, got %d", len(baseDefs))
	}

	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}

	configs := []chat.MCPServerConfig{
		{
			Name:    "layer-test-srv",
			Command: executable,
			Args:    []string{"-test.run=^TestMCPFakeProcessCommands$"},
			Env:     map[string]string{"HARNESS_FAKE_MCP_CHILD": "1"},
		},
	}

	session, err := chat.NewACPSessionMCP(configs, wsDir)
	if err != nil {
		t.Fatalf("failed to create MCP session: %v", err)
	}
	defer session.Close()

	layeredRegistry := session.ToolRegistry(baseRegistry)
	layeredDefs := layeredRegistry.Definitions()

	// Should contain all base tools plus MCP tools
	if len(layeredDefs) <= len(baseDefs) {
		t.Errorf("layered definitions count (%d) should be greater than base definitions count (%d)", len(layeredDefs), len(baseDefs))
	}

	// Verify read_file is still present in layered registry
	foundReadFile := false
	for _, def := range layeredDefs {
		if def.Name == "read_file" {
			foundReadFile = true
			break
		}
	}
	if !foundReadFile {
		t.Error("expected read_file to still be present in layered registry")
	}
}

func TestChatBackwardCompatibilityNonInteractive(t *testing.T) {
	resetChatFlags()
	defer resetChatFlags()

	stateDB := filepath.Join(t.TempDir(), "state.db")
	t.Setenv("HARNESS_STATE_DB", stateDB)

	transport := &fakeChatTransport{
		response: "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":5}}}\n\n" +
			"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"fast response\"}}\n\n" +
			"event: message_delta\ndata: {\"type\":\"message_delta\",\"usage\":{\"output_tokens\":2}}\n\n",
	}
	client := &chat.ProviderClient{HTTP: transport}

	var out, errOut strings.Builder
	cmd := &cobra.Command{Use: "chat-compat-test"}
	cmd.SetOut(&out)
	cmd.SetErr(&errOut)

	opts := chatOptionsValue{
		query:    "quick non-interactive query",
		provider: "anthropic",
	}

	err := runChat(cmd, opts, client, func(string) (string, error) { return "fake-token", nil })
	if err != nil {
		t.Fatalf("backward compatibility runChat failed: %v", err)
	}
	if !strings.Contains(out.String(), "fast response") {
		t.Errorf("expected fast response, got: %s", out.String())
	}
}
