package proxy

import (
	_ "embed"
	"sort"
	"strings"
)

//go:embed codex_base_instructions.txt
var codexBaseInstructions string

// ReasoningLevel is one entry in a model's supported_reasoning_levels list.
type ReasoningLevel struct {
	Description string `json:"description"`
	Effort      string `json:"effort"`
}

// ModelMeta is metadata derived from a model ID.
// All fields are inferred from the model ID's family and structure,
// not from a hardcoded model list.
type ModelMeta struct {
	DisplayName     string
	Description     string
	ContextWindow   int
	MaxOutputTokens int64
	InputModalities []string
}

// defaultReasoningLevels is the standard low/medium/high/xhigh set
// that all AGY-served models support.
var defaultReasoningLevels = []ReasoningLevel{
	{Description: "Fast responses with lighter reasoning", Effort: "low"},
	{Description: "Balances speed and reasoning depth for everyday tasks", Effort: "medium"},
	{Description: "Greater reasoning depth for complex problems", Effort: "high"},
	{Description: "Extra high reasoning depth for complex problems", Effort: "xhigh"},
}

// familyDefaults holds per-family defaults inferred from the model ID prefix.
// This is NOT a model list — it's family-level attributes (context window,
// max output tokens, modalities) that all models in a family share.
// Values are derived from AGY's fetchAvailableModels metadata.
type familyDefault struct {
	prefix        string
	contextWindow int
	maxOutput     int64
	modalities    []string
}

// modelMaxOutput overrides maxOutput for specific model IDs that differ
// from their family default. Derived from AGY's fetchAvailableModels.
var modelMaxOutput = map[string]int64{
	// Gemini 3.x flash models: AGY reports 65536
	"gemini-3-flash":             65536,
	"gemini-3-flash-agent":       65536,
	"gemini-3.5-flash-extra-low": 65536,
	"gemini-3.5-flash-low":       65536,
	"gemini-3.6-flash-high":      65536,
	"gemini-3.6-flash-low":       65536,
	"gemini-3.6-flash-medium":    65536,
	"gemini-3.6-flash-tiered":    65536,
	// Gemini 3.7 flash models: not in current AGY metadata, use 65535 (safe)
	// Gemini 2.5 / 3.1 pro / pro-agent: AGY reports 65535
	"gemini-2.5-flash":          65535,
	"gemini-2.5-flash-lite":     65535,
	"gemini-2.5-flash-thinking": 65535,
	"gemini-2.5-pro":            65535,
	"gemini-3.1-flash-lite":     65535,
	"gemini-3.1-pro-high":       65535,
	"gemini-3.1-pro-low":        65535,
	"gemini-pro-agent":          65535,
	// GPT-OSS: AGY reports 32768
	"gpt-oss-120b-medium": 32768,
}

var familyDefaults = []familyDefault{
	{prefix: "claude-", contextWindow: 200000, maxOutput: 64000, modalities: []string{"text", "image"}},
	{prefix: "gemini-", contextWindow: 1000000, maxOutput: 65535, modalities: []string{"text", "image"}},
	{prefix: "gpt-oss-", contextWindow: 128000, maxOutput: 65536, modalities: []string{"text"}},
	{prefix: "deepseek-", contextWindow: 128000, maxOutput: 65536, modalities: []string{"text"}},
}

// LookupModelMeta derives metadata from a model ID by family.
// The model list itself is dynamic (from AGY's fetchAvailableModels);
// this function only infers family-level attributes from the ID string.
func LookupModelMeta(model string) ModelMeta {
	m := strings.ToLower(strings.TrimSpace(model))
	for _, fam := range familyDefaults {
		if strings.HasPrefix(m, fam.prefix) {
			maxOut := fam.maxOutput
			if override, ok := modelMaxOutput[m]; ok {
				maxOut = override
			}
			return ModelMeta{
				DisplayName:     deriveDisplayName(m),
				Description:     deriveDisplayName(m),
				ContextWindow:   fam.contextWindow,
				MaxOutputTokens: maxOut,
				InputModalities: fam.modalities,
			}
		}
	}
	// Unknown family — use generous defaults.
	return ModelMeta{
		DisplayName:     deriveDisplayName(m),
		Description:     deriveDisplayName(m),
		ContextWindow:   1000000,
		MaxOutputTokens: 65536,
		InputModalities: []string{"text", "image"},
	}
}

// deriveDisplayName converts a model ID like "gemini-3.7-flash-high"
// to "Gemini 3.7 Flash High". A trailing "-thinking" segment becomes
// "(Thinking)".
func deriveDisplayName(model string) string {
	parts := strings.Split(model, "-")
	// Detect trailing "thinking" segment.
	thinking := false
	if len(parts) > 1 && strings.EqualFold(parts[len(parts)-1], "thinking") {
		thinking = true
		parts = parts[:len(parts)-1]
	}
	for i, p := range parts {
		parts[i] = titleCase(p)
	}
	name := strings.Join(parts, " ")
	if thinking {
		name += " (Thinking)"
	}
	return name
}

func titleCase(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

// CodexCatalogEntry is one model entry in the Codex model_catalog_json format.
type CodexCatalogEntry struct {
	Slug                        string           `json:"slug"`
	DisplayName                 string           `json:"display_name"`
	Description                 string           `json:"description"`
	ContextWindow               int              `json:"context_window"`
	MaxContextWindow            int              `json:"max_context_window"`
	EffectiveContextWindowPct   int              `json:"effective_context_window_percent"`
	MaxOutputTokens             *int64           `json:"max_output_tokens"`
	InputModalities             []string         `json:"input_modalities"`
	DefaultReasoningLevel       string           `json:"default_reasoning_level"`
	DefaultReasoningSummary     string           `json:"default_reasoning_summary"`
	DefaultVerbosity            string           `json:"default_verbosity"`
	SupportVerbosity            bool             `json:"support_verbosity"`
	SupportedReasoningLevels    []ReasoningLevel `json:"supported_reasoning_levels"`
	SupportsParallelToolCalls   bool             `json:"supports_parallel_tool_calls"`
	SupportsSearchTool          bool             `json:"supports_search_tool"`
	SupportedInAPI              bool             `json:"supported_in_api"`
	Visibility                  string           `json:"visibility"`
	ShellType                   string           `json:"shell_type"`
	ApplyPatchToolType          string           `json:"apply_patch_tool_type"`
	WebSearchToolType           string           `json:"web_search_tool_type"`
	TruncationPolicy            truncationPolicy `json:"truncation_policy"`
	Priority                    int              `json:"priority"`
	UseResponsesLite            bool             `json:"use_responses_lite"`
	SupportsImageDetailOriginal bool             `json:"supports_image_detail_original"`
	AdditionalSpeedTiers        []any            `json:"additional_speed_tiers"`
	ServiceTiers                []any            `json:"service_tiers"`
	ExperimentalSupportedTools  []any            `json:"experimental_supported_tools"`
	IncludeSkillsUsageInstr     bool             `json:"include_skills_usage_instructions"`
	Upgrade                     *any             `json:"upgrade"`
	AvailabilityNux             *any             `json:"availability_nux"`
	BaseInstructions            string           `json:"base_instructions"`
}

type truncationPolicy struct {
	Limit int    `json:"limit"`
	Mode  string `json:"mode"`
}

// CodexCatalog is the top-level catalog structure consumed by
// Codex's model_catalog_json config option.
type CodexCatalog struct {
	Models []CodexCatalogEntry `json:"models"`
}

// BuildCodexCatalog generates a Codex catalog from a dynamic model list.
func BuildCodexCatalog(models []string) CodexCatalog {
	sorted := make([]string, len(models))
	copy(sorted, models)
	sort.Strings(sorted)

	entries := make([]CodexCatalogEntry, 0, len(sorted))
	for i, id := range sorted {
		meta := LookupModelMeta(id)

		// Use dynamic AGY metadata when available.
		var maxOut *int64
		if dyn := LookupModelMetaDynamic(id); dyn != nil {
			if dyn.MaxOutputTokens > 0 {
				v := dyn.MaxOutputTokens
				maxOut = &v
			}
			if dyn.DisplayName != "" {
				meta.DisplayName = dyn.DisplayName
				meta.Description = dyn.DisplayName
			}
		}
		if maxOut == nil && meta.MaxOutputTokens > 0 {
			v := meta.MaxOutputTokens
			maxOut = &v
		}

		// Determine reasoning levels.
		// Models with -high/-medium/-low/-tiered suffix already encode
		// their thinking level. Show only one level so Codex doesn't
		// display a redundant reasoning effort selector.
		var reasoningLevels []ReasoningLevel
		var defaultLevel string
		if hasThinkingSuffix(id) {
			// Single level — no selector shown by Codex.
			reasoningLevels = []ReasoningLevel{
				{Description: "Thinking level is set by the model ID", Effort: "high"},
			}
			defaultLevel = "high"
		} else {
			reasoningLevels = defaultReasoningLevels
			defaultLevel = "medium"
		}

		entries = append(entries, CodexCatalogEntry{
			Slug:                        id,
			DisplayName:                 meta.DisplayName,
			Description:                 meta.Description,
			ContextWindow:               meta.ContextWindow,
			MaxContextWindow:            meta.ContextWindow,
			EffectiveContextWindowPct:   95,
			MaxOutputTokens:             maxOut,
			InputModalities:             meta.InputModalities,
			DefaultReasoningLevel:       defaultLevel,
			DefaultReasoningSummary:     "none",
			DefaultVerbosity:            "low",
			SupportVerbosity:            true,
			SupportedReasoningLevels:    reasoningLevels,
			SupportsParallelToolCalls:   true,
			SupportsSearchTool:          true,
			SupportedInAPI:              true,
			Visibility:                  "list",
			ShellType:                   "shell_command",
			ApplyPatchToolType:          "freeform",
			WebSearchToolType:           "text_and_image",
			TruncationPolicy:            truncationPolicy{Limit: 10000, Mode: "tokens"},
			Priority:                    1000 + i,
			UseResponsesLite:            false,
			SupportsImageDetailOriginal: true,
			AdditionalSpeedTiers:        []any{},
			ServiceTiers:                []any{},
			ExperimentalSupportedTools:  []any{},
			IncludeSkillsUsageInstr:     true,
			Upgrade:                     nil,
			AvailabilityNux:             nil,
			BaseInstructions:            codexBaseInstructions,
		})
	}
	return CodexCatalog{Models: entries}
}

// ClaudeModelConfig is the env-var config for Claude Code clients,
// mapping Claude's sonnet/opus/haiku slots to Hydra model IDs.
type ClaudeModelConfig struct {
	Env map[string]string `json:"env"`
}

// BuildClaudeConfig picks the best model for each Claude slot
// (sonnet, opus, haiku) from the dynamic model list.
func BuildClaudeConfig(models []string) ClaudeModelConfig {
	set := make(map[string]struct{}, len(models))
	for _, m := range models {
		set[strings.ToLower(m)] = struct{}{}
	}

	// Prefer thinking variants for opus/sonnet (AGY serves these natively).
	sonnet := pickByPrefix(set, "claude-sonnet")
	opus := pickByPrefix(set, "claude-opus")
	haiku := pickByPrefix(set, "claude-haiku")
	// Universal fallback: any gemini flash.
	flash := pickFirst(set,
		"gemini-3-flash", "gemini-3.7-flash", "gemini-3.6-flash",
		"gemini-3.5-flash", "gemini-2.5-flash",
	)
	if flash == "" {
		flash = pickByPrefix(set, "gemini-")
	}

	env := map[string]string{}
	if sonnet != "" {
		env["ANTHROPIC_DEFAULT_SONNET_MODEL"] = sonnet
		env["ANTHROPIC_DEFAULT_SONNET_MODEL_NAME"] = displayNameFor(sonnet)
	}
	if opus != "" {
		env["ANTHROPIC_DEFAULT_OPUS_MODEL"] = opus
		env["ANTHROPIC_DEFAULT_OPUS_MODEL_NAME"] = displayNameFor(opus)
	}
	if haiku != "" {
		env["ANTHROPIC_DEFAULT_HAIKU_MODEL"] = haiku
		env["ANTHROPIC_DEFAULT_HAIKU_MODEL_NAME"] = displayNameFor(haiku)
	}
	if flash != "" {
		env["ANTHROPIC_MODEL"] = flash
		env["ANTHROPIC_REASONING_MODEL"] = flash
		if _, ok := env["ANTHROPIC_DEFAULT_HAIKU_MODEL"]; !ok {
			env["ANTHROPIC_DEFAULT_HAIKU_MODEL"] = flash
			env["ANTHROPIC_DEFAULT_HAIKU_MODEL_NAME"] = displayNameFor(flash)
		}
	}
	return ClaudeModelConfig{Env: env}
}

// pickFirst returns the first candidate found in set.
func pickFirst(set map[string]struct{}, candidates ...string) string {
	for _, c := range candidates {
		if _, ok := set[strings.ToLower(c)]; ok {
			return c
		}
	}
	return ""
}

// pickByPrefix returns the first model in set that has the given prefix,
// sorted alphabetically for deterministic output.
func pickByPrefix(set map[string]struct{}, prefix string) string {
	prefix = strings.ToLower(prefix)
	var matches []string
	for m := range set {
		if strings.HasPrefix(m, prefix) {
			matches = append(matches, m)
		}
	}
	sort.Strings(matches)
	if len(matches) > 0 {
		return matches[0]
	}
	return ""
}

// displayNameFor returns a human-readable name for a model ID.
func displayNameFor(model string) string {
	name := deriveDisplayName(model)
	if strings.HasSuffix(strings.ToLower(model), "-thinking") {
		// deriveDisplayName already handles "(Thinking)" suffix.
		return name
	}
	return name
}
