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
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

const (
	MCPModernVersion      = "2026-07-28"
	MCPLegacyVersion      = "2025-11-25"
	MCPClientName         = "Harness"
	MCPClientVersion      = "0.15.0"
	MCPMaxServers         = 32
	MCPMaxArgs            = 128
	MCPMaxArgLength       = 8192
	MCPMaxEnvVars         = 128
	MCPMaxEnvValueLength  = 32768
	MCPMaxEnvTotalLength  = 131072
	MCPMaxMessageBytes    = 1_048_576
	MCPMaxPendingMessages = 128
	MCPMaxToolsPerServer  = 256
	MCPMaxToolsTotal      = 512
	MCPMaxSchemaBytes     = 131072
	MCPDiscoveryTimeout   = 2 * time.Second
	MCPRequestTimeout     = 10 * time.Second
	MCPShutdownGrace      = 500 * time.Millisecond
)

var (
	mcpEnvName              = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
	mcpAliasChars           = regexp.MustCompile(`[^A-Za-z0-9_-]+`)
	supportedLegacyVersions = map[string]struct{}{
		"2025-11-25": {}, "2025-06-18": {}, "2025-03-26": {}, "2024-11-05": {},
	}
)

type MCPServerConfig struct {
	Name    string
	Command string
	Args    []string
	Env     map[string]string
}

// ValidateMCPServers validates the ACP-provided stdio server subset.
func ValidateMCPServers(value any) ([]MCPServerConfig, error) {
	items, ok := value.([]any)
	if !ok || len(items) > MCPMaxServers {
		return nil, fmt.Errorf("mcpServers must be an array of at most %d entries", MCPMaxServers)
	}
	validated := make([]MCPServerConfig, 0, len(items))
	for index, raw := range items {
		object, ok := raw.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("mcpServers[%d] must be an object", index)
		}
		name, _ := object["name"].(string)
		command, commandOK := object["command"].(string)
		if value, exists := object["type"]; exists && value != nil && value != "stdio" {
			return nil, errors.New("only ACP stdio MCP servers are supported")
		}
		if strings.TrimSpace(name) == "" || len(name) > 128 {
			return nil, fmt.Errorf("mcpServers[%d].name must be a non-empty string of at most 128 characters", index)
		}
		if !commandOK || command == "" || len(command) > 4096 || strings.ContainsRune(command, 0) || !filepath.IsAbs(command) {
			return nil, fmt.Errorf("mcpServers[%d].command must be an absolute executable path", index)
		}
		args := []string{}
		if value, exists := object["args"]; exists {
			array, ok := value.([]any)
			if !ok || len(array) > MCPMaxArgs {
				return nil, fmt.Errorf("mcpServers[%d].args must be an array of bounded strings", index)
			}
			for _, item := range array {
				arg, ok := item.(string)
				if !ok || len(arg) > MCPMaxArgLength || strings.ContainsRune(arg, 0) {
					return nil, fmt.Errorf("mcpServers[%d].args must be an array of bounded strings", index)
				}
				args = append(args, arg)
			}
		}
		env := map[string]string{}
		if value, exists := object["env"]; exists {
			array, ok := value.([]any)
			if !ok || len(array) > MCPMaxEnvVars {
				return nil, fmt.Errorf("mcpServers[%d].env must be an array of at most %d entries", index, MCPMaxEnvVars)
			}
			envBytes := 0
			for envIndex, rawEnv := range array {
				pair, ok := rawEnv.(map[string]any)
				if !ok {
					return nil, fmt.Errorf("mcpServers[%d].env[%d] must be an object", index, envIndex)
				}
				key, keyOK := pair["name"].(string)
				entry, valueOK := pair["value"].(string)
				if !keyOK || !mcpEnvName.MatchString(key) || !valueOK || strings.ContainsRune(entry, 0) || len(entry) > MCPMaxEnvValueLength {
					return nil, fmt.Errorf("mcpServers[%d].env[%d] has an invalid name or value", index, envIndex)
				}
				if _, duplicate := env[key]; duplicate {
					return nil, fmt.Errorf("mcpServers[%d].env contains a duplicate variable", index)
				}
				env[key] = entry
				envBytes += len(key) + len(entry)
			}
			if envBytes > MCPMaxEnvTotalLength {
				return nil, fmt.Errorf("mcpServers[%d].env exceeds the size limit", index)
			}
		}
		validated = append(validated, MCPServerConfig{Name: name, Command: command, Args: args, Env: env})
	}
	return validated, nil
}

type mcpMessage struct {
	value map[string]any
	err   error
	stop  bool
}
type mcpRPCError struct {
	code int64
	text string
	data any
}

func (e *mcpRPCError) Error() string { return e.text }

type mcpTimeoutError struct{ method string }

func (e *mcpTimeoutError) Error() string { return "MCP request '" + e.method + "' timed out" }

type mcpClientError struct{ text string }

func (e *mcpClientError) Error() string { return e.text }

type MCPStdioClient struct {
	config          MCPServerConfig
	cwd             string
	requestMu       chan struct{}
	writeMu         chan struct{}
	activeMu        sync.Mutex
	active          map[int64]string
	cancelled       map[int64]bool
	nextID          int64
	stopMu          sync.Mutex
	processMu       sync.Mutex
	command         *exec.Cmd
	stdin           io.WriteCloser
	stdout          io.ReadCloser
	messages        chan mcpMessage
	closing         chan struct{}
	exit            chan struct{}
	readerDone      chan struct{}
	protocolVersion string
	modern          bool
	capabilities    map[string]any
	tools           []map[string]any
	closed          bool
}

func NewMCPStdioClient(config MCPServerConfig, cwd string) (*MCPStdioClient, error) {
	client := &MCPStdioClient{
		config: config, cwd: cwd, active: map[int64]string{}, cancelled: map[int64]bool{},
		requestMu: make(chan struct{}, 1), writeMu: make(chan struct{}, 1),
	}
	if err := client.startProcess(); err != nil {
		return nil, err
	}
	if err := client.negotiate(); err != nil {
		client.Close()
		return nil, err
	}
	if _, err := client.RefreshTools(context.Background()); err != nil {
		client.Close()
		return nil, err
	}
	return client, nil
}

func (c *MCPStdioClient) startProcess() error {
	stdinReader, stdinWriter, err := os.Pipe()
	if err != nil {
		return err
	}
	stdoutReader, stdoutWriter, err := os.Pipe()
	if err != nil {
		_ = stdinReader.Close()
		_ = stdinWriter.Close()
		return err
	}
	cmd := exec.Command(c.config.Command, c.config.Args...)
	cmd.Dir = c.cwd
	cmd.Env = mcpProcessEnv(c.config.Env)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = stdinReader, stdoutWriter, io.Discard
	if err := cmd.Start(); err != nil {
		_ = stdinReader.Close()
		_ = stdinWriter.Close()
		_ = stdoutReader.Close()
		_ = stdoutWriter.Close()
		return &mcpClientError{text: "MCP server process could not be started"}
	}
	_ = stdinReader.Close()
	_ = stdoutWriter.Close()
	messages := make(chan mcpMessage, MCPMaxPendingMessages)
	closing := make(chan struct{})
	exit := make(chan struct{})
	readerDone := make(chan struct{})
	c.processMu.Lock()
	c.command, c.stdin, c.stdout = cmd, stdinWriter, stdoutReader
	c.messages, c.closing, c.exit, c.readerDone = messages, closing, exit, readerDone
	c.closed = false
	c.processMu.Unlock()
	go func() { _ = cmd.Wait(); close(exit) }()
	go func() {
		defer close(readerDone)
		c.readOutput(stdoutReader, messages, closing)
	}()
	return nil
}

func mcpProcessEnv(extra map[string]string) []string {
	allowed := map[string]string{}
	for _, key := range []string{"SystemRoot", "WINDIR", "TEMP", "TMP", "PATH"} {
		if value, ok := os.LookupEnv(key); ok {
			allowed[key] = value
		}
	}
	for key, value := range extra {
		allowed[key] = value
	}
	result := make([]string, 0, len(allowed))
	for key, value := range allowed {
		result = append(result, key+"="+value)
	}
	return result
}

func (c *MCPStdioClient) readOutput(reader io.ReadCloser, messages chan mcpMessage, closing <-chan struct{}) {
	defer reader.Close()
	buf := bufio.NewReaderSize(reader, 64*1024)
	for {
		line, err := readMCPLine(buf, MCPMaxMessageBytes)
		if err != nil {
			if !errors.Is(err, io.EOF) {
				putMCPMessage(messages, closing, mcpMessage{err: &mcpClientError{text: "MCP server output exceeded the message limit"}})
			}
			break
		}
		if !utf8.Valid(line) {
			putMCPMessage(messages, closing, mcpMessage{err: &mcpClientError{text: "MCP server emitted invalid UTF-8 JSON-RPC output"}})
			break
		}
		var value map[string]any
		decoder := json.NewDecoder(strings.NewReader(string(line)))
		decoder.UseNumber()
		if decoder.Decode(&value) != nil || value["jsonrpc"] != "2.0" {
			putMCPMessage(messages, closing, mcpMessage{err: &mcpClientError{text: "MCP server emitted an invalid JSON-RPC message"}})
			break
		}
		if method, ok := value["method"].(string); ok {
			if _, hasID := value["id"]; hasID {
				c.processMu.Lock()
				modern := c.modern
				c.processMu.Unlock()
				if modern {
					putMCPMessage(messages, closing, mcpMessage{err: &mcpClientError{text: "modern MCP servers must not send client-directed requests"}})
					break
				}
				_ = c.write(map[string]any{"jsonrpc": "2.0", "id": value["id"], "error": map[string]any{"code": -32601, "message": "method not found"}})
			}
			_ = method
			continue
		}
		putMCPMessage(messages, closing, mcpMessage{value: value})
	}
	putMCPMessage(messages, closing, mcpMessage{stop: true})
}

func readMCPLine(reader *bufio.Reader, max int) ([]byte, error) {
	line := make([]byte, 0, 256)
	for {
		part, err := reader.ReadSlice('\n')
		line = append(line, part...)
		if len(line) > max {
			return nil, errors.New("line too long")
		}
		if err == nil {
			line = line[:len(line)-1]
			if len(line) > 0 && line[len(line)-1] == '\r' {
				line = line[:len(line)-1]
			}
			return line, nil
		}
		if errors.Is(err, bufio.ErrBufferFull) {
			continue
		}
		return nil, err
	}
}

func putMCPMessage(messages chan mcpMessage, closing <-chan struct{}, message mcpMessage) {
	if messages == nil || closing == nil {
		return
	}
	timer := time.NewTimer(time.Second)
	defer timer.Stop()
	select {
	case messages <- message:
	case <-closing:
	case <-timer.C:
	}
}

func (c *MCPStdioClient) write(message map[string]any) error {
	encoded, err := marshalJSON(message)
	if err != nil {
		return &mcpClientError{text: "MCP request could not be encoded"}
	}
	raw := append(encoded, '\n')
	if len(raw) > MCPMaxMessageBytes {
		return &mcpClientError{text: "MCP request exceeded the message limit"}
	}
	method, _ := message["method"].(string)
	timer := time.NewTimer(MCPRequestTimeout)
	defer timer.Stop()
	return c.writeWithLimit(context.Background(), timer.C, method, raw)
}

func (c *MCPStdioClient) writeWithLimit(ctx context.Context, timeout <-chan time.Time, method string, raw []byte) error {
	if ctx == nil {
		ctx = context.Background()
	}
	select {
	case <-ctx.Done():
		return &mcpClientError{text: "MCP tool call was cancelled"}
	case <-timeout:
		return &mcpTimeoutError{method: method}
	default:
	}
	select {
	case c.writeMu <- struct{}{}:
	case <-ctx.Done():
		return &mcpClientError{text: "MCP tool call was cancelled"}
	case <-timeout:
		return &mcpTimeoutError{method: method}
	}
	c.processMu.Lock()
	stdin, command := c.stdin, c.command
	c.processMu.Unlock()
	if stdin == nil || command == nil || command.Process == nil {
		<-c.writeMu
		return &mcpClientError{text: "MCP server process is not running"}
	}
	writeDone := make(chan error, 1)
	go func() {
		err := writeMCPBytes(stdin, raw)
		<-c.writeMu
		writeDone <- err
	}()
	select {
	case err := <-writeDone:
		if ctx.Err() != nil {
			return &mcpClientError{text: "MCP tool call was cancelled"}
		}
		select {
		case <-timeout:
			return &mcpTimeoutError{method: method}
		default:
		}
		if err != nil {
			return &mcpClientError{text: "MCP request could not be written"}
		}
		return nil
	case <-ctx.Done():
		_ = stdin.Close()
		_ = command.Process.Kill()
		return &mcpClientError{text: "MCP tool call was cancelled"}
	case <-timeout:
		_ = stdin.Close()
		_ = command.Process.Kill()
		return &mcpTimeoutError{method: method}
	}
}

func writeMCPBytes(writer io.Writer, raw []byte) error {
	for len(raw) > 0 {
		written, err := writer.Write(raw)
		if err != nil {
			return err
		}
		if written == 0 {
			return io.ErrShortWrite
		}
		raw = raw[written:]
	}
	return nil
}

func (c *MCPStdioClient) metadata(version string) map[string]any {
	return map[string]any{
		"io.modelcontextprotocol/protocolVersion":    version,
		"io.modelcontextprotocol/clientInfo":         map[string]any{"name": MCPClientName, "version": MCPClientVersion},
		"io.modelcontextprotocol/clientCapabilities": map[string]any{},
	}
}

func (c *MCPStdioClient) request(ctx context.Context, method string, params map[string]any, version string, timeout time.Duration) (map[string]any, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if timeout <= 0 {
		timeout = MCPRequestTimeout
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return nil, &mcpClientError{text: "MCP tool call was cancelled"}
	case <-timer.C:
		return nil, &mcpTimeoutError{method: method}
	default:
	}
	select {
	case c.requestMu <- struct{}{}:
		defer func() { <-c.requestMu }()
	case <-ctx.Done():
		return nil, &mcpClientError{text: "MCP tool call was cancelled"}
	case <-timer.C:
		return nil, &mcpTimeoutError{method: method}
	}
	c.nextID++
	id := c.nextID
	requestParams := map[string]any{}
	for key, value := range params {
		requestParams[key] = value
	}
	if version == MCPModernVersion {
		requestParams["_meta"] = c.metadata(version)
	}
	c.activeMu.Lock()
	c.active[id] = version
	delete(c.cancelled, id)
	c.activeMu.Unlock()
	defer func() { c.activeMu.Lock(); delete(c.active, id); delete(c.cancelled, id); c.activeMu.Unlock() }()
	raw, err := marshalJSON(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": requestParams})
	if err != nil {
		return nil, &mcpClientError{text: "MCP request could not be encoded"}
	}
	raw = append(raw, '\n')
	if len(raw) > MCPMaxMessageBytes {
		return nil, &mcpClientError{text: "MCP request exceeded the message limit"}
	}
	if err := c.writeWithLimit(ctx, timer.C, method, raw); err != nil {
		return nil, err
	}
	for {
		select {
		case <-ctx.Done():
			c.sendCancelledOnce(id, version)
			return nil, &mcpClientError{text: "MCP tool call was cancelled"}
		case <-timer.C:
			c.sendCancelledOnce(id, version)
			return nil, &mcpTimeoutError{method: method}
		case message := <-c.messages:
			if message.err != nil {
				return nil, message.err
			}
			if message.stop {
				return nil, &mcpClientError{text: "MCP server closed its output stream"}
			}
			if !sameMCPID(message.value["id"], id) {
				continue
			}
			if rawError, exists := message.value["error"]; exists {
				errorObject, ok := rawError.(map[string]any)
				if !ok {
					return nil, &mcpClientError{text: "MCP server returned an invalid JSON-RPC error"}
				}
				code, ok := anyInt64(errorObject["code"])
				if !ok {
					return nil, &mcpClientError{text: "MCP server returned an invalid JSON-RPC error"}
				}
				return nil, &mcpRPCError{code: code, text: truncateRunes(stringOrDefault(errorObject["message"], "MCP request failed"), 500), data: errorObject["data"]}
			}
			result, ok := message.value["result"].(map[string]any)
			if !ok {
				return nil, &mcpClientError{text: "MCP server returned an invalid result"}
			}
			return result, nil
		}
	}
}

func sameMCPID(value any, id int64) bool {
	switch typed := value.(type) {
	case json.Number:
		n, err := typed.Int64()
		return err == nil && n == id
	case float64:
		return int64(typed) == id
	case int64:
		return typed == id
	case int:
		return int64(typed) == id
	default:
		return false
	}
}

func anyInt64(value any) (int64, bool) {
	switch number := value.(type) {
	case json.Number:
		n, err := number.Int64()
		return n, err == nil
	case float64:
		return int64(number), number == float64(int64(number))
	case int:
		return int64(number), true
	case int64:
		return number, true
	default:
		return 0, false
	}
}

func (c *MCPStdioClient) notify(method string, params map[string]any) error {
	payload := map[string]any{"jsonrpc": "2.0", "method": method}
	if params != nil {
		requestParams := map[string]any{}
		for key, value := range params {
			requestParams[key] = value
		}
		if c.modern {
			requestParams["_meta"] = c.metadata(c.protocolVersion)
		}
		payload["params"] = requestParams
	}
	return c.write(payload)
}

func (c *MCPStdioClient) sendCancelledOnce(id int64, version string) {
	c.activeMu.Lock()
	if _, active := c.active[id]; !active || c.cancelled[id] {
		c.activeMu.Unlock()
		return
	}
	c.cancelled[id] = true
	c.activeMu.Unlock()
	params := map[string]any{"requestId": id}
	if version == MCPModernVersion {
		params["_meta"] = c.metadata(version)
	}
	_ = c.write(map[string]any{"jsonrpc": "2.0", "method": "notifications/cancelled", "params": params})
}

func (c *MCPStdioClient) negotiate() error {
	result, err := c.request(context.Background(), "server/discover", nil, MCPModernVersion, MCPDiscoveryTimeout)
	if err != nil {
		var rpcErr *mcpRPCError
		if errors.As(err, &rpcErr) && rpcErr.code == -32022 {
			data, _ := rpcErr.data.(map[string]any)
			supported, _ := data["supported"].([]any)
			compatible := false
			legacy := ""
			for _, version := range supported {
				if version == MCPModernVersion {
					compatible = true
				}
				if candidate, ok := version.(string); ok {
					if _, known := supportedLegacyVersions[candidate]; known && (legacy == "" || candidate > legacy) {
						legacy = candidate
					}
				}
			}
			if !compatible {
				if legacy != "" {
					return c.restartForLegacy(legacy)
				}
				return &mcpClientError{text: "MCP server supports no compatible modern protocol version"}
			}
			result, err = c.request(context.Background(), "server/discover", nil, MCPModernVersion, MCPRequestTimeout)
			if err != nil {
				return err
			}
		} else {
			return c.restartForLegacy(MCPLegacyVersion)
		}
	}
	if result["resultType"] != "complete" {
		return &mcpClientError{text: "MCP server returned an invalid server/discover result"}
	}
	versions, ok := result["supportedVersions"].([]any)
	if !ok {
		return &mcpClientError{text: "MCP server returned an invalid server/discover result"}
	}
	for _, version := range versions {
		if version == MCPModernVersion {
			c.protocolVersion, c.modern = MCPModernVersion, true
			c.capabilities, _ = result["capabilities"].(map[string]any)
			if c.capabilities == nil {
				c.capabilities = map[string]any{}
			}
			return nil
		}
	}
	return &mcpClientError{text: "MCP server supports no compatible modern protocol version"}
}

func (c *MCPStdioClient) restartForLegacy(version string) error {
	c.stopProcess()
	if err := c.startProcess(); err != nil {
		return err
	}
	result, err := c.request(context.Background(), "initialize", map[string]any{
		"protocolVersion": version,
		"capabilities":    map[string]any{},
		"clientInfo":      map[string]any{"name": MCPClientName, "version": MCPClientVersion},
	}, "legacy-initialize", MCPRequestTimeout)
	if err != nil {
		return err
	}
	negotiated, _ := result["protocolVersion"].(string)
	if _, ok := supportedLegacyVersions[negotiated]; !ok {
		return &mcpClientError{text: "MCP server negotiated an unsupported legacy version"}
	}
	c.protocolVersion, c.modern = negotiated, false
	c.capabilities, _ = result["capabilities"].(map[string]any)
	if c.capabilities == nil {
		c.capabilities = map[string]any{}
	}
	return c.notify("notifications/initialized", nil)
}

func (c *MCPStdioClient) RefreshTools(ctx context.Context) ([]map[string]any, error) {
	capabilities, _ := c.capabilities["tools"].(map[string]any)
	if capabilities == nil {
		c.tools = nil
		return nil, nil
	}
	cursor := ""
	visited := map[string]bool{}
	tools := make([]map[string]any, 0)
	for page := 0; page < 64; page++ {
		params := map[string]any{}
		if cursor != "" {
			params["cursor"] = cursor
		}
		result, err := c.request(ctx, "tools/list", params, c.protocolVersion, MCPRequestTimeout)
		if err != nil {
			return nil, err
		}
		resultType := result["resultType"]
		if c.modern && resultType != "complete" {
			return nil, &mcpClientError{text: "MCP tools/list returned an unsupported result type"}
		}
		if resultType != nil && resultType != "complete" {
			return nil, &mcpClientError{text: "MCP tools/list returned an unsupported result type"}
		}
		pageTools, ok := result["tools"].([]any)
		if !ok {
			return nil, &mcpClientError{text: "MCP tools/list did not return a tools array"}
		}
		for _, raw := range pageTools {
			tool, ok := raw.(map[string]any)
			if !ok {
				return nil, &mcpClientError{text: "MCP tools/list returned an invalid tool"}
			}
			name, nameOK := tool["name"].(string)
			schema, schemaOK := tool["inputSchema"].(map[string]any)
			if !nameOK || name == "" || len(name) > 128 || !schemaOK {
				return nil, &mcpClientError{text: "MCP tools/list returned an invalid tool name or schema"}
			}
			encoded, err := marshalJSON(schema)
			if err != nil {
				return nil, &mcpClientError{text: "MCP tool schema is not JSON-compatible"}
			}
			if len(encoded) > MCPMaxSchemaBytes {
				return nil, &mcpClientError{text: "MCP tool schema exceeded the size limit"}
			}
			tools = append(tools, tool)
			if len(tools) > MCPMaxToolsPerServer {
				return nil, &mcpClientError{text: "MCP server exposed too many tools"}
			}
		}
		nextCursor, exists := result["nextCursor"]
		if !exists || nextCursor == nil {
			break
		}
		value, ok := nextCursor.(string)
		if !ok || value == "" || visited[value] {
			return nil, &mcpClientError{text: "MCP tools/list returned an invalid pagination cursor"}
		}
		visited[value], cursor = true, value
		if page == 63 {
			return nil, &mcpClientError{text: "MCP tools/list exceeded the pagination limit"}
		}
	}
	c.tools = tools
	return append([]map[string]any(nil), tools...), nil
}

func (c *MCPStdioClient) CallTool(ctx context.Context, name string, arguments map[string]any) ToolOutcome {
	result, err := c.request(ctx, "tools/call", map[string]any{"name": name, "arguments": arguments}, c.protocolVersion, MCPRequestTimeout)
	if err != nil {
		var rpc *mcpRPCError
		var timeout *mcpTimeoutError
		var client *mcpClientError
		switch {
		case errors.As(err, &rpc):
			return ToolOutcome{Error: fmt.Sprintf("MCP server error (%d): %s", rpc.code, truncateRunes(rpc.Error(), 500))}
		case errors.As(err, &timeout):
			return ToolOutcome{Error: err.Error()}
		case errors.As(err, &client):
			if client.Error() == "MCP tool call was cancelled" {
				return ToolOutcome{Error: client.Error()}
			}
			return ToolOutcome{Error: "MCP server error: " + err.Error()}
		default:
			return ToolOutcome{Error: "MCP server error: MCP request failed"}
		}
	}
	resultType := result["resultType"]
	if c.modern && resultType != "complete" {
		return ToolOutcome{Error: "MCP server returned an invalid modern tool result"}
	}
	if resultType != nil && resultType != "complete" {
		return ToolOutcome{Error: fmt.Sprintf("MCP server returned unsupported result type '%v'", resultType)}
	}
	if result["isError"] == true {
		encoded, err := marshalJSON(result)
		if err != nil {
			return ToolOutcome{Error: "MCP tool returned an error: tool execution failed"}
		}
		return ToolOutcome{Error: "MCP tool returned an error: " + truncateRunes(string(encoded), 4000)}
	}
	return ToolOutcome{Result: result}
}

func (c *MCPStdioClient) CancelActiveRequests() {
	c.activeMu.Lock()
	active := make(map[int64]string, len(c.active))
	for id, version := range c.active {
		active[id] = version
	}
	c.activeMu.Unlock()
	for id, version := range active {
		c.sendCancelledOnce(id, version)
	}
}

func (c *MCPStdioClient) stopProcess() {
	c.stopMu.Lock()
	defer c.stopMu.Unlock()
	c.CancelActiveRequests()
	c.processMu.Lock()
	if c.closed && c.command == nil && c.stdout == nil && c.readerDone == nil {
		c.processMu.Unlock()
		return
	}
	c.closed = true
	closing, stdin, stdout, cmd, exit, readerDone := c.closing, c.stdin, c.stdout, c.command, c.exit, c.readerDone
	c.stdin, c.stdout, c.command = nil, nil, nil
	if closing != nil {
		close(closing)
	}
	c.processMu.Unlock()
	if stdin != nil {
		_ = stdin.Close()
	}
	if stdout != nil {
		_ = stdout.Close()
	}
	wait := func(done <-chan struct{}, duration time.Duration) bool {
		if done == nil {
			return true
		}
		timer := time.NewTimer(duration)
		defer timer.Stop()
		select {
		case <-done:
			return true
		case <-timer.C:
			return false
		}
	}
	if cmd != nil && cmd.Process != nil && !wait(exit, MCPShutdownGrace) {
		_ = cmd.Process.Kill()
		_ = wait(exit, MCPShutdownGrace)
	}
	_ = wait(readerDone, MCPShutdownGrace)
	c.processMu.Lock()
	if c.readerDone == readerDone {
		c.readerDone = nil
	}
	c.processMu.Unlock()
}

func (c *MCPStdioClient) Close() { c.stopProcess() }

func (c *MCPStdioClient) ReturnCode() (int, bool) {
	c.processMu.Lock()
	command, exit := c.command, c.exit
	c.processMu.Unlock()
	if exit == nil {
		return 0, true
	}
	select {
	case <-exit:
		return 0, true
	default:
		if command == nil || command.Process == nil {
			return 0, false
		}
		return 0, false
	}
}

type exposedMCPTool struct {
	alias       string
	client      *MCPStdioClient
	source      string
	description string
	schema      map[string]any
}

// ACPSessionMCP owns configured stdio MCP subprocesses for one active ACP session.
type ACPSessionMCP struct {
	configs []MCPServerConfig
	cwd     string
	mu      sync.Mutex
	clients []*MCPStdioClient
	ended   bool
}

func NewACPSessionMCP(configs []MCPServerConfig, cwd string) (*ACPSessionMCP, error) {
	session := &ACPSessionMCP{configs: configs, cwd: cwd}
	if err := session.start(); err != nil {
		return nil, err
	}
	return session, nil
}

func (s *ACPSessionMCP) start() error {
	clients := make([]*MCPStdioClient, 0, len(s.configs))
	for _, config := range s.configs {
		client, err := NewMCPStdioClient(config, s.cwd)
		if err != nil {
			for _, running := range clients {
				running.Close()
			}
			return err
		}
		clients = append(clients, client)
	}
	s.clients = clients
	return nil
}

func (s *ACPSessionMCP) ToolRegistry(base *ToolRegistry, ctx context.Context) (ToolDispatcher, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ended {
		return nil, &mcpClientError{text: "ACP session is closed"}
	}
	if base == nil {
		base = NewToolRegistry()
	}
	dead := false
	for _, client := range s.clients {
		if _, exited := client.ReturnCode(); exited {
			dead = true
			break
		}
	}
	if dead {
		s.closeProcesses()
	}
	if len(s.clients) == 0 && len(s.configs) > 0 {
		if err := s.start(); err != nil {
			return nil, err
		}
	}
	exposed := map[string]exposedMCPTool{}
	order := make([]string, 0)
	baseDefinitions := base.ProviderDefinitions()
	for serverIndex, client := range s.clients {
		tools, err := client.RefreshTools(ctx)
		if err != nil {
			return nil, err
		}
		for toolIndex, raw := range tools {
			source := raw["name"].(string)
			safe := strings.Trim(mcpAliasChars.ReplaceAllString(source, "_"), "_-")
			if safe == "" {
				safe = "tool"
			}
			alias := truncateRunes(fmt.Sprintf("mcp%d_%d_%s", serverIndex, toolIndex, safe), 64)
			if _, exists := exposed[alias]; exists {
				return nil, &mcpClientError{text: "MCP tool names collide after provider-safe namespacing"}
			}
			for _, local := range baseDefinitions {
				if local.Name == alias {
					return nil, &mcpClientError{text: "MCP tool names collide after provider-safe namespacing"}
				}
			}
			description, _ := raw["description"].(string)
			prefix := fmt.Sprintf("MCP server %q tool %q", client.config.Name, source)
			if description != "" {
				prefix += ": " + description
			}
			schema := raw["inputSchema"].(map[string]any)
			exposed[alias] = exposedMCPTool{alias: alias, client: client, source: source, description: truncateRunes(prefix, 8000), schema: schema}
			order = append(order, alias)
		}
	}
	if len(exposed)+len(baseDefinitions) > MCPMaxToolsTotal {
		return nil, &mcpClientError{text: "ACP session exposed too many total tools"}
	}
	return &acpToolRegistry{base: base, exposed: exposed, order: order, ctx: ctx}, nil
}

func (s *ACPSessionMCP) Cancel() {
	s.mu.Lock()
	clients := append([]*MCPStdioClient(nil), s.clients...)
	s.mu.Unlock()
	for _, client := range clients {
		client.CancelActiveRequests()
	}
}

func (s *ACPSessionMCP) Close() {
	s.mu.Lock()
	s.ended = true
	clients := append([]*MCPStdioClient(nil), s.clients...)
	s.mu.Unlock()
	for _, client := range clients {
		client.CancelActiveRequests()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closeProcesses()
}

func (s *ACPSessionMCP) closeProcesses() {
	clients := s.clients
	s.clients = nil
	for _, client := range clients {
		client.Close()
	}
}

type acpToolRegistry struct {
	base    *ToolRegistry
	exposed map[string]exposedMCPTool
	order   []string
	ctx     context.Context
}

func (r *acpToolRegistry) ProviderDefinitions() []ToolDefinition {
	definitions := r.base.ProviderDefinitions()
	for _, alias := range r.order {
		item := r.exposed[alias]
		definitions = append(definitions, ToolDefinition{Name: item.alias, Description: item.description, InputSchema: item.schema})
	}
	return definitions
}

func (r *acpToolRegistry) ExecuteTool(name string, arguments any) ToolOutcome {
	remote, exists := r.exposed[name]
	if !exists {
		return r.base.ExecuteTool(name, arguments)
	}
	object, ok := arguments.(map[string]any)
	if !ok {
		return ToolOutcome{Error: "MCP tool arguments must be a JSON object."}
	}
	return remote.client.CallTool(r.ctx, remote.source, object)
}

func truncateRunes(value string, max int) string {
	runes := []rune(value)
	if len(runes) <= max {
		return value
	}
	return string(runes[:max])
}

func stringOrDefault(value any, fallback string) string {
	if text, ok := value.(string); ok && text != "" {
		return text
	}
	return fallback
}
