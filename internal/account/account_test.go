package account

import (
	"sort"
	"testing"
)

func TestQuotaModels(t *testing.T) {
	a := &Account{
		QuotaJSON:    `{"models":[{"name":"gemini-3-pro","percentage":80,"reset_time":"2026-01-01"},{"name":"claude-sonnet-4-6","percentage":20,"reset_time":""}]}`,
		HasQuotaJSON: true,
	}
	models := a.QuotaModels()
	if len(models) != 2 {
		t.Fatalf("len = %d, want 2", len(models))
	}
	if models[0].Name != "gemini-3-pro" {
		t.Errorf("models[0].Name = %q", models[0].Name)
	}
	if models[0].Percentage != 80 {
		t.Errorf("models[0].Percentage = %d, want 80", models[0].Percentage)
	}
}

func TestQuotaModels_Empty(t *testing.T) {
	a := &Account{QuotaJSON: ""}
	if models := a.QuotaModels(); models != nil {
		t.Errorf("expected nil for empty QuotaJSON, got %v", models)
	}
}

func TestQuotaModels_InvalidJSON(t *testing.T) {
	a := &Account{QuotaJSON: "{invalid"}
	if models := a.QuotaModels(); models != nil {
		t.Errorf("expected nil for invalid JSON, got %v", models)
	}
}

func TestQuotaModels_FiltersEmptyNames(t *testing.T) {
	a := &Account{
		QuotaJSON: `{"models":[{"name":"","percentage":50},{"name":"gemini-3-pro","percentage":80}]}`,
	}
	models := a.QuotaModels()
	if len(models) != 1 {
		t.Fatalf("len = %d, want 1 (empty name filtered)", len(models))
	}
	if models[0].Name != "gemini-3-pro" {
		t.Errorf("models[0].Name = %q", models[0].Name)
	}
}

func TestAvailableModels(t *testing.T) {
	a := &Account{
		QuotaJSON: `{"models":[{"name":"Gemini-3-Pro","percentage":80},{"name":"Claude-Sonnet-4-6","percentage":20}]}`,
	}
	avail := a.AvailableModels()
	if len(avail) != 2 {
		t.Fatalf("len = %d, want 2", len(avail))
	}
	if _, ok := avail["gemini-3-pro"]; !ok {
		t.Error("should have lowercase gemini-3-pro")
	}
	if _, ok := avail["claude-sonnet-4-6"]; !ok {
		t.Error("should have lowercase claude-sonnet-4-6")
	}
	// Original case should not be present.
	if _, ok := avail["Gemini-3-Pro"]; ok {
		t.Error("should not have original case key")
	}
}

func TestAvailableModels_Empty(t *testing.T) {
	a := &Account{QuotaJSON: ""}
	avail := a.AvailableModels()
	if len(avail) != 0 {
		t.Errorf("expected empty, got %v", avail)
	}
}

func TestResetIn(t *testing.T) {
	tests := []struct {
		name string
		secs int64
		want string
	}{
		{"now", 0, "now"},
		{"negative", -100, "now"},
		{"30 seconds", 30, "0m"},
		{"5 minutes", 300, "5m"},
		{"1 hour", 3600, "1h 0m"},
		{"1h 30m", 5400, "1h 30m"},
		{"1 day", 86400, "1d 0h"},
		{"1d 12h", 86400 + 43200, "1d 12h"},
		{"2d 3h 15m", 2*86400 + 3*3600 + 15*60, "2d 3h"},
	}
	for _, tt := range tests {
		w := QuotaWindow{SecsUntilReset: tt.secs}
		got := w.ResetIn()
		if got != tt.want {
			t.Errorf("ResetIn(%s, %ds) = %q, want %q",
				tt.name, tt.secs, got, tt.want)
		}
	}
}

func TestFormatOne(t *testing.T) {
	if formatOne(5, "m") != "5m" {
		t.Error("formatOne(5, m) should be 5m")
	}
	if formatOne(0, "h") != "0h" {
		t.Error("formatOne(0, h) should be 0h")
	}
}

func TestFormatTwo(t *testing.T) {
	got := formatTwo(2, "d", 5, "h")
	want := "2d 5h"
	if got != want {
		t.Errorf("formatTwo = %q, want %q", got, want)
	}
}

func TestQuotaWindowsParsed_Empty(t *testing.T) {
	a := &Account{QuotaSummary: ""}
	w := a.QuotaWindowsParsed()
	if w.Gemini5h != nil || w.GeminiWeekly != nil {
		t.Error("expected nil windows for empty summary")
	}
}

func TestQuotaWindowsParsed_InvalidJSON(t *testing.T) {
	a := &Account{QuotaSummary: "{invalid"}
	w := a.QuotaWindowsParsed()
	if w.Gemini5h != nil {
		t.Error("expected nil for invalid JSON")
	}
}

// Helper to sort string slices for deterministic comparison.
func sortSlice(s []string) []string {
	out := append([]string{}, s...)
	sort.Strings(out)
	return out
}
