package chat

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const ACPCloseTimeout = 10 * time.Second

// RunACPStdio serves ACP v1 JSON-RPC over newline-delimited stdio.
func RunACPStdio(runtime *AdapterRuntime, input io.Reader, output io.Writer) error {
	writeMu := &sync.Mutex{}
	write := func(value map[string]any) error {
		encoded, err := marshalJSON(value)
		if err != nil {
			return err
		}
		writeMu.Lock()
		defer writeMu.Unlock()
		if _, err := output.Write(append(encoded, '\n')); err != nil {
			return err
		}
		if flusher, ok := output.(interface{ Flush() error }); ok {
			return flusher.Flush()
		}
		return nil
	}
	respond := func(id any, result any, rpcError map[string]any) {
		message := map[string]any{"jsonrpc": "2.0", "id": id}
		if rpcError != nil {
			message["error"] = rpcError
		} else if result == nil {
			message["result"] = map[string]any{}
		} else {
			message["result"] = result
		}
		_ = write(message)
	}
	update := func(sessionID string, data map[string]any) {
		_ = write(map[string]any{"jsonrpc": "2.0", "method": "session/update", "params": map[string]any{"sessionId": sessionID, "update": data}})
	}
	const principal = "stdio"
	initialized := false
	active := map[string]*ACPSessionMCP{}
	activeMu := &sync.Mutex{}
	promptMu := &sync.Mutex{}
	promptChanged := make(chan struct{})
	promptCounts := map[string]int{}
	signalPrompt := func() { close(promptChanged); promptChanged = make(chan struct{}) }
	startPrompt := func(requestID any, handle, sessionID, prompt string) {
		promptMu.Lock()
		promptCounts[handle]++
		signalPrompt()
		promptMu.Unlock()
		go func() {
			defer func() {
				promptMu.Lock()
				promptCounts[handle]--
				if promptCounts[handle] <= 0 {
					delete(promptCounts, handle)
				}
				signalPrompt()
				promptMu.Unlock()
			}()
			handlePrompt(runtime, active, activeMu, update, respond, requestID, handle, prompt)
		}()
	}
	waitPrompts := func(handle string, timeout time.Duration) bool {
		deadline := time.Now().Add(timeout)
		promptMu.Lock()
		for promptCounts[handle] > 0 {
			remaining := time.Until(deadline)
			if remaining <= 0 {
				promptMu.Unlock()
				return false
			}
			changed := promptChanged
			promptMu.Unlock()
			timer := time.NewTimer(remaining)
			select {
			case <-changed:
				timer.Stop()
			case <-timer.C:
				return false
			}
			promptMu.Lock()
		}
		promptMu.Unlock()
		return true
	}
	reader := bufio.NewScanner(input)
	reader.Buffer(make([]byte, 4096), MCPMaxMessageBytes)
	for reader.Scan() {
		line := strings.TrimSpace(reader.Text())
		if line == "" {
			continue
		}
		var request map[string]any
		decoder := json.NewDecoder(strings.NewReader(line))
		decoder.UseNumber()
		decodeErr := decoder.Decode(&request)
		if decodeErr == nil {
			decodeErr = ensureJSONEOF(decoder)
		}
		if decodeErr != nil {
			respond(nil, nil, map[string]any{"code": -32700, "message": "parse error"})
			continue
		}
		requestID, hasID := request["id"]
		method, methodOK := request["method"].(string)
		if request["jsonrpc"] != "2.0" || !methodOK {
			if hasID {
				respond(requestID, nil, map[string]any{"code": -32602, "message": "invalid JSON-RPC message"})
			}
			continue
		}
		params, ok := request["params"].(map[string]any)
		if request["params"] == nil {
			params, ok = map[string]any{}, true
		}
		if !ok {
			if hasID {
				respond(requestID, nil, map[string]any{"code": -32602, "message": "params must be an object"})
			}
			continue
		}
		if method == "initialize" {
			if !acpProtocolOne(params["protocolVersion"]) {
				if hasID {
					respond(requestID, nil, map[string]any{"code": -32602, "message": "only ACP protocol version 1 is supported"})
				}
				continue
			}
			initialized = true
			respond(requestID, map[string]any{
				"protocolVersion": 1,
				"agentCapabilities": map[string]any{
					"loadSession":         true,
					"promptCapabilities":  map[string]any{"image": false, "audio": false, "embeddedContext": false},
					"mcpCapabilities":     map[string]any{"http": false, "sse": false},
					"sessionCapabilities": map[string]any{"close": map[string]any{}},
				},
				"agentInfo": map[string]any{"name": MCPClientName, "version": MCPClientVersion},
			}, nil)
			continue
		}
		if !hasID { // ACP session/cancel is a notification.
			if !initialized {
				continue
			}
			if method == "session/cancel" {
				handle, _ := params["sessionId"].(string)
				if handle != "" {
					runtime.Cancel("acp", principal, ACPRoute, handle)
					activeMu.Lock()
					session := active[handle]
					activeMu.Unlock()
					if session != nil {
						session.Cancel()
					}
				}
			}
			continue
		}
		if !initialized {
			respond(requestID, nil, map[string]any{"code": -32602, "message": "initialize must be called first"})
			continue
		}
		switch method {
		case "session/new":
			cwd, _ := params["cwd"].(string)
			if cwd == "" || !filepath.IsAbs(cwd) {
				respond(requestID, nil, map[string]any{"code": -32602, "message": "cwd must be an absolute path"})
				continue
			}
			serversValue, exists := params["mcpServers"]
			if !exists {
				serversValue = []any{}
			}
			servers, err := ValidateMCPServers(serversValue)
			if err != nil {
				respond(requestID, nil, map[string]any{"code": -32602, "message": err.Error()})
				continue
			}
			if runtime.Model == "" {
				respond(requestID, nil, map[string]any{"code": -32602, "message": fmt.Sprintf("provider '%s' requires --model", runtime.Provider)})
				continue
			}
			resolvedCWD := resolveACPPath(cwd)
			mcpSession, err := NewACPSessionMCP(servers, resolvedCWD)
			if err != nil {
				respond(requestID, nil, map[string]any{"code": -32000, "message": err.Error()})
				continue
			}
			handle, _, err := runtime.BindSession("acp", principal, ACPRoute, "", runtime.Model, resolvedCWD, "ACP session", nil)
			if err != nil {
				mcpSession.Close()
				respond(requestID, nil, map[string]any{"code": -32602, "message": err.Error()})
				continue
			}
			activeMu.Lock()
			active[handle] = mcpSession
			activeMu.Unlock()
			respond(requestID, map[string]any{"sessionId": handle}, nil)
		case "session/load":
			handle, handleOK := params["sessionId"].(string)
			cwd, cwdOK := params["cwd"].(string)
			if !handleOK || !cwdOK || !filepath.IsAbs(cwd) {
				respond(requestID, nil, map[string]any{"code": -32602, "message": "sessionId and absolute cwd are required"})
				continue
			}
			serversValue, exists := params["mcpServers"]
			if !exists {
				serversValue = []any{}
			}
			servers, err := ValidateMCPServers(serversValue)
			if err != nil {
				respond(requestID, nil, map[string]any{"code": -32602, "message": err.Error()})
				continue
			}
			session, err := runtime.GetSession("acp", principal, ACPRoute, handle)
			if err != nil {
				respond(requestID, nil, map[string]any{"code": -32000, "message": "session load failed"})
				continue
			}
			if session == nil {
				respond(requestID, nil, map[string]any{"code": -32001, "message": "session not found"})
				continue
			}
			resolvedCWD := resolveACPPath(cwd)
			if resolveACPPath(session.CWD) != resolvedCWD {
				respond(requestID, nil, map[string]any{"code": -32602, "message": "cwd does not match the stored session"})
				continue
			}
			mcpSession, err := NewACPSessionMCP(servers, resolvedCWD)
			if err != nil {
				respond(requestID, nil, map[string]any{"code": -32000, "message": err.Error()})
				continue
			}
			activeMu.Lock()
			previous := active[handle]
			activeMu.Unlock()
			if previous != nil {
				runtime.Cancel("acp", principal, ACPRoute, handle)
				previous.Cancel()
				deadline := time.Now().Add(ACPCloseTimeout)
				queueStopped := runtime.CancelAndWait("acp", principal, ACPRoute, handle, time.Until(deadline))
				promptsStopped := waitPrompts(handle, time.Until(deadline))
				if !queueStopped || !promptsStopped {
					mcpSession.Close()
					respond(requestID, nil, map[string]any{"code": -32000, "message": "session work did not stop before the load timeout; existing session resources remain active"})
					continue
				}
			}
			activeMu.Lock()
			active[handle] = mcpSession
			activeMu.Unlock()
			if previous != nil {
				previous.Close()
			}
			history, err := runtime.History("acp", principal, ACPRoute, handle)
			if err != nil {
				respond(requestID, nil, map[string]any{"code": -32000, "message": "session load failed"})
				continue
			}
			replayACPHistory(handle, history, update)
			respond(requestID, map[string]any{}, nil)
		case "session/prompt":
			handle, ok := params["sessionId"].(string)
			if !ok {
				respond(requestID, nil, map[string]any{"code": -32602, "message": "sessionId is required"})
				continue
			}
			session, err := runtime.GetSession("acp", principal, ACPRoute, handle)
			if err != nil {
				respond(requestID, nil, map[string]any{"code": -32000, "message": "session lookup failed"})
				continue
			}
			if session == nil {
				respond(requestID, nil, map[string]any{"code": -32001, "message": "session not found"})
				continue
			}
			prompt, err := acpPromptText(params["prompt"])
			if err != nil {
				respond(requestID, nil, map[string]any{"code": -32602, "message": err.Error()})
				continue
			}
			startPrompt(requestID, handle, session.ID, prompt)
		case "session/cancel":
			handle, _ := params["sessionId"].(string)
			if handle != "" {
				runtime.Cancel("acp", principal, ACPRoute, handle)
				activeMu.Lock()
				session := active[handle]
				activeMu.Unlock()
				if session != nil {
					session.Cancel()
				}
			}
			respond(requestID, map[string]any{}, nil)
		case "session/close":
			handle, ok := params["sessionId"].(string)
			if !ok {
				respond(requestID, nil, map[string]any{"code": -32602, "message": "sessionId is required"})
				continue
			}
			runtime.Cancel("acp", principal, ACPRoute, handle)
			activeMu.Lock()
			mcpSession := active[handle]
			activeMu.Unlock()
			if mcpSession != nil {
				mcpSession.Cancel()
			}
			deadline := time.Now().Add(ACPCloseTimeout)
			queueStopped := runtime.CancelAndWait("acp", principal, ACPRoute, handle, time.Until(deadline))
			promptsStopped := waitPrompts(handle, time.Until(deadline))
			if !queueStopped || !promptsStopped {
				respond(requestID, nil, map[string]any{"code": -32000, "message": "session work did not stop before the close timeout; session resources remain active"})
				continue
			}
			activeMu.Lock()
			delete(active, handle)
			activeMu.Unlock()
			if mcpSession != nil {
				mcpSession.Close()
			}
			respond(requestID, map[string]any{}, nil)
		default:
			respond(requestID, nil, map[string]any{"code": -32601, "message": "method not found"})
		}
	}
	activeMu.Lock()
	closing := active
	active = map[string]*ACPSessionMCP{}
	activeMu.Unlock()
	for handle, session := range closing {
		runtime.Cancel("acp", principal, ACPRoute, handle)
		session.Cancel()
		deadline := time.Now().Add(ACPCloseTimeout)
		runtime.CancelAndWait("acp", principal, ACPRoute, handle, time.Until(deadline))
		waitPrompts(handle, time.Until(deadline))
		session.Close()
	}
	return reader.Err()
}

func handlePrompt(runtime *AdapterRuntime, active map[string]*ACPSessionMCP, activeMu *sync.Mutex, update func(string, map[string]any), respond func(any, any, map[string]any), requestID any, handle, promptText string) {
	activeMu.Lock()
	mcpSession := active[handle]
	activeMu.Unlock()
	if mcpSession == nil {
		respond(requestID, nil, map[string]any{"code": -32001, "message": "session is not active; load it before prompting"})
		return
	}
	messageID, _ := newID()
	update(handle, map[string]any{"sessionUpdate": "user_message_chunk", "messageId": messageID, "content": map[string]any{"type": "text", "text": promptText}})
	agentID, _ := newID()
	var streamedMu sync.Mutex
	streamed := false
	toolIDs := map[string]string{}
	var toolMu sync.Mutex
	callbacks := PromptCallbacks{
		OnDelta: func(delta string) {
			streamedMu.Lock()
			streamed = true
			streamedMu.Unlock()
			update(handle, map[string]any{"sessionUpdate": "agent_message_chunk", "messageId": agentID, "content": map[string]any{"type": "text", "text": delta}})
		},
		OnToolStart: func(callID, name string, args map[string]any) {
			stableID := callID
			if stableID == "" {
				stableID, _ = newID()
			}
			toolMu.Lock()
			toolIDs[callID+"\x00"+name] = stableID
			toolMu.Unlock()
			update(handle, map[string]any{"sessionUpdate": "tool_call", "toolCallId": stableID, "title": name, "name": name, "kind": "other", "status": "in_progress", "rawInput": args})
		},
		OnToolComplete: func(callID, name string, _ map[string]any, outcome ToolOutcome) {
			toolMu.Lock()
			stableID := toolIDs[callID+"\x00"+name]
			toolMu.Unlock()
			if stableID == "" {
				stableID = callID
				if stableID == "" {
					stableID, _ = newID()
				}
			}
			output := outcome.Result
			status := "completed"
			if outcome.Error != "" {
				output, status = outcome.Error, "failed"
			}
			update(handle, map[string]any{"sessionUpdate": "tool_call_update", "toolCallId": stableID, "status": status, "rawOutput": output})
		},
		ToolRegistry: func(ctx context.Context) (ToolDispatcher, error) {
			return mcpSession.ToolRegistry(runtime.ToolRegistry, ctx)
		},
	}
	result, err := runtime.RunPrompt(context.Background(), "acp", "stdio", ACPRoute, handle, promptText, callbacks)
	if err != nil {
		if errors.Is(err, TurnCancelled) || errors.Is(err, context.Canceled) {
			respond(requestID, map[string]any{"stopReason": "cancelled"}, nil)
			return
		}
		var providerErr *ProviderError
		if errors.As(err, &providerErr) {
			respond(requestID, nil, map[string]any{"code": -32000, "message": "provider request failed"})
			return
		}
		respond(requestID, nil, map[string]any{"code": -32000, "message": "prompt failed"})
		return
	}
	streamedMu.Lock()
	hadStream := streamed
	streamedMu.Unlock()
	if !hadStream && result.Text != "" {
		update(handle, map[string]any{"sessionUpdate": "agent_message_chunk", "messageId": agentID, "content": map[string]any{"type": "text", "text": result.Text}})
	}
	respond(requestID, map[string]any{"stopReason": "end_turn"}, nil)
}

func acpProtocolOne(value any) bool {
	switch number := value.(type) {
	case json.Number:
		return number == "1"
	case float64:
		return number == 1
	case int:
		return number == 1
	default:
		return false
	}
}

func resolveACPPath(path string) string {
	abs, err := filepath.Abs(path)
	if err != nil {
		return filepath.Clean(path)
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err == nil {
		return resolved
	}
	return filepath.Clean(abs)
}

func acpPromptText(value any) (string, error) {
	blocks, ok := value.([]any)
	if !ok || len(blocks) == 0 {
		return "", errors.New("prompt must be a non-empty content array")
	}
	var output strings.Builder
	for _, raw := range blocks {
		block, ok := raw.(map[string]any)
		if !ok {
			return "", errors.New("prompt content blocks must be objects")
		}
		if block["type"] == "text" {
			text, ok := block["text"].(string)
			if !ok {
				return "", errors.New("text and resource_link prompt blocks are supported")
			}
			output.WriteString(text)
			continue
		}
		if block["type"] != "resource_link" {
			return "", errors.New("text and resource_link prompt blocks are supported")
		}
		if stringOrEmpty(block["name"]) == "" || stringOrEmpty(block["uri"]) == "" {
			return "", errors.New("resource_link prompt blocks require non-empty name and uri strings")
		}
		encoded, err := marshalJSON(block)
		if err != nil {
			return "", errors.New("resource_link prompt block must contain JSON-compatible fields")
		}
		output.WriteString("\n[ACP resource_link reference; untrusted and not fetched]\n")
		output.Write(encoded)
		output.WriteByte('\n')
	}
	return output.String(), nil
}

func replayACPHistory(handle string, messages []Message, update func(string, map[string]any)) {
	activeTools := map[string]string{}
	for _, message := range messages {
		switch message.Role {
		case "user":
			update(handle, map[string]any{"sessionUpdate": "user_message_chunk", "messageId": message.ID, "content": map[string]any{"type": "text", "text": message.Content}})
		case "assistant":
			if message.Content != "" {
				update(handle, map[string]any{"sessionUpdate": "agent_message_chunk", "messageId": message.ID, "content": map[string]any{"type": "text", "text": message.Content}})
			}
			for _, call := range nativeToolCalls(message.ProviderData) {
				stable := call.id
				if stable == "" {
					stable, _ = newID()
				}
				activeTools[call.id] = stable
				update(handle, map[string]any{"sessionUpdate": "tool_call", "toolCallId": stable, "title": call.name, "name": call.name, "kind": "other", "status": "in_progress", "rawInput": call.arguments})
			}
		case "tool":
			data, _ := message.ProviderData.(map[string]any)
			result, _ := data["tool_result"].(map[string]any)
			id := stringOrEmpty(result["id"])
			if stable := activeTools[id]; stable != "" {
				status := "completed"
				if result["is_error"] == true {
					status = "failed"
				}
				rawOutput := result["content"]
				if rawOutput == nil {
					rawOutput = message.Content
				}
				update(handle, map[string]any{"sessionUpdate": "tool_call_update", "toolCallId": stable, "status": status, "rawOutput": rawOutput})
			}
		}
	}
}

type nativeACPToolCall struct {
	id, name  string
	arguments any
}

func nativeToolCalls(providerData any) []nativeACPToolCall {
	data, ok := providerData.(map[string]any)
	if !ok {
		return nil
	}
	native, _ := data["message"].(map[string]any)
	provider, _ := data["provider"].(string)
	result := make([]nativeACPToolCall, 0)
	switch provider {
	case "openai":
		calls, _ := native["tool_calls"].([]any)
		for _, raw := range calls {
			call, _ := raw.(map[string]any)
			function, _ := call["function"].(map[string]any)
			arguments := function["arguments"]
			if text, ok := arguments.(string); ok {
				var value any
				decoder := json.NewDecoder(strings.NewReader(text))
				decoder.UseNumber()
				if decoder.Decode(&value) == nil {
					arguments = value
				}
			}
			result = append(result, nativeACPToolCall{id: stringOrEmpty(call["id"]), name: stringOrDefault(function["name"], "tool"), arguments: arguments})
		}
	case "anthropic":
		blocks, _ := native["content"].([]any)
		for _, raw := range blocks {
			block, _ := raw.(map[string]any)
			if block["type"] == "tool_use" {
				result = append(result, nativeACPToolCall{id: stringOrEmpty(block["id"]), name: stringOrDefault(block["name"], "tool"), arguments: block["input"]})
			}
		}
	case "gemini":
		parts, _ := native["parts"].([]any)
		for _, raw := range parts {
			part, _ := raw.(map[string]any)
			function, _ := part["functionCall"].(map[string]any)
			if function != nil {
				result = append(result, nativeACPToolCall{id: stringOrEmpty(function["id"]), name: stringOrDefault(function["name"], "tool"), arguments: function["args"]})
			}
		}
	}
	return result
}
