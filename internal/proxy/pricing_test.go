package proxy

import (
	"math"
	"testing"
)

func approxEqual(a, b float64) bool {
	return math.Abs(a-b) < 1e-9
}

func TestComputeCost_GeminiFlash(t *testing.T) {
	// gemini-2.5-flash: input=0.075, output=0.30, cached=0.01875
	// 1000 prompt, 500 completion, 200 cached
	// freshPrompt = 1000-200 = 800
	// cost = 800/1e6 * 0.075 + 200/1e6 * 0.01875 + 500/1e6 * 0.30
	//      = 0.00006 + 0.00000375 + 0.00015
	//      = 0.00021375
	got := ComputeCost("gemini-2.5-flash", 1000, 500, 200)
	want := 0.00021375
	if !approxEqual(got, want) {
		t.Errorf("ComputeCost = %.10f, want %.10f", got, want)
	}
}

func TestComputeCost_GeminiPro(t *testing.T) {
	// gemini-3-pro: input=1.25, output=5.00, cached=0.3125
	// 1M prompt, 500K completion, 100K cached
	// freshPrompt = 1M - 100K = 900K
	// cost = 900000/1e6 * 1.25 + 100000/1e6 * 0.3125 + 500000/1e6 * 5.00
	//      = 1.125 + 0.03125 + 2.5
	//      = 3.65625
	got := ComputeCost("gemini-3-pro", 1_000_000, 500_000, 100_000)
	want := 3.65625
	if !approxEqual(got, want) {
		t.Errorf("ComputeCost = %.10f, want %.10f", got, want)
	}
}

func TestComputeCost_ClaudeSonnet(t *testing.T) {
	// claude-sonnet-4-6: input=3.00, output=15.00, cached=0.30
	// 1000 prompt, 1000 completion, 0 cached
	// cost = 1000/1e6 * 3.00 + 0 + 1000/1e6 * 15.00
	//      = 0.003 + 0.015
	//      = 0.018
	got := ComputeCost("claude-sonnet-4-6", 1000, 1000, 0)
	want := 0.018
	if !approxEqual(got, want) {
		t.Errorf("ComputeCost = %.10f, want %.10f", got, want)
	}
}

func TestComputeCost_ClaudeOpus(t *testing.T) {
	// claude-opus-4-6: input=5.00, output=25.00, cached=0.50
	// 2000 prompt, 500 completion, 500 cached
	// freshPrompt = 2000-500 = 1500
	// cost = 1500/1e6 * 5.00 + 500/1e6 * 0.50 + 500/1e6 * 25.00
	//      = 0.0075 + 0.00025 + 0.0125
	//      = 0.02025
	got := ComputeCost("claude-opus-4-6", 2000, 500, 500)
	want := 0.02025
	if !approxEqual(got, want) {
		t.Errorf("ComputeCost = %.10f, want %.10f", got, want)
	}
}

func TestComputeCost_AllCached(t *testing.T) {
	// If all prompt tokens are cached, freshPrompt = 0.
	// gemini-2.5-flash: 1000 prompt, 0 completion, 1000 cached
	// cost = 0 + 1000/1e6 * 0.01875 + 0
	//      = 0.00001875
	got := ComputeCost("gemini-2.5-flash", 1000, 0, 1000)
	want := 0.00001875
	if !approxEqual(got, want) {
		t.Errorf("ComputeCost = %.10f, want %.10f", got, want)
	}
}

func TestComputeCost_CachedExceedsPrompt(t *testing.T) {
	// cached > prompt should be clamped to prompt.
	// gemini-2.5-flash: 100 prompt, 0 completion, 500 cached
	// cached clamped to 100, freshPrompt = 0
	// cost = 100/1e6 * 0.01875
	//      = 0.000001875
	got := ComputeCost("gemini-2.5-flash", 100, 0, 500)
	want := 0.000001875
	if !approxEqual(got, want) {
		t.Errorf("ComputeCost = %.10f, want %.10f", got, want)
	}
}

func TestComputeCost_NegativeTokens(t *testing.T) {
	// Negative tokens should be treated as 0.
	got := ComputeCost("gemini-2.5-flash", -100, -50, -200)
	want := 0.0
	if !approxEqual(got, want) {
		t.Errorf("ComputeCost = %.10f, want %.10f", got, want)
	}
}

func TestComputeCost_ZeroTokens(t *testing.T) {
	got := ComputeCost("gemini-3-pro", 0, 0, 0)
	want := 0.0
	if !approxEqual(got, want) {
		t.Errorf("ComputeCost = %.10f, want %.10f", got, want)
	}
}

func TestComputeCost_UnknownModel(t *testing.T) {
	// Unknown model → zeroPrice → cost = 0
	got := ComputeCost("unknown-model", 1000, 500, 100)
	want := 0.0
	if !approxEqual(got, want) {
		t.Errorf("ComputeCost = %.10f, want %.10f", got, want)
	}
}

func TestComputeCost_ModelAliasResolution(t *testing.T) {
	// "gemini-3-pro-high" should resolve to gemini-3-pro pricing.
	got := ComputeCost("gemini-3-pro-high", 1_000_000, 0, 0)
	want := 1.25 // 1M input * $1.25/M
	if !approxEqual(got, want) {
		t.Errorf("ComputeCost = %.10f, want %.10f", got, want)
	}
}

func TestComputeCost_ClaudeOpusAliasResolution(t *testing.T) {
	// "claude-opus-4-6-thinking" should resolve to claude-opus pricing.
	got := ComputeCost("claude-opus-4-6-thinking", 1_000_000, 0, 0)
	want := 5.0 // 1M input * $5.00/M
	if !approxEqual(got, want) {
		t.Errorf("ComputeCost = %.10f, want %.10f", got, want)
	}
}

func TestComputeCost_CaseInsensitiveModel(t *testing.T) {
	// lookupPrice lowercases the model before prefix matching.
	got := ComputeCost("GEMINI-3-PRO", 1_000_000, 0, 0)
	want := 1.25
	if !approxEqual(got, want) {
		t.Errorf("ComputeCost = %.10f, want %.10f", got, want)
	}
}
