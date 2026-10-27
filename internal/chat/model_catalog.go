package chat

import (
	"math"
	"strconv"
	"strings"
)

// ModelMetadata defines context window limits and token pricing per million tokens.
type ModelMetadata struct {
	ContextWindow  int
	InputCostPerM  float64 // USD per 1M input tokens
	OutputCostPerM float64 // USD per 1M output tokens
}

const DefaultContextWindow = 128000

// Canonical model catalog mapping normalized model identifiers to metadata.
var modelPricingCatalog = map[string]ModelMetadata{
	// Anthropic Claude models (200k context)
	"claude-sonnet-4-5": {200000, 3.00, 15.00},
	"claude-3-7-sonnet": {200000, 3.00, 15.00},
	"claude-3-5-sonnet": {200000, 3.00, 15.00},
	"claude-3-5-haiku":  {200000, 0.80, 4.00},
	"claude-3-opus":     {200000, 15.00, 75.00},
	"claude-default":    {200000, 3.00, 15.00},

	// OpenAI models (128k context standard; gpt-4 8k; gpt-3.5 16k)
	"gpt-4o":         {128000, 2.50, 10.00},
	"gpt-4o-mini":    {128000, 0.15, 0.60},
	"o1":             {200000, 15.00, 60.00},
	"o1-mini":        {128000, 1.10, 4.40},
	"o1-preview":     {128000, 15.00, 60.00},
	"o3":             {200000, 15.00, 60.00},
	"o3-mini":        {200000, 1.10, 4.40},
	"gpt-4-turbo":    {128000, 10.00, 30.00},
	"gpt-4":          {8192, 30.00, 60.00},
	"gpt-3.5-turbo":  {16385, 0.50, 1.50},
	"openai-default": {128000, 2.50, 10.00},

	// Google Gemini models (1M context)
	"gemini-2.5-flash": {1000000, 0.075, 0.30},
	"gemini-2.5-pro":   {1000000, 1.25, 5.00},
	"gemini-2.0-flash": {1000000, 0.075, 0.30},
	"gemini-1.5-flash": {1000000, 0.075, 0.30},
	"gemini-1.5-pro":   {1000000, 1.25, 5.00},
	"gemini-default":   {1000000, 0.075, 0.30},

	// Local / Ollama models (always $0.00; 32k default context)
	"qwen2.5-coder:14b": {32768, 0.0, 0.0},
	"local-default":     {32768, 0.0, 0.0},
}

func normalizeCatalogProvider(provider string) string {
	prov := strings.ToLower(strings.TrimSpace(provider))
	if canonical, ok := NormalizeProvider(prov); ok {
		return canonical
	}
	return prov
}

// GetContextWindow returns the context window token ceiling for a model and provider.
// Defaults to 128,000 tokens when the specific limit is unknown.
func GetContextWindow(model, provider string) int {
	prov := normalizeCatalogProvider(provider)
	mod := strings.ToLower(strings.TrimSpace(model))

	if prov == "local" {
		return 32768
	}
	if strings.Contains(mod, "qwen") {
		return 32768
	}
	if meta, ok := modelPricingCatalog[mod]; ok {
		return meta.ContextWindow
	}
	if strings.HasPrefix(mod, "claude") || prov == "anthropic" {
		return 200000
	}
	if strings.HasPrefix(mod, "gemini") || prov == "gemini" {
		return 1000000
	}
	if strings.HasPrefix(mod, "o1-mini") || strings.HasPrefix(mod, "o1-preview") {
		return 128000
	}
	if strings.HasPrefix(mod, "o1") || strings.HasPrefix(mod, "o3") {
		return 200000
	}
	if strings.HasPrefix(mod, "gpt-4o") || strings.HasPrefix(mod, "gpt-4-turbo") || prov == "openai" || prov == "copilot" {
		if mod == "gpt-4" {
			return 8192
		}
		if mod == "gpt-3.5-turbo" {
			return 16385
		}
		return 128000
	}
	return DefaultContextWindow
}

func resolveModelMetadata(provider, model string) ModelMetadata {
	prov := normalizeCatalogProvider(provider)
	mod := strings.ToLower(strings.TrimSpace(model))

	if prov == "local" || prov == "copilot" {
		ctx := GetContextWindow(model, provider)
		return ModelMetadata{ContextWindow: ctx, InputCostPerM: 0.0, OutputCostPerM: 0.0}
	}
	if meta, ok := modelPricingCatalog[mod]; ok {
		return meta
	}

	// Prefix / family matching
	if strings.HasPrefix(mod, "claude") || prov == "anthropic" {
		if strings.Contains(mod, "haiku") {
			return modelPricingCatalog["claude-3-5-haiku"]
		}
		if strings.Contains(mod, "opus") {
			return modelPricingCatalog["claude-3-opus"]
		}
		return modelPricingCatalog["claude-default"]
	}

	if strings.HasPrefix(mod, "gemini") || prov == "gemini" {
		if strings.Contains(mod, "pro") {
			return modelPricingCatalog["gemini-1.5-pro"]
		}
		return modelPricingCatalog["gemini-default"]
	}

	if prov == "openai" || strings.HasPrefix(mod, "gpt") || strings.HasPrefix(mod, "o1") || strings.HasPrefix(mod, "o3") {
		if strings.Contains(mod, "o1-mini") {
			return modelPricingCatalog["o1-mini"]
		}
		if strings.Contains(mod, "o3-mini") {
			return modelPricingCatalog["o3-mini"]
		}
		if strings.Contains(mod, "gpt-4o-mini") || (strings.Contains(mod, "gpt-4o") && strings.Contains(mod, "mini")) {
			return modelPricingCatalog["gpt-4o-mini"]
		}
		if strings.HasPrefix(mod, "o1") || strings.HasPrefix(mod, "o3") {
			return modelPricingCatalog["o1"]
		}
		return modelPricingCatalog["openai-default"]
	}

	// Default fallback rate: $3.00 input, $15.00 output per 1M tokens
	return ModelMetadata{
		ContextWindow:  DefaultContextWindow,
		InputCostPerM:  3.00,
		OutputCostPerM: 15.00,
	}
}

// CalculateTokenCost calculates the token cost in USD. Local provider models are always $0.00.
func CalculateTokenCost(provider, model string, inputTokens, outputTokens int) float64 {
	prov := normalizeCatalogProvider(provider)
	if prov == "local" || prov == "copilot" {
		return 0.0
	}
	if inputTokens <= 0 && outputTokens <= 0 {
		return 0.0
	}
	meta := resolveModelMetadata(provider, model)
	inTokens := float64(inputTokens)
	if inTokens < 0 {
		inTokens = 0
	}
	outTokens := float64(outputTokens)
	if outTokens < 0 {
		outTokens = 0
	}
	cost := (inTokens * meta.InputCostPerM / 1000000.0) + (outTokens * meta.OutputCostPerM / 1000000.0)
	return math.Round(cost*1e6) / 1e6
}

// ResolveThinking normalizes thinking budget in tokens and reasoning effort string.
// Supports integer budget or effort level ("off", "low", "medium", "high").
func ResolveThinking(budget int, effort string, thinking string) (int, string) {
	b := budget
	e := strings.ToLower(strings.TrimSpace(effort))
	th := strings.ToLower(strings.TrimSpace(thinking))

	if th != "" {
		if val, err := strconv.Atoi(th); err == nil {
			b = val
		} else if th == "off" || th == "none" || th == "false" || th == "0" {
			return 0, ""
		} else if th == "low" || th == "medium" || th == "high" {
			e = th
		}
	}

	if e == "off" || e == "none" || e == "false" || e == "0" {
		return 0, ""
	}

	if e != "low" && e != "medium" && e != "high" {
		e = ""
	}

	if b < 0 {
		b = 0
	}

	if e != "" && b == 0 {
		effortMap := map[string]int{"low": 2048, "medium": 8192, "high": 16384}
		if mapped, ok := effortMap[e]; ok {
			b = mapped
		}
	}

	if b > 0 && e == "" {
		if b <= 2048 {
			e = "low"
		} else if b <= 8192 {
			e = "medium"
		} else {
			e = "high"
		}
	}

	return b, e
}

// IsOpenAIReasoningModel returns true if model is an OpenAI reasoning model that supports reasoning_effort
// (o1, o3, o-series, excluding o1-mini and o1-preview which reject reasoning_effort with HTTP 400).
func IsOpenAIReasoningModel(model string) bool {
	mod := strings.ToLower(strings.TrimSpace(model))
	if mod == "o1-mini" || strings.HasPrefix(mod, "o1-mini") || mod == "o1-preview" || strings.HasPrefix(mod, "o1-preview") {
		return false
	}
	return strings.HasPrefix(mod, "o1") || strings.HasPrefix(mod, "o3") || strings.HasPrefix(mod, "o-")
}

// IsGeminiThinkingSupported returns true if Gemini model supports thinkingConfig (2.5 series or 2.0-flash-thinking).
func IsGeminiThinkingSupported(model string) bool {
	mod := strings.ToLower(strings.TrimSpace(model))
	return strings.Contains(mod, "2.5") || strings.Contains(mod, "2.0-flash-thinking") || strings.Contains(mod, "thinking")
}
