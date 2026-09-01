package proxy

import (
	"encoding/json"
	"strings"
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

// sseRecord captures a single Anthropic SSE event emitted by the state machine.
type sseRecord struct {
	event string
	data  map[string]any
}

// collectSSE parses the raw SSE text produced by AnthropicStreamState into a
// slice of {event, data} records. Each record's data payload is decoded as
// generic JSON so that tests can assert on structured fields rather than on
// raw string fragments.
func collectSSE(t *testing.T, lines []string) []sseRecord {
	t.Helper()
	var records []sseRecord
	for _, l := range lines {
		// Each emitted chunk is "event: <name>\ndata: <json>\n\n".
		l = strings.TrimSpace(l)
		if l == "" {
			continue
		}
		var event string
		var dataStr string
		for _, line := range strings.Split(l, "\n") {
			line = strings.TrimSpace(line)
			if strings.HasPrefix(line, "event: ") {
				event = strings.TrimPrefix(line, "event: ")
			} else if strings.HasPrefix(line, "data: ") {
				dataStr = strings.TrimPrefix(line, "data: ")
			}
		}
		if event == "" {
			t.Fatalf("missing event header in chunk: %q", l)
		}
		var data map[string]any
		if dataStr != "" {
			if err := json.Unmarshal([]byte(dataStr), &data); err != nil {
				t.Fatalf("decode SSE data for %q: %v (raw=%q)", event, err, dataStr)
			}
		}
		records = append(records, sseRecord{event: event, data: data})
	}
	return records
}

// eventTypes returns the ordered list of event names from records.
func eventTypes(records []sseRecord) []string {
	out := make([]string, 0, len(records))
	for _, r := range records {
		out = append(out, r.event)
	}
	return out
}

// assertEventSequence checks that the emitted events match the expected
// lifecycle exactly, in order.
func assertEventSequence(t *testing.T, records []sseRecord, want ...string) {
	t.Helper()
	got := eventTypes(records)
	if len(got) != len(want) {
		t.Fatalf("event sequence length = %d (%v), want %d (%v)", len(got), got, len(want), want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("event[%d] = %q, want %q (full got=%v)", i, got[i], want[i], got)
		}
	}
}

// TestAnthropicStreamStateNormal covers a typical text stream that ends with a
// finishReason. The lifecycle must be:
//
//	message_start -> content_block_start -> content_block_delta ->
//	content_block_stop -> message_delta -> message_stop
func TestAnthropicStreamStateNormal(t *testing.T) {
	s := NewAnthropicStreamState("claude-test")
	var all []string
	all = append(all, s.ProcessChunk(map[string]any{
		"usageMetadata": map[string]any{
			"promptTokenCount":    float64(7),
			"candidatesTokenCount": float64(3),
		},
		"candidates": []any{
			map[string]any{
				"content": map[string]any{
					"parts": []any{
						map[string]any{"text": "Hello"},
					},
				},
				"finishReason": "STOP",
			},
		},
	})...)
	all = append(all, s.Finalize()...)

	records := collectSSE(t, all)
	assertEventSequence(t, records,
		"message_start",
		"content_block_start",
		"content_block_delta",
		"content_block_stop",
		"message_delta",
		"message_stop",
	)

	// message_start carries prompt-side usage.
	ms := records[0].data["message"].(map[string]any)
	if ms["id"] == "" || ms["id"] == nil {
		t.Fatal("message_start missing id")
	}
	if ms["model"] != "claude-test" {
		t.Errorf("message_start model = %v, want claude-test", ms["model"])
	}
	usage := ms["usage"].(map[string]any)
	if int64Or(usage, "input_tokens", 0) != 7 {
		t.Errorf("message_start input_tokens = %v, want 7", usage["input_tokens"])
	}

	// content_block_start is a text block at index 0.
	cbs := records[1].data
	if int64Or(cbs, "index", -1) != 0 {
		t.Errorf("content_block_start index = %v, want 0", cbs["index"])
	}
	cb := cbs["content_block"].(map[string]any)
	if cb["type"] != "text" {
		t.Errorf("content_block type = %v, want text", cb["type"])
	}

	// content_block_delta carries text_delta.
	delta := records[2].data["delta"].(map[string]any)
	if delta["type"] != "text_delta" {
		t.Errorf("delta type = %v, want text_delta", delta["type"])
	}
	if delta["text"] != "Hello" {
		t.Errorf("delta text = %v, want Hello", delta["text"])
	}

	// content_block_stop at index 0.
	if int64Or(records[3].data, "index", -1) != 0 {
		t.Errorf("content_block_stop index = %v, want 0", records[3].data["index"])
	}

	// message_delta reports end_turn and final usage.
	md := records[4].data["delta"].(map[string]any)
	if md["stop_reason"] != "end_turn" {
		t.Errorf("stop_reason = %v, want end_turn", md["stop_reason"])
	}
	mdUsage := records[4].data["usage"].(map[string]any)
	if int64Or(mdUsage, "output_tokens", 0) != 3 {
		t.Errorf("message_delta output_tokens = %v, want 3", mdUsage["output_tokens"])
	}

	// Finalize must be a no-op once stopped.
	if extra := s.Finalize(); len(extra) != 0 {
		t.Errorf("Finalize after stop emitted %d events, want 0", len(extra))
	}
}

// TestAnthropicStreamStateUsageOnly covers a stream that carries only
// usageMetadata and a finishReason with no content parts. The lifecycle must
// still emit message_start, message_delta, and message_stop.
func TestAnthropicStreamStateUsageOnly(t *testing.T) {
	s := NewAnthropicStreamState("claude-usage")
	var all []string
	all = append(all, s.ProcessChunk(map[string]any{
		"usageMetadata": map[string]any{
			"promptTokenCount":     float64(11),
			"candidatesTokenCount": float64(0),
			"cachedContentTokenCount": float64(4),
		},
		"candidates": []any{
			map[string]any{
				"content":      map[string]any{"parts": []any{}},
				"finishReason": "STOP",
			},
		},
	})...)
	all = append(all, s.Finalize()...)

	records := collectSSE(t, all)
	assertEventSequence(t, records,
		"message_start",
		"message_delta",
		"message_stop",
	)

	// message_start reflects cached tokens on the prompt side.
	msUsage := records[0].data["message"].(map[string]any)["usage"].(map[string]any)
	if int64Or(msUsage, "input_tokens", 0) != 11 {
		t.Errorf("input_tokens = %v, want 11", msUsage["input_tokens"])
	}
	if int64Or(msUsage, "cache_read_input_tokens", 0) != 4 {
		t.Errorf("cache_read_input_tokens = %v, want 4", msUsage["cache_read_input_tokens"])
	}

	// message_delta reports end_turn with no tool usage.
	md := records[1].data["delta"].(map[string]any)
	if md["stop_reason"] != "end_turn" {
		t.Errorf("stop_reason = %v, want end_turn", md["stop_reason"])
	}
}

// TestAnthropicStreamStateAbruptTermination covers an upstream stream that ends
// without ever setting finishReason. Finalize must close any open block and
// emit the terminal message_delta + message_stop pair.
func TestAnthropicStreamStateAbruptTermination(t *testing.T) {
	s := NewAnthropicStreamState("claude-abrupt")
	var all []string
	all = append(all, s.ProcessChunk(map[string]any{
		"candidates": []any{
			map[string]any{
				"content": map[string]any{
					"parts": []any{
						map[string]any{"text": "partial"},
					},
				},
				// no finishReason
			},
		},
	})...)
	// Simulate the scanner loop ending and the caller invoking Finalize.
	all = append(all, s.Finalize()...)

	records := collectSSE(t, all)
	assertEventSequence(t, records,
		"message_start",
		"content_block_start",
		"content_block_delta",
		"content_block_stop",
		"message_delta",
		"message_stop",
	)

	// The text delta is preserved.
	delta := records[2].data["delta"].(map[string]any)
	if delta["text"] != "partial" {
		t.Errorf("delta text = %v, want partial", delta["text"])
	}

	// Finalize closed the open text block (content_block_stop at index 0).
	if int64Or(records[3].data, "index", -1) != 0 {
		t.Errorf("content_block_stop index = %v, want 0", records[3].data["index"])
	}

	// message_delta defaults to end_turn when no tool was used and no
	// finishReason was observed.
	md := records[4].data["delta"].(map[string]any)
	if md["stop_reason"] != "end_turn" {
		t.Errorf("stop_reason = %v, want end_turn", md["stop_reason"])
	}
}

// TestAnthropicStreamStateToolUse covers a stream that emits a functionCall
// part. The lifecycle must include a tool_use content block whose start carries
// the tool id/name, an input_json_delta carrying the serialized args, and a
// stop_reason of "tool_use".
func TestAnthropicStreamStateToolUse(t *testing.T) {
	s := NewAnthropicStreamState("claude-tool")
	var all []string
	all = append(all, s.ProcessChunk(map[string]any{
		"candidates": []any{
			map[string]any{
				"content": map[string]any{
					"parts": []any{
						map[string]any{
							"functionCall": map[string]any{
								"id":   "call_123",
								"name": "get_weather",
								"args": map[string]any{"city": "SF"},
							},
						},
					},
				},
				"finishReason": "STOP",
			},
		},
	})...)
	all = append(all, s.Finalize()...)

	records := collectSSE(t, all)
	assertEventSequence(t, records,
		"message_start",
		"content_block_start",
		"content_block_delta",
		"content_block_stop",
		"message_delta",
		"message_stop",
	)

	// content_block_start describes a tool_use block.
	cbs := records[1].data
	cb := cbs["content_block"].(map[string]any)
	if cb["type"] != "tool_use" {
		t.Errorf("content_block type = %v, want tool_use", cb["type"])
	}
	if cb["id"] != "call_123" {
		t.Errorf("tool id = %v, want call_123", cb["id"])
	}
	if cb["name"] != "get_weather" {
		t.Errorf("tool name = %v, want get_weather", cb["name"])
	}

	// input_json_delta carries the serialized args as partial_json.
	delta := records[2].data["delta"].(map[string]any)
	if delta["type"] != "input_json_delta" {
		t.Errorf("delta type = %v, want input_json_delta", delta["type"])
	}
	partial, _ := delta["partial_json"].(string)
	if partial == "" {
		t.Fatal("input_json_delta missing partial_json")
	}
	var parsed map[string]any
	if err := json.Unmarshal([]byte(partial), &parsed); err != nil {
		t.Fatalf("partial_json not valid JSON: %v (raw=%q)", err, partial)
	}
	if parsed["city"] != "SF" {
		t.Errorf("partial_json city = %v, want SF", parsed["city"])
	}

	// stop_reason reflects tool_use.
	md := records[4].data["delta"].(map[string]any)
	if md["stop_reason"] != "tool_use" {
		t.Errorf("stop_reason = %v, want tool_use", md["stop_reason"])
	}
}

// TestAnthropicStreamStateDuplicateFinalize ensures Finalize is idempotent:
// calling it again after the stream has already terminated must not emit any
// additional terminal events.
func TestAnthropicStreamStateDuplicateFinalize(t *testing.T) {
	s := NewAnthropicStreamState("claude-dup")
	var all []string
	all = append(all, s.ProcessChunk(map[string]any{
		"candidates": []any{
			map[string]any{
				"content": map[string]any{
					"parts": []any{
						map[string]any{"text": "hi"},
					},
				},
				"finishReason": "STOP",
			},
		},
	})...)
	all = append(all, s.Finalize()...)
	first := collectSSE(t, all)
	assertEventSequence(t, first,
		"message_start",
		"content_block_start",
		"content_block_delta",
		"content_block_stop",
		"message_delta",
		"message_stop",
	)

	// Repeated Finalize must be a no-op.
	second := s.Finalize()
	if len(second) != 0 {
		t.Errorf("duplicate Finalize emitted %d events, want 0: %v", len(second), second)
	}

	// Repeated Finalize after abrupt termination (no finishReason) is also a
	// no-op once stopped.
	s2 := NewAnthropicStreamState("claude-dup2")
	_ = s2.ProcessChunk(map[string]any{
		"candidates": []any{
			map[string]any{
				"content": map[string]any{
					"parts": []any{map[string]any{"text": "x"}},
				},
			},
		},
	})
	if firstFinal := s2.Finalize(); len(firstFinal) == 0 {
		t.Fatal("first Finalize on abrupt stream should emit terminal events")
	}
	if dup := s2.Finalize(); len(dup) != 0 {
		t.Errorf("duplicate Finalize on abrupt stream emitted %d events, want 0", len(dup))
	}
}
