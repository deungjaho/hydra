// Package registry provides a unified model registry that merges models
// from three sources:
//
//  1. AGY dynamic models (from fetchAvailableModels, stored per-account)
//  2. Provider models (from TOML [[models]] config with API key providers)
//  3. Static family metadata (fallback defaults inferred from model ID)
//
// The registry is the single source of truth for:
//   - Which models exist and are routable
//   - Which provider/AGY path each model routes to
//   - Model metadata (display name, context window, max output, capabilities)
//
// This replaces the scattered logic in mapper.go (DynamicModelList),
// apikey_provider.go (registeredModelIDs/registeredModelMeta),
// catalog.go (LookupModelMeta), and modelmeta.go (LookupModelMetaDynamic).
package registry

import (
	"sort"
	"strings"
	"sync"

	"github.com/deungjaho/hydra/internal/account"
	"github.com/deungjaho/hydra/internal/config"
	"github.com/deungjaho/hydra/internal/db"
	"github.com/deungjaho/hydra/internal/provider"
)

// Source indicates where a model entry originated.
type Source int

const (
	SourceAntigravity Source = iota // AGY dynamic model
	SourceProvider                  // TOML-configured API key provider
)

// Provider is a resolved API key provider.
type Provider struct {
	Name    string
	BaseURL string
	APIKey  string
}

// Entry is one model in the registry.
type Entry struct {
	ID               string
	DisplayName      string
	ContextWindow    int
	MaxOutputTokens  int64
	InputModalities  []string
	SupportsThinking bool
	SupportsImages   bool
	Source           Source
	// Providers is set when Source == SourceProvider. Multiple providers
	// can serve the same model for multi-key failover, ordered by priority
	// (index 0 = highest priority). For backward compatibility, Provider
	// returns the first element.
	Providers []*Provider
	// AGYMeta is the raw AGY metadata when Source == SourceAntigravity.
	AGYMeta *account.ModelMeta
}

// Provider returns the first (highest-priority) provider for this entry,
// or nil if there are none. This preserves backward compatibility with
// code that expects a single provider.
func (e *Entry) Provider() *Provider {
	if len(e.Providers) == 0 {
		return nil
	}
	return e.Providers[0]
}

// Registry is a thread-safe model registry.
type Registry struct {
	mu      sync.RWMutex
	entries map[string]*Entry // keyed by lowercase model ID
}

// New creates an empty registry.
func New() *Registry {
	return &Registry{entries: make(map[string]*Entry)}
}

// RebuildFromAGY replaces all AGY-sourced entries from the given accounts'
// available models and dynamic metadata.
func (r *Registry) RebuildFromAGY(accounts []*account.Account, metas map[string]account.ModelMeta) {
	r.mu.Lock()
	defer r.mu.Unlock()

	// Remove old AGY entries.
	for id, e := range r.entries {
		if e.Source == SourceAntigravity {
			delete(r.entries, id)
		}
	}

	// Add AGY models from all accounts.
	seen := make(map[string]struct{})
	for _, a := range accounts {
		for m := range a.AvailableModels() {
			if !isRoutableModel(m) {
				continue
			}
			if _, ok := seen[m]; ok {
				continue
			}
			seen[m] = struct{}{}

			entry := &Entry{
				ID:     m,
				Source: SourceAntigravity,
			}

			// Apply dynamic AGY metadata if available.
			lowerM := strings.ToLower(m)
			if meta, ok := metas[lowerM]; ok {
				metaCopy := meta
				entry.AGYMeta = &metaCopy
				if meta.DisplayName != "" {
					entry.DisplayName = meta.DisplayName
				}
				if meta.MaxOutputTokens > 0 {
					entry.MaxOutputTokens = meta.MaxOutputTokens
				}
				entry.SupportsThinking = meta.SupportsThinking
				entry.SupportsImages = meta.SupportsImages
			}

			// Fill in family defaults for anything not set.
			fam := familyDefaultFor(m)
			if entry.DisplayName == "" {
				entry.DisplayName = deriveDisplayName(m)
			}
			if entry.ContextWindow == 0 {
				entry.ContextWindow = fam.contextWindow
			}
			if entry.MaxOutputTokens == 0 {
				entry.MaxOutputTokens = fam.maxOutput
				if override, ok := modelMaxOutput[lowerM]; ok {
					entry.MaxOutputTokens = override
				}
			}
			if entry.InputModalities == nil {
				entry.InputModalities = fam.modalities
			}

			r.entries[lowerM] = entry
		}
	}
}

// RebuildFromConfig replaces all provider-sourced entries from TOML config.
func (r *Registry) RebuildFromConfig(cfg *config.AppConfig) {
	r.mu.Lock()
	defer r.mu.Unlock()

	// Remove old provider entries.
	for id, e := range r.entries {
		if e.Source == SourceProvider {
			delete(r.entries, id)
		}
	}

	// Build provider lookup.
	providers := make(map[string]*Provider)
	for i := range cfg.APIProviders {
		p := &cfg.APIProviders[i]
		providers[strings.ToLower(p.Name)] = &Provider{
			Name:    p.Name,
			BaseURL: strings.TrimRight(p.BaseURL, "/"),
			APIKey:  p.APIKey,
		}
	}

	// Add configured models.
	for _, m := range cfg.Models {
		lowerID := strings.ToLower(m.ID)
		prov := providers[strings.ToLower(m.Provider)]
		if prov == nil {
			continue
		}
		entry := &Entry{
			ID:              m.ID,
			DisplayName:     m.DisplayName,
			ContextWindow:   m.ContextWindow,
			MaxOutputTokens: m.MaxOutputTokens,
			InputModalities: []string{"text"},
			Source:          SourceProvider,
			Providers:       []*Provider{prov},
		}
		if entry.DisplayName == "" {
			entry.DisplayName = deriveDisplayName(m.ID)
		}
		if entry.ContextWindow == 0 {
			entry.ContextWindow = defaultContextWindow
		}
		r.entries[lowerID] = entry
	}
}

// RebuildFromDB replaces all provider-sourced entries from the database.
// This is the runtime-managed equivalent of RebuildFromConfig — providers
// and their model mappings are stored in the database and managed via
// CLI/TUI commands.
func (r *Registry) RebuildFromDB(d *db.Db) error {
	providers, err := provider.ListProviders(d)
	if err != nil {
		return err
	}
	mappings, err := provider.ListModelMappings(d)
	if err != nil {
		return err
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	// Remove old provider entries.
	for id, e := range r.entries {
		if e.Source == SourceProvider {
			delete(r.entries, id)
		}
	}

	// Build provider lookup, skipping disabled providers.
	provMap := make(map[int64]*Provider)
	for _, p := range providers {
		if p.Disabled() {
			continue
		}
		provMap[p.ID] = &Provider{
			Name:    p.Name,
			BaseURL: strings.TrimRight(p.BaseURL, "/"),
			APIKey:  p.APIKey,
		}
	}

	// Add configured model mappings. Multiple providers can map to the
	// same model_id for multi-key failover. Mappings are already sorted
	// by priority from ListModelMappings.
	modelProviders := make(map[string][]*Provider)
	modelMeta := make(map[string]*provider.ModelMapping)
	for _, m := range mappings {
		if m.Disabled {
			continue
		}
		prov := provMap[m.ProviderID]
		if prov == nil {
			continue
		}
		lowerID := strings.ToLower(m.ModelID)
		modelProviders[lowerID] = append(modelProviders[lowerID], prov)
		// Keep the first mapping's metadata (highest priority).
		if _, exists := modelMeta[lowerID]; !exists {
			modelMeta[lowerID] = m
		}
	}

	for lowerID, provs := range modelProviders {
		m := modelMeta[lowerID]
		entry := &Entry{
			ID:              m.ModelID,
			DisplayName:     m.DisplayName,
			ContextWindow:   m.ContextWindow,
			MaxOutputTokens: m.MaxOutputTokens,
			InputModalities: []string{"text"},
			Source:          SourceProvider,
			Providers:       provs,
		}
		if entry.DisplayName == "" {
			entry.DisplayName = deriveDisplayName(m.ModelID)
		}
		if entry.ContextWindow == 0 {
			entry.ContextWindow = defaultContextWindow
		}
		r.entries[lowerID] = entry
	}
	return nil
}

// Lookup returns the entry for a model ID, or nil if not found.
func (r *Registry) Lookup(model string) *Entry {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.entries[strings.ToLower(strings.TrimSpace(model))]
}

// IsProviderModel returns true if the model routes to an API key provider.
func (r *Registry) IsProviderModel(model string) bool {
	e := r.Lookup(model)
	return e != nil && e.Source == SourceProvider && len(e.Providers) > 0
}

// ProviderFor returns the first (highest-priority) provider for a model,
// or nil if it routes to AGY. For multi-key failover, use ProvidersFor.
func (r *Registry) ProviderFor(model string) *Provider {
	e := r.Lookup(model)
	if e == nil || e.Source != SourceProvider {
		return nil
	}
	return e.Provider()
}

// ProvidersFor returns all providers for a model in priority order, or
// nil if it routes to AGY. The first element is the highest-priority
// provider; subsequent elements are failover candidates.
func (r *Registry) ProvidersFor(model string) []*Provider {
	e := r.Lookup(model)
	if e == nil || e.Source != SourceProvider {
		return nil
	}
	return e.Providers
}

// AllModels returns all registered model IDs, sorted.
func (r *Registry) AllModels() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]string, 0, len(r.entries))
	for id, e := range r.entries {
		out = append(out, e.ID)
		_ = id
	}
	sort.Strings(out)
	return out
}

// AGYModels returns only AGY-sourced model IDs, sorted.
func (r *Registry) AGYModels() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var out []string
	for _, e := range r.entries {
		if e.Source == SourceAntigravity {
			out = append(out, e.ID)
		}
	}
	sort.Strings(out)
	return out
}

// ProviderModels returns only provider-sourced model IDs, sorted.
func (r *Registry) ProviderModels() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var out []string
	for _, e := range r.entries {
		if e.Source == SourceProvider {
			out = append(out, e.ID)
		}
	}
	sort.Strings(out)
	return out
}

// AllEntries returns all entries, sorted by ID.
func (r *Registry) AllEntries() []*Entry {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]*Entry, 0, len(r.entries))
	for _, e := range r.entries {
		out = append(out, e)
	}
	sort.Slice(out, func(i, j int) bool {
		return strings.ToLower(out[i].ID) < strings.ToLower(out[j].ID)
	})
	return out
}

// --- Family defaults (migrated from catalog.go) ---

type familyDefault struct {
	prefix        string
	contextWindow int
	maxOutput     int64
	modalities    []string
}

const defaultContextWindow = 128000

var familyDefaults = []familyDefault{
	{prefix: "claude-", contextWindow: 200000, maxOutput: 64000, modalities: []string{"text", "image"}},
	{prefix: "gemini-", contextWindow: 1000000, maxOutput: 65535, modalities: []string{"text", "image"}},
	{prefix: "gpt-oss-", contextWindow: 128000, maxOutput: 65536, modalities: []string{"text"}},
	{prefix: "deepseek-", contextWindow: 128000, maxOutput: 65536, modalities: []string{"text"}},
}

var modelMaxOutput = map[string]int64{
	"gemini-3-flash":             65536,
	"gemini-3-flash-agent":       65536,
	"gemini-3.5-flash-extra-low": 65536,
	"gemini-3.5-flash-low":       65536,
	"gemini-3.6-flash-high":      65536,
	"gemini-3.6-flash-low":       65536,
	"gemini-3.6-flash-medium":    65536,
	"gemini-3.6-flash-tiered":    65536,
	"gemini-2.5-flash":           65535,
	"gemini-2.5-flash-lite":      65535,
	"gemini-2.5-flash-thinking":  65535,
	"gemini-2.5-pro":             65535,
	"gemini-3.1-flash-lite":      65535,
	"gemini-3.1-pro-high":        65535,
	"gemini-3.1-pro-low":         65535,
	"gemini-pro-agent":           65535,
	"gpt-oss-120b-medium":        32768,
}

func familyDefaultFor(model string) familyDefault {
	m := strings.ToLower(model)
	for _, fam := range familyDefaults {
		if strings.HasPrefix(m, fam.prefix) {
			return fam
		}
	}
	return familyDefault{
		contextWindow: 1000000,
		maxOutput:     65536,
		modalities:    []string{"text", "image"},
	}
}

func isRoutableModel(name string) bool {
	m := strings.ToLower(name)
	if strings.HasPrefix(m, "chat_") || strings.HasPrefix(m, "tab_") {
		return false
	}
	return strings.HasPrefix(m, "gemini-") ||
		strings.HasPrefix(m, "claude-") ||
		strings.HasPrefix(m, "gpt-") ||
		strings.HasPrefix(m, "deepseek-")
}

func deriveDisplayName(model string) string {
	parts := strings.Split(model, "-")
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
