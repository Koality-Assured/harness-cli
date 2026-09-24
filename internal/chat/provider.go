package chat

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"
	"time"
)

const (
	DefaultTimeout        = 120 * time.Second
	AnthropicDefaultModel = "claude-sonnet-4-5"
	AnthropicBaseURLEnv   = "HARNESS_ANTHROPIC_BASE_URL"
	anthropicMessagesPath = "/v1/messages"
)

var providerAliases = map[string]string{
	"claude": "anthropic", "anthropic": "anthropic", "cursor": "cursor",
	"gemini": "gemini", "google": "gemini", "openai": "openai", "gpt": "openai",
}

// HTTPDoer makes provider requests replaceable with deterministic fake transports.
type HTTPDoer interface {
	Do(*http.Request) (*http.Response, error)
}

// ProviderError contains a redacted provider failure without the credential.
type ProviderError struct {
	Provider string
	Status   int
	Message  string
	Excerpt  string
}

func (e *ProviderError) Error() string {
	if e.Status != 0 {
		if e.Excerpt != "" {
			return fmt.Sprintf("%s HTTP %d: %s", e.Provider, e.Status, e.Excerpt)
		}
		return fmt.Sprintf("%s HTTP %d", e.Provider, e.Status)
	}
	if e.Message != "" {
		return e.Message
	}
	return e.Provider + " request failed"
}

var ErrStreamCancelled = errors.New("provider stream was cancelled")

// ToolCall retains the provider's ID and untrusted input until local validation.
type ToolCall struct {
	ID        string
	Name      string
	Arguments any
}

// Completion is one provider response with native history and optional usage.
type Completion struct {
	Text            string
	InputTokens     *int64
	OutputTokens    *int64
	ToolCalls       []ToolCall
	ProviderMessage map[string]any
}

// ProviderClient calls only the documented provider endpoints used by the Python prototype.
type ProviderClient struct {
	HTTP    HTTPDoer
	Timeout time.Duration
}

func NormalizeProvider(raw string) (string, bool) {
	provider, ok := providerAliases[strings.ToLower(strings.TrimSpace(raw))]
	return provider, ok
}

func (c *ProviderClient) client() HTTPDoer {
	if c.HTTP != nil {
		return c.HTTP
	}
	timeout := c.Timeout
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	return &http.Client{Timeout: timeout}
}

func resolveModel(provider, model string) (string, error) {
	if model != "" {
		return model, nil
	}
	if provider == "anthropic" {
		return AnthropicDefaultModel, nil
	}
	return "", &ProviderError{Provider: provider, Message: provider + " requires --model"}
}

func (c *ProviderClient) Complete(ctx context.Context, provider, model string, messages []Message, apiKey string, tools []ToolDefinition) (Completion, error) {
	canonical, ok := NormalizeProvider(provider)
	if !ok || canonical == "cursor" {
		if canonical == "cursor" {
			return Completion{}, &ProviderError{Provider: canonical, Message: "provider 'cursor' chat is not wired: no documented public chat endpoint to call"}
		}
		return Completion{}, &ProviderError{Provider: provider, Message: fmt.Sprintf("unsupported provider %q", provider)}
	}
	resolved, err := resolveModel(canonical, model)
	if err != nil {
		return Completion{}, err
	}
	endpoint, headers, payload, err := buildRequest(canonical, resolved, messages, apiKey, tools, false)
	if err != nil {
		return Completion{}, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return Completion{}, err
	}
	for name, value := range headers {
		request.Header.Set(name, value)
	}
	response, err := c.client().Do(request)
	if err != nil {
		if ctx.Err() != nil {
			return Completion{}, ctx.Err()
		}
		return Completion{}, &ProviderError{Provider: canonical, Message: "network error: " + err.Error()}
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		return Completion{}, &ProviderError{Provider: canonical, Message: "failed to read provider response"}
	}
	if response.StatusCode != http.StatusOK {
		return Completion{}, providerHTTPError(canonical, response.StatusCode, body, apiKey)
	}
	data, err := decodeObject(body)
	if err != nil {
		return Completion{}, &ProviderError{Provider: canonical, Message: "provider returned an invalid JSON response"}
	}
	return decodeCompletion(canonical, data)
}

// Stream emits text deltas and returns only after the provider's final stream event is assembled.
func (c *ProviderClient) Stream(ctx context.Context, provider, model string, messages []Message, apiKey string, tools []ToolDefinition, onDelta func(string)) (Completion, error) {
	canonical, ok := NormalizeProvider(provider)
	if !ok || canonical == "cursor" {
		if canonical == "cursor" {
			return Completion{}, &ProviderError{Provider: canonical, Message: "provider 'cursor' chat is not wired: no documented public chat endpoint to call"}
		}
		return Completion{}, &ProviderError{Provider: provider, Message: fmt.Sprintf("unsupported provider %q", provider)}
	}
	resolved, err := resolveModel(canonical, model)
	if err != nil {
		return Completion{}, err
	}
	endpoint, headers, payload, err := buildRequest(canonical, resolved, messages, apiKey, tools, true)
	if err != nil {
		return Completion{}, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return Completion{}, err
	}
	for name, value := range headers {
		request.Header.Set(name, value)
	}
	response, err := c.client().Do(request)
	if err != nil {
		if ctx.Err() != nil {
			return Completion{}, ErrStreamCancelled
		}
		return Completion{}, &ProviderError{Provider: canonical, Message: "network error: " + err.Error()}
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(response.Body)
		return Completion{}, providerHTTPError(canonical, response.StatusCode, body, apiKey)
	}
	return readStream(ctx, canonical, response.Body, apiKey, onDelta)
}

func buildRequest(provider, model string, messages []Message, apiKey string, tools []ToolDefinition, stream bool) (string, map[string]string, []byte, error) {
	headers := map[string]string{"content-type": "application/json"}
	var payload map[string]any
	switch provider {
	case "anthropic":
		payload = map[string]any{"model": model, "max_tokens": 4096, "messages": messagesForAnthropic(messages)}
		if system := systemFromMessages(messages); system != "" {
			payload["system"] = system
		}
		if stream {
			payload["stream"] = true
		}
		if definitions := anthropicTools(tools); len(definitions) > 0 {
			payload["tools"] = definitions
		}
		headers["x-api-key"] = apiKey
		headers["anthropic-version"] = "2023-06-01"
		endpoint, err := anthropicMessagesEndpoint()
		if err != nil {
			return "", nil, nil, err
		}
		return marshalRequest(endpoint, headers, payload)
	case "openai":
		payload = map[string]any{"model": model, "messages": messagesForOpenAI(messages)}
		if stream {
			payload["stream"] = true
			payload["stream_options"] = map[string]any{"include_usage": true}
		}
		if definitions := openAITools(tools); len(definitions) > 0 {
			payload["tools"] = definitions
		}
		headers["authorization"] = "Bearer " + apiKey
		return marshalRequest("https://api.openai.com/v1/chat/completions", headers, payload)
	case "gemini":
		payload = map[string]any{"contents": geminiContents(messages)}
		if system := systemFromMessages(messages); system != "" {
			payload["systemInstruction"] = map[string]any{"parts": []any{map[string]any{"text": system}}}
		}
		if definitions := geminiTools(tools); len(definitions) > 0 {
			payload["tools"] = []any{map[string]any{"functionDeclarations": definitions}}
		}
		headers["x-goog-api-key"] = apiKey
		method := "generateContent"
		if stream {
			method = "streamGenerateContent?alt=sse"
		}
		endpoint := "https://generativelanguage.googleapis.com/v1beta/models/" + url.PathEscape(model) + ":" + method
		return marshalRequest(endpoint, headers, payload)
	default:
		return "", nil, nil, fmt.Errorf("unsupported provider %q", provider)
	}
}

func anthropicMessagesEndpoint() (string, error) {
	base := strings.TrimSpace(os.Getenv(AnthropicBaseURLEnv))
	if base == "" {
		return "https://api.anthropic.com" + anthropicMessagesPath, nil
	}
	parsed, err := url.Parse(base)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" || parsed.User != nil || (parsed.Path != "" && parsed.Path != "/") || parsed.RawQuery != "" || parsed.ForceQuery || parsed.Fragment != "" {
		return "", &ProviderError{Provider: "anthropic", Message: "invalid " + AnthropicBaseURLEnv + "; expected an HTTP(S) origin"}
	}
	if _, err := url.ParseRequestURI(base); err != nil {
		return "", &ProviderError{Provider: "anthropic", Message: "invalid " + AnthropicBaseURLEnv + "; expected an HTTP(S) origin"}
	}
	return strings.TrimRight(base, "/") + anthropicMessagesPath, nil
}

func marshalRequest(endpoint string, headers map[string]string, payload map[string]any) (string, map[string]string, []byte, error) {
	body, err := json.Marshal(payload)
	return endpoint, headers, body, err
}

func anthropicTools(tools []ToolDefinition) []any {
	result := make([]any, 0, len(tools))
	for _, tool := range tools {
		result = append(result, map[string]any{"name": tool.Name, "description": tool.Description, "input_schema": tool.InputSchema})
	}
	return result
}

func openAITools(tools []ToolDefinition) []any {
	result := make([]any, 0, len(tools))
	for _, tool := range tools {
		result = append(result, map[string]any{"type": "function", "function": map[string]any{
			"name": tool.Name, "description": tool.Description, "parameters": tool.InputSchema,
		}})
	}
	return result
}

func geminiTools(tools []ToolDefinition) []any {
	result := make([]any, 0, len(tools))
	for _, tool := range tools {
		result = append(result, map[string]any{"name": tool.Name, "description": tool.Description, "parameters": tool.InputSchema})
	}
	return result
}

func providerData(message Message) map[string]any {
	data, _ := message.ProviderData.(map[string]any)
	return data
}

func systemFromMessages(messages []Message) string {
	var parts []string
	for _, message := range messages {
		if message.Role == "system" && message.Content != "" {
			parts = append(parts, message.Content)
		}
	}
	return strings.Join(parts, "\n")
}

func messagesForAnthropic(messages []Message) []any {
	var out []any
	var pending []any
	flush := func() {
		if len(pending) > 0 {
			out = append(out, map[string]any{"role": "user", "content": append([]any(nil), pending...)})
			pending = pending[:0]
		}
	}
	for _, message := range messages {
		if message.Role == "system" {
			continue
		}
		data := providerData(message)
		if message.Role == "tool" && data["provider"] == "anthropic" {
			if result, ok := data["tool_result"].(map[string]any); ok {
				block := map[string]any{"type": "tool_result", "tool_use_id": valueOr(result["id"], ""), "content": valueOr(result["content"], message.Content)}
				if result["is_error"] == true {
					block["is_error"] = true
				}
				pending = append(pending, block)
				continue
			}
		}
		flush()
		if message.Role == "assistant" && data["provider"] == "anthropic" {
			if native, ok := data["message"].(map[string]any); ok && native["role"] == "assistant" {
				out = append(out, native)
				continue
			}
		}
		role := message.Role
		content := any(message.Content)
		if role != "user" && role != "assistant" {
			role, content = "user", "Tool result: "+message.Content
		}
		out = append(out, map[string]any{"role": role, "content": content})
	}
	flush()
	return out
}

func messagesForOpenAI(messages []Message) []any {
	var out []any
	for _, message := range messages {
		data := providerData(message)
		if message.Role == "assistant" && data["provider"] == "openai" {
			if native, ok := data["message"].(map[string]any); ok && native["role"] == "assistant" {
				out = append(out, native)
				continue
			}
		}
		if message.Role == "tool" && data["provider"] == "openai" {
			if result, ok := data["tool_result"].(map[string]any); ok {
				out = append(out, map[string]any{"role": "tool", "tool_call_id": valueOr(result["id"], ""), "content": valueOr(result["content"], message.Content)})
				continue
			}
		}
		role, content := message.Role, any(message.Content)
		if role != "system" && role != "user" && role != "assistant" {
			role, content = "user", "Tool result: "+message.Content
		}
		out = append(out, map[string]any{"role": role, "content": content})
	}
	return out
}

func geminiContents(messages []Message) []any {
	var out []any
	var pending []any
	flush := func() {
		if len(pending) > 0 {
			out = append(out, map[string]any{"role": "user", "parts": append([]any(nil), pending...)})
			pending = pending[:0]
		}
	}
	for _, message := range messages {
		if message.Role == "system" {
			continue
		}
		data := providerData(message)
		if message.Role == "tool" && data["provider"] == "gemini" {
			if result, ok := data["tool_result"].(map[string]any); ok {
				response := result["result"]
				if result["is_error"] == true {
					response = map[string]any{"error": valueOr(result["content"], "Tool failed.")}
				} else if _, ok := response.(map[string]any); !ok {
					response = map[string]any{"result": response}
				}
				functionResponse := map[string]any{"name": valueOr(result["name"], ""), "response": response}
				if id, ok := result["id"].(string); ok && id != "" {
					functionResponse["id"] = id
				}
				pending = append(pending, map[string]any{"functionResponse": functionResponse})
				continue
			}
		}
		flush()
		if message.Role == "assistant" && data["provider"] == "gemini" {
			if native, ok := data["message"].(map[string]any); ok && native["role"] == "model" {
				out = append(out, native)
				continue
			}
		}
		role, content := "user", message.Content
		if message.Role == "assistant" {
			role = "model"
		} else if message.Role != "user" {
			content = "Tool result: " + message.Content
		}
		out = append(out, map[string]any{"role": role, "parts": []any{map[string]any{"text": content}}})
	}
	flush()
	return out
}

func valueOr(value, fallback any) any {
	if value == nil {
		return fallback
	}
	return value
}

func decodeObject(body []byte) (map[string]any, error) {
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	var result map[string]any
	if err := decoder.Decode(&result); err != nil {
		return nil, err
	}
	if result == nil {
		return nil, errors.New("expected JSON object")
	}
	return result, nil
}

func decodeCompletion(provider string, data map[string]any) (Completion, error) {
	var result Completion
	switch provider {
	case "anthropic":
		blocks, _ := data["content"].([]any)
		var texts []string
		var calls []ToolCall
		for _, value := range blocks {
			block, ok := value.(map[string]any)
			if !ok {
				continue
			}
			switch block["type"] {
			case "text":
				if text, ok := block["text"].(string); ok {
					texts = append(texts, text)
				}
			case "tool_use":
				calls = append(calls, ToolCall{ID: stringValue(block["id"]), Name: stringValue(block["name"]), Arguments: block["input"]})
			}
		}
		result.Text = strings.Join(texts, "")
		result.ToolCalls = calls
		result.ProviderMessage = map[string]any{"role": "assistant", "content": blocks}
		usage, _ := data["usage"].(map[string]any)
		result.InputTokens, result.OutputTokens = tokenCount(usage["input_tokens"]), tokenCount(usage["output_tokens"])
	case "openai":
		choices, _ := data["choices"].([]any)
		if len(choices) > 0 {
			choice, _ := choices[0].(map[string]any)
			message, _ := choice["message"].(map[string]any)
			result.Text = stringValue(message["content"])
			result.ProviderMessage = message
			if result.ProviderMessage == nil {
				result.ProviderMessage = map[string]any{"role": "assistant", "content": ""}
			}
			toolCalls, _ := message["tool_calls"].([]any)
			for _, value := range toolCalls {
				call, _ := value.(map[string]any)
				function, _ := call["function"].(map[string]any)
				result.ToolCalls = append(result.ToolCalls, ToolCall{ID: stringValue(call["id"]), Name: stringValue(function["name"]), Arguments: function["arguments"]})
			}
		} else {
			result.ProviderMessage = map[string]any{"role": "assistant", "content": ""}
		}
		usage, _ := data["usage"].(map[string]any)
		result.InputTokens, result.OutputTokens = tokenCount(usage["prompt_tokens"]), tokenCount(usage["completion_tokens"])
	case "gemini":
		candidates, _ := data["candidates"].([]any)
		if len(candidates) > 0 {
			candidate, _ := candidates[0].(map[string]any)
			content, _ := candidate["content"].(map[string]any)
			parts, _ := content["parts"].([]any)
			var texts []string
			for _, value := range parts {
				part, _ := value.(map[string]any)
				if text, ok := part["text"].(string); ok {
					texts = append(texts, text)
				}
				functionCall, _ := part["functionCall"].(map[string]any)
				if functionCall != nil {
					result.ToolCalls = append(result.ToolCalls, ToolCall{ID: stringValue(functionCall["id"]), Name: stringValue(functionCall["name"]), Arguments: functionCall["args"]})
				}
			}
			result.Text = strings.Join(texts, "")
			result.ProviderMessage = cloneMap(content)
			if result.ProviderMessage == nil {
				result.ProviderMessage = map[string]any{"role": "model"}
			} else if result.ProviderMessage["role"] == nil {
				result.ProviderMessage["role"] = "model"
			}
		}
		usage, _ := data["usageMetadata"].(map[string]any)
		result.InputTokens, result.OutputTokens = tokenCount(usage["promptTokenCount"]), tokenCount(usage["candidatesTokenCount"])
	}
	if provider == "anthropic" || provider == "openai" {
		for _, call := range result.ToolCalls {
			if strings.TrimSpace(call.ID) == "" {
				return Completion{}, &ProviderError{Provider: provider, Message: provider + " returned a tool call without a required call ID"}
			}
		}
	}
	return result, nil
}

func cloneMap(value map[string]any) map[string]any {
	if value == nil {
		return nil
	}
	result := make(map[string]any, len(value))
	for key, item := range value {
		result[key] = item
	}
	return result
}

func stringValue(value any) string {
	text, _ := value.(string)
	return text
}

func tokenCount(value any) *int64 {
	switch number := value.(type) {
	case json.Number:
		parsed, err := number.Int64()
		if err == nil && parsed >= 0 {
			return &parsed
		}
	case int:
		if number >= 0 {
			value := int64(number)
			return &value
		}
	case int64:
		if number >= 0 {
			value := number
			return &value
		}
	case float64:
		if number >= 0 && math.Trunc(number) == number && number <= math.MaxInt64 {
			value := int64(number)
			return &value
		}
	}
	return nil
}

func providerHTTPError(provider string, status int, body []byte, apiKey string) error {
	excerpt := string(body)
	if apiKey != "" {
		excerpt = strings.ReplaceAll(excerpt, apiKey, "[redacted]")
	}
	excerpt = strings.Join(strings.Fields(excerpt), " ")
	if len(excerpt) > 240 {
		excerpt = excerpt[:237] + "..."
	}
	return &ProviderError{Provider: provider, Status: status, Excerpt: excerpt}
}

func readStream(ctx context.Context, provider string, source io.Reader, apiKey string, onDelta func(string)) (Completion, error) {
	reader := bufio.NewReader(source)
	var eventName string
	var dataLines []string
	var assembled strings.Builder
	inputTokens, outputTokens := (*int64)(nil), (*int64)(nil)
	anthropicBlocks := map[int]map[string]any{}
	anthropicArguments := map[int][]string{}
	openAICalls := map[int]map[string]any{}
	geminiTemplates := map[int]map[string]any{}
	geminiParts := map[int]map[int]map[string]any{}

	consume := func(name, raw string) error {
		if raw == "[DONE]" {
			return errStreamDone
		}
		data, err := decodeObject([]byte(raw))
		if err != nil {
			if strings.TrimSpace(raw) == "" {
				return nil
			}
			return &ProviderError{Provider: provider, Message: provider + " returned an invalid streaming event"}
		}
		if provider == "anthropic" {
			if name == "error" || data["type"] == "error" {
				return &ProviderError{Provider: provider, Message: "anthropic returned a streaming error event"}
			}
			switch data["type"] {
			case "message_start":
				message, _ := data["message"].(map[string]any)
				usage, _ := message["usage"].(map[string]any)
				inputTokens = tokenCount(usage["input_tokens"])
			case "message_delta":
				usage, _ := data["usage"].(map[string]any)
				if value, exists := usage["output_tokens"]; exists {
					outputTokens = tokenCount(value)
				}
			case "content_block_start":
				index, ok := intValue(data["index"])
				block, blockOK := data["content_block"].(map[string]any)
				if ok && blockOK {
					anthropicBlocks[index] = cloneMap(block)
					if block["type"] == "tool_use" {
						anthropicArguments[index] = nil
					}
				}
			case "content_block_delta":
				delta, _ := data["delta"].(map[string]any)
				index, indexOK := intValue(data["index"])
				if delta["type"] == "text_delta" {
					if text, ok := delta["text"].(string); ok && text != "" {
						assembled.WriteString(text)
						if indexOK && anthropicBlocks[index] != nil {
							anthropicBlocks[index]["text"] = stringValue(anthropicBlocks[index]["text"]) + text
						}
						if onDelta != nil {
							onDelta(text)
						}
					}
				} else if delta["type"] == "input_json_delta" && indexOK {
					if partial, ok := delta["partial_json"].(string); ok {
						anthropicArguments[index] = append(anthropicArguments[index], partial)
					}
				}
			}
			return nil
		}
		if provider == "openai" {
			usage, _ := data["usage"].(map[string]any)
			if value, exists := usage["prompt_tokens"]; exists {
				inputTokens = tokenCount(value)
			}
			if value, exists := usage["completion_tokens"]; exists {
				outputTokens = tokenCount(value)
			}
			choices, _ := data["choices"].([]any)
			for _, rawChoice := range choices {
				choice, _ := rawChoice.(map[string]any)
				delta, _ := choice["delta"].(map[string]any)
				if text, ok := delta["content"].(string); ok && text != "" {
					assembled.WriteString(text)
					if onDelta != nil {
						onDelta(text)
					}
				}
				fragments, _ := delta["tool_calls"].([]any)
				for _, rawFragment := range fragments {
					fragment, _ := rawFragment.(map[string]any)
					index, ok := intValue(fragment["index"])
					if !ok {
						continue
					}
					call := openAICalls[index]
					if call == nil {
						call = map[string]any{"id": nil, "type": "function", "function": map[string]any{"name": "", "arguments": ""}}
						openAICalls[index] = call
					}
					if id, ok := fragment["id"].(string); ok {
						call["id"] = id
					}
					if fragment["type"] != nil {
						call["type"] = fragment["type"]
					}
					function, _ := fragment["function"].(map[string]any)
					stored, _ := call["function"].(map[string]any)
					if name, ok := function["name"].(string); ok {
						stored["name"] = stringValue(stored["name"]) + name
					}
					if args, ok := function["arguments"].(string); ok {
						stored["arguments"] = stringValue(stored["arguments"]) + args
					}
				}
			}
			return nil
		}
		usage, _ := data["usageMetadata"].(map[string]any)
		if value, exists := usage["promptTokenCount"]; exists {
			inputTokens = tokenCount(value)
		}
		if value, exists := usage["candidatesTokenCount"]; exists {
			outputTokens = tokenCount(value)
		}
		candidates, _ := data["candidates"].([]any)
		for _, rawCandidate := range candidates {
			candidate, _ := rawCandidate.(map[string]any)
			candidateIndex, ok := intValue(valueOr(candidate["index"], json.Number("0")))
			if !ok {
				continue
			}
			content, _ := candidate["content"].(map[string]any)
			if content == nil {
				continue
			}
			if geminiTemplates[candidateIndex] == nil {
				geminiTemplates[candidateIndex] = cloneMap(content)
			}
			if geminiParts[candidateIndex] == nil {
				geminiParts[candidateIndex] = map[int]map[string]any{}
			}
			parts, _ := content["parts"].([]any)
			for partIndex, rawPart := range parts {
				part, _ := rawPart.(map[string]any)
				if part == nil {
					continue
				}
				existing := geminiParts[candidateIndex][partIndex]
				if existing == nil {
					existing = map[string]any{}
					geminiParts[candidateIndex][partIndex] = existing
				}
				if text, ok := part["text"].(string); ok && text != "" {
					existing["text"] = stringValue(existing["text"]) + text
					assembled.WriteString(text)
					if onDelta != nil {
						onDelta(text)
					}
				}
				if functionCall, ok := part["functionCall"].(map[string]any); ok {
					previous, _ := existing["functionCall"].(map[string]any)
					existing["functionCall"] = mergeFragment(previous, functionCall)
				}
				for key, value := range part {
					if key != "text" && key != "functionCall" {
						existing[key] = value
					}
				}
			}
		}
		return nil
	}

	lineEnding := false
	streamDone := false
	for {
		if ctx.Err() != nil {
			return Completion{}, ErrStreamCancelled
		}
		line, err := reader.ReadString('\n')
		if err != nil && !errors.Is(err, io.EOF) {
			if ctx.Err() != nil {
				return Completion{}, ErrStreamCancelled
			}
			return Completion{}, &ProviderError{Provider: provider, Message: "failed while reading provider stream"}
		}
		if len(line) > 0 {
			lineEnding = strings.HasSuffix(line, "\n")
			line = strings.TrimSuffix(line, "\n")
			line = strings.TrimSuffix(line, "\r")
			if line == "" {
				if len(dataLines) > 0 {
					if consumeErr := consume(eventName, strings.Join(dataLines, "\n")); errors.Is(consumeErr, errStreamDone) {
						streamDone = true
						break
					} else if consumeErr != nil {
						return Completion{}, consumeErr
					}
				}
				eventName, dataLines = "", nil
			} else if strings.HasPrefix(line, ":") {
				// SSE comment/heartbeat.
			} else {
				field, value, found := strings.Cut(line, ":")
				if found {
					value = strings.TrimPrefix(value, " ")
				}
				switch field {
				case "event":
					eventName = value
				case "data":
					dataLines = append(dataLines, value)
				}
			}
		}
		if errors.Is(err, io.EOF) {
			if len(dataLines) > 0 {
				if consumeErr := consume(eventName, strings.Join(dataLines, "\n")); errors.Is(consumeErr, errStreamDone) {
					streamDone = true
				} else if consumeErr != nil {
					return Completion{}, consumeErr
				}
			}
			break
		}
		if !lineEnding && len(line) == 0 {
			continue
		}
	}
	if provider == "openai" && !streamDone {
		return Completion{}, &ProviderError{Provider: provider, Message: "openai stream ended before [DONE]"}
	}

	completion := Completion{Text: assembled.String(), InputTokens: inputTokens, OutputTokens: outputTokens}
	switch provider {
	case "anthropic":
		indices := sortedIntKeys(anthropicBlocks)
		blocks := make([]any, 0, len(indices))
		for _, index := range indices {
			block := cloneMap(anthropicBlocks[index])
			if block["type"] == "tool_use" {
				rawArguments := strings.Join(anthropicArguments[index], "")
				var arguments any = block["input"]
				if rawArguments != "" {
					parsed, err := decodeObject([]byte(rawArguments))
					if err != nil {
						arguments = rawArguments
					} else {
						arguments = parsed
					}
					if _, ok := arguments.(map[string]any); !ok {
						arguments = map[string]any{}
					}
				}
				block["input"] = arguments
				completion.ToolCalls = append(completion.ToolCalls, ToolCall{ID: stringValue(block["id"]), Name: stringValue(block["name"]), Arguments: arguments})
			}
			blocks = append(blocks, block)
		}
		completion.ProviderMessage = map[string]any{"role": "assistant", "content": blocks}
	case "openai":
		indices := sortedIntKeys(openAICalls)
		nativeCalls := make([]any, 0, len(indices))
		for _, index := range indices {
			call := openAICalls[index]
			function, _ := call["function"].(map[string]any)
			completion.ToolCalls = append(completion.ToolCalls, ToolCall{ID: stringValue(call["id"]), Name: stringValue(function["name"]), Arguments: function["arguments"]})
			nativeCalls = append(nativeCalls, call)
		}
		var content any = completion.Text
		if len(nativeCalls) > 0 && completion.Text == "" {
			content = nil
		}
		native := map[string]any{"role": "assistant", "content": content}
		if len(nativeCalls) > 0 {
			native["tool_calls"] = nativeCalls
		}
		completion.ProviderMessage = native
	case "gemini":
		if len(geminiParts) > 0 {
			indices := sortedIntKeys(geminiParts)
			selected := indices[0]
			providerMessage := cloneMap(geminiTemplates[selected])
			if providerMessage["role"] == nil || providerMessage["role"] == "" {
				providerMessage["role"] = "model"
			}
			partIndices := sortedIntKeys(geminiParts[selected])
			parts := make([]any, 0, len(partIndices))
			for _, partIndex := range partIndices {
				part := geminiParts[selected][partIndex]
				parts = append(parts, part)
				if functionCall, ok := part["functionCall"].(map[string]any); ok {
					completion.ToolCalls = append(completion.ToolCalls, ToolCall{ID: stringValue(functionCall["id"]), Name: stringValue(functionCall["name"]), Arguments: functionCall["args"]})
				}
			}
			providerMessage["parts"] = parts
			completion.ProviderMessage = providerMessage
		}
	}
	if provider == "anthropic" || provider == "openai" {
		for _, call := range completion.ToolCalls {
			if strings.TrimSpace(call.ID) == "" {
				return Completion{}, &ProviderError{Provider: provider, Message: provider + " returned a tool call without a required call ID"}
			}
		}
	}
	return completion, nil
}

var errStreamDone = errors.New("stream done")

func intValue(value any) (int, bool) {
	switch v := value.(type) {
	case int:
		return v, true
	case int64:
		return int(v), int64(int(v)) == v
	case json.Number:
		n, err := v.Int64()
		return int(n), err == nil && int64(int(n)) == n
	case float64:
		return int(v), math.Trunc(v) == v && v <= math.MaxInt && v >= math.MinInt
	default:
		return 0, false
	}
}

func sortedIntKeys[T any](values map[int]T) []int {
	keys := make([]int, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Ints(keys)
	return keys
}

func mergeFragment(previous, fragment map[string]any) map[string]any {
	merged := cloneMap(previous)
	if merged == nil {
		merged = map[string]any{}
	}
	for key, value := range fragment {
		if next, ok := value.(map[string]any); ok {
			old, _ := merged[key].(map[string]any)
			merged[key] = mergeFragment(old, next)
		} else if next, ok := value.(string); ok {
			if old, ok := merged[key].(string); ok {
				merged[key] = old + next
			} else {
				merged[key] = next
			}
		} else {
			merged[key] = value
		}
	}
	return merged
}
