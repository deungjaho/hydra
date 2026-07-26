package account

import (
	"sort"
	"strings"
	"testing"
)

func sortedStrings(s []string) []string {
	out := append([]string{}, s...)
	sort.Strings(out)
	return out
}

func TestComputeProtectedModels_AddsBelowThreshold(t *testing.T) {
	current := []string{}
	percentages := map[string]int32{
		"gemini-3-pro":          2,
		"claude-sonnet-4-6":     50,
	}
	got := ComputeProtectedModels(current, percentages, nil, 5)
	// gemini-3-pro (2% < 5%) should be protected
	// claude-sonnet-4-6 (50% >= 5%) should not
	want := []string{"gemini-3-pro"}
	if !equalStrings(sortedStrings(got), sortedStrings(want)) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestComputeProtectedModels_RemovesAboveThreshold(t *testing.T) {
	current := []string{"gemini-3-pro", "claude-sonnet-4-6"}
	percentages := map[string]int32{
		"gemini-3-pro":      80,
		"claude-sonnet-4-6": 2,
	}
	got := ComputeProtectedModels(current, percentages, nil, 5)
	// gemini-3-pro (80% >= 5%) should be removed from protected
	// claude-sonnet-4-6 (2% < 5%) should stay protected
	want := []string{"claude-sonnet-4-6"}
	if !equalStrings(sortedStrings(got), sortedStrings(want)) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestComputeProtectedModels_KeepsProtectedBelowThreshold(t *testing.T) {
	current := []string{"gemini-3-pro"}
	percentages := map[string]int32{
		"gemini-3-pro": 3,
	}
	got := ComputeProtectedModels(current, percentages, nil, 5)
	// Already protected and still below threshold → stays
	want := []string{"gemini-3-pro"}
	if !equalStrings(sortedStrings(got), sortedStrings(want)) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestComputeProtectedModels_KeepsUnprotectedAboveThreshold(t *testing.T) {
	current := []string{}
	percentages := map[string]int32{
		"gemini-3-pro": 90,
	}
	got := ComputeProtectedModels(current, percentages, nil, 5)
	// Not protected and above threshold → not added
	want := []string{}
	if !equalStrings(sortedStrings(got), sortedStrings(want)) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestComputeProtectedModels_MonitoredFilter(t *testing.T) {
	current := []string{}
	percentages := map[string]int32{
		"gemini-3-pro":       2,
		"claude-sonnet-4-6":  2,
		"claude-opus-4-6":    2,
	}
	monitored := []string{"gemini-3-pro"}
	got := ComputeProtectedModels(current, percentages, monitored, 5)
	// Only monitored model is checked; others ignored
	want := []string{"gemini-3-pro"}
	if !equalStrings(sortedStrings(got), sortedStrings(want)) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestComputeProtectedModels_MonitoredNotInPercentages(t *testing.T) {
	current := []string{}
	percentages := map[string]int32{
		"gemini-3-pro": 2,
	}
	monitored := []string{"gemini-3-pro", "claude-sonnet-4-6"}
	got := ComputeProtectedModels(current, percentages, monitored, 5)
	// claude-sonnet-4-6 not in percentages → pct defaults to 100
	// 100 >= 5 → not protected
	// gemini-3-pro: 2 < 5 → protected
	want := []string{"gemini-3-pro"}
	if !equalStrings(sortedStrings(got), sortedStrings(want)) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestComputeProtectedModels_EmptyPercentages(t *testing.T) {
	got := ComputeProtectedModels(nil, nil, nil, 5)
	if len(got) != 0 {
		t.Errorf("got %v, want empty", got)
	}
}

func TestComputeProtectedModels_MultipleMixed(t *testing.T) {
	current := []string{"model-a", "model-c"}
	percentages := map[string]int32{
		"model-a": 90, // protected but above → remove
		"model-b": 2,  // not protected, below → add
		"model-c": 3,  // protected, below → keep
		"model-d": 80, // not protected, above → skip
	}
	got := ComputeProtectedModels(current, percentages, nil, 5)
	want := []string{"model-b", "model-c"}
	if !equalStrings(sortedStrings(got), sortedStrings(want)) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestComputeProtectedModels_DoesNotMutateCurrent(t *testing.T) {
	current := []string{"model-a"}
	percentages := map[string]int32{
		"model-a": 90,
	}
	_ = ComputeProtectedModels(current, percentages, nil, 5)
	if len(current) != 1 || current[0] != "model-a" {
		t.Errorf("current was mutated: %v", current)
	}
}

func TestComputeProtectedModels_ThresholdBoundary(t *testing.T) {
	// At exactly threshold: pct >= threshold → not protected
	current := []string{}
	percentages := map[string]int32{
		"model-a": 5,
	}
	got := ComputeProtectedModels(current, percentages, nil, 5)
	want := []string{}
	if !equalStrings(sortedStrings(got), sortedStrings(want)) {
		t.Errorf("got %v, want %v (pct==threshold is not below)", got, want)
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// --- parseModelPercentages tests ---

func TestParseModelPercentages_WithFraction(t *testing.T) {
	body := `{"models":{"gemini-3-pro":{"remainingFraction":0.8,"resetTime":"2026-01-01"}}}`
	m := parseModelPercentages(body)
	if len(m) != 1 {
		t.Fatalf("len = %d, want 1", len(m))
	}
	if m["gemini-3-pro"] != 80 {
		t.Errorf("gemini-3-pro = %d, want 80", m["gemini-3-pro"])
	}
}

func TestParseModelPercentages_WithResetNoFraction(t *testing.T) {
	body := `{"models":{"gemini-3-pro":{"resetTime":"2026-01-01"}}}`
	m := parseModelPercentages(body)
	if m["gemini-3-pro"] != 0 {
		t.Errorf("gemini-3-pro = %d, want 0", m["gemini-3-pro"])
	}
}

func TestParseModelPercentages_NoResetNoFraction(t *testing.T) {
	body := `{"models":{"gemini-3-pro":{}}}`
	m := parseModelPercentages(body)
	if len(m) != 0 {
		t.Errorf("expected empty, got %v", m)
	}
}

func TestParseModelPercentages_LowercasesNames(t *testing.T) {
	body := `{"models":{"Gemini-3-Pro":{"remainingFraction":0.5}}}`
	m := parseModelPercentages(body)
	if _, ok := m["gemini-3-pro"]; !ok {
		t.Errorf("expected lowercase key, got %v", m)
	}
}

func TestParseModelPercentages_ClampsTo100(t *testing.T) {
	body := `{"models":{"m":{"remainingFraction":1.5}}}`
	m := parseModelPercentages(body)
	if m["m"] != 100 {
		t.Errorf("m = %d, want 100 (clamped)", m["m"])
	}
}

func TestParseModelPercentages_ClampsTo0(t *testing.T) {
	body := `{"models":{"m":{"remainingFraction":-0.5}}}`
	m := parseModelPercentages(body)
	if m["m"] != 0 {
		t.Errorf("m = %d, want 0 (clamped)", m["m"])
	}
}

func TestParseModelPercentages_Rounding(t *testing.T) {
	body := `{"models":{"m":{"remainingFraction":0.845}}}`
	m := parseModelPercentages(body)
	if m["m"] != 85 {
		t.Errorf("m = %d, want 85 (rounded)", m["m"])
	}
}

func TestParseModelPercentages_InvalidJSON(t *testing.T) {
	m := parseModelPercentages("{invalid")
	if len(m) != 0 {
		t.Errorf("expected empty for invalid JSON, got %v", m)
	}
}

func TestParseModelPercentages_Empty(t *testing.T) {
	m := parseModelPercentages(`{"models":{}}`)
	if len(m) != 0 {
		t.Errorf("expected empty, got %v", m)
	}
}

func TestParseModelPercentages_MultipleModels(t *testing.T) {
	body := `{"models":{
		"gemini-3-pro":{"remainingFraction":0.9},
		"claude-sonnet-4-6":{"remainingFraction":0.1}
	}}`
	m := parseModelPercentages(body)
	if len(m) != 2 {
		t.Fatalf("len = %d, want 2", len(m))
	}
	if m["gemini-3-pro"] != 90 {
		t.Errorf("gemini-3-pro = %d, want 90", m["gemini-3-pro"])
	}
	if m["claude-sonnet-4-6"] != 10 {
		t.Errorf("claude-sonnet-4-6 = %d, want 10", m["claude-sonnet-4-6"])
	}
}

// --- buildModelsBlob tests ---

func TestBuildModelsBlob_WithFraction(t *testing.T) {
	body := `{"models":{"gemini-3-pro":{"remainingFraction":0.8,"resetTime":"2026-01-01"}}}`
	blob, maxPct, hasMax := buildModelsBlob(body)
	if !hasMax {
		t.Error("hasMax should be true")
	}
	if maxPct != 80 {
		t.Errorf("maxPct = %d, want 80", maxPct)
	}
	if !strings.Contains(blob, "gemini-3-pro") {
		t.Errorf("blob should contain model name: %s", blob)
	}
	if !strings.Contains(blob, "80") {
		t.Errorf("blob should contain percentage: %s", blob)
	}
}

func TestBuildModelsBlob_NoResetNoFraction(t *testing.T) {
	body := `{"models":{"m":{}}}`
	_, maxPct, hasMax := buildModelsBlob(body)
	if !hasMax {
		t.Error("hasMax should be true")
	}
	if maxPct != 100 {
		t.Errorf("maxPct = %d, want 100", maxPct)
	}
}

func TestBuildModelsBlob_InvalidJSON(t *testing.T) {
	blob, maxPct, hasMax := buildModelsBlob("{invalid")
	if hasMax {
		t.Error("hasMax should be false for invalid JSON")
	}
	if maxPct != 0 {
		t.Errorf("maxPct = %d, want 0", maxPct)
	}
	if !strings.Contains(blob, "models") {
		t.Errorf("blob should have empty models: %s", blob)
	}
}

func TestBuildModelsBlob_MultipleMaxPercentage(t *testing.T) {
	body := `{"models":{
		"a":{"remainingFraction":0.3},
		"b":{"remainingFraction":0.9},
		"c":{"remainingFraction":0.5}
	}}`
	_, maxPct, _ := buildModelsBlob(body)
	if maxPct != 90 {
		t.Errorf("maxPct = %d, want 90", maxPct)
	}
}

// --- parseSummary tests ---

func TestParseSummary_GeminiFamily(t *testing.T) {
	body := `{"groups":[{"buckets":[
		{"bucketId":"gemini-5h","window":"5h","resetTime":"2026-01-01","remainingFraction":0.8,"disabled":false}
	]}]}`
	_, windows := parseSummary(body)
	if len(windows) != 1 {
		t.Fatalf("len = %d, want 1", len(windows))
	}
	if windows[0].Family != "gemini" {
		t.Errorf("family = %q, want gemini", windows[0].Family)
	}
	if windows[0].Percentage != 80 {
		t.Errorf("percentage = %d, want 80", windows[0].Percentage)
	}
}

func TestParseSummary_ThirdPartyFamily(t *testing.T) {
	body := `{"groups":[{"buckets":[
		{"bucketId":"claude-5h","window":"5h","remainingFraction":0.5}
	]}]}`
	_, windows := parseSummary(body)
	if len(windows) != 1 {
		t.Fatalf("len = %d, want 1", len(windows))
	}
	if windows[0].Family != "third_party" {
		t.Errorf("family = %q, want third_party", windows[0].Family)
	}
}

func TestParseSummary_ClampsPercentage(t *testing.T) {
	body := `{"groups":[{"buckets":[
		{"bucketId":"gemini-5h","window":"5h","remainingFraction":1.5}
	]}]}`
	_, windows := parseSummary(body)
	if windows[0].Percentage != 100 {
		t.Errorf("percentage = %d, want 100 (clamped)",
			windows[0].Percentage)
	}
}

func TestParseSummary_InvalidJSON(t *testing.T) {
	blob, windows := parseSummary("{invalid")
	if windows != nil {
		t.Errorf("expected nil windows, got %v", windows)
	}
	if !strings.Contains(blob, "windows") {
		t.Errorf("blob should have empty windows: %s", blob)
	}
}

func TestParseSummary_MultipleBuckets(t *testing.T) {
	body := `{"groups":[{"buckets":[
		{"bucketId":"gemini-5h","window":"5h","remainingFraction":0.8},
		{"bucketId":"gemini-weekly","window":"weekly","remainingFraction":0.6},
		{"bucketId":"other-5h","window":"5h","remainingFraction":0.4}
	]}]}`
	_, windows := parseSummary(body)
	if len(windows) != 3 {
		t.Fatalf("len = %d, want 3", len(windows))
	}
}

func TestParseSummary_DisabledFlag(t *testing.T) {
	body := `{"groups":[{"buckets":[
		{"bucketId":"gemini-5h","window":"5h","remainingFraction":0.8,"disabled":true}
	]}]}`
	_, windows := parseSummary(body)
	if !windows[0].Disabled {
		t.Error("disabled should be true")
	}
}
