package chat

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type doerFunc func(*http.Request) (*http.Response, error)

func (f doerFunc) Do(request *http.Request) (*http.Response, error) { return f(request) }

func fakeResponse(status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}
}

func TestProviderCompleteShapesAndUsage(t *testing.T) {
	cases := []struct {
		provider, model, endpoint, body string
		check                           func(*testing.T, *http.Request, Completion)
	}{
		{
			provider: "anthropic", model: "claude-test", endpoint: "https://api.anthropic.com/v1/messages",
			body: `{"content":[{"type":"text","text":"hello"},{"type":"tool_use","id":"ant-1","name":"double","input":{"value":4}}],"usage":{"input_tokens":7,"output_tokens":3}}`,
			check: func(t *testing.T, request *http.Request, got Completion) {
				if request.Header.Get("x-api-key") != "fake-secret" || request.Header.Get("anthropic-version") != "2023-06-01" {
					t.Fatalf("Anthropic headers = %#v", request.Header)
				}
				payload := readRequestJSON(t, request)
				if payload["max_tokens"] != json.Number("4096") || payload["stream"] != nil {
					t.Fatalf("Anthropic payload = %#v", payload)
				}
				if got.Text != "hello" || len(got.ToolCalls) != 1 || got.ToolCalls[0].ID != "ant-1" || *got.InputTokens != 7 || *got.OutputTokens != 3 {
					t.Fatalf("Anthropic completion = %#v", got)
				}
			},
		},
		{
			provider: "openai", model: "gpt-test", endpoint: "https://api.openai.com/v1/chat/completions",
			body: `{"choices":[{"message":{"role":"assistant","content":"hello","tool_calls":[{"id":"oa-1","type":"function","function":{"name":"double","arguments":"{\"value\":4}"}}]}}],"usage":{"prompt_tokens":8,"completion_tokens":2}}`,
			check: func(t *testing.T, request *http.Request, got Completion) {
				if request.Header.Get("authorization") != "Bearer fake-secret" {
					t.Fatalf("OpenAI auth header = %q", request.Header.Get("authorization"))
				}
				payload := readRequestJSON(t, request)
				if payload["stream"] != nil || got.Text != "hello" || len(got.ToolCalls) != 1 || got.ToolCalls[0].Name != "double" || *got.InputTokens != 8 || *got.OutputTokens != 2 {
					t.Fatalf("OpenAI payload/completion = %#v / %#v", payload, got)
				}
			},
		},
		{
			provider: "gemini", model: "gemini-test", endpoint: "https://generativelanguage.googleapis.com/v1beta/models/gemini-test:generateContent",
			body: `{"candidates":[{"content":{"parts":[{"text":"hello"},{"functionCall":{"id":"gm-1","name":"double","args":{"value":4}}}]}}],"usageMetadata":{"promptTokenCount":9,"candidatesTokenCount":4}}`,
			check: func(t *testing.T, request *http.Request, got Completion) {
				if request.Header.Get("x-goog-api-key") != "fake-secret" {
					t.Fatalf("Gemini API key header = %q", request.Header.Get("x-goog-api-key"))
				}
				payload := readRequestJSON(t, request)
				if payload["contents"] == nil || payload["stream"] != nil || got.Text != "hello" || len(got.ToolCalls) != 1 || got.ToolCalls[0].ID != "gm-1" || *got.InputTokens != 9 || *got.OutputTokens != 4 {
					t.Fatalf("Gemini payload/completion = %#v / %#v", payload, got)
				}
			},
		},
	}
	for _, test := range cases {
		t.Run(test.provider, func(t *testing.T) {
			var captured *http.Request
			client := &ProviderClient{HTTP: doerFunc(func(request *http.Request) (*http.Response, error) {
				if request.URL.String() != test.endpoint {
					t.Errorf("endpoint = %q; want %q", request.URL, test.endpoint)
				}
				body, err := io.ReadAll(request.Body)
				if err != nil {
					return nil, err
				}
				_ = request.Body.Close()
				request.Body = io.NopCloser(strings.NewReader(string(body)))
				captured = request
				return fakeResponse(http.StatusOK, test.body), nil
			})}
			completion, err := client.Complete(context.Background(), test.provider, test.model, []Message{{Role: "user", Content: "hello"}}, "fake-secret", nil)
			if err != nil {
				t.Fatal(err)
			}
			test.check(t, captured, completion)
		})
	}
}

func readRequestJSON(t *testing.T, request *http.Request) map[string]any {
	t.Helper()
	body, err := io.ReadAll(request.Body)
	if err != nil {
		t.Fatal(err)
	}
	decoder := json.NewDecoder(strings.NewReader(string(body)))
	decoder.UseNumber()
	var payload map[string]any
	if err := decoder.Decode(&payload); err != nil {
		t.Fatal(err)
	}
	return payload
}

func TestProviderStreamingAssemblesTextToolsAndUsage(t *testing.T) {
	cases := []struct {
		provider, model, endpoint, stream string
	}{
		{
			provider: "anthropic", model: "claude-test", endpoint: "https://api.anthropic.com/v1/messages",
			stream: "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":2}}}\n\nevent: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"tool_use\",\"id\":\"ant-1\",\"name\":\"receive\",\"input\":{}}}\n\nevent: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"input_json_delta\",\"partial_json\":\"{\\\"value\\\":\"}}}\n\nevent: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"input_json_delta\",\"partial_json\":\"\\\"streamed\\\"}\"}}\n\nevent: message_delta\ndata: {\"type\":\"message_delta\",\"usage\":{\"output_tokens\":1}}\n\n",
		},
		{
			provider: "openai", model: "gpt-test", endpoint: "https://api.openai.com/v1/chat/completions",
			stream: "data: {\"choices\":[{\"delta\":{\"content\":\"hel\",\"tool_calls\":[{\"index\":0,\"id\":\"oa-1\",\"type\":\"function\",\"function\":{\"name\":\"receive\",\"arguments\":\"{\\\"value\\\":\"}}]}}]}\n\n" +
				"data: {\"choices\":[{\"delta\":{\"content\":\"lo\",\"tool_calls\":[{\"index\":0,\"function\":{\"arguments\":\"\\\"streamed\\\"}\"}}]}}]}\n\n" +
				"data: {\"choices\":[],\"usage\":{\"prompt_tokens\":3,\"completion_tokens\":2}}\n\n" + "data: [DONE]\n\n",
		},
		{
			provider: "gemini", model: "gemini-test", endpoint: "https://generativelanguage.googleapis.com/v1beta/models/gemini-test:streamGenerateContent?alt=sse",
			stream: "data: {\"candidates\":[{\"index\":0,\"content\":{\"role\":\"model\",\"parts\":[{\"functionCall\":{\"id\":\"gm-1\",\"name\":\"receive\",\"args\":{\"prefix\":\"kept\"}},\"thoughtSignature\":\"sig\"}]}}],\"usageMetadata\":{\"promptTokenCount\":4}}\n\ndata: {\"candidates\":[{\"index\":0,\"content\":{\"role\":\"model\",\"parts\":[{\"functionCall\":{\"id\":\"gm-1\",\"name\":\"receive\",\"args\":{\"value\":\"streamed\"}}}]}}],\"usageMetadata\":{\"candidatesTokenCount\":2}}\n\n",
		},
	}
	for _, test := range cases {
		t.Run(test.provider, func(t *testing.T) {
			recorder := &requestRecorder{responses: []string{test.stream}}
			client := &ProviderClient{HTTP: recorder}
			var deltas []string
			completion, err := client.Stream(context.Background(), test.provider, test.model, []Message{{Role: "user", Content: "test"}}, "fake-secret", nil, func(value string) { deltas = append(deltas, value) })
			if err != nil {
				t.Fatal(err)
			}
			if len(recorder.requests) != 1 || recorder.requests[0].URL.String() != test.endpoint {
				t.Fatalf("stream endpoint = %#v", recorder.requests)
			}
			if test.provider == "openai" {
				if completion.Text != "hello" || len(completion.ToolCalls) != 1 || completion.ToolCalls[0].Name != "receive" {
					t.Fatalf("OpenAI stream completion = %#v", completion)
				}
			} else if test.provider == "gemini" {
				if len(completion.ToolCalls) != 1 || completion.ToolCalls[0].Arguments.(map[string]any)["prefix"] != "kept" || completion.ToolCalls[0].Arguments.(map[string]any)["value"] != "streamed" {
					t.Fatalf("Gemini streamed call merge = %#v", completion.ToolCalls)
				}
				parts := completion.ProviderMessage["parts"].([]any)
				if parts[0].(map[string]any)["thoughtSignature"] != "sig" {
					t.Fatalf("Gemini native thought signature lost: %#v", parts)
				}
			} else if len(completion.ToolCalls) != 1 || completion.ToolCalls[0].Arguments.(map[string]any)["value"] != "streamed" {
				t.Fatalf("Anthropic stream completion = %#v", completion)
			}
			if *completion.InputTokens == 0 || *completion.OutputTokens == 0 {
				t.Fatalf("stream usage missing: %#v", completion)
			}
			if test.provider == "openai" && strings.Join(deltas, "") != "hello" {
				t.Fatalf("OpenAI deltas = %#v", deltas)
			}
		})
	}
}

func TestAnthropicBaseURLLoopbackStreamingRequest(t *testing.T) {
	var requestPath string
	var requestHeaders http.Header
	var requestBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		requestPath = request.URL.Path
		requestHeaders = request.Header.Clone()
		body, err := io.ReadAll(request.Body)
		if err != nil {
			t.Errorf("read request: %v", err)
			writer.WriteHeader(http.StatusBadRequest)
			return
		}
		if err := json.Unmarshal(body, &requestBody); err != nil {
			t.Errorf("decode request: %v", err)
			writer.WriteHeader(http.StatusBadRequest)
			return
		}
		writer.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(writer, "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":2}}}\n\n")
		_, _ = io.WriteString(writer, "event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"loopback answer\"}}\n\n")
		_, _ = io.WriteString(writer, "event: message_delta\ndata: {\"type\":\"message_delta\",\"usage\":{\"output_tokens\":3}}\n\n")
		_, _ = io.WriteString(writer, "event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
	}))
	defer server.Close()
	t.Setenv(AnthropicBaseURLEnv, server.URL+"/")

	var deltas []string
	completion, err := (&ProviderClient{}).Stream(context.Background(), "anthropic", "claude-test", []Message{{Role: "user", Content: "same prompt"}}, "synthetic-key", nil, func(value string) {
		deltas = append(deltas, value)
	})
	if err != nil {
		t.Fatal(err)
	}
	if requestPath != anthropicMessagesPath {
		t.Fatalf("request path = %q", requestPath)
	}
	if requestHeaders.Get("X-Api-Key") != "synthetic-key" || requestHeaders.Get("Anthropic-Version") != "2023-06-01" {
		t.Fatalf("request headers = %#v", requestHeaders)
	}
	if requestBody["model"] != "claude-test" || requestBody["max_tokens"] != float64(4096) || requestBody["stream"] != true {
		t.Fatalf("request body = %#v", requestBody)
	}
	messages, _ := requestBody["messages"].([]any)
	if len(messages) != 1 || messages[0].(map[string]any)["content"] != "same prompt" {
		t.Fatalf("request messages = %#v", messages)
	}
	if completion.Text != "loopback answer" || strings.Join(deltas, "") != completion.Text || completion.InputTokens == nil || *completion.InputTokens != 2 || completion.OutputTokens == nil || *completion.OutputTokens != 3 {
		t.Fatalf("loopback completion/deltas = %#v / %#v", completion, deltas)
	}
}

func TestAnthropicBaseURLRejectsEmptyQuery(t *testing.T) {
	t.Setenv(AnthropicBaseURLEnv, "http://127.0.0.1:43121?")
	_, _, _, err := buildRequest("anthropic", "claude-test", []Message{{Role: "user", Content: "hello"}}, "synthetic-key", nil, true)
	if err == nil || !strings.Contains(err.Error(), "invalid "+AnthropicBaseURLEnv) {
		t.Fatalf("empty-query base URL error = %v", err)
	}
}

func TestOpenAIStreamRejectsEOFWithoutDone(t *testing.T) {
	recorder := &requestRecorder{responses: []string{sseData(map[string]any{"choices": []any{map[string]any{"delta": map[string]any{"content": "partial answer"}}}})}}
	var deltas []string
	completion, err := (&ProviderClient{HTTP: recorder}).Stream(context.Background(), "openai", "gpt-test", []Message{{Role: "user", Content: "hello"}}, "synthetic-key", nil, func(value string) {
		deltas = append(deltas, value)
	})
	if err == nil || !strings.Contains(err.Error(), "ended before [DONE]") {
		t.Fatalf("truncated OpenAI stream error = %v, completion = %#v", err, completion)
	}
	if strings.Join(deltas, "") != "partial answer" {
		t.Fatalf("received partial deltas = %#v", deltas)
	}
}

func sseData(value any) string {
	encoded, _ := json.Marshal(value)
	return "data: " + string(encoded) + "\n\n"
}

type requestRecorder struct {
	requests  []*http.Request
	bodies    [][]byte
	responses []string
	status    int
}

func (r *requestRecorder) Do(request *http.Request) (*http.Response, error) {
	body, err := io.ReadAll(request.Body)
	if err != nil {
		return nil, err
	}
	_ = request.Body.Close()
	r.requests = append(r.requests, request)
	r.bodies = append(r.bodies, body)
	status := r.status
	if status == 0 {
		status = http.StatusOK
	}
	response := "{}"
	if len(r.responses) > 0 {
		response, r.responses = r.responses[0], r.responses[1:]
	}
	return fakeResponse(status, response), nil
}

func TestProviderErrorsRedactCredentialsAndRequireModels(t *testing.T) {
	client := &ProviderClient{HTTP: doerFunc(func(request *http.Request) (*http.Response, error) {
		return fakeResponse(http.StatusForbidden, "credential fake-secret was rejected"), nil
	})}
	_, err := client.Complete(context.Background(), "openai", "gpt-test", nil, "fake-secret", nil)
	if err == nil || strings.Contains(err.Error(), "fake-secret") {
		t.Fatalf("provider error did not redact credential: %v", err)
	}
	if _, err := (&ProviderClient{}).Complete(context.Background(), "gemini", "", nil, "", nil); err == nil {
		t.Fatal("Gemini completion accepted an empty model")
	}
	if _, err := (&ProviderClient{}).Complete(context.Background(), "cursor", "", nil, "", nil); err == nil || !strings.Contains(err.Error(), "not wired") {
		t.Fatalf("Cursor error = %v", err)
	}
}
