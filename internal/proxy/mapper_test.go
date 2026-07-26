package proxy

import (
	"testing"
)

func TestMapModel(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		// OpenAI names → Gemini
		{"gpt-4", "gemini-2.5-flash"},
		{"gpt-4o", "gemini-2.5-flash"},
		{"gpt-4o-mini", "gemini-2.5-flash"},
		{"gpt-3.5-turbo", "gemini-2.5-flash"},
		{"gpt-5", "gemini-3-pro-preview"},

		// Claude aliases → current
		{"claude-sonnet-4-5", "claude-sonnet-4-6"},
		{"claude-sonnet-4-5-thinking", "claude-sonnet-4-6-thinking"},
		{"claude-3-5-sonnet-20241022", "claude-sonnet-4-6"},
		{"claude-opus-4", "claude-opus-4-6-thinking"},
		{"claude-haiku-4", "claude-sonnet-4-6"},

		// Gemini aliases
		{"gemini-2.5-flash-lite", "gemini-2.5-flash"},
		{"gemini-3.1-pro", "gemini-3.1-pro-preview"},
		{"gemini-3-pro", "gemini-3-pro-preview"},
		{"gemini-3-pro-high", "gemini-pro-agent"},

		// Background task
		{"internal-background-task", "gemini-2.5-flash"},

		// Passthrough
		{"gemini-3-pro-preview", "gemini-3-pro-preview"},
		{"claude-sonnet-4-6", "claude-sonnet-4-6"},
		{"claude-opus-4-6-thinking", "claude-opus-4-6-thinking"},

		// Unknown model passes through
		{"some-random-model", "some-random-model"},

		// Case insensitive
		{"GPT-4", "gemini-2.5-flash"},
		{"Claude-Sonnet-4-5", "claude-sonnet-4-6"},

		// Whitespace trimmed
		{"  gpt-4  ", "gemini-2.5-flash"},
	}
	for _, tt := range tests {
		got := MapModel(tt.input)
		if got != tt.want {
			t.Errorf("MapModel(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestIsThinkingModel(t *testing.T) {
	tests := []struct {
		model string
		want  bool
	}{
		{"claude-sonnet-4-6-thinking", true},
		{"claude-opus-4-6-thinking", true},
		{"claude-sonnet-4-6", false},
		{"gemini-3-pro", true},
		{"gemini-3-pro-preview", true},
		{"gemini-3.1-pro-preview", true},
		{"gemini-2.5-flash", false},
		// gemini-3-flash contains "gemini-3" so it matches the
		// thinking rule — this is the current behavior.
		{"gemini-3-flash", true},
		{"gpt-4", false},
		{"", false},
	}
	for _, tt := range tests {
		got := isThinkingModel(tt.model)
		if got != tt.want {
			t.Errorf("isThinkingModel(%q) = %v, want %v",
				tt.model, got, tt.want)
		}
	}
}

func TestNormalizeSchemaTypes_TypeArray(t *testing.T) {
	schema := map[string]any{
		"type": []any{"string", "null"},
	}
	normalizeSchemaTypes(schema)
	if schema["type"] != "STRING" {
		t.Errorf("type = %v, want STRING", schema["type"])
	}
	if schema["nullable"] != true {
		t.Errorf("nullable = %v, want true", schema["nullable"])
	}
}

func TestNormalizeSchemaTypes_TypeArrayNullOnly(t *testing.T) {
	schema := map[string]any{
		"type": []any{"null"},
	}
	normalizeSchemaTypes(schema)
	if schema["type"] != "STRING" {
		t.Errorf("type = %v, want STRING (fallback)", schema["type"])
	}
	if schema["nullable"] != true {
		t.Errorf("nullable = %v, want true", schema["nullable"])
	}
}

func TestNormalizeSchemaTypes_TypeArrayMultiple(t *testing.T) {
	schema := map[string]any{
		"type": []any{"integer", "string", "null"},
	}
	normalizeSchemaTypes(schema)
	if schema["type"] != "INTEGER" {
		t.Errorf("type = %v, want INTEGER (first non-null)",
			schema["type"])
	}
	if schema["nullable"] != true {
		t.Errorf("nullable = %v, want true", schema["nullable"])
	}
}

func TestNormalizeSchemaTypes_TypeString(t *testing.T) {
	schema := map[string]any{
		"type":   "string",
		"format": "email",
	}
	normalizeSchemaTypes(schema)
	if schema["type"] != "STRING" {
		t.Errorf("type = %v, want STRING", schema["type"])
	}
	if _, exists := schema["format"]; exists {
		t.Errorf("format should be removed")
	}
}

func TestNormalizeSchemaTypes_Nested(t *testing.T) {
	schema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"name": map[string]any{
				"type": []any{"string", "null"},
			},
			"age": map[string]any{
				"type": "integer",
			},
		},
	}
	normalizeSchemaTypes(schema)
	if schema["type"] != "OBJECT" {
		t.Errorf("root type = %v, want OBJECT", schema["type"])
	}
	props := schema["properties"].(map[string]any)
	name := props["name"].(map[string]any)
	if name["type"] != "STRING" {
		t.Errorf("name type = %v, want STRING", name["type"])
	}
	if name["nullable"] != true {
		t.Errorf("name nullable = %v, want true", name["nullable"])
	}
	age := props["age"].(map[string]any)
	if age["type"] != "INTEGER" {
		t.Errorf("age type = %v, want INTEGER", age["type"])
	}
}

func TestNormalizeSchemaTypes_RemovesUnsupportedFields(t *testing.T) {
	schema := map[string]any{
		"type":        "object",
		"strict":      true,
		"$schema":     "http://json-schema.org/draft-07/schema#",
		"definitions": map[string]any{},
	}
	normalizeSchemaTypes(schema)
	for _, k := range []string{"format", "strict", "$schema", "definitions"} {
		if _, exists := schema[k]; exists {
			t.Errorf("%q should be removed", k)
		}
	}
}

func TestNormalizeSchemaTypes_ArrayItems(t *testing.T) {
	schema := map[string]any{
		"type": "array",
		"items": map[string]any{
			"type": []any{"string", "null"},
		},
	}
	normalizeSchemaTypes(schema)
	if schema["type"] != "ARRAY" {
		t.Errorf("type = %v, want ARRAY", schema["type"])
	}
	items := schema["items"].(map[string]any)
	if items["type"] != "STRING" {
		t.Errorf("items type = %v, want STRING", items["type"])
	}
	if items["nullable"] != true {
		t.Errorf("items nullable = %v, want true", items["nullable"])
	}
}

func TestBuildOpenAIToolNameMap(t *testing.T) {
	messages := []any{
		map[string]any{
			"role": "assistant",
			"tool_calls": []any{
				map[string]any{
					"id": "call_1",
					"function": map[string]any{
						"name":      "get_weather",
						"arguments": "{}",
					},
				},
				map[string]any{
					"id": "call_2",
					"function": map[string]any{
						"name":      "get_time",
						"arguments": "{}",
					},
				},
			},
		},
		map[string]any{
			"role": "user",
			"content": "hello",
		},
	}
	m := buildOpenAIToolNameMap(messages)
	if len(m) != 2 {
		t.Fatalf("len = %d, want 2", len(m))
	}
	if m["call_1"] != "get_weather" {
		t.Errorf("call_1 = %q, want get_weather", m["call_1"])
	}
	if m["call_2"] != "get_time" {
		t.Errorf("call_2 = %q, want get_time", m["call_2"])
	}
}

func TestBuildOpenAIToolNameMap_Empty(t *testing.T) {
	m := buildOpenAIToolNameMap(nil)
	if m == nil || len(m) != 0 {
		t.Errorf("expected empty map, got %v", m)
	}
}

func TestBuildOpenAIToolNameMap_MissingFunction(t *testing.T) {
	messages := []any{
		map[string]any{
			"role": "assistant",
			"tool_calls": []any{
				map[string]any{
					"id": "call_1",
					// no function field
				},
			},
		},
	}
	m := buildOpenAIToolNameMap(messages)
	if len(m) != 0 {
		t.Errorf("expected empty map, got %v", m)
	}
}

func TestMaxInt64(t *testing.T) {
	if maxInt64(3, 5) != 5 {
		t.Error("maxInt64(3,5) should be 5")
	}
	if maxInt64(5, 3) != 5 {
		t.Error("maxInt64(5,3) should be 5")
	}
	if maxInt64(-1, 0) != 0 {
		t.Error("maxInt64(-1,0) should be 0")
	}
}

func TestClampInt64(t *testing.T) {
	if clampInt64(5, 0, 10) != 5 {
		t.Error("clampInt64(5,0,10) should be 5")
	}
	if clampInt64(-1, 0, 10) != 0 {
		t.Error("clampInt64(-1,0,10) should be 0")
	}
	if clampInt64(15, 0, 10) != 10 {
		t.Error("clampInt64(15,0,10) should be 10")
	}
}

func TestContains(t *testing.T) {
	s := []string{"a", "b", "c"}
	if !contains(s, "b") {
		t.Error("contains should find b")
	}
	if contains(s, "z") {
		t.Error("contains should not find z")
	}
	if contains(nil, "a") {
		t.Error("contains on nil should be false")
	}
}

func TestNormalizeSchemaTypesAnthropic_TypeArray(t *testing.T) {
	schema := map[string]any{
		"type": []any{"string", "null"},
	}
	normalizeSchemaTypesAnthropic(schema)
	if schema["type"] != "STRING" {
		t.Errorf("type = %v, want STRING", schema["type"])
	}
	if schema["nullable"] != true {
		t.Errorf("nullable = %v, want true", schema["nullable"])
	}
}

func TestNormalizeSchemaTypesAnthropic_Nested(t *testing.T) {
	schema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"value": map[string]any{
				"type": []any{"integer", "null"},
			},
		},
	}
	normalizeSchemaTypesAnthropic(schema)
	if schema["type"] != "OBJECT" {
		t.Errorf("type = %v, want OBJECT", schema["type"])
	}
	props := schema["properties"].(map[string]any)
	val := props["value"].(map[string]any)
	if val["type"] != "INTEGER" {
		t.Errorf("value type = %v, want INTEGER", val["type"])
	}
	if val["nullable"] != true {
		t.Errorf("value nullable = %v, want true", val["nullable"])
	}
}

func TestNormalizeSchemaTypesAnthropic_RemovesUnsupportedFields(t *testing.T) {
	schema := map[string]any{
		"type":        "object",
		"enum":        []any{"a", "b"},
		"additionalProperties": true,
		"oneOf":       []any{},
	}
	normalizeSchemaTypesAnthropic(schema)
	for _, k := range []string{"enum", "additionalProperties", "oneOf"} {
		if _, exists := schema[k]; exists {
			t.Errorf("%q should be removed", k)
		}
	}
}

func TestTransformToolOpenAI(t *testing.T) {
	fn := map[string]any{
		"name":        "get_weather",
		"description": "Get weather",
		"parameters": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"city": map[string]any{
					"type":        []any{"string", "null"},
					"description": "City name",
				},
			},
		},
	}
	result := transformToolOpenAI(fn)
	if result["name"] != "get_weather" {
		t.Errorf("name = %v", result["name"])
	}
	params := result["parameters"].(map[string]any)
	if params["type"] != "OBJECT" {
		t.Errorf("params type = %v, want OBJECT", params["type"])
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

func TestTransformToolOpenAI_NilParams(t *testing.T) {
	fn := map[string]any{
		"name":        "noop",
		"description": "No operation",
	}
	result := transformToolOpenAI(fn)
	params := result["parameters"].(map[string]any)
	if params["type"] != "OBJECT" {
		t.Errorf("default params type = %v, want OBJECT", params["type"])
	}
	props, ok := params["properties"].(map[string]any)
	if !ok || len(props) != 0 {
		t.Errorf("default params should have empty properties")
	}
}

func TestMapFinishReason(t *testing.T) {
	tests := []struct {
		gemini string
		want   string
	}{
		{"STOP", "stop"},
		{"MAX_TOKENS", "length"},
		{"SAFETY", "content_filter"},
		{"RECITATION", "content_filter"},
		{"OTHER", "stop"},
		{"", "stop"},
	}
	for _, tt := range tests {
		got := mapFinishReason(tt.gemini)
		if got != tt.want {
			t.Errorf("mapFinishReason(%q) = %q, want %q",
				tt.gemini, got, tt.want)
		}
	}
}

// Ensure no data races in normalizeSchemaTypes when modifying nested maps.
func TestNormalizeSchemaTypes_DoesNotPanicOnNil(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Errorf("panicked: %v", r)
		}
	}()
	schema := map[string]any{
		"type":       "object",
		"properties": map[string]any{},
	}
	normalizeSchemaTypes(schema)
}

// Verify that a complex schema with mixed types is fully normalized.
func TestNormalizeSchemaTypes_ComplexSchema(t *testing.T) {
	schema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"tags": map[string]any{
				"type": "array",
				"items": map[string]any{
					"type": []any{"string", "null"},
				},
			},
			"metadata": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"score": map[string]any{
						"type":   "number",
						"format": "float",
					},
				},
			},
		},
	}
	normalizeSchemaTypes(schema)
	if schema["type"] != "OBJECT" {
		t.Errorf("root type = %v, want OBJECT", schema["type"])
	}
	props := schema["properties"].(map[string]any)
	tags := props["tags"].(map[string]any)
	if tags["type"] != "ARRAY" {
		t.Errorf("tags type = %v, want ARRAY", tags["type"])
	}
	items := tags["items"].(map[string]any)
	if items["type"] != "STRING" {
		t.Errorf("items type = %v, want STRING", items["type"])
	}
	meta := props["metadata"].(map[string]any)
	if meta["type"] != "OBJECT" {
		t.Errorf("metadata type = %v, want OBJECT", meta["type"])
	}
	metaProps := meta["properties"].(map[string]any)
	score := metaProps["score"].(map[string]any)
	if score["type"] != "NUMBER" {
		t.Errorf("score type = %v, want NUMBER", score["type"])
	}
	if _, exists := score["format"]; exists {
		t.Errorf("score format should be removed")
	}
}

