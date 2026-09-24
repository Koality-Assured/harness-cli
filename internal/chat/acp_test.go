package chat

import (
	"bufio"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type acpCancelAwareProvider struct {
	started  chan struct{}
	canceled chan struct{}
}

func (p *acpCancelAwareProvider) Do(request *http.Request) (*http.Response, error) {
	close(p.started)
	<-request.Context().Done()
	close(p.canceled)
	return nil, request.Context().Err()
}

func TestACPStdioMCPToolRoundTripAndNativeHistory(t *testing.T) {
	toolResponse := "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":2}}}\n\n" +
		"event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"tool_use\",\"id\":\"provider-tool-1\",\"name\":\"mcp0_0_echo\",\"input\":{}}}\n\n" +
		"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"input_json_delta\",\"partial_json\":\"{\\\"value\\\":\\\"from ACP\\\"}\"}}\n\n" +
		"event: message_delta\ndata: {\"type\":\"message_delta\",\"usage\":{\"output_tokens\":1}}\n\n"
	fakeProvider := &gatewayFakeProvider{answers: []string{toolResponse, anthroTextStream("MCP answered", 5, 2)}}
	runtime, err := NewAdapterRuntime(AdapterRuntimeConfig{
		Provider: "anthropic", Model: "claude-test", DBPath: filepath.Join(t.TempDir(), "state.db"),
		Client:             &ProviderClient{HTTP: fakeProvider},
		CredentialResolver: func(string) (string, error) { return "fake-provider-token", nil },
	})
	if err != nil {
		t.Fatal(err)
	}

	inputReader, inputWriter := io.Pipe()
	outputReader, outputWriter := io.Pipe()
	runResult := make(chan error, 1)
	go func() {
		defer outputWriter.Close()
		runResult <- RunACPStdio(runtime, inputReader, outputWriter)
	}()
	lines := make(chan map[string]any, 32)
	go func() {
		scanner := bufio.NewScanner(outputReader)
		for scanner.Scan() {
			var message map[string]any
			decoder := json.NewDecoder(strings.NewReader(scanner.Text()))
			decoder.UseNumber()
			if decoder.Decode(&message) == nil {
				lines <- message
			}
		}
		close(lines)
	}()
	writeACPTestMessage(t, inputWriter, map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": map[string]any{"protocolVersion": 1},
	})
	if message := awaitACPResponse(t, lines, "1"); message["error"] != nil {
		t.Fatalf("ACP initialize response = %#v", message)
	}

	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cwd, err := filepath.Abs(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	writeACPTestMessage(t, inputWriter, map[string]any{
		"jsonrpc": "2.0", "id": 2, "method": "session/new", "params": map[string]any{
			"cwd": cwd,
			"mcpServers": []any{map[string]any{
				"name": "fake", "command": executable,
				"args": []any{"-test.run=^TestMCPFakeProcess$"},
				"env":  []any{map[string]any{"name": "HARNESS_FAKE_MCP_MODE", "value": "modern"}},
			}},
		},
	})
	newResponse := awaitACPResponse(t, lines, "2")
	if newResponse["error"] != nil {
		t.Fatalf("ACP session/new response = %#v", newResponse)
	}
	result, ok := newResponse["result"].(map[string]any)
	if !ok {
		t.Fatalf("ACP session/new result = %#v", newResponse["result"])
	}
	handle, ok := result["sessionId"].(string)
	if !ok || handle == "" {
		t.Fatalf("ACP session handle = %#v", result["sessionId"])
	}

	writeACPTestMessage(t, inputWriter, map[string]any{
		"jsonrpc": "2.0", "id": 3, "method": "session/prompt", "params": map[string]any{
			"sessionId": handle, "prompt": []any{map[string]any{"type": "text", "text": "ask MCP"}},
		},
	})
	var toolCallUpdate, toolResultUpdate bool
	for {
		message := nextACPMessage(t, lines)
		if message["method"] == "session/update" {
			params, _ := message["params"].(map[string]any)
			update, _ := params["update"].(map[string]any)
			switch update["sessionUpdate"] {
			case "tool_call":
				toolCallUpdate = true
			case "tool_call_update":
				toolResultUpdate = true
			}
			continue
		}
		if message["id"] != json.Number("3") {
			continue
		}
		if message["error"] != nil {
			t.Fatalf("ACP prompt response = %#v", message)
		}
		promptResult, _ := message["result"].(map[string]any)
		if promptResult["stopReason"] != "end_turn" {
			t.Fatalf("ACP prompt stop reason = %#v", promptResult)
		}
		break
	}
	if !toolCallUpdate || !toolResultUpdate {
		t.Fatalf("ACP did not report tool lifecycle: call=%v result=%v", toolCallUpdate, toolResultUpdate)
	}
	if len(fakeProvider.requests) != 2 || fakeProvider.requests[0].URL.String() != "https://api.anthropic.com/v1/messages" {
		t.Fatalf("provider fake transport calls = %#v", fakeProvider.requests)
	}
	if strings.Contains(fakeProvider.requests[0].URL.String(), "invalid") || !strings.Contains(string(fakeProvider.bodies[0]), "mcp0_0_echo") || !strings.Contains(string(fakeProvider.bodies[1]), "tool_result") || !strings.Contains(string(fakeProvider.bodies[1]), "from ACP") {
		t.Fatalf("provider native MCP round trip was not transported through fake requests: %s / %s", fakeProvider.bodies[0], fakeProvider.bodies[1])
	}
	history, err := runtime.History("acp", "stdio", ACPRoute, handle)
	if err != nil {
		t.Fatal(err)
	}
	if len(history) != 4 || history[1].ProviderData == nil || history[2].ProviderData == nil {
		t.Fatalf("ACP persisted native tool history = %#v", history)
	}

	writeACPTestMessage(t, inputWriter, map[string]any{"jsonrpc": "2.0", "id": 4, "method": "session/close", "params": map[string]any{"sessionId": handle}})
	if message := awaitACPResponse(t, lines, "4"); message["error"] != nil {
		t.Fatalf("ACP session/close response = %#v", message)
	}
	_ = inputWriter.Close()
	select {
	case err := <-runResult:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("ACP stdio server did not stop after input closed")
	}
	_ = outputReader.Close()
}

func TestACPSessionLoadCancelsAndWaitsForActivePromptBeforeReplacingMCP(t *testing.T) {
	provider := &acpCancelAwareProvider{started: make(chan struct{}), canceled: make(chan struct{})}
	dbPath := filepath.Join(t.TempDir(), "state.db")
	runtime, err := NewAdapterRuntime(AdapterRuntimeConfig{
		Provider: "anthropic", Model: "claude-test", DBPath: dbPath,
		Client:             &ProviderClient{HTTP: provider},
		CredentialResolver: func(string) (string, error) { return "fake-provider-token", nil },
	})
	if err != nil {
		t.Fatal(err)
	}

	inputReader, inputWriter := io.Pipe()
	outputReader, outputWriter := io.Pipe()
	runResult := make(chan error, 1)
	runFinished := make(chan struct{})
	go func() {
		defer outputWriter.Close()
		runResult <- RunACPStdio(runtime, inputReader, outputWriter)
		close(runFinished)
	}()
	lines := make(chan map[string]any, 32)
	go func() {
		scanner := bufio.NewScanner(outputReader)
		for scanner.Scan() {
			var message map[string]any
			decoder := json.NewDecoder(strings.NewReader(scanner.Text()))
			decoder.UseNumber()
			if decoder.Decode(&message) == nil {
				lines <- message
			}
		}
		close(lines)
	}()
	t.Cleanup(func() {
		_ = inputWriter.Close()
		_ = outputReader.Close()
		select {
		case <-runFinished:
		case <-time.After(2 * ACPCloseTimeout):
			t.Error("ACP stdio server did not stop during test cleanup")
		}
	})

	writeACPTestMessage(t, inputWriter, map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": map[string]any{"protocolVersion": 1},
	})
	if response := awaitACPResponse(t, lines, "1"); response["error"] != nil {
		t.Fatalf("ACP initialize response = %#v", response)
	}

	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cwd, err := filepath.Abs(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	eofMarker := filepath.Join(t.TempDir(), "old-mcp-eof")
	oldServer := map[string]any{
		"name": "old", "command": executable,
		"args": []any{"-test.run=^TestMCPFakeProcess$"},
		"env": []any{
			map[string]any{"name": "HARNESS_FAKE_MCP_MODE", "value": "modern"},
			map[string]any{"name": "HARNESS_FAKE_MCP_EOF", "value": eofMarker},
		},
	}
	writeACPTestMessage(t, inputWriter, map[string]any{
		"jsonrpc": "2.0", "id": 2, "method": "session/new", "params": map[string]any{
			"cwd": cwd, "mcpServers": []any{oldServer},
		},
	})
	newResponse := awaitACPResponse(t, lines, "2")
	if newResponse["error"] != nil {
		t.Fatalf("ACP session/new response = %#v", newResponse)
	}
	handle := newResponse["result"].(map[string]any)["sessionId"].(string)
	writeACPTestMessage(t, inputWriter, map[string]any{
		"jsonrpc": "2.0", "id": 3, "method": "session/prompt", "params": map[string]any{
			"sessionId": handle, "prompt": []any{map[string]any{"type": "text", "text": "wait for provider"}},
		},
	})
	select {
	case <-provider.started:
	case <-time.After(3 * time.Second):
		t.Fatal("provider prompt did not become active")
	}

	writeACPTestMessage(t, inputWriter, map[string]any{
		"jsonrpc": "2.0", "id": 4, "method": "session/load", "params": map[string]any{
			"sessionId": handle, "cwd": cwd, "mcpServers": []any{},
		},
	})
	var promptResponse, loadResponse map[string]any
	var responseOrder []string
	for promptResponse == nil || loadResponse == nil {
		message := nextACPMessage(t, lines)
		id, ok := message["id"].(json.Number)
		if !ok {
			continue
		}
		switch id.String() {
		case "3":
			promptResponse = message
			responseOrder = append(responseOrder, "prompt")
		case "4":
			loadResponse = message
			responseOrder = append(responseOrder, "load")
		}
	}
	if len(responseOrder) != 2 || responseOrder[0] != "prompt" || responseOrder[1] != "load" {
		t.Fatalf("session/load response order = %#v", responseOrder)
	}
	if promptResponse["error"] != nil || promptResponse["result"].(map[string]any)["stopReason"] != "cancelled" {
		t.Fatalf("active prompt cancellation = %#v", promptResponse)
	}
	if loadResponse["error"] != nil {
		t.Fatalf("session/load response = %#v", loadResponse)
	}
	select {
	case <-provider.canceled:
	case <-time.After(time.Second):
		t.Fatal("session/load did not cancel the active provider request")
	}
	if _, err := os.Stat(eofMarker); err != nil {
		t.Fatalf("previous MCP process was not closed after the prompt stopped: %v", err)
	}
	_ = inputWriter.Close()
	select {
	case err := <-runResult:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("ACP stdio server did not stop after input closed")
	}
}

func writeACPTestMessage(t *testing.T, writer io.Writer, message map[string]any) {
	t.Helper()
	encoded, err := json.Marshal(message)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := writer.Write(append(encoded, '\n')); err != nil {
		t.Fatal(err)
	}
}

func awaitACPResponse(t *testing.T, messages <-chan map[string]any, id string) map[string]any {
	t.Helper()
	for {
		message := nextACPMessage(t, messages)
		if got, ok := message["id"].(json.Number); ok && got.String() == id {
			return message
		}
	}
}

func nextACPMessage(t *testing.T, messages <-chan map[string]any) map[string]any {
	t.Helper()
	select {
	case message, ok := <-messages:
		if !ok {
			t.Fatal("ACP output closed unexpectedly")
		}
		return message
	case <-time.After(15 * time.Second):
		t.Fatal("timed out waiting for ACP output")
		return nil
	}
}
