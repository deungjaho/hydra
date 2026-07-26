package account

import (
	"sort"
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
