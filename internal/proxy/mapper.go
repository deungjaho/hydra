package proxy

import (
	"encoding/json"
	"sort"
	"strings"
	"time"

	"github.com/deungjaho/hydra/internal/account"
	"github.com/google/uuid"
)

// MapModel maps an incoming client model name to the upstream AGY model id.
// The only remapping is the internal background-task virtual ID.
func MapModel(openaiModel string) string {
	m := strings.ToLower(strings.TrimSpace(openaiModel))
	switch m {
	case "internal-background-task":
		return "gemini-2.5-flash"
	}
	return m
}

// maxOutputTokensCap returns the maximum output tokens the upstream
// streaming endpoint accepts for a given model. Uses AGY's dynamic
// metadata when available, falls back to LookupModelMeta.
func maxOutputTokensCap(model string) int64 {
	if dyn := LookupModelMetaDynamic(model); dyn != nil && dyn.MaxOutputTokens > 0 {
		return dyn.MaxOutputTokens
	}
	meta := LookupModelMeta(model)
	if meta.MaxOutputTokens > 0 {
		return meta.MaxOutputTokens
	}
	return defaultMaxOutputTokens
}

// thinkingModelPrefixes lists model ID prefixes that support extended
// thinking. Used as a fallback when AGY dynamic metadata is not loaded.
//
// UPDATE THIS WHEN AGY ADDS NEW THINKING MODELS.
var thinkingModelPrefixes = []string{
	"gemini-3-pro",
	"gemini-3.1-pro",
	"gemini-pro-agent",
	"gemini-2.5-pro",
	"gemini-2.5-flash-thinking",
	"gemini-3-flash",
	"gemini-3.5-flash",
	"gemini-3.6-flash",
	"gemini-3.7-flash",
	"gpt-oss-",
}

// isThinkingModel returns true if the model supports extended thinking.
// Uses AGY's dynamic metadata when available, falls back to ID-based heuristics.
func isThinkingModel(model string) bool {
	if dyn := LookupModelMetaDynamic(model); dyn != nil {
		return dyn.SupportsThinking
	}
	m := strings.ToLower(model)
	if strings.HasSuffix(m, "-thinking") {
		return true
	}
	for _, prefix := range thinkingModelPrefixes {
		if strings.HasPrefix(m, prefix) {
			return true
		}
	}
	return false
}

// hasThinkingSuffix returns true if the model ID ends with a thinking-level suffix.
func hasThinkingSuffix(model string) bool {
	m := strings.ToLower(model)
	for _, suffix := range []string{"-high", "-medium", "-low", "-tiered", "-extra-low"} {
		if strings.HasSuffix(m, suffix) {
			return true
		}
	}
	return false
}

// thinkingBudgetFor maps a reasoning effort level to an AGY thinkingBudget.
func thinkingBudgetFor(model, effort string) int64 {
	m := strings.ToLower(model)
	effort = strings.ToLower(strings.TrimSpace(effort))

	// Get AGY dynamic metadata for this model.
	var agyBudget, agyMinBudget int64
	var hasDyn bool
	if dyn := LookupModelMetaDynamic(model); dyn != nil && dyn.SupportsThinking {
		agyBudget = dyn.ThinkingBudget
		agyMinBudget = dyn.MinThinkingBudget
		if agyMinBudget == 0 {
			agyMinBudget = 128
		}
		hasDyn = true
	}

	// Models with thinking-level suffix: use AGY's built-in budget, ignore effort.
	if hasThinkingSuffix(m) {
		if hasDyn && agyBudget != 0 {
			return agyBudget
		}
		// Fallback to hardcoded defaults if no dynamic metadata.
		return hardcodedThinkingBudget(m)
	}

	// Models without suffix: scale by effort level.
	if effort == "" {
		if hasDyn {
			return agyBudget
		}
		return hardcodedThinkingBudget(m)
	}

	minB := int64(128)
	if hasDyn {
		minB = agyMinBudget
	}

	// Pro models: concrete budget scaling.
	isPro := strings.Contains(m, "pro") || strings.Contains(m, "gpt-oss")
	if isPro {
		switch effort {
		case "low":
			return minB
		case "medium":
			if hasDyn && agyBudget > 0 {
				return agyBudget / 2
			}
			return 5000
		case "high":
			if hasDyn && agyBudget > 0 {
				return agyBudget
			}
			return 10001
		case "xhigh":
			return 24000
		default:
			if hasDyn && agyBudget > 0 {
				return agyBudget
			}
			return 10001
		}
	}

	// Flash models: -1 means dynamic (let model decide).
	switch effort {
	case "low":
		return minB
	case "medium":
		return 4000
	case "high", "xhigh":
		return -1 // dynamic
	default:
		return 4000
	}
}

// hardcodedThinkingBudgets provides fallback thinkingBudget values
// when AGY dynamic metadata is not yet loaded. Values sourced from
// AGY's fetchAvailableModels response.
//
// -1 means "dynamic" (let the model decide).
// UPDATE THIS WHEN AGY ADDS NEW MODELS OR CHANGES BUDGETS.
var hardcodedThinkingBudgets = map[string]int64{
	"gemini-3.1-pro-high":        10001,
	"gemini-3.1-pro-low":         1001,
	"gemini-pro-agent":           10001,
	"gemini-2.5-pro":             1024,
	"gemini-3-flash":             -1,
	"gemini-3-flash-agent":       -1,
	"gemini-3.5-flash-low":       4000,
	"gemini-3.5-flash-extra-low": 1000,
	"gemini-3.6-flash-high":      -1,
	"gemini-3.6-flash-medium":    4000,
	"gemini-3.6-flash-low":       1000,
	"gemini-3.6-flash-tiered":    -1,
	"gemini-3.7-flash-high":      -1,
	"gemini-3.7-flash-medium":    4000,
	"gemini-3.7-flash-low":       1000,
	"gemini-3.7-flash-tiered":    -1,
	"gpt-oss-120b-medium":        8192,
}

const fallbackThinkingBudget = 10000

// hardcodedThinkingBudget provides fallback values when AGY dynamic
// metadata is not yet loaded. Values from AGY's fetchAvailableModels.
func hardcodedThinkingBudget(m string) int64 {
	if b, ok := hardcodedThinkingBudgets[m]; ok {
		return b
	}
	return fallbackThinkingBudget
}

// Legacy translation functions (buildOpenAIToolNameMap, openAIMessageToContent,
// textOfMessage, transformToolOpenAI, normalizeSchemaTypes, TransformResponse,
// mapFinishReason) have been removed. The IR layer (internal/ir) now handles
// all protocol translation for the Antigravity/AGY path. See ir_adapter.go.

// modelFamilyPrefix returns the family prefix of a model ID, used for
// same-family fallback resolution. e.g. "gemini-3-pro-high" → "gemini-3-pro",
// "claude-sonnet-4-6-thinking" → "claude-sonnet".
func modelFamilyPrefix(model string) string {
	m := strings.ToLower(strings.TrimSpace(model))
	parts := strings.Split(m, "-")
	if len(parts) < 2 {
		return m
	}
	// Two-segment families: "gpt-oss", "deepseek-r1".
	if len(parts) == 2 {
		return m
	}
	// Three-segment families: "gemini-3-pro", "gemini-3-flash",
	// "claude-sonnet-4", "claude-opus-4".
	if len(parts) >= 3 {
		family := strings.Join(parts[:3], "-")
		// For claude, the third segment is a version number (e.g.
		// "claude-sonnet-4-6" → family "claude-sonnet-4"). But we want
		// "claude-sonnet" so all sonnet variants are in the same family.
		if parts[0] == "claude" {
			return strings.Join(parts[:2], "-")
		}
		// For gemini image models, keep the "image" segment in the prefix.
		if parts[2] == "pro" && len(parts) >= 4 && parts[3] == "image" {
			return strings.Join(parts[:4], "-")
		}
		return family
	}
	return m
}

// ResolveModelForAccount picks the model to actually send to upstream for a
// specific account. If the account's quota lists the requested model, use it
// as-is. Otherwise, try same-family models from the account's available list.
// If no same-family match, return the original model unchanged.
func ResolveModelForAccount(mappedModel string, available map[string]struct{}) string {
	modelLC := strings.ToLower(strings.TrimSpace(mappedModel))
	if _, ok := available[modelLC]; ok {
		return mappedModel
	}
	family := modelFamilyPrefix(modelLC)
	if family == modelLC {
		return mappedModel
	}
	// Collect same-family models from the account's available list, sorted
	// for deterministic selection.
	var matches []string
	for m := range available {
		if strings.HasPrefix(m, family) {
			matches = append(matches, m)
		}
	}
	sortStrings(matches)
	if len(matches) > 0 {
		return matches[0]
	}
	return mappedModel
}

// DynamicModelList returns the union of routable models available across the
// given accounts, fetched dynamically from Google's fetchAvailableModels API.
// Returns an empty list if no accounts have quota data yet.
func DynamicModelList(accounts []*account.Account) []string {
	set := make(map[string]struct{})
	for _, a := range accounts {
		for m := range a.AvailableModels() {
			if isRoutableModel(m) {
				set[m] = struct{}{}
			}
		}
	}
	out := make([]string, 0, len(set))
	for m := range set {
		out = append(out, m)
	}
	sortStrings(out)
	return out
}

func isRoutableModel(name string) bool {
	m := strings.ToLower(name)
	// Exclude internal/non-routable models (chat_*, tab_*, etc.)
	if strings.HasPrefix(m, "chat_") || strings.HasPrefix(m, "tab_") {
		return false
	}
	return strings.HasPrefix(m, "gemini-") ||
		strings.HasPrefix(m, "claude-") ||
		strings.HasPrefix(m, "gpt-") ||
		strings.HasPrefix(m, "deepseek-")
}

func innerResponse(geminiResp map[string]any) map[string]any {
	if inner, ok := geminiResp["response"].(map[string]any); ok {
		return inner
	}
	return geminiResp
}

func strOr(m map[string]any, key, def string) string {
	if v, ok := m[key].(string); ok {
		return v
	}
	return def
}

func int64Or(m map[string]any, key string, def int64) int64 {
	switch v := m[key].(type) {
	case float64:
		return int64(v)
	case int64:
		return v
	case int:
		return int64(v)
	case json.Number:
		if n, err := v.Int64(); err == nil {
			return n
		}
	}
	return def
}

// float64Or extracts a float64 from a map field, returning def if absent.
func float64Or(m map[string]any, key string, def float64) float64 {
	switch v := m[key].(type) {
	case float64:
		return v
	case int64:
		return float64(v)
	case int:
		return float64(v)
	case json.Number:
		if n, err := v.Float64(); err == nil {
			return n
		}
	}
	return def
}

func valueOr(m map[string]any, key, def string) any {
	if v, ok := m[key]; ok {
		return v
	}
	return def
}

func orDefault(m map[string]any, key string, def any) any {
	if v, ok := m[key]; ok {
		return v
	}
	return def
}

// toInt64 safely converts any JSON number type (float64, int64, json.Number)
// to int64, returning def on failure.
func toInt64(v any, def int64) int64 {
	switch n := v.(type) {
	case float64:
		return int64(n)
	case int64:
		return n
	case int:
		return int64(n)
	case json.Number:
		if i, err := n.Int64(); err == nil {
			return i
		}
	}
	return def
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

func sortStrings(s []string) {
	sort.Strings(s)
}
func compactUUID() string {
	return strings.ReplaceAll(uuid.NewString(), "-", "")
}

func nowUnix() int64 {
	return time.Now().Unix()
}
