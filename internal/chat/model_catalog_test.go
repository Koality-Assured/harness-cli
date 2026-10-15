package chat

import (
	"context"
	"encoding/json"
	"math"
	"strings"
	"testing"
)

func TestGetContextWindow(t *testing.T) {
	tests := []struct {
		model    string
		provider string
		expected int
	}{
		// Anthropic
		{"claude-sonnet-4-5", "anthropic", 200000},
		{"claude-3-5-haiku", "claude", 200000},
		{"claude-3-opus", "anthropic", 200000},
		// OpenAI
		{"gpt-4o", "openai", 128000},
		{"o1", "openai", 200000},
		{"o3", "openai", 200000},
		{"o3-mini", "gpt", 200000},
		{"o1-mini", "openai", 128000},
		{"o1-preview", "openai", 128000},
		{"gpt-4", "openai", 8192},
		{"gpt-3.5-turbo", "openai", 16385},
		// Gemini
		{"gemini-2.5-flash", "gemini", 1000000},
		{"gemini-1.5-pro", "google", 1000000},
		// Local / Ollama
		{"qwen2.5-coder:14b", "local", 32768},
		{"qwen-7b", "ollama", 32768},
		// Fallbacks
		{"unknown-model", "unknown-provider", 128000},
		{"", "", 128000},
	}

	for _, tc := range tests {
		got := GetContextWindow(tc.model, tc.provider)
		if got != tc.expected {
			t.Errorf("GetContextWindow(%q, %q) = %d; want %d", tc.model, tc.provider, got, tc.expected)
		}
	}
}

func TestCalculateTokenCost(t *testing.T) {
	tests := []struct {
		name     string
		provider string
		model    string
		in       int
		out      int
		expected float64
	}{
		{"local always zero", "local", "qwen2.5-coder:14b", 500000, 500000, 0.0},
		{"ollama always zero", "ollama", "llama3", 1000000, 1000000, 0.0},
		{"zero tokens", "anthropic", "claude-sonnet-4-5", 0, 0, 0.0},
		{"claude-3-5-sonnet standard", "anthropic", "claude-3-5-sonnet", 1000000, 1000000, 18.0},
		{"claude-3-5-sonnet partial", "anthropic", "claude-3-5-sonnet", 10000, 2000, 0.06},
		{"claude-3-5-haiku", "anthropic", "claude-3-5-haiku", 1000000, 1000000, 4.8},
		{"claude-3-opus", "anthropic", "claude-3-opus", 1000000, 1000000, 90.0},
		{"gpt-4o", "openai", "gpt-4o", 1000000, 500000, 7.5},
		{"gpt-4o-mini", "openai", "gpt-4o-mini", 1000000, 1000000, 0.75},
		{"o1", "openai", "o1", 1000000, 1000000, 75.0},
		{"o1-mini", "openai", "o1-mini", 1000000, 1000000, 5.5},
		{"o3-mini", "openai", "o3-mini", 1000000, 1000000, 5.5},
		{"gemini-1.5-flash", "gemini", "gemini-1.5-flash", 1000000, 1000000, 0.375},
		{"gemini-2.5-pro", "gemini", "gemini-2.5-pro", 1000000, 1000000, 6.25},
		{"unknown fallback rate $3/$15", "unknown-provider", "unknown-model", 1000000, 1000000, 18.0},
	}

	for _, tc := range tests {
		got := CalculateTokenCost(tc.provider, tc.model, tc.in, tc.out)
		if math.Abs(got-tc.expected) > 1e-6 {
			t.Errorf("%s: CalculateTokenCost(%q, %q, %d, %d) = %f; want %f", tc.name, tc.provider, tc.model, tc.in, tc.out, got, tc.expected)
		}
	}
}

func TestResolveThinking(t *testing.T) {
	budget, effort := ResolveThinking(4096, "", "")
	if budget != 4096 || effort != "medium" {
		t.Errorf("ResolveThinking(4096, '', '') = (%d, %q); want (4096, 'medium')", budget, effort)
	}

	budget, effort = ResolveThinking(0, "high", "")
	if budget != 16384 || effort != "high" {
		t.Errorf("ResolveThinking(0, 'high', '') = (%d, %q); want (16384, 'high')", budget, effort)
	}

	budget, effort = ResolveThinking(0, "", "low")
	if budget != 2048 || effort != "low" {
		t.Errorf("ResolveThinking(0, '', 'low') = (%d, %q); want (2048, 'low')", budget, effort)
	}

	budget, effort = ResolveThinking(0, "", "8192")
	if budget != 8192 || effort != "medium" {
		t.Errorf("ResolveThinking(0, '', '8192') = (%d, %q); want (8192, 'medium')", budget, effort)
	}

	budget, effort = ResolveThinking(0, "", "off")
	if budget != 0 || effort != "" {
		t.Errorf("ResolveThinking(0, '', 'off') = (%d, %q); want (0, '')", budget, effort)
	}

	budget, effort = ResolveThinking(0, "off", "")
	if budget != 0 || effort != "" {
		t.Errorf("ResolveThinking(0, 'off', '') = (%d, %q); want (0, '')", budget, effort)
	}

	budget, effort = ResolveThinking(0, "", "")
	if budget != 0 || effort != "" {
		t.Errorf("ResolveThinking(0, '', '') = (%d, %q); want (0, '')", budget, effort)
	}

	// Sanitization of invalid effort choices
	budget, effort = ResolveThinking(0, "invalid-effort", "")
	if budget != 0 || effort != "" {
		t.Errorf("ResolveThinking(0, 'invalid-effort', '') = (%d, %q); want (0, '')", budget, effort)
	}

	budget, effort = ResolveThinking(0, "ultra-high", "")
	if budget != 0 || effort != "" {
		t.Errorf("ResolveThinking(0, 'ultra-high', '') = (%d, %q); want (0, '')", budget, effort)
	}
}

func TestIsOpenAIReasoningModel(t *testing.T) {
	tests := []struct {
		model    string
		expected bool
	}{
		{"o1", true},
		{"o1-2024-12-17", true},
		{"o3", true},
		{"o3-mini", true},
		{"o3-mini-2025-01-31", true},
		{"o-series", true},
		// Excluded models that reject reasoning_effort with HTTP 400
		{"o1-mini", false},
		{"o1-mini-2024-09-12", false},
		{"o1-preview", false},
		{"o1-preview-2024-09-12", false},
		// Non-reasoning models
		{"gpt-4o", false},
		{"gpt-4o-mini", false},
		{"gpt-4", false},
		{"claude-3-5-sonnet", false},
		{"gemini-2.5-flash", false},
	}

	for _, tc := range tests {
		got := IsOpenAIReasoningModel(tc.model)
		if got != tc.expected {
			t.Errorf("IsOpenAIReasoningModel(%q) = %v; want %v", tc.model, got, tc.expected)
		}
	}
}

func TestProviderPayloadGeneration(t *testing.T) {
	// 1. Anthropic thinking payload
	{
		_, _, body, err := BuildRequest("anthropic", "claude-sonnet-4-5", []Message{{Role: "user", Content: "hi"}}, "sk-test", nil, false, TurnOptions{
			ThinkingBudget: 4096,
		})
		if err != nil {
			t.Fatalf("Anthropic BuildRequest error: %v", err)
		}
		var payload map[string]any
		if err := json.Unmarshal(body, &payload); err != nil {
			t.Fatalf("Unmarshal error: %v", err)
		}
		thinking, ok := payload["thinking"].(map[string]any)
		if !ok {
			t.Fatalf("expected thinking object in payload, got %#v", payload["thinking"])
		}
		if thinking["type"] != "enabled" || thinking["budget_tokens"] != float64(4096) {
			t.Errorf("Anthropic thinking = %#v; want type=enabled, budget_tokens=4096", thinking)
		}
		maxTokens, _ := payload["max_tokens"].(float64)
		if maxTokens < 4096+1024 {
			t.Errorf("Anthropic max_tokens = %v; want >= %d", maxTokens, 4096+1024)
		}
		if _, exists := payload["temperature"]; exists {
			t.Errorf("Anthropic temperature was not stripped when thinking is enabled")
		}
	}

	// 2. Anthropic budget clamped to minimum 1024
	{
		_, _, body, err := BuildRequest("anthropic", "claude-sonnet-4-5", []Message{{Role: "user", Content: "hi"}}, "sk-test", nil, false, TurnOptions{
			ThinkingBudget: 500,
		})
		if err != nil {
			t.Fatalf("Anthropic BuildRequest error: %v", err)
		}
		var payload map[string]any
		if err := json.Unmarshal(body, &payload); err != nil {
			t.Fatalf("Unmarshal error: %v", err)
		}
		thinking, ok := payload["thinking"].(map[string]any)
		if !ok || thinking["budget_tokens"] != float64(1024) {
			t.Errorf("Anthropic minimum budget clamp failed: got %#v", thinking)
		}
		maxTokens, _ := payload["max_tokens"].(float64)
		if maxTokens < 1024+1024 {
			t.Errorf("Anthropic max_tokens = %v; want >= %d", maxTokens, 1024+1024)
		}
	}

	// 3. Anthropic thinking off
	{
		_, _, body, err := BuildRequest("anthropic", "claude-sonnet-4-5", []Message{{Role: "user", Content: "hi"}}, "sk-test", nil, false, TurnOptions{
			Thinking: "off",
		})
		if err != nil {
			t.Fatalf("Anthropic BuildRequest error: %v", err)
		}
		var payload map[string]any
		if err := json.Unmarshal(body, &payload); err != nil {
			t.Fatalf("Unmarshal error: %v", err)
		}
		if _, exists := payload["thinking"]; exists {
			t.Errorf("Anthropic thinking should be omitted when thinking=off, got %#v", payload["thinking"])
		}
	}

	// 4. OpenAI reasoning model (o3-mini) gets reasoning_effort
	{
		_, _, body, err := BuildRequest("openai", "o3-mini", []Message{{Role: "user", Content: "hi"}}, "sk-test", nil, false, TurnOptions{
			ReasoningEffort: "high",
		})
		if err != nil {
			t.Fatalf("OpenAI BuildRequest error: %v", err)
		}
		var payload map[string]any
		if err := json.Unmarshal(body, &payload); err != nil {
			t.Fatalf("Unmarshal error: %v", err)
		}
		if payload["reasoning_effort"] != "high" {
			t.Errorf("OpenAI o3-mini reasoning_effort = %v; want 'high'", payload["reasoning_effort"])
		}
	}

	// 5. OpenAI non-reasoning model (gpt-4o) omits reasoning_effort
	{
		_, _, body, err := BuildRequest("openai", "gpt-4o", []Message{{Role: "user", Content: "hi"}}, "sk-test", nil, false, TurnOptions{
			ReasoningEffort: "high",
		})
		if err != nil {
			t.Fatalf("OpenAI BuildRequest error: %v", err)
		}
		var payload map[string]any
		if err := json.Unmarshal(body, &payload); err != nil {
			t.Fatalf("Unmarshal error: %v", err)
		}
		if _, exists := payload["reasoning_effort"]; exists {
			t.Errorf("gpt-4o must omit reasoning_effort to prevent HTTP 400, got %v", payload["reasoning_effort"])
		}
	}

	// 5b. OpenAI o1-mini and o1-preview omit reasoning_effort to prevent HTTP 400
	{
		for _, m := range []string{"o1-mini", "o1-preview"} {
			_, _, body, err := BuildRequest("openai", m, []Message{{Role: "user", Content: "hi"}}, "sk-test", nil, false, TurnOptions{
				ReasoningEffort: "high",
			})
			if err != nil {
				t.Fatalf("OpenAI %s BuildRequest error: %v", m, err)
			}
			var payload map[string]any
			if err := json.Unmarshal(body, &payload); err != nil {
				t.Fatalf("Unmarshal error: %v", err)
			}
			if _, exists := payload["reasoning_effort"]; exists {
				t.Errorf("%s must omit reasoning_effort to prevent HTTP 400, got %v", m, payload["reasoning_effort"])
			}
		}
	}

	// 6. Local provider ignores reasoning_effort
	{
		t.Setenv(LocalBaseURLEnv, "http://127.0.0.1:11434/v1")
		_, _, body, err := BuildRequest("local", "qwen2.5-coder:14b", []Message{{Role: "user", Content: "hi"}}, "", nil, false, TurnOptions{
			ReasoningEffort: "high",
		})
		if err != nil {
			t.Fatalf("Local BuildRequest error: %v", err)
		}
		var payload map[string]any
		if err := json.Unmarshal(body, &payload); err != nil {
			t.Fatalf("Unmarshal error: %v", err)
		}
		if _, exists := payload["reasoning_effort"]; exists {
			t.Errorf("local must omit reasoning_effort, got %v", payload["reasoning_effort"])
		}
	}

	// 7. Gemini 2.5 with thinking budget
	{
		_, _, body, err := BuildRequest("gemini", "gemini-2.5-flash", []Message{{Role: "user", Content: "hi"}}, "sk-test", nil, false, TurnOptions{
			ThinkingBudget: 2048,
		})
		if err != nil {
			t.Fatalf("Gemini BuildRequest error: %v", err)
		}
		var payload map[string]any
		if err := json.Unmarshal(body, &payload); err != nil {
			t.Fatalf("Unmarshal error: %v", err)
		}
		genConfig, ok := payload["generationConfig"].(map[string]any)
		if !ok {
			t.Fatalf("expected generationConfig in Gemini payload, got %#v", payload)
		}
		thinkingConfig, ok := genConfig["thinkingConfig"].(map[string]any)
		if !ok || thinkingConfig["thinkingBudget"] != float64(2048) {
			t.Errorf("Gemini thinkingConfig = %#v; want thinkingBudget=2048", thinkingConfig)
		}
	}

	// 8. Gemini 2.5 with thinking="off" passes thinkingBudget: 0
	{
		_, _, body, err := BuildRequest("gemini", "gemini-2.5-flash", []Message{{Role: "user", Content: "hi"}}, "sk-test", nil, false, TurnOptions{
			Thinking: "off",
		})
		if err != nil {
			t.Fatalf("Gemini BuildRequest error: %v", err)
		}
		var payload map[string]any
		if err := json.Unmarshal(body, &payload); err != nil {
			t.Fatalf("Unmarshal error: %v", err)
		}
		genConfig, ok := payload["generationConfig"].(map[string]any)
		if !ok {
			t.Fatalf("expected generationConfig in Gemini payload, got %#v", payload)
		}
		thinkingConfig, ok := genConfig["thinkingConfig"].(map[string]any)
		if !ok || thinkingConfig["thinkingBudget"] != float64(0) {
			t.Errorf("Gemini thinkingConfig = %#v; want thinkingBudget=0", thinkingConfig)
		}
	}

	// 8b. Gemini 2.5 with ReasoningEffort="off" passes thinkingBudget: 0
	{
		_, _, body, err := BuildRequest("gemini", "gemini-2.5-flash", []Message{{Role: "user", Content: "hi"}}, "sk-test", nil, false, TurnOptions{
			ReasoningEffort: "off",
		})
		if err != nil {
			t.Fatalf("Gemini BuildRequest error: %v", err)
		}
		var payload map[string]any
		if err := json.Unmarshal(body, &payload); err != nil {
			t.Fatalf("Unmarshal error: %v", err)
		}
		genConfig, ok := payload["generationConfig"].(map[string]any)
		if !ok {
			t.Fatalf("expected generationConfig in Gemini payload, got %#v", payload)
		}
		thinkingConfig, ok := genConfig["thinkingConfig"].(map[string]any)
		if !ok || thinkingConfig["thinkingBudget"] != float64(0) {
			t.Errorf("Gemini thinkingConfig = %#v; want thinkingBudget=0", thinkingConfig)
		}
	}

	// 9. Gemini 1.5 does not support thinkingConfig
	{
		_, _, body, err := BuildRequest("gemini", "gemini-1.5-flash", []Message{{Role: "user", Content: "hi"}}, "sk-test", nil, false, TurnOptions{
			ThinkingBudget: 2048,
		})
		if err != nil {
			t.Fatalf("Gemini BuildRequest error: %v", err)
		}
		var payload map[string]any
		if err := json.Unmarshal(body, &payload); err != nil {
			t.Fatalf("Unmarshal error: %v", err)
		}
		if _, exists := payload["generationConfig"]; exists {
			t.Errorf("Gemini 1.5 must omit generationConfig to prevent HTTP 400, got %#v", payload["generationConfig"])
		}
	}
}

func TestSessionStatusContextAndCost(t *testing.T) {
	store, err := OpenSessionStore(":memory:")
	if err != nil {
		t.Fatalf("OpenSessionStore error: %v", err)
	}
	defer store.Close()

	session, err := store.CreateSession(Session{
		Title:    "Title",
		CWD:      "/cwd",
		Provider: "anthropic",
		Model:    "claude-sonnet-4-5",
		BusyMode: "interrupt",
	})
	if err != nil {
		t.Fatalf("CreateSession error: %v", err)
	}

	// Empty session
	st, err := GetSessionStatus(store, session.ID)
	if err != nil {
		t.Fatalf("GetSessionStatus error: %v", err)
	}
	if st.ContextWindow != 200000 {
		t.Errorf("empty session context window = %d; want 200000", st.ContextWindow)
	}
	if st.ContextPct != 0.0 {
		t.Errorf("empty session context pct = %f; want 0.0", st.ContextPct)
	}
	if st.Cost != 0.0 {
		t.Errorf("empty session cost = %f; want 0.0", st.Cost)
	}
	if st.InputTokens != nil || st.OutputTokens != nil {
		t.Errorf("empty session tokens should be nil; got input=%v, output=%v", st.InputTokens, st.OutputTokens)
	}

	// Session with usage
	inTokens := int64(10000)
	outTokens := int64(2000)
	_, err = store.AppendMessage(session.ID, Message{
		Role:         "assistant",
		Content:      "hello",
		InputTokens:  &inTokens,
		OutputTokens: &outTokens,
	})
	if err != nil {
		t.Fatalf("AppendMessage error: %v", err)
	}

	st, err = GetSessionStatus(store, session.ID)
	if err != nil {
		t.Fatalf("GetSessionStatus error: %v", err)
	}
	// 12000 / 200000 * 100 = 6.0%
	if st.ContextPct != 6.0 {
		t.Errorf("context pct = %f; want 6.0", st.ContextPct)
	}
	if st.ActiveTokens != 12000 {
		t.Errorf("active tokens = %d; want 12000", st.ActiveTokens)
	}
	// 10000 * 3 / 1e6 + 2000 * 15 / 1e6 = 0.03 + 0.03 = 0.06
	if math.Abs(st.Cost-0.06) > 1e-6 {
		t.Errorf("cost = %f; want 0.06", st.Cost)
	}

	// Multi-turn session: working context must be latest turn (input+output), not cumulative sum
	inTokens2 := int64(15000)
	outTokens2 := int64(3000)
	_, err = store.AppendMessage(session.ID, Message{
		Role:         "assistant",
		Content:      "second turn",
		InputTokens:  &inTokens2,
		OutputTokens: &outTokens2,
	})
	if err != nil {
		t.Fatalf("AppendMessage 2 error: %v", err)
	}

	st, err = GetSessionStatus(store, session.ID)
	if err != nil {
		t.Fatalf("GetSessionStatus error: %v", err)
	}
	// Active tokens = latest turn input (15000) + output (3000) = 18000
	if st.ActiveTokens != 18000 {
		t.Errorf("active tokens = %d; want 18000", st.ActiveTokens)
	}
	// Context pct = 18000 / 200000 * 100 = 9.0% (NOT (12000+18000)/200000 = 15.0%)
	if st.ContextPct != 9.0 {
		t.Errorf("multi-turn context pct = %f; want 9.0", st.ContextPct)
	}
	// Cumulative tokens = 10000+15000 = 25000 in, 2000+3000 = 5000 out
	if inVal, ok := st.InputTokens.(int64); !ok || inVal != 25000 {
		t.Errorf("cumulative input tokens = %v; want 25000", st.InputTokens)
	}
	if outVal, ok := st.OutputTokens.(int64); !ok || outVal != 5000 {
		t.Errorf("cumulative output tokens = %v; want 5000", st.OutputTokens)
	}
	// Cost = 25000 * 3 / 1e6 + 5000 * 15 / 1e6 = 0.075 + 0.075 = 0.1500
	if math.Abs(st.Cost-0.15) > 1e-6 {
		t.Errorf("cumulative cost = %f; want 0.15", st.Cost)
	}
}

func TestAnthropicStreamingThinkingAndSignature(t *testing.T) {
	sseData := "event: message_start\n" +
		"data: {\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":100}}}\n\n" +
		"event: content_block_start\n" +
		"data: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"thinking\",\"thinking\":\"\"}}\n\n" +
		"event: content_block_delta\n" +
		"data: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"thinking_delta\",\"thinking\":\"Let me think.\"}}\n\n" +
		"event: content_block_delta\n" +
		"data: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"signature_delta\",\"signature\":\"sig_crypto_123\"}}\n\n" +
		"event: content_block_stop\n" +
		"data: {\"type\":\"content_block_stop\",\"index\":0}\n\n" +
		"event: content_block_start\n" +
		"data: {\"type\":\"content_block_start\",\"index\":1,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n" +
		"event: content_block_delta\n" +
		"data: {\"type\":\"content_block_delta\",\"index\":1,\"delta\":{\"type\":\"text_delta\",\"text\":\"Hello world!\"}}\n\n" +
		"event: content_block_stop\n" +
		"data: {\"type\":\"content_block_stop\",\"index\":1}\n\n" +
		"event: message_delta\n" +
		"data: {\"type\":\"message_delta\",\"usage\":{\"output_tokens\":50}}\n\n"

	var deltas []string
	completion, err := readStream(context.Background(), "anthropic", strings.NewReader(sseData), "test-key", func(d string) {
		deltas = append(deltas, d)
	})
	if err != nil {
		t.Fatalf("readStream error: %v", err)
	}

	// 1. Text should only contain user-facing text, not thinking
	if completion.Text != "Hello world!" {
		t.Errorf("completion.Text = %q; want 'Hello world!'", completion.Text)
	}
	if strings.Join(deltas, "") != "Hello world!" {
		t.Errorf("deltas = %q; want 'Hello world!'", strings.Join(deltas, ""))
	}

	// 2. ProviderMessage must preserve the thinking block with thinking text and signature
	blocks, ok := completion.ProviderMessage["content"].([]any)
	if !ok || len(blocks) != 2 {
		t.Fatalf("expected 2 content blocks in ProviderMessage, got %#v", completion.ProviderMessage)
	}
	thinkingBlock, ok := blocks[0].(map[string]any)
	if !ok || thinkingBlock["type"] != "thinking" {
		t.Fatalf("expected thinking block, got %#v", blocks[0])
	}
	if thinkingBlock["thinking"] != "Let me think." {
		t.Errorf("thinking = %q; want 'Let me think.'", thinkingBlock["thinking"])
	}
	if thinkingBlock["signature"] != "sig_crypto_123" {
		t.Errorf("signature = %q; want 'sig_crypto_123'", thinkingBlock["signature"])
	}
}

func TestGeminiThoughtFiltering(t *testing.T) {
	// 1. Non-streaming decodeCompletion filters thought parts from Text
	rawResponse := map[string]any{
		"candidates": []any{
			map[string]any{
				"content": map[string]any{
					"role": "model",
					"parts": []any{
						map[string]any{"text": "Internal thought reasoning...", "thought": true},
						map[string]any{"text": "User visible reply."},
					},
				},
			},
		},
		"usageMetadata": map[string]any{
			"promptTokenCount":     10,
			"candidatesTokenCount": 5,
		},
	}
	completion, err := decodeCompletion("gemini", rawResponse)
	if err != nil {
		t.Fatalf("decodeCompletion error: %v", err)
	}
	if completion.Text != "User visible reply." {
		t.Errorf("decodeCompletion Text = %q; want 'User visible reply.'", completion.Text)
	}
	// ProviderMessage retains all parts
	parts, ok := completion.ProviderMessage["parts"].([]any)
	if !ok || len(parts) != 2 {
		t.Fatalf("expected 2 parts in ProviderMessage, got %#v", completion.ProviderMessage)
	}

	// 2. Streaming readStream filters thought parts from Text and onDelta
	geminiSSE := "data: {\"candidates\":[{\"index\":0,\"content\":{\"role\":\"model\",\"parts\":[{\"text\":\"Streaming thought...\",\"thought\":true}]}}]}\n\n" +
		"data: {\"candidates\":[{\"index\":0,\"content\":{\"role\":\"model\",\"parts\":[{\"text\":\"Streaming answer.\"}]}}],\"usageMetadata\":{\"promptTokenCount\":10,\"candidatesTokenCount\":5}}\n\n"

	var deltas []string
	streamComp, err := readStream(context.Background(), "gemini", strings.NewReader(geminiSSE), "test-key", func(d string) {
		deltas = append(deltas, d)
	})
	if err != nil {
		t.Fatalf("readStream error: %v", err)
	}
	if streamComp.Text != "Streaming answer." {
		t.Errorf("streamComp.Text = %q; want 'Streaming answer.'", streamComp.Text)
	}
	if strings.Join(deltas, "") != "Streaming answer." {
		t.Errorf("streaming deltas = %q; want 'Streaming answer.'", strings.Join(deltas, ""))
	}
}
