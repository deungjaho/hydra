package provider

import (
	"testing"

	"github.com/deungjaho/hydra/internal/db"
)

func openTestDB(t *testing.T) *db.Db {
	t.Helper()
	d, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { d.Close() })
	return d
}

func TestAddAndListProviders(t *testing.T) {
	d := openTestDB(t)
	id1, err := AddProvider(d, "deepseek", "https://api.deepseek.com", "sk-abc123def456")
	if err != nil {
		t.Fatalf("add provider: %v", err)
	}
	if id1 == 0 {
		t.Error("id should not be 0")
	}
	id2, _ := AddProvider(d, "zhipu", "https://open.bigmodel.cn", "sk-xyz789abc012")
	if id2 == 0 {
		t.Error("id2 should not be 0")
	}

	providers, err := ListProviders(d)
	if err != nil {
		t.Fatalf("list providers: %v", err)
	}
	if len(providers) != 2 {
		t.Fatalf("expected 2 providers, got %d", len(providers))
	}
	if providers[0].Name != "deepseek" {
		t.Errorf("provider[0].Name = %s, want deepseek", providers[0].Name)
	}
	if providers[1].Name != "zhipu" {
		t.Errorf("provider[1].Name = %s, want zhipu", providers[1].Name)
	}
}

func TestGetProviderByName(t *testing.T) {
	d := openTestDB(t)
	AddProvider(d, "deepseek", "https://api.deepseek.com", "sk-xxx")

	p, err := GetProviderByName(d, "deepseek")
	if err != nil {
		t.Fatalf("get by name: %v", err)
	}
	if p.Name != "deepseek" {
		t.Errorf("name = %s", p.Name)
	}
	if p.BaseURL != "https://api.deepseek.com" {
		t.Errorf("base_url = %s", p.BaseURL)
	}
}

func TestRemoveProvider(t *testing.T) {
	d := openTestDB(t)
	id, _ := AddProvider(d, "deepseek", "https://api.deepseek.com", "sk-xxx")
	AddModelMapping(d, "deepseek-chat", id, "", 0, 0, 0)

	if err := RemoveProvider(d, id); err != nil {
		t.Fatalf("remove: %v", err)
	}
	providers, _ := ListProviders(d)
	if len(providers) != 0 {
		t.Errorf("expected 0 providers, got %d", len(providers))
	}
	// Model mappings should be cascade-deleted.
	mappings, _ := ListModelMappings(d)
	if len(mappings) != 0 {
		t.Errorf("expected 0 mappings after cascade delete, got %d", len(mappings))
	}
}

func TestUpdateProvider(t *testing.T) {
	d := openTestDB(t)
	id, _ := AddProvider(d, "deepseek", "https://api.deepseek.com", "sk-old")

	if err := UpdateProvider(d, id, "https://api.new.com", "sk-new"); err != nil {
		t.Fatalf("update: %v", err)
	}
	p, _ := GetProvider(d, id)
	if p.BaseURL != "https://api.new.com" {
		t.Errorf("base_url = %s", p.BaseURL)
	}
	if p.APIKey != "sk-new" {
		t.Errorf("api_key = %s", p.APIKey)
	}
}

func TestSetProviderDisabled(t *testing.T) {
	d := openTestDB(t)
	id, _ := AddProvider(d, "deepseek", "https://api.deepseek.com", "sk-xxx")

	SetProviderDisabled(d, id, true)
	p, _ := GetProvider(d, id)
	if !p.OperatorDisabled {
		t.Error("should be disabled")
	}

	SetProviderDisabled(d, id, false)
	p, _ = GetProvider(d, id)
	if p.OperatorDisabled {
		t.Error("should be enabled")
	}
}

func TestMarkProviderHealth(t *testing.T) {
	d := openTestDB(t)
	id, _ := AddProvider(d, "deepseek", "https://api.deepseek.com", "sk-xxx")

	// Mark unhealthy
	MarkProviderHealthDisabled(d, id, "connection refused")
	p, _ := GetProvider(d, id)
	if !p.HealthDisabled {
		t.Error("should be health-disabled")
	}
	if p.LastError != "connection refused" {
		t.Errorf("last_error = %s", p.LastError)
	}

	// Mark recovered
	MarkProviderHealthRecovered(d, id)
	p, _ = GetProvider(d, id)
	if p.HealthDisabled {
		t.Error("should not be health-disabled")
	}
	if p.LastError != "" {
		t.Errorf("last_error should be cleared, got %s", p.LastError)
	}
}

func TestAddAndListModelMappings(t *testing.T) {
	d := openTestDB(t)
	pid, _ := AddProvider(d, "deepseek", "https://api.deepseek.com", "sk-xxx")
	AddModelMapping(d, "deepseek-chat", pid, "DeepSeek Chat", 64000, 8192, 0)
	AddModelMapping(d, "deepseek-reasoner", pid, "DeepSeek Reasoner", 64000, 8192, 0)

	mappings, err := ListModelMappings(d)
	if err != nil {
		t.Fatalf("list mappings: %v", err)
	}
	if len(mappings) != 2 {
		t.Fatalf("expected 2 mappings, got %d", len(mappings))
	}
	if mappings[0].ModelID != "deepseek-chat" {
		t.Errorf("mapping[0].ModelID = %s", mappings[0].ModelID)
	}
	if mappings[0].ProviderID != pid {
		t.Errorf("mapping[0].ProviderID = %d, want %d", mappings[0].ProviderID, pid)
	}
	if mappings[0].DisplayName != "DeepSeek Chat" {
		t.Errorf("mapping[0].DisplayName = %s", mappings[0].DisplayName)
	}
}

func TestRemoveModelMapping(t *testing.T) {
	d := openTestDB(t)
	pid, _ := AddProvider(d, "deepseek", "https://api.deepseek.com", "sk-xxx")
	AddModelMapping(d, "deepseek-chat", pid, "", 0, 0, 0)

	if err := RemoveModelMapping(d, "deepseek-chat"); err != nil {
		t.Fatalf("remove mapping: %v", err)
	}
	mappings, _ := ListModelMappings(d)
	if len(mappings) != 0 {
		t.Errorf("expected 0 mappings, got %d", len(mappings))
	}
}

func TestMaskAPIKey(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"sk-abcdefghij", "sk-abc...ghij"},
		{"short", "*****"},
		{"sk-verylongkey1234567890", "sk-ver...7890"},
	}
	for _, tt := range tests {
		got := MaskAPIKey(tt.input)
		if got != tt.want {
			t.Errorf("MaskAPIKey(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestProviderDisabled(t *testing.T) {
	p := &Provider{}
	if p.Disabled() {
		t.Error("new provider should not be disabled")
	}
	p.OperatorDisabled = true
	if !p.Disabled() {
		t.Error("operator-disabled should be disabled")
	}
	p.OperatorDisabled = false
	p.HealthDisabled = true
	if !p.Disabled() {
		t.Error("health-disabled should be disabled")
	}
}
