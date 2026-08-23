package proxy

import (
	"bytes"
	"net/http"
	"strings"

	"github.com/deungjaho/hydra/internal/account"
	"github.com/deungjaho/hydra/internal/provider"
	"github.com/deungjaho/hydra/internal/registry"
)

func (s *ProxyServer) handleHealthz(w http.ResponseWriter, r *http.Request) {
	_, _ = w.Write([]byte("ok"))
}

func (s *ProxyServer) handleListModels(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.checkAuth(r); !ok {
		writeUnauthorized(w)
		return
	}
	// Build a set of disabled provider model IDs from the DB for
	// real-time filtering (TUI may have toggled them since last rebuild).
	disabledModels := make(map[string]bool)
	if mappings, err := provider.ListDisabledModelIDs(s.State.DB); err == nil {
		for _, id := range mappings {
			disabledModels[strings.ToLower(id)] = true
		}
	}
	var models []string
	if s.Registry != nil {
		all := s.Registry.AllModels()
		for _, id := range all {
			if disabledModels[strings.ToLower(id)] {
				continue
			}
			models = append(models, id)
		}
	} else {
		// Fallback: merge AGY + provider models the old way.
		accounts, _ := account.ListAccounts(s.State.DB)
		models = DynamicModelList(accounts)
		models = append(models, s.registeredModelIDs()...)
	}
	data := make([]any, 0, len(models))
	for _, id := range models {
		ownedBy := "antigravity"
		entry := map[string]any{
			"id":       id,
			"object":   "model",
			"owned_by": ownedBy,
		}
		if s.Registry != nil {
			e := s.Registry.Lookup(id)
			if e != nil {
				if e.Source == registry.SourceProvider {
					entry["owned_by"] = "api_provider"
				}
				// Capability metadata.
				if e.DisplayName != "" {
					entry["display_name"] = e.DisplayName
				}
				if e.ContextWindow > 0 {
					entry["context_window"] = e.ContextWindow
				}
				if e.MaxOutputTokens > 0 {
					entry["max_output_tokens"] = e.MaxOutputTokens
				}
				if len(e.InputModalities) > 0 {
					entry["input_modalities"] = e.InputModalities
				}
				supports := map[string]bool{
					"thinking": e.SupportsThinking,
					"images":   e.SupportsImages,
				}
				entry["supports"] = supports
			}
		}
		data = append(data, entry)
	}
	writeJSON(w, http.StatusOK, map[string]any{"object": "list", "data": data})
}

func (s *ProxyServer) handleCodexCatalog(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.checkAuth(r); !ok {
		writeUnauthorized(w)
		return
	}
	// Build disabled set for real-time filtering.
	disabledModels := make(map[string]bool)
	if ids, err := provider.ListDisabledModelIDs(s.State.DB); err == nil {
		for _, id := range ids {
			disabledModels[strings.ToLower(id)] = true
		}
	}
	var agyModels []string
	var providerModels []string
	if s.Registry != nil {
		agyModels = s.Registry.AGYModels()
		for _, id := range s.Registry.ProviderModels() {
			if !disabledModels[strings.ToLower(id)] {
				providerModels = append(providerModels, id)
			}
		}
	} else {
		accounts, _ := account.ListAccounts(s.State.DB)
		agyModels = DynamicModelList(accounts)
		providerModels = s.registeredModelIDs()
	}
	catalog := BuildCodexCatalog(agyModels)
	// Append registered API key provider models.
	for i, id := range providerModels {
		display, ctx, maxOut, _ := s.registeredModelMeta(id)
		var maxOutPtr *int64
		if maxOut > 0 {
			v := maxOut
			maxOutPtr = &v
		}
		if ctx == 0 {
			ctx = defaultContextWindow
		}
		catalog.Models = append(catalog.Models, CodexCatalogEntry{
			Slug:                        id,
			DisplayName:                 display,
			Description:                 display,
			ContextWindow:               ctx,
			MaxContextWindow:            ctx,
			EffectiveContextWindowPct:   95,
			MaxOutputTokens:             maxOutPtr,
			InputModalities:             []string{"text"},
			DefaultReasoningLevel:       "medium",
			DefaultReasoningSummary:     "none",
			DefaultVerbosity:            "low",
			SupportVerbosity:            true,
			SupportedReasoningLevels:    defaultReasoningLevels,
			SupportsParallelToolCalls:   true,
			SupportsSearchTool:          false,
			SupportedInAPI:              true,
			Visibility:                  "list",
			ShellType:                   "shell_command",
			ApplyPatchToolType:          "freeform",
			WebSearchToolType:           "text_and_image",
			TruncationPolicy:            truncationPolicy{Limit: 10000, Mode: "tokens"},
			Priority:                    2000 + i,
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
	writeJSON(w, http.StatusOK, catalog)
}

func (s *ProxyServer) handleClaudeConfig(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.checkAuth(r); !ok {
		writeUnauthorized(w)
		return
	}
	disabledModels := make(map[string]bool)
	if ids, err := provider.ListDisabledModelIDs(s.State.DB); err == nil {
		for _, id := range ids {
			disabledModels[strings.ToLower(id)] = true
		}
	}
	var models []string
	if s.Registry != nil {
		for _, id := range s.Registry.AllModels() {
			if !disabledModels[strings.ToLower(id)] {
				models = append(models, id)
			}
		}
	} else {
		accounts, _ := account.ListAccounts(s.State.DB)
		models = DynamicModelList(accounts)
		models = append(models, s.registeredModelIDs()...)
	}
	writeJSON(w, http.StatusOK, BuildClaudeConfig(models))
}

// isRetryableStatus returns true for status codes that warrant a
// failover to another account (quota exhausted, capacity unavailable).
// 401 is NOT retryable — it's a token refresh issue, not an account
// exhaustion issue, and retrying on another account doesn't help.
func isRetryableStatus(code int) bool {
	return code == 429 || code == 503
}

// isLocationError returns true if the response body indicates a
// "User location is not supported" error from AGY. These are intermittent
// and retrying on a different account often succeeds.
func isLocationError(body []byte) bool {
	return bytes.Contains(body, []byte("User location is not supported"))
}
