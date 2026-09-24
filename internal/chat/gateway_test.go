package chat

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type gatewayFakeProvider struct {
	answers  []string
	requests []*http.Request
	bodies   [][]byte
	delay    time.Duration
}

func (f *gatewayFakeProvider) Do(request *http.Request) (*http.Response, error) {
	body, err := io.ReadAll(request.Body)
	if err != nil {
		return nil, err
	}
	_ = request.Body.Close()
	f.requests = append(f.requests, request)
	f.bodies = append(f.bodies, body)
	if f.delay > 0 {
		time.Sleep(f.delay)
	}
	answer := f.answers[0]
	f.answers = f.answers[1:]
	return fakeResponse(http.StatusOK, answer), nil
}

func anthroTextStream(text string, input, output int) string {
	return "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":" + intString(input) + "}}}\n\n" +
		"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":" + quoteJSONString(text) + "}}\n\n" +
		"event: message_delta\ndata: {\"type\":\"message_delta\",\"usage\":{\"output_tokens\":" + intString(output) + "}}\n\n"
}

func TestGatewayStreamResumeAndUsage(t *testing.T) {
	temp := t.TempDir()
	fake := &gatewayFakeProvider{answers: []string{anthroTextStream("hello", 2, 1), anthroTextStream("resumed", 6, 3)}}
	runtime, err := NewAdapterRuntime(AdapterRuntimeConfig{
		Provider: "anthropic", Model: "claude-test", DBPath: temp + "/state.db",
		Client:             &ProviderClient{HTTP: fake},
		CredentialResolver: func(string) (string, error) { return "fake-provider-token", nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, GatewayRoute, strings.NewReader(`{"model":"claude-test","stream":true,"messages":[{"role":"system","content":"Follow the task."},{"role":"user","content":"first"}]}`))
	request.Header.Set("Authorization", "Bearer gateway-token")
	response := httptest.NewRecorder()
	NewGatewayHandler(runtime, "gateway-token").ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status %d: %s", response.Code, response.Body.String())
	}
	handle := response.Header().Get("X-Hermes-Session-Id")
	if handle == "" || !strings.Contains(response.Body.String(), `"content":"hello"`) || !strings.Contains(response.Body.String(), "data: [DONE]") {
		t.Fatalf("stream handle/body = %q / %s", handle, response.Body.String())
	}
	resume := httptest.NewRequest(http.MethodPost, GatewayRoute, strings.NewReader(`{"model":"claude-test","messages":[{"role":"user","content":"continue"}]}`))
	resume.Header.Set("Authorization", "Bearer gateway-token")
	resume.Header.Set("X-Hermes-Session-Id", handle)
	resumedResponse := httptest.NewRecorder()
	NewGatewayHandler(runtime, "gateway-token").ServeHTTP(resumedResponse, resume)
	if resumedResponse.Code != http.StatusOK || resumedResponse.Header().Get("X-Hermes-Session-Id") != handle {
		t.Fatalf("resume status/handle = %d/%q: %s", resumedResponse.Code, resumedResponse.Header().Get("X-Hermes-Session-Id"), resumedResponse.Body.String())
	}
	if !strings.Contains(resumedResponse.Body.String(), `"content":"resumed"`) || !strings.Contains(resumedResponse.Body.String(), `"prompt_tokens":6`) {
		t.Fatalf("resume result = %s", resumedResponse.Body.String())
	}
	if len(fake.bodies) != 2 || !strings.Contains(string(fake.bodies[0]), `"system":"Follow the task."`) || !strings.Contains(string(fake.bodies[1]), `"content":"hello"`) {
		t.Fatalf("provider requests did not resume history: %s / %s", fake.bodies[0], fake.bodies[1])
	}
}

func TestGatewayAcceptsChunkedRequestsWithinBound(t *testing.T) {
	fake := &gatewayFakeProvider{answers: []string{anthroTextStream("accepted chunked request", 2, 1)}}
	runtime, err := NewAdapterRuntime(AdapterRuntimeConfig{
		Provider: "anthropic", Model: "claude-test", DBPath: t.TempDir() + "/state.db",
		Client:             &ProviderClient{HTTP: fake},
		CredentialResolver: func(string) (string, error) { return "fake-provider-token", nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(NewGatewayHandler(runtime, "gateway-token"))
	defer server.Close()

	body := `{"model":"claude-test","messages":[{"role":"user","content":"chunked"}]}`
	request, err := http.NewRequest(http.MethodPost, server.URL+GatewayRoute, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	request.ContentLength = -1 // net/http must frame the request as chunked.
	request.Header.Set("Authorization", "Bearer gateway-token")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	responseBody, readErr := io.ReadAll(response.Body)
	_ = response.Body.Close()
	if readErr != nil {
		t.Fatal(readErr)
	}
	if response.StatusCode != http.StatusOK || !strings.Contains(string(responseBody), "accepted chunked request") {
		t.Fatalf("chunked response = %d %s", response.StatusCode, responseBody)
	}
	if len(fake.bodies) != 1 {
		t.Fatalf("fake provider calls for chunked request = %d", len(fake.bodies))
	}

	tooLarge := `{"model":"claude-test","messages":[{"role":"user","content":"` + strings.Repeat("x", MaxGatewayBody) + `"}]}`
	request, err = http.NewRequest(http.MethodPost, server.URL+GatewayRoute, strings.NewReader(tooLarge))
	if err != nil {
		t.Fatal(err)
	}
	request.ContentLength = -1
	request.Header.Set("Authorization", "Bearer gateway-token")
	response, err = http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	responseBody, readErr = io.ReadAll(response.Body)
	_ = response.Body.Close()
	if readErr != nil {
		t.Fatal(readErr)
	}
	if response.StatusCode != http.StatusRequestEntityTooLarge || len(fake.bodies) != 1 {
		t.Fatalf("oversized chunked body = status %d, provider calls %d, body %s", response.StatusCode, len(fake.bodies), responseBody)
	}
}

func TestGatewayBodyReadTimeoutAndLongSSEResponse(t *testing.T) {
	t.Run("authenticated slow body times out before provider", func(t *testing.T) {
		fake := &gatewayFakeProvider{answers: []string{anthroTextStream("unexpected", 1, 1)}}
		runtime, err := NewAdapterRuntime(AdapterRuntimeConfig{
			Provider: "anthropic", Model: "claude-test", DBPath: t.TempDir() + "/state.db",
			Client:             &ProviderClient{HTTP: fake},
			CredentialResolver: func(string) (string, error) { return "fake-provider-token", nil },
		})
		if err != nil {
			t.Fatal(err)
		}
		server := httptest.NewUnstartedServer(newGatewayHandler(runtime, "gateway-token", 100*time.Millisecond))
		server.Start()
		defer server.Close()

		conn, err := net.DialTimeout("tcp", server.Listener.Addr().String(), time.Second)
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close()
		body := `{"model":"claude-test","messages":[{"role":"user","content":"slow upload"}]}`
		if _, err := fmt.Fprintf(conn,
			"POST %s HTTP/1.1\r\nHost: %s\r\nAuthorization: Bearer gateway-token\r\nContent-Type: application/json\r\nContent-Length: %d\r\nConnection: close\r\n\r\n",
			GatewayRoute, server.Listener.Addr().String(), len(body)); err != nil {
			t.Fatal(err)
		}
		if _, err := io.WriteString(conn, body[:8]); err != nil {
			t.Fatal(err)
		}
		start := time.Now()
		response, err := http.ReadResponse(bufio.NewReader(conn), &http.Request{Method: http.MethodPost})
		if err != nil {
			t.Fatalf("timed-out request response: %v", err)
		}
		responseBody, readErr := io.ReadAll(response.Body)
		_ = response.Body.Close()
		if readErr != nil {
			t.Fatal(readErr)
		}
		if response.StatusCode != http.StatusBadRequest || !strings.Contains(string(responseBody), "invalid request body") {
			t.Fatalf("slow body response = %d %s", response.StatusCode, responseBody)
		}
		if elapsed := time.Since(start); elapsed > 2*time.Second {
			t.Fatalf("slow body timeout took %s", elapsed)
		}
		if len(fake.requests) != 0 {
			t.Fatalf("slow body reached provider %d times", len(fake.requests))
		}
	})

	t.Run("SSE may outlast body-read timeout", func(t *testing.T) {
		const timeout = 100 * time.Millisecond
		fake := &gatewayFakeProvider{answers: []string{anthroTextStream("SSE completed", 2, 1)}, delay: 250 * time.Millisecond}
		runtime, err := NewAdapterRuntime(AdapterRuntimeConfig{
			Provider: "anthropic", Model: "claude-test", DBPath: t.TempDir() + "/state.db",
			Client:             &ProviderClient{HTTP: fake},
			CredentialResolver: func(string) (string, error) { return "fake-provider-token", nil },
		})
		if err != nil {
			t.Fatal(err)
		}
		server := httptest.NewUnstartedServer(newGatewayHandler(runtime, "gateway-token", timeout))
		server.Start()
		defer server.Close()
		request, err := http.NewRequest(http.MethodPost, server.URL+GatewayRoute,
			strings.NewReader(`{"model":"claude-test","stream":true,"messages":[{"role":"user","content":"long response"}]}`))
		if err != nil {
			t.Fatal(err)
		}
		request.Header.Set("Authorization", "Bearer gateway-token")
		start := time.Now()
		response, err := http.DefaultClient.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		responseBody, readErr := io.ReadAll(response.Body)
		_ = response.Body.Close()
		if readErr != nil {
			t.Fatal(readErr)
		}
		if response.StatusCode != http.StatusOK || !strings.Contains(string(responseBody), "SSE completed") || !strings.Contains(string(responseBody), "data: [DONE]") {
			t.Fatalf("SSE response after timeout window = %d %s", response.StatusCode, responseBody)
		}
		if elapsed := time.Since(start); elapsed <= timeout {
			t.Fatalf("test provider did not outlast body deadline: %s", elapsed)
		}
	})
}

func TestGatewayNativeToolsAndValidation(t *testing.T) {
	fake := &gatewayFakeProvider{answers: []string{
		"event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":2}}}\n\n" +
			"event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"tool_use\",\"id\":\"tool-1\",\"name\":\"echo\",\"input\":{}}}\n\n" +
			"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"input_json_delta\",\"partial_json\":\"{\\\"value\\\":\\\"called\\\"}\"}}\n\n" +
			"event: message_delta\ndata: {\"type\":\"message_delta\",\"usage\":{\"output_tokens\":1}}\n\n",
		anthroTextStream("done", 5, 3),
	}}
	registry := NewToolRegistry()
	if err := registry.Register("echo", "echo", map[string]any{"type": "object", "properties": map[string]any{"value": map[string]any{"type": "string"}}, "required": []any{"value"}}, func(args map[string]any) (any, error) {
		return map[string]any{"echo": args["value"]}, nil
	}); err != nil {
		t.Fatal(err)
	}
	runtime, err := NewAdapterRuntime(AdapterRuntimeConfig{
		Provider: "anthropic", Model: "claude-test", DBPath: t.TempDir() + "/state.db",
		Client: &ProviderClient{HTTP: fake}, ToolRegistry: registry,
		CredentialResolver: func(string) (string, error) { return "fake-provider-token", nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, GatewayRoute, strings.NewReader(`{"model":"claude-test","stream":true,"messages":[{"role":"user","content":"please echo"}]}`))
	request.Header.Set("Authorization", "Bearer gateway-token")
	response := httptest.NewRecorder()
	NewGatewayHandler(runtime, "gateway-token").ServeHTTP(response, request)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "hermes.tool.progress") || !strings.Contains(response.Body.String(), `"content":"done"`) {
		t.Fatalf("tool response = %d %s", response.Code, response.Body.String())
	}
	principal := PrincipalForToken("gateway-token")
	history, err := runtime.History("gateway", principal, GatewayRoute, response.Header().Get("X-Hermes-Session-Id"))
	if err != nil {
		t.Fatal(err)
	}
	if len(history) != 4 || history[1].ProviderData == nil || history[2].ProviderData == nil {
		t.Fatalf("native tool history = %#v", history)
	}
	if len(fake.bodies) != 2 || !strings.Contains(string(fake.bodies[1]), `"tool_result"`) {
		t.Fatalf("provider-native result not sent on next request: %s", fake.bodies[1])
	}

	for _, body := range []string{
		`{"model":"claude-test","messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"x"}}]}]}`,
		`{"model":"claude-test","tools":[],"messages":[{"role":"user","content":"no"}]}`,
	} {
		bad := httptest.NewRequest(http.MethodPost, GatewayRoute, strings.NewReader(body))
		bad.Header.Set("Authorization", "Bearer gateway-token")
		badResponse := httptest.NewRecorder()
		NewGatewayHandler(runtime, "gateway-token").ServeHTTP(badResponse, bad)
		if badResponse.Code != http.StatusBadRequest {
			t.Errorf("accepted invalid body %s: %d", body, badResponse.Code)
		}
	}
	unauthorized := httptest.NewRequest(http.MethodPost, GatewayRoute, strings.NewReader(`{}`))
	unauthorizedResponse := httptest.NewRecorder()
	NewGatewayHandler(runtime, "gateway-token").ServeHTTP(unauthorizedResponse, unauthorized)
	if unauthorizedResponse.Code != http.StatusUnauthorized || len(fake.bodies) != 2 {
		t.Fatalf("auth failed to gate provider: %d calls=%d", unauthorizedResponse.Code, len(fake.bodies))
	}

	// Slice 15 intentionally exposes only /v1/chat/completions. There is no
	// run-creation API, and rejecting it must not reach the provider transport.
	runs := httptest.NewRequest(http.MethodPost, "/v1/runs", strings.NewReader(`{}`))
	runs.Header.Set("Authorization", "Bearer gateway-token")
	runsResponse := httptest.NewRecorder()
	NewGatewayHandler(runtime, "gateway-token").ServeHTTP(runsResponse, runs)
	if runsResponse.Code != http.StatusNotFound || len(fake.bodies) != 2 {
		t.Fatalf("excluded /v1/runs route = status %d, provider calls %d", runsResponse.Code, len(fake.bodies))
	}
}

func intString(value int) string { return fmt.Sprint(value) }
