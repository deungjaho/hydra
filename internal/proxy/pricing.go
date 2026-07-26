package proxy

import "strings"

// Model pricing table + cost calculation.
//
// Prices are USD per 1 million tokens, sourced from public model pricing pages.
// Antigravity accounts don't actually bill per-token (it's a subscription), but
// tracking the equivalent USD cost gives a useful "what would this cost on the
// open market" metric for comparing accounts and models.

type price struct {
	input  float64
	output float64
	cached float64
}

var prices = map[string]price{
	// Gemini family (Google AI Studio pricing, per 1M tokens).
	"gemini-2.5-flash":          {0.075, 0.30, 0.01875},
	"gemini-2.5-flash-thinking": {0.075, 0.30, 0.01875},
	"gemini-3-flash":            {0.10, 0.40, 0.025},
	"gemini-3-pro":              {1.25, 5.00, 0.3125},
	"gemini-3-pro-preview":      {1.25, 5.00, 0.3125},
	"gemini-3-pro-low":          {1.25, 5.00, 0.3125},
	"gemini-3-pro-high":         {1.25, 5.00, 0.3125},
	"gemini-3.1-pro-preview":    {1.25, 5.00, 0.3125},
	"gemini-3-pro-image":        {1.25, 5.00, 0.3125},
	"gemini-pro-agent":          {1.25, 5.00, 0.3125},
	// Claude family (Anthropic API pricing, per 1M tokens).
	"claude-sonnet-4-6":          {3.00, 15.00, 0.30},
	"claude-sonnet-4-6-thinking": {3.00, 15.00, 0.30},
	"claude-opus-4-6":            {5.00, 25.00, 0.50},
	"claude-opus-4-6-thinking":   {5.00, 25.00, 0.50},
}

var zeroPrice = price{}

// ComputeCost returns the USD cost of a single request.
//
// model is the orbit-mapped model id (e.g. claude-sonnet-4-6).
// Falls back to family prefix when an exact match isn't found.
// cachedTokens are billed at the cache-read rate (~10-25% of input price).
// Non-cached prompt tokens = promptTokens - cachedTokens (clamped to ≥0).
func ComputeCost(model string, promptTokens, completionTokens, cachedTokens int64) float64 {
	p := lookupPrice(model)
	cached := float64(clampInt64(cachedTokens, 0, maxInt64(promptTokens, 0))) / 1_000_000.0
	freshPrompt := float64(maxInt64(promptTokens, 0)-clampInt64(cachedTokens, 0, maxInt64(promptTokens, 0))) / 1_000_000.0
	c := float64(maxInt64(completionTokens, 0)) / 1_000_000.0
	return freshPrompt*p.input + cached*p.cached + c*p.output
}

func lookupPrice(model string) price {
	if p, ok := prices[model]; ok {
		return p
	}
	m := strings.ToLower(model)
	switch {
	case strings.HasPrefix(m, "claude-opus"):
		return prices["claude-opus-4-6"]
	case strings.HasPrefix(m, "claude-sonnet"):
		return prices["claude-sonnet-4-6"]
	case strings.HasPrefix(m, "claude-haiku"):
		return prices["claude-sonnet-4-6"]
	case strings.HasPrefix(m, "gemini-3-pro") || strings.HasPrefix(m, "gemini-3.1-pro"):
		return prices["gemini-3-pro"]
	case strings.HasPrefix(m, "gemini-3-flash") || strings.HasPrefix(m, "gemini-2.5-flash"):
		return prices["gemini-2.5-flash"]
	}
	return zeroPrice
}

func maxInt64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}

func clampInt64(v, lo, hi int64) int64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
