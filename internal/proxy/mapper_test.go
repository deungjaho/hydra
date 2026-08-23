package proxy

import (
	"testing"
)

func TestMapModel(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		// Background task virtual ID → real model
		{"internal-background-task", "gemini-2.5-flash"},

		// All real model IDs pass through unchanged
		{"gemini-3-pro-preview", "gemini-3-pro-preview"},
		{"gemini-3-pro-high", "gemini-3-pro-high"},
		{"gemini-3.7-flash-high", "gemini-3.7-flash-high"},
		{"claude-sonnet-4-6", "claude-sonnet-4-6"},
		{"claude-opus-4-6-thinking", "claude-opus-4-6-thinking"},
		{"gpt-oss-120b", "gpt-oss-120b"},

		// Unknown model passes through
		{"some-random-model", "some-random-model"},

		// Case insensitive
		{"GEMINI-3-PRO-HIGH", "gemini-3-pro-high"},
		{"Claude-Sonnet-4-6", "claude-sonnet-4-6"},

		// Whitespace trimmed
		{"  gemini-3-pro  ", "gemini-3-pro"},
	}
	for _, tt := range tests {
		got := MapModel(tt.input)
		if got != tt.want {
			t.Errorf("MapModel(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestIsThinkingModel(t *testing.T) {
	tests := []struct {
		model string
		want  bool
	}{
		{"claude-sonnet-4-6-thinking", true},
		{"claude-opus-4-6-thinking", true},
		{"claude-sonnet-4-6", false},
		{"gemini-3-pro", true},
		{"gemini-3-pro-preview", true},
		{"gemini-3-pro-high", true},
		{"gemini-3.1-pro-preview", true},
		{"gemini-pro-agent", true},
		{"gemini-2.5-flash", false},
		{"gemini-3-flash", true},
		{"gemini-3.7-flash-high", true},
		{"gpt-oss-120b", true},
		{"", false},
	}
	for _, tt := range tests {
		got := isThinkingModel(tt.model)
		if got != tt.want {
			t.Errorf("isThinkingModel(%q) = %v, want %v",
				tt.model, got, tt.want)
		}
	}
}

func TestContains(t *testing.T) {
	s := []string{"a", "b", "c"}
	if !contains(s, "b") {
		t.Error("contains should find b")
	}
	if contains(s, "z") {
		t.Error("contains should not find z")
	}
	if contains(nil, "a") {
		t.Error("contains on nil should be false")
	}
}

// --- helper function tests ---

func TestStrOr(t *testing.T) {
	m := map[string]any{"name": "alice", "age": float64(30)}
	if strOr(m, "name", "default") != "alice" {
		t.Error("should return string value")
	}
	if strOr(m, "age", "default") != "default" {
		t.Error("non-string should return default")
	}
	if strOr(m, "missing", "default") != "default" {
		t.Error("missing key should return default")
	}
}

func TestInt64Or(t *testing.T) {
	tests := []struct {
		name string
		m    map[string]any
		key  string
		want int64
	}{
		{"float64", map[string]any{"n": float64(42)}, "n", 42},
		{"int64", map[string]any{"n": int64(42)}, "n", 42},
		{"int", map[string]any{"n": 42}, "n", 42},
		{"string", map[string]any{"n": "42"}, "n", 99},
		{"missing", map[string]any{}, "n", 99},
		{"nil", nil, "n", 99},
	}
	for _, tt := range tests {
		got := int64Or(tt.m, tt.key, 99)
		if got != tt.want {
			t.Errorf("int64Or(%s) = %d, want %d", tt.name, got, tt.want)
		}
	}
}

func TestValueOr(t *testing.T) {
	m := map[string]any{"x": 123}
	if valueOr(m, "x", "def") != 123 {
		t.Error("should return value")
	}
	if valueOr(m, "y", "def") != "def" {
		t.Error("missing key should return default")
	}
}

func TestOrDefault(t *testing.T) {
	m := map[string]any{"x": 123}
	if orDefault(m, "x", 999) != 123 {
		t.Error("should return value")
	}
	if orDefault(m, "y", 999) != 999 {
		t.Error("missing key should return default")
	}
}

func TestToInt64(t *testing.T) {
	tests := []struct {
		name string
		v    any
		want int64
	}{
		{"float64", float64(42), 42},
		{"int64", int64(42), 42},
		{"int", 42, 42},
		{"string", "42", 99},
		{"nil", nil, 99},
	}
	for _, tt := range tests {
		got := toInt64(tt.v, 99)
		if got != tt.want {
			t.Errorf("toInt64(%s) = %d, want %d", tt.name, got, tt.want)
		}
	}
}

func TestIsRoutableModel(t *testing.T) {
	tests := []struct {
		model string
		want  bool
	}{
		{"gemini-3-pro", true},
		{"claude-sonnet-4-6", true},
		{"gpt-4", true},
		{"deepseek-r1", true},
		{"chat_something", false},
		{"tab_something", false},
		{"random-model", false},
		{"", false},
		{"GEMINI-3-PRO", true}, // case insensitive
	}
	for _, tt := range tests {
		got := isRoutableModel(tt.model)
		if got != tt.want {
			t.Errorf("isRoutableModel(%q) = %v, want %v",
				tt.model, got, tt.want)
		}
	}
}

func TestModelFamilyPrefix(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"gemini-3-pro-high", "gemini-3-pro"},
		{"gemini-3-pro", "gemini-3-pro"},
		{"gemini-3-flash", "gemini-3-flash"},
		{"gemini-3.7-flash-high", "gemini-3.7-flash"},
		{"gemini-3-pro-image", "gemini-3-pro-image"},
		{"claude-sonnet-4-6-thinking", "claude-sonnet"},
		{"claude-opus-4-6", "claude-opus"},
		{"gpt-oss-120b", "gpt-oss-120b"},
		{"deepseek-r1", "deepseek-r1"},
		{"", ""},
	}
	for _, tt := range tests {
		got := modelFamilyPrefix(tt.input)
		if got != tt.want {
			t.Errorf("modelFamilyPrefix(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestResolveModelForAccount_ExactMatch(t *testing.T) {
	available := map[string]struct{}{
		"gemini-3-pro": {},
	}
	got := ResolveModelForAccount("gemini-3-pro", available)
	if got != "gemini-3-pro" {
		t.Errorf("got %q, want gemini-3-pro", got)
	}
}

func TestResolveModelForAccount_SameFamilyFallback(t *testing.T) {
	// Requested model not in available, but a same-family model is.
	available := map[string]struct{}{
		"gemini-3-pro-high": {},
	}
	got := ResolveModelForAccount("gemini-3-pro-low", available)
	if got != "gemini-3-pro-high" {
		t.Errorf("got %q, want gemini-3-pro-high (same family)", got)
	}
}

func TestResolveModelForAccount_NoMatch(t *testing.T) {
	available := map[string]struct{}{
		"some-other-model": {},
	}
	got := ResolveModelForAccount("gemini-3-pro", available)
	if got != "gemini-3-pro" {
		t.Errorf("got %q, want gemini-3-pro (unchanged)", got)
	}
}

func TestResolveModelForAccount_ClaudeFamily(t *testing.T) {
	// Requested model not in available, but a same-family model is.
	available := map[string]struct{}{
		"claude-sonnet-4-6-thinking": {},
	}
	got := ResolveModelForAccount("claude-sonnet-4-6", available)
	if got != "claude-sonnet-4-6-thinking" {
		t.Errorf("got %q, want claude-sonnet-4-6-thinking", got)
	}
}

func TestInnerResponse(t *testing.T) {
	// With "response" wrapper.
	resp := map[string]any{
		"response": map[string]any{"candidates": []any{}},
	}
	inner := innerResponse(resp)
	if _, ok := inner["candidates"]; !ok {
		t.Error("should unwrap response")
	}

	// Without wrapper — passthrough.
	resp2 := map[string]any{"candidates": []any{}}
	inner2 := innerResponse(resp2)
	if _, ok := inner2["candidates"]; !ok {
		t.Error("should passthrough when no response wrapper")
	}
}

func TestSortStrings(t *testing.T) {
	s := []string{"banana", "apple", "cherry"}
	sortStrings(s)
	if s[0] != "apple" || s[1] != "banana" || s[2] != "cherry" {
		t.Errorf("not sorted: %v", s)
	}
}

func TestMaxOutputTokensCap(t *testing.T) {
	tests := []struct {
		model string
		want  int64
	}{
		{"claude-sonnet-4-6", 64000},
		{"claude-opus-4-6-thinking", 64000},
		{"Claude-Sonnet-4-6", 64000}, // case insensitive
		{"gemini-3-pro", 65535},
		{"gemini-2.5-flash", 65535},
		{"gpt-4", 65536},
		{"unknown-model", 65536},
	}
	for _, tt := range tests {
		got := maxOutputTokensCap(tt.model)
		if got != tt.want {
			t.Errorf("maxOutputTokensCap(%q) = %d, want %d",
				tt.model, got, tt.want)
		}
	}
}
