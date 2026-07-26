package proxy

import (
	"testing"
)

func TestBuildToolNameMap(t *testing.T) {
	messages := []any{
		map[string]any{
			"role": "assistant",
			"content": []any{
				map[string]any{
					"type": "tool_use",
					"id":   "toolu_1",
					"name": "get_weather",
				},
				map[string]any{
					"type": "text",
					"text": "Let me check the weather.",
				},
				map[string]any{
					"type": "tool_use",
					"id":   "toolu_2",
					"name": "get_time",
				},
			},
		},
		map[string]any{
			"role": "user",
			"content": "hello",
		},
	}
	m := buildToolNameMap(messages)
	if len(m) != 2 {
		t.Fatalf("len = %d, want 2", len(m))
	}
	if m["toolu_1"] != "get_weather" {
		t.Errorf("toolu_1 = %q, want get_weather", m["toolu_1"])
	}
	if m["toolu_2"] != "get_time" {
		t.Errorf("toolu_2 = %q, want get_time", m["toolu_2"])
	}
}

func TestBuildToolNameMap_Empty(t *testing.T) {
	m := buildToolNameMap(nil)
	if len(m) != 0 {
		t.Errorf("expected empty map, got %v", m)
	}
}

func TestBuildToolNameMap_NoToolUse(t *testing.T) {
	messages := []any{
		map[string]any{
			"role": "assistant",
			"content": []any{
				map[string]any{"type": "text", "text": "hello"},
			},
		},
	}
	m := buildToolNameMap(messages)
	if len(m) != 0 {
		t.Errorf("expected empty map, got %v", m)
	}
}

func TestBuildToolNameMap_MissingID(t *testing.T) {
	messages := []any{
		map[string]any{
			"role": "assistant",
			"content": []any{
				map[string]any{
					"type": "tool_use",
					"name": "get_weather",
					// no id
				},
			},
		},
	}
	m := buildToolNameMap(messages)
	if len(m) != 0 {
		t.Errorf("expected empty map for missing id, got %v", m)
	}
}

func TestExtractToolResultContent_String(t *testing.T) {
	got := extractToolResultContent("result text")
	if got != "result text" {
		t.Errorf("got %q, want result text", got)
	}
}

func TestExtractToolResultContent_Array(t *testing.T) {
	content := []any{
		map[string]any{"type": "text", "text": "line 1"},
		map[string]any{"type": "text", "text": "line 2"},
		map[string]any{"type": "tool_use", "name": "foo"}, // ignored
	}
	got := extractToolResultContent(content)
	want := "line 1\nline 2"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestExtractToolResultContent_EmptyArray(t *testing.T) {
	got := extractToolResultContent([]any{})
	if got != "" {
		t.Errorf("got %q, want empty", got)
	}
}

func TestExtractToolResultContent_Nil(t *testing.T) {
	got := extractToolResultContent(nil)
	if got != "" {
		t.Errorf("got %q, want empty", got)
	}
}

func TestExtractToolResultContent_OtherType(t *testing.T) {
	got := extractToolResultContent(42)
	if got != "" {
		t.Errorf("got %q, want empty for int", got)
	}
}

func TestMergeConsecutiveRoles(t *testing.T) {
	contents := []any{
		map[string]any{
			"role":  "user",
			"parts": []any{map[string]any{"text": "msg 1"}},
		},
		map[string]any{
			"role":  "user",
			"parts": []any{map[string]any{"text": "msg 2"}},
		},
		map[string]any{
			"role":  "model",
			"parts": []any{map[string]any{"text": "reply"}},
		},
	}
	result := mergeConsecutiveRoles(contents)
	if len(result) != 2 {
		t.Fatalf("len = %d, want 2 (merged user msgs)", len(result))
	}
	first := result[0].(map[string]any)
	firstParts := first["parts"].([]any)
	if len(firstParts) != 2 {
		t.Errorf("merged user parts len = %d, want 2", len(firstParts))
	}
	second := result[1].(map[string]any)
	if second["role"] != "model" {
		t.Errorf("second role = %v, want model", second["role"])
	}
}

func TestMergeConsecutiveRoles_NoMerge(t *testing.T) {
	contents := []any{
		map[string]any{"role": "user", "parts": []any{}},
		map[string]any{"role": "model", "parts": []any{}},
		map[string]any{"role": "user", "parts": []any{}},
	}
	result := mergeConsecutiveRoles(contents)
	if len(result) != 3 {
		t.Errorf("len = %d, want 3 (no consecutive)", len(result))
	}
}

func TestMergeConsecutiveRoles_Empty(t *testing.T) {
	result := mergeConsecutiveRoles(nil)
	if len(result) != 0 {
		t.Errorf("expected empty, got %v", result)
	}
}

func TestMergeConsecutiveRoles_ThreeConsecutive(t *testing.T) {
	contents := []any{
		map[string]any{
			"role":  "user",
			"parts": []any{map[string]any{"text": "a"}},
		},
		map[string]any{
			"role":  "user",
			"parts": []any{map[string]any{"text": "b"}},
		},
		map[string]any{
			"role":  "user",
			"parts": []any{map[string]any{"text": "c"}},
		},
	}
	result := mergeConsecutiveRoles(contents)
	if len(result) != 1 {
		t.Fatalf("len = %d, want 1", len(result))
	}
	parts := result[0].(map[string]any)["parts"].([]any)
	if len(parts) != 3 {
		t.Errorf("parts len = %d, want 3", len(parts))
	}
}

func TestTransformToolAnthropic(t *testing.T) {
	tool := map[string]any{
		"name":        "get_weather",
		"description": "Get weather",
		"input_schema": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"city": map[string]any{
					"type":        []any{"string", "null"},
					"description": "City name",
				},
			},
		},
	}
	result := transformToolAnthropic(tool)
	if result["name"] != "get_weather" {
		t.Errorf("name = %v", result["name"])
	}
	params := result["parameters"].(map[string]any)
	if params["type"] != "OBJECT" {
		t.Errorf("type = %v, want OBJECT", params["type"])
	}
	props := params["properties"].(map[string]any)
	city := props["city"].(map[string]any)
	if city["type"] != "STRING" {
		t.Errorf("city type = %v, want STRING", city["type"])
	}
	if city["nullable"] != true {
		t.Errorf("city nullable = %v, want true", city["nullable"])
	}
}

func TestTransformToolAnthropic_NilSchema(t *testing.T) {
	tool := map[string]any{
		"name":        "noop",
		"description": "No-op",
	}
	result := transformToolAnthropic(tool)
	params := result["parameters"].(map[string]any)
	if params["type"] != "OBJECT" {
		t.Errorf("default type = %v, want OBJECT", params["type"])
	}
}

func TestTransformToolAnthropic_EmptyTool(t *testing.T) {
	result := transformToolAnthropic(nil)
	if result["name"] != "" {
		t.Errorf("name = %v, want empty", result["name"])
	}
	params := result["parameters"].(map[string]any)
	if params["type"] != "OBJECT" {
		t.Errorf("default type = %v, want OBJECT", params["type"])
	}
}

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
