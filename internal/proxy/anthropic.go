package proxy

import (
	"encoding/json"
	"strconv"
)

// Legacy Anthropic translation functions (AnthropicTransformRequest,
// AnthropicTransformResponse, anthropicMessageToContent, buildToolNameMap,
// extractToolResultContent, mergeConsecutiveRoles, transformToolAnthropic,
// normalizeSchemaTypesAnthropic, buildAnthropicUsage) have been removed.
// The IR layer (internal/ir) now handles all protocol translation for the
// Antigravity/AGY path. See ir_adapter.go.
//
// The AnthropicStreamState below is still used by streamAnthropicSSEIR
// for streaming response conversion.

// AnthropicStreamState is the state machine for converting Gemini SSE into
// Anthropic SSE events.
type AnthropicStreamState struct {
	msgID           string
	model           string
	blockIndex      int
	currentBlock    *anthropicBlockType
	messageStartSet bool
	stopped         bool
	usedTool        bool
	inputTokens     int64
	outputTokens    int64
	cachedTokens    int64
}

type anthropicBlockType int

const (
	blockNone anthropicBlockType = iota
	blockText
	blockThinking
	blockToolUse
)

// NewAnthropicStreamState creates a fresh state machine.
func NewAnthropicStreamState(model string) *AnthropicStreamState {
	return &AnthropicStreamState{
		msgID: "msg_" + compactUUID()[:12],
		model: model,
	}
}

func (s *AnthropicStreamState) sse(event string, data any) string {
	b, _ := json.Marshal(data)
	return "event: " + event + "\ndata: " + string(b) + "\n\n"
}

func (s *AnthropicStreamState) ensureMessageStart() string {
	if s.messageStartSet {
		return ""
	}
	s.messageStartSet = true
	return s.sse("message_start", map[string]any{
		"type": "message_start",
		"message": map[string]any{
			"id":            s.msgID,
			"type":          "message",
			"role":          "assistant",
			"content":       []any{},
			"model":         s.model,
			"stop_reason":   nil,
			"stop_sequence": nil,
			"usage": map[string]any{
				"input_tokens":                  s.inputTokens,
				"output_tokens":                 0,
				"cache_creation_input_tokens":   0,
				"cache_read_input_tokens":       s.cachedTokens,
			},
		},
	})
}

func (s *AnthropicStreamState) startBlock(bt anthropicBlockType, contentBlock any) string {
	out := s.sse("content_block_start", map[string]any{
		"type":          "content_block_start",
		"index":         s.blockIndex,
		"content_block": contentBlock,
	})
	s.currentBlock = &bt
	return out
}

func (s *AnthropicStreamState) delta(deltaType string, payload map[string]any) string {
	payload["type"] = deltaType
	return s.sse("content_block_delta", map[string]any{
		"type":  "content_block_delta",
		"index": s.blockIndex,
		"delta": payload,
	})
}

func (s *AnthropicStreamState) endBlock() string {
	out := s.sse("content_block_stop", map[string]any{
		"type":  "content_block_stop",
		"index": s.blockIndex,
	})
	s.blockIndex++
	s.currentBlock = nil
	return out
}

// ProcessChunk processes one Gemini SSE chunk (parsed inner JSON) and emits
// Anthropic SSE event lines.
func (s *AnthropicStreamState) ProcessChunk(inner map[string]any) []string {
	var out []string

	// Parse usageMetadata before emitting message_start so that the
	// prompt-side usage (input_tokens, cache_read_input_tokens) is
	// reflected in the message_start event, matching Anthropic's
	// canonical streaming format where message_start carries the full
	// prompt-side token counts.
	if usage, ok := inner["usageMetadata"].(map[string]any); ok {
		if v := int64Or(usage, "promptTokenCount", 0); v != 0 {
			s.inputTokens = v
		}
		if v := int64Or(usage, "candidatesTokenCount", 0); v != 0 {
			s.outputTokens = v
		}
		if v := int64Or(usage, "cachedContentTokenCount", 0); v != 0 {
			s.cachedTokens = v
		}
	}

	if msg := s.ensureMessageStart(); msg != "" {
		out = append(out, msg)
	}

	candidates, _ := inner["candidates"].([]any)
	var candidate map[string]any
	if len(candidates) > 0 {
		candidate, _ = candidates[0].(map[string]any)
	}
	var parts []any
	if candidate != nil {
		if content, ok := candidate["content"].(map[string]any); ok {
			parts, _ = content["parts"].([]any)
		}
	}

	for _, partAny := range parts {
		part, _ := partAny.(map[string]any)
		if part == nil {
			continue
		}
		if thought, _ := part["thought"].(bool); thought {
			if t, ok := part["text"].(string); ok && t != "" {
				if s.currentBlock == nil || *s.currentBlock != blockThinking {
					if s.currentBlock != nil {
						out = append(out, s.endBlock())
					}
					out = append(out, s.startBlock(blockThinking, map[string]any{"type": "thinking", "thinking": ""}))
				}
				out = append(out, s.delta("thinking_delta", map[string]any{"thinking": t}))
			}
			if sig, ok := part["thoughtSignature"].(string); ok {
				out = append(out, s.delta("signature_delta", map[string]any{"signature": sig}))
			}
			continue
		}
		if t, ok := part["text"].(string); ok && t != "" {
			if s.currentBlock == nil || *s.currentBlock != blockText {
				if s.currentBlock != nil {
					out = append(out, s.endBlock())
				}
				out = append(out, s.startBlock(blockText, map[string]any{"type": "text", "text": ""}))
			}
			out = append(out, s.delta("text_delta", map[string]any{"text": t}))
			continue
		}
		if fc, ok := part["functionCall"].(map[string]any); ok {
			s.usedTool = true
			if s.currentBlock != nil {
				out = append(out, s.endBlock())
			}
			id := strOr(fc, "id", "")
			if id == "" {
				name := strOr(fc, "name", "tool")
				id = "toolu_" + name + "_" + compactUUID()[:8]
			}
			name := strOr(fc, "name", "")
			input := fc["args"]
			if input == nil {
				input = map[string]any{}
			}
			inputBytes, _ := json.Marshal(input)
			if inputBytes == nil {
				inputBytes = []byte("{}")
			}
			out = append(out, s.startBlock(blockToolUse, map[string]any{
				"type":  "tool_use",
				"id":    id,
				"name":  name,
				"input": map[string]any{},
			}))
			out = append(out, s.delta("input_json_delta", map[string]any{"partial_json": string(inputBytes)}))
			out = append(out, s.endBlock())
		}
	}

	// Check finishReason on the last chunk.
	var finishReason string
	if candidate != nil {
		finishReason = strOr(candidate, "finishReason", "")
	}
	if finishReason != "" {
		if s.currentBlock != nil {
			out = append(out, s.endBlock())
		}
		stopReason := "end_turn"
		if s.usedTool {
			stopReason = "tool_use"
		} else if finishReason == "MAX_TOKENS" {
			stopReason = "max_tokens"
		}
		out = append(out, s.sse("message_delta", map[string]any{
			"type":  "message_delta",
			"delta": map[string]any{"stop_reason": stopReason, "stop_sequence": nil},
			"usage": map[string]any{
				"input_tokens":                s.inputTokens,
				"output_tokens":               s.outputTokens,
				"cache_creation_input_tokens": 0,
				"cache_read_input_tokens":     s.cachedTokens,
			},
		}))
		out = append(out, s.sse("message_stop", map[string]any{"type": "message_stop"}))
		s.stopped = true
	}

	return out
}

// Finalize emits terminal Anthropic SSE events when the upstream stream ends
// without a finishReason. Per the Anthropic SSE protocol, every stream must
// terminate with message_delta + message_stop. If ProcessChunk already emitted
// them (finishReason was set on the last chunk), Finalize is a no-op.
func (s *AnthropicStreamState) Finalize() []string {
	if s.stopped {
		return nil
	}
	var out []string
	if s.currentBlock != nil {
		out = append(out, s.endBlock())
	}
	stopReason := "end_turn"
	if s.usedTool {
		stopReason = "tool_use"
	}
	out = append(out, s.sse("message_delta", map[string]any{
		"type":  "message_delta",
		"delta": map[string]any{"stop_reason": stopReason, "stop_sequence": nil},
		"usage": map[string]any{
			"input_tokens":                s.inputTokens,
			"output_tokens":               s.outputTokens,
			"cache_creation_input_tokens": 0,
			"cache_read_input_tokens":     s.cachedTokens,
		},
	}))
	out = append(out, s.sse("message_stop", map[string]any{"type": "message_stop"}))
	s.stopped = true
	return out
}

func uint64Or(m map[string]any, key string, def uint64) uint64 {
	switch v := m[key].(type) {
	case float64:
		return uint64(v)
	case int64:
		return uint64(v)
	case int:
		return uint64(v)
	}
	return def
}

func itoaUint64(v uint64) string {
	return strconv.FormatUint(v, 10)
}
