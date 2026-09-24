package chat

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

const MaxGatewayBody = 1_048_576
const gatewayBodyReadTimeout = 30 * time.Second

// NewGatewayHandler builds the text-only OpenAI-compatible chat-completions endpoint.
func NewGatewayHandler(runtime *AdapterRuntime, apiKey string) http.Handler {
	return newGatewayHandler(runtime, apiKey, gatewayBodyReadTimeout)
}

func newGatewayHandler(runtime *AdapterRuntime, apiKey string, bodyReadTimeout time.Duration) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost || request.URL.Path != GatewayRoute || request.URL.RawQuery != "" {
			writeGatewayJSON(writer, http.StatusNotFound, gatewayError("not found", "invalid_request_error"), "")
			return
		}
		scheme, supplied, ok := strings.Cut(request.Header.Get("Authorization"), " ")
		if !ok || !strings.EqualFold(scheme, "Bearer") || subtle.ConstantTimeCompare([]byte(strings.TrimSpace(supplied)), []byte(apiKey)) != 1 {
			writeGatewayJSON(writer, http.StatusUnauthorized, gatewayError("unauthorized", "authentication_error"), "")
			return
		}
		if request.ContentLength > MaxGatewayBody {
			writeGatewayJSON(writer, http.StatusRequestEntityTooLarge, gatewayError("request body too large", "invalid_request_error"), "")
			return
		}
		controller := http.NewResponseController(writer)
		readDeadlineSet := bodyReadTimeout > 0 && controller.SetReadDeadline(time.Now().Add(bodyReadTimeout)) == nil
		body, err := io.ReadAll(http.MaxBytesReader(writer, request.Body, MaxGatewayBody))
		if readDeadlineSet {
			_ = controller.SetReadDeadline(time.Time{})
		}
		if err != nil {
			status := http.StatusBadRequest
			if strings.Contains(strings.ToLower(err.Error()), "request body too large") {
				status = http.StatusRequestEntityTooLarge
			}
			writeGatewayJSON(writer, status, gatewayError("invalid request body", "invalid_request_error"), "")
			return
		}
		if !utf8.Valid(body) {
			writeGatewayJSON(writer, http.StatusBadRequest, gatewayError("request body must be UTF-8 JSON", "invalid_request_error"), "")
			return
		}
		decoder := json.NewDecoder(strings.NewReader(string(body)))
		decoder.UseNumber()
		var payload map[string]any
		if err := decoder.Decode(&payload); err != nil || payload == nil || ensureJSONEOF(decoder) != nil {
			writeGatewayJSON(writer, http.StatusBadRequest, gatewayError("request body must be a JSON object", "invalid_request_error"), "")
			return
		}
		for _, key := range []string{"tools", "tool_choice", "functions", "function_call"} {
			if value, exists := payload[key]; exists && value != nil {
				writeGatewayJSON(writer, http.StatusBadRequest, gatewayError("client-provided tools are unsupported; harness tools are configured by the server", "invalid_request_error"), "")
				return
			}
		}
		rawMessages, ok := payload["messages"].([]any)
		if !ok {
			writeGatewayJSON(writer, http.StatusBadRequest, gatewayError("messages must be an array", "invalid_request_error"), "")
			return
		}
		history, prompt, err := gatewayHistory(rawMessages, runtime.Provider)
		if err != nil {
			writeGatewayJSON(writer, http.StatusBadRequest, gatewayError(err.Error(), "invalid_request_error"), "")
			return
		}
		stream := false
		if value, exists := payload["stream"]; exists {
			stream, ok = value.(bool)
			if !ok {
				writeGatewayJSON(writer, http.StatusBadRequest, gatewayError("stream must be a boolean", "invalid_request_error"), "")
				return
			}
		}
		model, ok := payload["model"].(string)
		if !ok || model == "" {
			writeGatewayJSON(writer, http.StatusBadRequest, gatewayError("model is required and must be a non-empty string", "invalid_request_error"), "")
			return
		}
		principal := PrincipalForToken(strings.TrimSpace(supplied))
		cwd, _ := os.Getwd()
		handle, session, err := runtime.BindSession("gateway", principal, GatewayRoute, request.Header.Get("X-Hermes-Session-Id"), model, cwd, "Gateway conversation", history)
		if err != nil {
			writeGatewayJSON(writer, http.StatusBadRequest, gatewayError(err.Error(), "invalid_request_error"), "")
			return
		}
		if session.Provider != runtime.Provider {
			writeGatewayJSON(writer, http.StatusBadRequest, gatewayError("session is bound to a different provider", "invalid_request_error"), "")
			return
		}
		sessionModel := firstNonempty(session.Model, model)
		if sessionModel != model {
			writeGatewayJSON(writer, http.StatusBadRequest, gatewayError("session is bound to a different model", "invalid_request_error"), "")
			return
		}
		if stream {
			serveGatewayStream(writer, request, runtime, principal, handle, prompt, sessionModel)
			return
		}
		result, err := runtime.RunPrompt(request.Context(), "gateway", principal, GatewayRoute, handle, prompt, PromptCallbacks{})
		if err != nil {
			writeGatewayJSON(writer, http.StatusBadGateway, gatewayError("turn failed", "server_error"), handle)
			return
		}
		writeGatewayJSON(writer, http.StatusOK, map[string]any{
			"id": gatewayCompletionID(), "object": "chat.completion", "created": time.Now().Unix(), "model": sessionModel,
			"session_id": handle,
			"choices":    []any{map[string]any{"index": 0, "message": map[string]any{"role": "assistant", "content": result.Text}, "finish_reason": "stop"}},
			"usage":      gatewayUsage(result),
		}, handle)
	})
}

func writeGatewayJSON(writer http.ResponseWriter, status int, payload map[string]any, sessionID string) {
	body, err := marshalJSON(payload)
	if err != nil {
		body = []byte(`{"error":{"message":"response encoding failed","type":"server_error"}}`)
	}
	writer.Header().Set("Content-Type", "application/json; charset=utf-8")
	writer.Header().Set("Content-Length", fmt.Sprint(len(body)))
	if sessionID != "" {
		writer.Header().Set("X-Hermes-Session-Id", sessionID)
	}
	writer.WriteHeader(status)
	_, _ = writer.Write(body)
}

func gatewayError(message, kind string) map[string]any {
	return map[string]any{"error": map[string]any{"message": message, "type": kind}}
}

func gatewayCompletionID() string {
	id, _ := newID()
	return "chatcmpl-" + strings.ReplaceAll(id, "-", "")
}

func gatewayUsage(result TurnResult) map[string]int64 {
	return map[string]int64{"prompt_tokens": result.PromptTokens, "completion_tokens": result.CompletionTokens, "total_tokens": result.PromptTokens + result.CompletionTokens}
}

func gatewayHistory(messages []any, provider string) ([]Message, string, error) {
	if len(messages) == 0 {
		return nil, "", errors.New("messages must contain at least one user message")
	}
	normalized := make([]Message, 0, len(messages))
	lastUser := -1
	for _, raw := range messages {
		object, ok := raw.(map[string]any)
		if !ok {
			return nil, "", errors.New("each message must be an object")
		}
		role, ok := object["role"].(string)
		if !ok || (role != "system" && role != "user" && role != "assistant" && role != "tool") {
			return nil, "", errors.New("message role must be system, user, assistant, or tool")
		}
		content, err := gatewayMessageText(object["content"])
		if err != nil {
			return nil, "", err
		}
		message := Message{Role: role, Content: content}
		calls, hasCalls := object["tool_calls"]
		if role == "assistant" && hasCalls && isTruthyJSON(calls) {
			callList, ok := calls.([]any)
			if !ok {
				return nil, "", errors.New("assistant tool_calls must be an array")
			}
			switch provider {
			case "openai":
				message.ProviderData = map[string]any{"provider": "openai", "message": map[string]any{"role": "assistant", "content": content, "tool_calls": callList}}
			case "anthropic":
				blocks := make([]any, 0, len(callList)+1)
				if content != "" {
					blocks = append(blocks, map[string]any{"type": "text", "text": content})
				}
				for _, item := range callList {
					call, ok := item.(map[string]any)
					if !ok {
						return nil, "", errors.New("assistant tool call must include a function object")
					}
					function, ok := call["function"].(map[string]any)
					if !ok {
						return nil, "", errors.New("assistant tool call must include a function object")
					}
					arguments, exists := function["arguments"]
					if !exists {
						arguments = map[string]any{}
					}
					if text, ok := arguments.(string); ok {
						var decoded any
						decoder := json.NewDecoder(strings.NewReader(text))
						decoder.UseNumber()
						if decoder.Decode(&decoded) != nil || ensureJSONEOF(decoder) != nil {
							return nil, "", errors.New("assistant tool arguments must be valid JSON")
						}
						arguments = decoded
					}
					if _, ok := arguments.(map[string]any); !ok {
						return nil, "", errors.New("assistant tool arguments must be an object")
					}
					blocks = append(blocks, map[string]any{"type": "tool_use", "id": stringOrEmpty(call["id"]), "name": stringOrEmpty(function["name"]), "input": arguments})
				}
				message.ProviderData = map[string]any{"provider": "anthropic", "message": map[string]any{"role": "assistant", "content": blocks}}
			default:
				return nil, "", errors.New("tool-call history is only supported for anthropic and openai providers")
			}
		}
		if role == "tool" {
			toolID, ok := object["tool_call_id"].(string)
			if !ok || toolID == "" {
				return nil, "", errors.New("tool message must include tool_call_id")
			}
			message.ProviderData = map[string]any{"provider": provider, "tool_result": map[string]any{
				"id": toolID, "name": stringOrEmpty(object["name"]), "content": content, "result": nil, "is_error": false,
			}}
		}
		normalized = append(normalized, message)
		if role == "user" {
			lastUser = len(normalized) - 1
		}
	}
	if lastUser < 0 {
		return nil, "", errors.New("messages must contain a user message")
	}
	if lastUser != len(normalized)-1 {
		return nil, "", errors.New("the final message must be from the user")
	}
	return normalized[:lastUser], normalized[lastUser].Content, nil
}

func gatewayMessageText(value any) (string, error) {
	if text, ok := value.(string); ok {
		return text, nil
	}
	if value == nil {
		return "", nil
	}
	items, ok := value.([]any)
	if !ok {
		return "", errors.New("message content must be text")
	}
	var result strings.Builder
	for _, item := range items {
		block, ok := item.(map[string]any)
		if !ok || block["type"] != "text" {
			return "", errors.New("only text message content is supported")
		}
		text, ok := block["text"].(string)
		if !ok {
			return "", errors.New("only text message content is supported")
		}
		result.WriteString(text)
	}
	return result.String(), nil
}

func stringOrEmpty(value any) string {
	text, _ := value.(string)
	return text
}

func isTruthyJSON(value any) bool {
	switch typed := value.(type) {
	case nil:
		return false
	case bool:
		return typed
	case string:
		return typed != ""
	case []any:
		return len(typed) != 0
	case map[string]any:
		return len(typed) != 0
	default:
		return true
	}
}

func serveGatewayStream(writer http.ResponseWriter, request *http.Request, runtime *AdapterRuntime, principal, handle, prompt, model string) {
	header := writer.Header()
	header.Set("Content-Type", "text/event-stream; charset=utf-8")
	header.Set("Cache-Control", "no-cache")
	header.Set("Connection", "close")
	header.Set("X-Hermes-Session-Id", handle)
	writer.WriteHeader(http.StatusOK)
	controller := http.NewResponseController(writer)
	_ = controller.Flush()
	completionID := gatewayCompletionID()
	created := time.Now().Unix()
	var writeMu sync.Mutex
	writeFrame := func(payload map[string]any, event string) error {
		encoded, err := marshalJSON(payload)
		if err != nil {
			return err
		}
		var frame strings.Builder
		if event != "" {
			frame.WriteString("event: ")
			frame.WriteString(event)
			frame.WriteByte('\n')
		}
		frame.WriteString("data: ")
		frame.Write(encoded)
		frame.WriteString("\n\n")
		writeMu.Lock()
		defer writeMu.Unlock()
		if _, err := io.WriteString(writer, frame.String()); err != nil {
			return err
		}
		return controller.Flush()
	}
	keepaliveDone := make(chan struct{})
	keepaliveStopped := make(chan struct{})
	go func() {
		defer close(keepaliveStopped)
		ticker := time.NewTicker(15 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-keepaliveDone:
				return
			case <-request.Context().Done():
				return
			case <-ticker.C:
				writeMu.Lock()
				_, err := io.WriteString(writer, ": keep-alive\n\n")
				if err == nil {
					err = controller.Flush()
				}
				writeMu.Unlock()
				if err != nil {
					runtime.Cancel("gateway", principal, GatewayRoute, handle)
					return
				}
			}
		}
	}()
	delta := func(text string) {
		if err := writeFrame(map[string]any{
			"id": completionID, "object": "chat.completion.chunk", "created": created, "model": model,
			"choices": []any{map[string]any{"index": 0, "delta": map[string]any{"content": text}, "finish_reason": nil}},
		}, ""); err != nil {
			runtime.Cancel("gateway", principal, GatewayRoute, handle)
		}
	}
	toolStart := func(id, name string, _ map[string]any) {
		if err := writeFrame(map[string]any{"tool": name, "toolCallId": nilIfEmpty(id), "status": "running"}, "hermes.tool.progress"); err != nil {
			runtime.Cancel("gateway", principal, GatewayRoute, handle)
		}
	}
	result, err := runtime.RunPrompt(request.Context(), "gateway", principal, GatewayRoute, handle, prompt, PromptCallbacks{OnDelta: delta, OnToolStart: toolStart})
	if err != nil {
		if !errors.Is(err, context.Canceled) && !errors.Is(err, TurnCancelled) {
			_ = writeFrame(gatewayError("turn failed", "server_error"), "")
		}
	} else {
		_ = writeFrame(map[string]any{
			"id": completionID, "object": "chat.completion.chunk", "created": created, "model": model, "session_id": handle,
			"choices": []any{map[string]any{"index": 0, "delta": map[string]any{}, "finish_reason": "stop"}},
			"usage":   gatewayUsage(result),
		}, "")
	}
	close(keepaliveDone)
	<-keepaliveStopped
	writeMu.Lock()
	_, _ = io.WriteString(writer, "data: [DONE]\n\n")
	_ = controller.Flush()
	writeMu.Unlock()
}

func nilIfEmpty(value string) any {
	if value == "" {
		return nil
	}
	return value
}
