package proxy

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestLookupModelMeta_GeminiFamily(t *testing.T) {
	meta := LookupModelMeta("gemini-3-pro-high")
	if meta.DisplayName != "Gemini 3 Pro High" {
		t.Errorf("DisplayName = %q, want Gemini 3 Pro High", meta.DisplayName)
	}
	if meta.ContextWindow != 1000000 {
		t.Errorf("ContextWindow = %d, want 1000000", meta.ContextWindow)
	}
	if meta.MaxOutputTokens != 65535 {
		t.Errorf("MaxOutputTokens = %d, want 65535", meta.MaxOutputTokens)
	}
}

func TestLookupModelMeta_ClaudeFamily(t *testing.T) {
	meta := LookupModelMeta("claude-sonnet-4-6-thinking")
	if meta.DisplayName != "Claude Sonnet 4 6 (Thinking)" {
		t.Errorf("DisplayName = %q, want Claude Sonnet 4 6 (Thinking)", meta.DisplayName)
	}
	if meta.ContextWindow != 200000 {
		t.Errorf("ContextWindow = %d, want 200000", meta.ContextWindow)
	}
	if meta.MaxOutputTokens != 64000 {
		t.Errorf("MaxOutputTokens = %d, want 64000", meta.MaxOutputTokens)
	}
}

func TestLookupModelMeta_GPTOSSFamily(t *testing.T) {
	meta := LookupModelMeta("gpt-oss-120b")
	if meta.DisplayName != "Gpt Oss 120b" {
		t.Errorf("DisplayName = %q, want Gpt Oss 120b", meta.DisplayName)
	}
	if meta.ContextWindow != 128000 {
		t.Errorf("ContextWindow = %d, want 128000", meta.ContextWindow)
	}
}

func TestLookupModelMeta_UnknownFamily(t *testing.T) {
	meta := LookupModelMeta("some-unknown-model")
	if meta.ContextWindow != 1000000 {
		t.Errorf("ContextWindow = %d, want default 1000000", meta.ContextWindow)
	}
}

func TestLookupModelMeta_CaseInsensitive(t *testing.T) {
	meta := LookupModelMeta("GEMINI-3-PRO-HIGH")
	if meta.ContextWindow != 1000000 {
		t.Errorf("ContextWindow = %d", meta.ContextWindow)
	}
}

func TestDeriveDisplayName(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"gemini-3.7-flash-high", "Gemini 3.7 Flash High"},
		{"gemini-3-flash", "Gemini 3 Flash"},
		{"claude-sonnet-4-6-thinking", "Claude Sonnet 4 6 (Thinking)"},
		{"claude-opus-4-6", "Claude Opus 4 6"},
		{"gpt-oss-120b", "Gpt Oss 120b"},
		{"deepseek-r1", "Deepseek R1"},
	}
	for _, tt := range tests {
		got := deriveDisplayName(tt.input)
		if got != tt.want {
			t.Errorf("deriveDisplayName(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestBuildCodexCatalog_Basic(t *testing.T) {
	models := []string{
		"gemini-3-pro-high",
		"claude-sonnet-4-6-thinking",
	}
	cat := BuildCodexCatalog(models)
	if len(cat.Models) != 2 {
		t.Fatalf("len = %d, want 2", len(cat.Models))
	}
	// Sorted alphabetically.
	if cat.Models[0].Slug != "claude-sonnet-4-6-thinking" {
		t.Errorf("first slug = %q, want claude-sonnet-4-6-thinking", cat.Models[0].Slug)
	}
	if cat.Models[1].Slug != "gemini-3-pro-high" {
		t.Errorf("second slug = %q, want gemini-3-pro-high", cat.Models[1].Slug)
	}
}

func TestBuildCodexCatalog_Fields(t *testing.T) {
	cat := BuildCodexCatalog([]string{"gemini-3-pro-high"})
	e := cat.Models[0]
	if e.DisplayName != "Gemini 3 Pro High" {
		t.Errorf("DisplayName = %q", e.DisplayName)
	}
	if e.ContextWindow != 1000000 {
		t.Errorf("ContextWindow = %d", e.ContextWindow)
	}
	if e.MaxContextWindow != 1000000 {
		t.Errorf("MaxContextWindow = %d", e.MaxContextWindow)
	}
	if e.EffectiveContextWindowPct != 95 {
		t.Errorf("EffectiveContextWindowPct = %d", e.EffectiveContextWindowPct)
	}
	if e.MaxOutputTokens == nil || *e.MaxOutputTokens != 65535 {
		t.Errorf("MaxOutputTokens = %v, want 65535", e.MaxOutputTokens)
	}
	// gemini-3-pro-high has -high suffix → single reasoning level (no selector)
	if len(e.SupportedReasoningLevels) != 1 {
		t.Errorf("len(SupportedReasoningLevels) = %d, want 1 (suffix model)", len(e.SupportedReasoningLevels))
	}
	if e.SupportedReasoningLevels[0].Effort != "high" {
		t.Errorf("effort = %q, want high", e.SupportedReasoningLevels[0].Effort)
	}
	if e.DefaultReasoningLevel != "high" {
		t.Errorf("DefaultReasoningLevel = %q, want high", e.DefaultReasoningLevel)
	}
	if e.Visibility != "list" {
		t.Errorf("Visibility = %q", e.Visibility)
	}
	if e.SupportedInAPI != true {
		t.Errorf("SupportedInAPI = %v", e.SupportedInAPI)
	}
	if e.ShellType != "shell_command" {
		t.Errorf("ShellType = %q", e.ShellType)
	}
	if e.TruncationPolicy.Limit != 10000 {
		t.Errorf("TruncationPolicy.Limit = %d", e.TruncationPolicy.Limit)
	}
}

func TestBuildCodexCatalog_NoSuffix_HasAllReasoningLevels(t *testing.T) {
	cat := BuildCodexCatalog([]string{"gemini-pro-agent"})
	e := cat.Models[0]
	if len(e.SupportedReasoningLevels) != 4 {
		t.Errorf("len(SupportedReasoningLevels) = %d, want 4 (no-suffix model)", len(e.SupportedReasoningLevels))
	}
	if e.DefaultReasoningLevel != "medium" {
		t.Errorf("DefaultReasoningLevel = %q, want medium", e.DefaultReasoningLevel)
	}
}

func TestBuildCodexCatalog_Sorted(t *testing.T) {
	models := []string{
		"gemini-3-pro",
		"claude-sonnet-4-6",
		"gemini-2.5-flash",
	}
	cat := BuildCodexCatalog(models)
	if cat.Models[0].Slug != "claude-sonnet-4-6" {
		t.Errorf("first = %q, want claude-sonnet-4-6", cat.Models[0].Slug)
	}
	if cat.Models[1].Slug != "gemini-2.5-flash" {
		t.Errorf("second = %q, want gemini-2.5-flash", cat.Models[1].Slug)
	}
	if cat.Models[2].Slug != "gemini-3-pro" {
		t.Errorf("third = %q, want gemini-3-pro", cat.Models[2].Slug)
	}
}

func TestBuildCodexCatalog_Empty(t *testing.T) {
	cat := BuildCodexCatalog(nil)
	if len(cat.Models) != 0 {
		t.Errorf("len = %d, want 0", len(cat.Models))
	}
}

func TestBuildCodexCatalog_JSONRoundtrip(t *testing.T) {
	cat := BuildCodexCatalog([]string{"gemini-3-pro-high"})
	b, err := json.Marshal(cat)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var back CodexCatalog
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(back.Models) != 1 {
		t.Fatalf("len = %d, want 1", len(back.Models))
	}
	if back.Models[0].Slug != "gemini-3-pro-high" {
		t.Errorf("Slug = %q", back.Models[0].Slug)
	}
}

func TestBuildClaudeConfig_WithClaudeModels(t *testing.T) {
	models := []string{
		"claude-sonnet-4-6-thinking",
		"claude-opus-4-6-thinking",
		"gemini-3-flash",
	}
	cfg := BuildClaudeConfig(models)
	if cfg.Env["ANTHROPIC_DEFAULT_SONNET_MODEL"] != "claude-sonnet-4-6-thinking" {
		t.Errorf("sonnet = %q", cfg.Env["ANTHROPIC_DEFAULT_SONNET_MODEL"])
	}
	if cfg.Env["ANTHROPIC_DEFAULT_OPUS_MODEL"] != "claude-opus-4-6-thinking" {
		t.Errorf("opus = %q", cfg.Env["ANTHROPIC_DEFAULT_OPUS_MODEL"])
	}
	if cfg.Env["ANTHROPIC_MODEL"] != "gemini-3-flash" {
		t.Errorf("model = %q", cfg.Env["ANTHROPIC_MODEL"])
	}
}

func TestBuildClaudeConfig_NoClaudeModels(t *testing.T) {
	models := []string{
		"gemini-3-pro-high",
		"gemini-3-flash",
	}
	cfg := BuildClaudeConfig(models)
	if cfg.Env["ANTHROPIC_MODEL"] != "gemini-3-flash" {
		t.Errorf("model = %q, want gemini-3-flash", cfg.Env["ANTHROPIC_MODEL"])
	}
	if _, ok := cfg.Env["ANTHROPIC_DEFAULT_SONNET_MODEL"]; ok {
		t.Error("should not set sonnet when no claude models available")
	}
}

func TestBuildClaudeConfig_Empty(t *testing.T) {
	cfg := BuildClaudeConfig(nil)
	if len(cfg.Env) != 0 {
		t.Errorf("env = %v, want empty", cfg.Env)
	}
}

func TestBuildClaudeConfig_DisplayNames(t *testing.T) {
	models := []string{"claude-sonnet-4-6-thinking", "claude-opus-4-6-thinking", "gemini-3-flash"}
	cfg := BuildClaudeConfig(models)
	if !strings.Contains(cfg.Env["ANTHROPIC_DEFAULT_SONNET_MODEL_NAME"], "Claude Sonnet") {
		t.Errorf("sonnet name = %q", cfg.Env["ANTHROPIC_DEFAULT_SONNET_MODEL_NAME"])
	}
	if !strings.Contains(cfg.Env["ANTHROPIC_DEFAULT_OPUS_MODEL_NAME"], "Claude Opus") {
		t.Errorf("opus name = %q", cfg.Env["ANTHROPIC_DEFAULT_OPUS_MODEL_NAME"])
	}
}

func TestPickByPrefix(t *testing.T) {
	set := map[string]struct{}{
		"gemini-3-pro-high": {},
		"gemini-3-flash":    {},
	}
	got := pickByPrefix(set, "gemini-3-pro")
	if got != "gemini-3-pro-high" {
		t.Errorf("got %q, want gemini-3-pro-high", got)
	}
}

func TestPickByPrefix_NoMatch(t *testing.T) {
	set := map[string]struct{}{"gemini-3-pro": {}}
	got := pickByPrefix(set, "claude-")
	if got != "" {
		t.Errorf("got %q, want empty", got)
	}
}

func TestPickFirst(t *testing.T) {
	set := map[string]struct{}{"gemini-3.7-flash": {}}
	got := pickFirst(set, "gemini-3-flash", "gemini-3.7-flash", "gemini-2.5-flash")
	if got != "gemini-3.7-flash" {
		t.Errorf("got %q, want gemini-3.7-flash", got)
	}
}

func TestPickFirst_NoMatch(t *testing.T) {
	set := map[string]struct{}{"other-model": {}}
	got := pickFirst(set, "gemini-3-flash", "gemini-3.7-flash")
	if got != "" {
		t.Errorf("got %q, want empty", got)
	}
}
