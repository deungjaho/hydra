package registry

import (
	"testing"

	"github.com/deungjaho/hydra/internal/account"
	"github.com/deungjaho/hydra/internal/config"
	"github.com/deungjaho/hydra/internal/db"
	"github.com/deungjaho/hydra/internal/provider"
)

func TestRegistry_AGYModels(t *testing.T) {
	r := New()
	accounts := []*account.Account{
		{Email: "a@test", QuotaJSON: `{"models":[{"name":"gemini-3-pro","percentage":50},{"name":"claude-sonnet-4-6","percentage":30}]}`},
		{Email: "b@test", QuotaJSON: `{"models":[{"name":"gemini-3-flash","percentage":80},{"name":"gemini-3-pro","percentage":20}]}`},
	}
	r.RebuildFromAGY(accounts, nil)

	models := r.AGYModels()
	if len(models) != 3 {
		t.Fatalf("AGY models = %d, want 3: %v", len(models), models)
	}
	// Should be sorted
	if models[0] != "claude-sonnet-4-6" {
		t.Errorf("models[0] = %s, want claude-sonnet-4-6", models[0])
	}
}

func TestRegistry_ProviderModels(t *testing.T) {
	r := New()
	cfg := &config.AppConfig{
		APIProviders: []config.APIProviderConfig{
			{Name: "deepseek", BaseURL: "https://api.deepseek.com", APIKey: "sk-xxx"},
		},
		Models: []config.ModelEntry{
			{ID: "deepseek-chat", Provider: "deepseek", DisplayName: "DeepSeek Chat", ContextWindow: 64000, MaxOutputTokens: 8192},
			{ID: "deepseek-reasoner", Provider: "deepseek"},
		},
	}
	r.RebuildFromConfig(cfg)

	models := r.ProviderModels()
	if len(models) != 2 {
		t.Fatalf("provider models = %d, want 2", len(models))
	}

	// Check routing
	prov := r.ProviderFor("deepseek-chat")
	if prov == nil {
		t.Fatal("provider should not be nil for deepseek-chat")
	}
	if prov.Name != "deepseek" {
		t.Errorf("provider name = %s, want deepseek", prov.Name)
	}
	if prov.BaseURL != "https://api.deepseek.com" {
		t.Errorf("base URL = %s", prov.BaseURL)
	}

	// Non-provider model should return nil
	if r.ProviderFor("gemini-3-pro") != nil {
		t.Error("gemini-3-pro should not route to a provider")
	}
}

func TestRegistry_IsProviderModel(t *testing.T) {
	r := New()
	cfg := &config.AppConfig{
		APIProviders: []config.APIProviderConfig{
			{Name: "deepseek", BaseURL: "https://api.deepseek.com", APIKey: "sk-xxx"},
		},
		Models: []config.ModelEntry{
			{ID: "deepseek-chat", Provider: "deepseek"},
		},
	}
	r.RebuildFromConfig(cfg)
	accounts := []*account.Account{
		{QuotaJSON: `{"models":[{"name":"gemini-3-pro","percentage":50}]}`},
	}
	r.RebuildFromAGY(accounts, nil)

	if !r.IsProviderModel("deepseek-chat") {
		t.Error("deepseek-chat should be a provider model")
	}
	if r.IsProviderModel("gemini-3-pro") {
		t.Error("gemini-3-pro should not be a provider model")
	}
}

func TestRegistry_LookupWithMetadata(t *testing.T) {
	r := New()
	metas := map[string]account.ModelMeta{
		"gemini-3-pro": {
			DisplayName:      "Gemini 3 Pro",
			MaxOutputTokens:  65535,
			SupportsThinking: true,
			SupportsImages:   true,
		},
	}
	accounts := []*account.Account{
		{QuotaJSON: `{"models":[{"name":"gemini-3-pro","percentage":50}]}`},
	}
	r.RebuildFromAGY(accounts, metas)

	e := r.Lookup("gemini-3-pro")
	if e == nil {
		t.Fatal("entry not found")
	}
	if e.DisplayName != "Gemini 3 Pro" {
		t.Errorf("display name = %s, want 'Gemini 3 Pro'", e.DisplayName)
	}
	if e.MaxOutputTokens != 65535 {
		t.Errorf("max output = %d, want 65535", e.MaxOutputTokens)
	}
	if !e.SupportsThinking {
		t.Error("should support thinking")
	}
	if !e.SupportsImages {
		t.Error("should support images")
	}
	if e.Source != SourceAntigravity {
		t.Errorf("source = %v, want SourceAntigravity", e.Source)
	}
}

func TestRegistry_FamilyDefaults(t *testing.T) {
	r := New()
	accounts := []*account.Account{
		{QuotaJSON: `{"models":[{"name":"claude-sonnet-4-6","percentage":50}]}`},
	}
	r.RebuildFromAGY(accounts, nil)

	e := r.Lookup("claude-sonnet-4-6")
	if e == nil {
		t.Fatal("entry not found")
	}
	if e.ContextWindow != 200000 {
		t.Errorf("context window = %d, want 200000", e.ContextWindow)
	}
}

func TestRegistry_AllModels(t *testing.T) {
	r := New()
	accounts := []*account.Account{
		{QuotaJSON: `{"models":[{"name":"gemini-3-pro","percentage":50}]}`},
	}
	r.RebuildFromAGY(accounts, nil)
	cfg := &config.AppConfig{
		APIProviders: []config.APIProviderConfig{
			{Name: "deepseek", BaseURL: "https://api.deepseek.com", APIKey: "sk-xxx"},
		},
		Models: []config.ModelEntry{
			{ID: "deepseek-chat", Provider: "deepseek"},
		},
	}
	r.RebuildFromConfig(cfg)

	all := r.AllModels()
	if len(all) != 2 {
		t.Fatalf("all models = %d, want 2: %v", len(all), all)
	}
}

func TestRegistry_NonRoutableExcluded(t *testing.T) {
	r := New()
	accounts := []*account.Account{
		{QuotaJSON: `{"models":[{"name":"chat_foo","percentage":50},{"name":"gemini-3-pro","percentage":50}]}`},
	}
	r.RebuildFromAGY(accounts, nil)

	if r.Lookup("chat_foo") != nil {
		t.Error("chat_foo should be excluded as non-routable")
	}
	if r.Lookup("gemini-3-pro") == nil {
		t.Error("gemini-3-pro should be included")
	}
}

func TestRegistry_ProviderWithMissingProvider(t *testing.T) {
	r := New()
	cfg := &config.AppConfig{
		Models: []config.ModelEntry{
			{ID: "orphan-model", Provider: "nonexistent"},
		},
	}
	r.RebuildFromConfig(cfg)

	if r.Lookup("orphan-model") != nil {
		t.Error("model with nonexistent provider should not be registered")
	}
}

func TestRegistry_RebuildFromDB(t *testing.T) {
	d, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer d.Close()

	pid, _ := provider.AddProvider(d, "deepseek", "https://api.deepseek.com", "sk-xxx")
	provider.AddModelMapping(d, "deepseek-chat", pid, "DeepSeek Chat", 64000, 8192, 0)
	provider.AddModelMapping(d, "deepseek-reasoner", pid, "", 0, 0, 0)

	r := New()
	if err := r.RebuildFromDB(d); err != nil {
		t.Fatalf("rebuild from DB: %v", err)
	}

	models := r.ProviderModels()
	if len(models) != 2 {
		t.Fatalf("provider models = %d, want 2", len(models))
	}

	prov := r.ProviderFor("deepseek-chat")
	if prov == nil {
		t.Fatal("provider should not be nil for deepseek-chat")
	}
	if prov.Name != "deepseek" {
		t.Errorf("provider name = %s, want deepseek", prov.Name)
	}
	if prov.BaseURL != "https://api.deepseek.com" {
		t.Errorf("base URL = %s", prov.BaseURL)
	}

	e := r.Lookup("deepseek-chat")
	if e == nil {
		t.Fatal("lookup failed for deepseek-chat")
	}
	if e.DisplayName != "DeepSeek Chat" {
		t.Errorf("display name = %s, want DeepSeek Chat", e.DisplayName)
	}
	if e.ContextWindow != 64000 {
		t.Errorf("context window = %d, want 64000", e.ContextWindow)
	}
}

func TestRegistry_RebuildFromDB_SkipsDisabled(t *testing.T) {
	d, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer d.Close()

	pid, _ := provider.AddProvider(d, "deepseek", "https://api.deepseek.com", "sk-xxx")
	provider.AddModelMapping(d, "deepseek-chat", pid, "", 0, 0, 0)
	provider.SetProviderDisabled(d, pid, true)

	r := New()
	if err := r.RebuildFromDB(d); err != nil {
		t.Fatalf("rebuild from DB: %v", err)
	}

	if r.Lookup("deepseek-chat") != nil {
		t.Error("disabled provider's models should not be registered")
	}
}

func TestRegistry_MultiKeyFailover(t *testing.T) {
	d, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer d.Close()

	// Two providers serving the same model with different priorities.
	pid1, _ := provider.AddProvider(d, "deepseek-primary", "https://api.deepseek.com", "sk-primary")
	pid2, _ := provider.AddProvider(d, "deepseek-backup", "https://api.backup.deepseek.com", "sk-backup")
	provider.AddModelMapping(d, "deepseek-chat", pid1, "DeepSeek Chat", 64000, 8192, 0)
	provider.AddModelMapping(d, "deepseek-chat", pid2, "DeepSeek Chat Backup", 64000, 8192, 1)

	r := New()
	if err := r.RebuildFromDB(d); err != nil {
		t.Fatalf("rebuild from DB: %v", err)
	}

	// ProvidersFor should return both providers in priority order.
	provs := r.ProvidersFor("deepseek-chat")
	if len(provs) != 2 {
		t.Fatalf("providers = %d, want 2", len(provs))
	}
	if provs[0].Name != "deepseek-primary" {
		t.Errorf("first provider = %s, want deepseek-primary", provs[0].Name)
	}
	if provs[1].Name != "deepseek-backup" {
		t.Errorf("second provider = %s, want deepseek-backup", provs[1].Name)
	}
	if provs[0].APIKey != "sk-primary" {
		t.Errorf("first API key = %s, want sk-primary", provs[0].APIKey)
	}

	// ProviderFor (backward compat) should return the first one.
	prov := r.ProviderFor("deepseek-chat")
	if prov == nil || prov.Name != "deepseek-primary" {
		t.Errorf("ProviderFor = %v, want deepseek-primary", prov)
	}

	// Entry should have both providers.
	e := r.Lookup("deepseek-chat")
	if e == nil {
		t.Fatal("lookup failed")
	}
	if len(e.Providers) != 2 {
		t.Errorf("entry providers = %d, want 2", len(e.Providers))
	}
}
