package proxy

import (
	"testing"
)

func TestUint64Or(t *testing.T) {
	tests := []struct {
		name string
		m    map[string]any
		want uint64
	}{
		{"float64", map[string]any{"n": float64(42)}, 42},
		{"int64", map[string]any{"n": int64(42)}, 42},
		{"int", map[string]any{"n": 42}, 42},
		{"string", map[string]any{"n": "42"}, 99},
		{"missing", map[string]any{}, 99},
		{"nil", nil, 99},
	}
	for _, tt := range tests {
		got := uint64Or(tt.m, "n", 99)
		if got != tt.want {
			t.Errorf("uint64Or(%s) = %d, want %d", tt.name, got, tt.want)
		}
	}
}

func TestItoaUint64(t *testing.T) {
	if itoaUint64(0) != "0" {
		t.Error("itoaUint64(0) should be 0")
	}
	if itoaUint64(42) != "42" {
		t.Error("itoaUint64(42) should be 42")
	}
	if itoaUint64(18446744073709551615) != "18446744073709551615" {
		t.Error("itoaUint64(max) should be max")
	}
}
