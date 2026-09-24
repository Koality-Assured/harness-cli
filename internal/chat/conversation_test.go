package chat

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestRunTurnPersistsNativeToolHistoryAndUsage(t *testing.T) {
	store := openTestStore(t)
	session, err := store.CreateSession(Session{Provider: "openai", Model: "gpt-test"})
	if err != nil {
		t.Fatal(err)
	}
	var requests []map[string]any
	answers := []string{
		`{"choices":[{"message":{"role":"assistant","content":"","tool_calls":[{"id":"call-1","type":"function","function":{"name":"echo","arguments":"{\"value\":\"hi\"}"}}]}}],"usage":{"prompt_tokens":5,"completion_tokens":2}}`,
		`{"choices":[{"message":{"role":"assistant","content":"done"}}],"usage":{"prompt_tokens":7,"completion_tokens":3}}`,
	}
	client := &ProviderClient{HTTP: doerFunc(func(request *http.Request) (*http.Response, error) {
		body, err := io.ReadAll(request.Body)
		if err != nil {
			return nil, err
		}
		var payload map[string]any
		if err := json.Unmarshal(body, &payload); err != nil {
			return nil, err
		}
		requests = append(requests, payload)
		answer := answers[0]
		answers = answers[1:]
		return fakeResponse(http.StatusOK, answer), nil
	})}
	registry := NewToolRegistry()
	if err := registry.Register("echo", "echo", map[string]any{
		"type": "object", "properties": map[string]any{"value": map[string]any{"type": "string"}},
		"required": []any{"value"}, "additionalProperties": false,
	}, func(arguments map[string]any) (any, error) { return map[string]any{"echo": arguments["value"]}, nil }); err != nil {
		t.Fatal(err)
	}
	reply, err := RunTurn(context.Background(), store, session.ID, "hello", "openai", "gpt-test", "fake-key", client, TurnOptions{ToolRegistry: registry})
	if err != nil {
		t.Fatal(err)
	}
	if reply != "done" || len(requests) != 2 {
		t.Fatalf("reply/calls = %q/%d", reply, len(requests))
	}
	secondMessages := requests[1]["messages"].([]any)
	var sawNativeCall, sawToolResult bool
	for _, item := range secondMessages {
		message := item.(map[string]any)
		if calls, ok := message["tool_calls"].([]any); ok && len(calls) == 1 {
			sawNativeCall = true
		}
		if message["role"] == "tool" && message["tool_call_id"] == "call-1" {
			sawToolResult = true
		}
	}
	if !sawNativeCall || !sawToolResult {
		t.Fatalf("second provider history lost native call/result: %#v", secondMessages)
	}
	messages, err := store.ListMessages(session.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 4 || messages[1].ProviderData == nil || messages[2].ProviderData == nil {
		t.Fatalf("persisted tool cycle = %#v", messages)
	}
	status, err := GetSessionStatus(store, session.ID)
	if err != nil {
		t.Fatal(err)
	}
	if status.InputTokens != int64(12) || status.OutputTokens != int64(5) {
		t.Fatalf("usage totals = %#v", status)
	}
}

func TestRunTurnDoesNotPersistTruncatedOpenAIStream(t *testing.T) {
	store := openTestStore(t)
	session, err := store.CreateSession(Session{Provider: "openai", Model: "gpt-test"})
	if err != nil {
		t.Fatal(err)
	}
	client := &ProviderClient{HTTP: &requestRecorder{responses: []string{sseData(map[string]any{"choices": []any{map[string]any{"delta": map[string]any{"content": "partial answer"}}}})}}}
	var deltas []string
	_, err = RunTurn(context.Background(), store, session.ID, "hello", "openai", "gpt-test", "synthetic-key", client, TurnOptions{
		Stream:  true,
		OnDelta: func(value string) { deltas = append(deltas, value) },
	})
	if err == nil || !strings.Contains(err.Error(), "ended before [DONE]") {
		t.Fatalf("truncated OpenAI turn error = %v", err)
	}
	if strings.Join(deltas, "") != "partial answer" {
		t.Fatalf("partial deltas = %#v", deltas)
	}
	messages, err := store.ListMessages(session.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 1 || messages[0].Role != "user" || messages[0].Content != "hello" {
		t.Fatalf("truncated stream persisted an assistant response: %#v", messages)
	}
}

func TestCompactSessionPreservesBoundariesAndCarriesUsage(t *testing.T) {
	store := openTestStore(t)
	input, output := int64(2), int64(1)
	session, err := store.CreateSession(Session{Title: "source", CWD: "C:/project", Provider: "anthropic", Model: "old-model", BusyMode: "steer"})
	if err != nil {
		t.Fatal(err)
	}
	native := map[string]any{"provider": "anthropic", "message": map[string]any{"role": "assistant", "content": []any{map[string]any{"type": "tool_use", "id": "call-1"}}}}
	checkpointData := map[string]any{"harness_context_checkpoint": map[string]any{"carried_input_tokens": 4, "carried_output_tokens": 3, "carried_usage_known": true}}
	for _, message := range []Message{
		{Role: "user", Content: "first"},
		{Role: "assistant", Content: "head", InputTokens: &input, OutputTokens: &output, ProviderData: native},
		{Role: "tool", Content: "head tool", ProviderData: map[string]any{"provider": "anthropic", "tool_result": map[string]any{"id": "call-1"}}},
		{Role: "assistant", Content: "head final"},
		{Role: "user", Content: "middle"},
		{Role: "assistant", Content: "middle answer", ProviderData: checkpointData},
		{Role: "user", Content: "latest"},
		{Role: "assistant", Content: "tail", InputTokens: &input, OutputTokens: &output},
		{Role: "user", Content: "unfinished"},
	} {
		if _, err := store.AppendMessage(session.ID, message); err != nil {
			t.Fatal(err)
		}
	}
	checkpoint := `{"summary":"keep this","open_items":["follow up"]}`
	client := &ProviderClient{HTTP: doerFunc(func(request *http.Request) (*http.Response, error) {
		body, err := io.ReadAll(request.Body)
		if err != nil {
			return nil, err
		}
		var payload map[string]any
		if err := json.Unmarshal(body, &payload); err != nil {
			return nil, err
		}
		if payload["tools"] != nil {
			t.Errorf("compaction enabled tools: %#v", payload["tools"])
		}
		return fakeResponse(http.StatusOK, `{"content":[{"type":"text","text":`+quoteJSONString(checkpoint)+`}],"usage":{"input_tokens":11,"output_tokens":4}}`), nil
	})}
	result, err := CompactSession(context.Background(), store, session.ID, "claude", "claude-new", "fake-key", client)
	if err != nil {
		t.Fatal(err)
	}
	if result == nil || result.CompactedTurns != 1 || result.OmittedMessages != 2 {
		t.Fatalf("compaction result = %#v", result)
	}
	if result.Session.ID == session.ID || result.Session.BusyMode != "steer" || result.Session.Title != "source" || result.Session.Model != "claude-new" {
		t.Fatalf("new session metadata = %#v", result.Session)
	}
	source, err := store.ListMessages(session.ID)
	if err != nil || len(source) != 9 {
		t.Fatalf("source modified: %d messages, %v", len(source), err)
	}
	compacted, err := store.ListMessages(result.Session.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(compacted) != 8 || compacted[1].Content != "head" || compacted[1].ProviderData == nil || compacted[4].Content != `{"open_items":["follow up"],"summary":"keep this"}` || compacted[6].Content != "tail" || compacted[7].Content != "unfinished" {
		t.Fatalf("compacted history = %#v", compacted)
	}
	status, err := GetSessionStatus(store, result.Session.ID)
	if err != nil {
		t.Fatal(err)
	}
	if status.InputTokens != int64(19) || status.OutputTokens != int64(9) {
		t.Fatalf("carried usage totals = %#v", status)
	}
}

func quoteJSONString(value string) string {
	encoded, _ := json.Marshal(value)
	return string(encoded)
}

func TestValidatedCheckpointRejectsDuplicateOrExtraFields(t *testing.T) {
	for _, text := range []string{
		`{"summary":"a","summary":"b","open_items":[]}`,
		`{"summary":"a","open_items":[],"other":true}`,
		`{"summary":" ","open_items":[]}`,
		`{"summary":"a","open_items":[1]}`,
	} {
		if _, err := validatedCheckpoint(text); err == nil {
			t.Errorf("accepted invalid checkpoint %s", text)
		}
	}
}

func TestToolArgumentJSONRejectsTrailingData(t *testing.T) {
	if _, message := toolArguments(`{"value":1} {}`); message == "" {
		t.Fatal("accepted trailing tool argument JSON")
	}
}

func TestProviderHTTPErrorWithEmptyCredentialPreservesMessage(t *testing.T) {
	err := providerHTTPError("openai", 401, []byte("auth failed"), "")
	if !strings.Contains(err.Error(), "auth failed") {
		t.Fatalf("error = %v", err)
	}
}
