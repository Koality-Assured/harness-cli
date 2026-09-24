package chat

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestMCPFakeProcess is invoked as a child executable by the fake-only stdio tests.
func TestMCPFakeProcess(t *testing.T) {
	mode := os.Getenv("HARNESS_FAKE_MCP_MODE")
	if mode == "" {
		return
	}
	fakeMCPProcess(os.Stdin, os.Stdout, mode)
	os.Exit(0)
}

func fakeMCPProcess(input io.Reader, output io.Writer, mode string) {
	if marker := os.Getenv("HARNESS_FAKE_MCP_EOF"); marker != "" {
		defer func() { _ = os.WriteFile(marker, []byte("eof"), 0600) }()
	}
	if counter := os.Getenv("HARNESS_FAKE_MCP_COUNTER"); counter != "" {
		contents, _ := os.ReadFile(counter)
		value := 0
		if len(contents) > 0 {
			_, _ = fmt.Sscanf(string(contents), "%d", &value)
		}
		_ = os.WriteFile(counter, []byte(fmt.Sprint(value+1)), 0600)
	}
	reader := bufio.NewScanner(input)
	reader.Buffer(make([]byte, 4096), MCPMaxMessageBytes)
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
		if strings.HasPrefix(os.Getenv("HARNESS_FAKE_PARENT_SECRET"), "parent-") {
			return
		}
		if mode == "legacy" && method == "server/discover" {
			writeFakeRPC(output, map[string]any{"jsonrpc": "2.0", "id": id, "error": map[string]any{"code": -32601, "message": "method not found"}})
			continue
		}
		if mode == "legacy-versions" && method == "server/discover" {
			writeFakeRPC(output, map[string]any{"jsonrpc": "2.0", "id": id, "error": map[string]any{
				"code": -32022, "message": "unsupported protocol version", "data": map[string]any{"supported": []string{MCPLegacyVersion}},
			}})
			continue
		}
		switch method {
		case "server/discover":
			writeFakeRPC(output, map[string]any{"jsonrpc": "2.0", "id": id, "result": map[string]any{
				"resultType": "complete", "supportedVersions": []string{MCPModernVersion}, "capabilities": map[string]any{"tools": map[string]any{}},
			}})
		case "initialize":
			writeFakeRPC(output, map[string]any{"jsonrpc": "2.0", "id": id, "result": map[string]any{
				"protocolVersion": MCPLegacyVersion, "capabilities": map[string]any{"tools": map[string]any{}},
			}})
		case "tools/list":
			writeFakeRPC(output, map[string]any{"jsonrpc": "2.0", "id": id, "result": map[string]any{
				"resultType": "complete", "tools": []any{map[string]any{
					"name": "echo", "description": "return the argument", "inputSchema": map[string]any{
						"type": "object", "properties": map[string]any{"value": map[string]any{"type": "string"}}, "required": []string{"value"},
					},
				}},
			}})
			if mode == "block-write" {
				if marker := os.Getenv("HARNESS_FAKE_MCP_READY"); marker != "" {
					_ = os.WriteFile(marker, []byte("ready"), 0600)
				}
				for {
					time.Sleep(time.Hour)
				}
			}
		case "tools/call":
			if mode == "cancel" {
				marker := os.Getenv("HARNESS_FAKE_MCP_READY")
				_ = os.WriteFile(marker, []byte("ready"), 0600)
				continue
			}
			arguments, _ := params["arguments"].(map[string]any)
			writeFakeRPC(output, map[string]any{"jsonrpc": "2.0", "id": id, "result": map[string]any{
				"resultType": "complete", "content": []any{map[string]any{"type": "text", "text": arguments["value"]}},
			}})
		case "notifications/cancelled":
			marker := os.Getenv("HARNESS_FAKE_MCP_CANCELLED")
			_ = os.WriteFile(marker, []byte("cancelled"), 0600)
		default:
			if id != nil {
				writeFakeRPC(output, map[string]any{"jsonrpc": "2.0", "id": id, "error": map[string]any{"code": -32601, "message": "method not found"}})
			}
		}
	}
}

func writeFakeRPC(output io.Writer, response map[string]any) {
	encoded, _ := json.Marshal(response)
	_, _ = output.Write(append(encoded, '\n'))
}

func fakeMCPConfig(t *testing.T, mode string) MCPServerConfig {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	return MCPServerConfig{Name: "fake", Command: executable, Args: []string{"-test.run=^TestMCPFakeProcess$"}, Env: map[string]string{"HARNESS_FAKE_MCP_MODE": mode}}
}

type heldEOFReader struct {
	started chan struct{}
	release chan struct{}
	start   sync.Once
	close   sync.Once
}

func (r *heldEOFReader) Read([]byte) (int, error) {
	r.start.Do(func() { close(r.started) })
	<-r.release
	return 0, io.EOF
}

func (r *heldEOFReader) Close() error {
	r.close.Do(func() { close(r.release) })
	return nil
}

type mcpTestWriteCloser struct{ io.Writer }

func (mcpTestWriteCloser) Close() error { return nil }

func TestMCPStdioOldReaderStopDoesNotContaminateLegacyRetry(t *testing.T) {
	oldMessages := make(chan mcpMessage, MCPMaxPendingMessages)
	oldClosing := make(chan struct{})
	reader := &heldEOFReader{started: make(chan struct{}), release: make(chan struct{})}
	readerDone := make(chan struct{})
	client := &MCPStdioClient{
		active: map[int64]string{}, cancelled: map[int64]bool{},
		requestMu: make(chan struct{}, 1), writeMu: make(chan struct{}, 1),
		stdin: nil, stdout: reader, messages: oldMessages, closing: oldClosing, readerDone: readerDone,
	}
	go func() {
		defer close(readerDone)
		client.readOutput(reader, oldMessages, oldClosing)
	}()
	<-reader.started

	// Simulate restartForLegacy stopping the old child. It must close the old
	// reader and join its goroutine before a replacement generation is installed.
	client.stopProcess()
	select {
	case <-readerDone:
	default:
		t.Fatal("stopProcess returned before the prior output reader finished")
	}

	newMessages := make(chan mcpMessage, MCPMaxPendingMessages)
	newClosing := make(chan struct{})
	client.processMu.Lock()
	client.messages, client.closing, client.closed = newMessages, newClosing, false
	client.stdin = mcpTestWriteCloser{Writer: io.Discard}
	client.command = &exec.Cmd{Process: &os.Process{}}
	client.processMu.Unlock()

	// A delayed terminal notification from the prior reader must not enter the
	// replacement channel ahead of the retry's initialize response.
	reader.Close()
	select {
	case <-readerDone:
	case <-time.After(time.Second):
		t.Fatal("prior output reader did not terminate")
	}
	newMessages <- mcpMessage{value: map[string]any{
		"jsonrpc": "2.0", "id": json.Number("1"),
		"result": map[string]any{"protocolVersion": MCPLegacyVersion},
	}}
	result, err := client.request(context.Background(), "initialize", map[string]any{}, "legacy-initialize", time.Second)
	if err != nil || result["protocolVersion"] != MCPLegacyVersion {
		t.Fatalf("legacy retry initialize result = %#v err=%v", result, err)
	}
	if len(newMessages) != 0 {
		t.Fatalf("replacement channel retained unexpected message: %#v", <-newMessages)
	}
	close(newClosing)
}

func TestMCPStdioModernDiscoveryAndToolDispatch(t *testing.T) {
	t.Setenv("HARNESS_FAKE_PARENT_SECRET", "parent-do-not-pass")
	client, err := NewMCPStdioClient(fakeMCPConfig(t, "modern"), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	if !client.modern || client.protocolVersion != MCPModernVersion || len(client.tools) != 1 {
		t.Fatalf("discovered client = modern:%v version:%q tools:%#v", client.modern, client.protocolVersion, client.tools)
	}
	outcome := client.CallTool(context.Background(), "echo", map[string]any{"value": "fake-only"})
	if outcome.Error != "" {
		t.Fatal(outcome.Error)
	}
	content := outcome.Result.(map[string]any)["content"].([]any)[0].(map[string]any)
	if content["text"] != "fake-only" {
		t.Fatalf("tool result = %#v", outcome.Result)
	}
}

func TestMCPStdioLegacyFallbackRestartsProcess(t *testing.T) {
	counter := filepath.Join(t.TempDir(), "launch-count")
	config := fakeMCPConfig(t, "legacy")
	config.Env["HARNESS_FAKE_MCP_COUNTER"] = counter
	client, err := NewMCPStdioClient(config, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	if client.modern || client.protocolVersion != MCPLegacyVersion {
		t.Fatalf("legacy negotiation version = %#v", client.protocolVersion)
	}
	count, err := os.ReadFile(counter)
	if err != nil || strings.TrimSpace(string(count)) != "2" {
		t.Fatalf("legacy fallback launch count=%q err=%v", count, err)
	}
	if _, err := client.RefreshTools(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestMCPStdioSupportedLegacyVersionUsesBoundedFallback(t *testing.T) {
	counter := filepath.Join(t.TempDir(), "launch-count")
	config := fakeMCPConfig(t, "legacy-versions")
	config.Env["HARNESS_FAKE_MCP_COUNTER"] = counter
	client, err := NewMCPStdioClient(config, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	if client.modern || client.protocolVersion != MCPLegacyVersion {
		t.Fatalf("legacy supported-version negotiation = modern:%v version:%q", client.modern, client.protocolVersion)
	}
	count, err := os.ReadFile(counter)
	if err != nil || strings.TrimSpace(string(count)) != "2" {
		t.Fatalf("bounded legacy fallback launch count=%q err=%v", count, err)
	}
	if _, err := client.RefreshTools(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestMCPStdioCancellationAndBoundedShutdown(t *testing.T) {
	dir := t.TempDir()
	ready, cancelled := filepath.Join(dir, "ready"), filepath.Join(dir, "cancelled")
	config := fakeMCPConfig(t, "cancel")
	config.Env["HARNESS_FAKE_MCP_READY"], config.Env["HARNESS_FAKE_MCP_CANCELLED"] = ready, cancelled
	client, err := NewMCPStdioClient(config, dir)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan ToolOutcome, 1)
	go func() { done <- client.CallTool(ctx, "echo", map[string]any{"value": "wait"}) }()
	waitForTestFile(t, ready)
	cancel()
	select {
	case result := <-done:
		if result.Error != "MCP tool call was cancelled" {
			t.Fatalf("cancel outcome = %#v", result)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("cancelled MCP call did not return promptly")
	}
	waitForTestFile(t, cancelled)
	start := time.Now()
	client.Close()
	if time.Since(start) > 2*time.Second {
		t.Fatal("MCP child shutdown exceeded its bound")
	}
	if _, exited := client.ReturnCode(); !exited {
		t.Fatal("MCP child is still running after close")
	}
}

func TestMCPStdioRequestWriteHonorsTimeoutAndCancellation(t *testing.T) {
	for _, test := range []struct {
		name     string
		cancel   bool
		timeout  time.Duration
		wantText string
	}{
		{name: "timeout", timeout: 120 * time.Millisecond, wantText: "MCP request 'tools/call' timed out"},
		{name: "cancellation", cancel: true, timeout: 5 * time.Second, wantText: "MCP tool call was cancelled"},
	} {
		t.Run(test.name, func(t *testing.T) {
			dir := t.TempDir()
			ready := filepath.Join(dir, "ready")
			config := fakeMCPConfig(t, "block-write")
			config.Env["HARNESS_FAKE_MCP_READY"] = ready
			client, err := NewMCPStdioClient(config, dir)
			if err != nil {
				t.Fatal(err)
			}
			defer client.Close()
			waitForTestFile(t, ready)

			payload := strings.Repeat("x", MCPMaxMessageBytes-2048)
			params := map[string]any{"arguments": map[string]any{"value": payload}}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			result := make(chan error, 1)
			started := time.Now()
			go func() {
				_, requestErr := client.request(ctx, "tools/call", params, MCPModernVersion, test.timeout)
				result <- requestErr
			}()
			if test.cancel {
				time.Sleep(75 * time.Millisecond)
				cancel()
			}
			var requestErr error
			select {
			case requestErr = <-result:
			case <-time.After(1500 * time.Millisecond):
				t.Fatalf("blocked MCP write did not honor %s", test.name)
			}
			if requestErr == nil || requestErr.Error() != test.wantText {
				t.Fatalf("request error = %v, want %q", requestErr, test.wantText)
			}
			if elapsed := time.Since(started); elapsed > 1500*time.Millisecond {
				t.Fatalf("blocked write took %s to return", elapsed)
			}
			if test.cancel && time.Since(started) < 50*time.Millisecond {
				t.Fatalf("cancellation returned before the test cancelled the context")
			}
			if test.name == "timeout" {
				var timeoutErr *mcpTimeoutError
				if !errors.As(requestErr, &timeoutErr) {
					t.Fatalf("timeout error has type %T", requestErr)
				}
			}
		})
	}
}

func TestValidateMCPServersRejectsUnsafeOrUnboundedConfig(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, value := range []any{
		[]any{map[string]any{"name": "bad", "command": "relative"}},
		[]any{map[string]any{"name": "bad", "command": executable, "type": "http"}},
		[]any{map[string]any{"name": "bad", "command": executable, "args": []any{"bad\x00arg"}}},
		[]any{map[string]any{"name": "bad", "command": executable, "env": []any{map[string]any{"name": "BAD-NAME", "value": "x"}}}},
	} {
		if _, err := ValidateMCPServers(value); err == nil {
			t.Errorf("accepted unsafe config %#v", value)
		}
	}
}

func TestSlice15MCPHTTPAndSSETransportsAreExcluded(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, transport := range []string{"http", "sse"} {
		value := []any{map[string]any{"name": "remote", "command": executable, "type": transport}}
		if _, err := ValidateMCPServers(value); err == nil {
			t.Errorf("accepted excluded MCP transport %q", transport)
		}
	}
}

func waitForTestFile(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for fake MCP marker %s", path)
}
