package proxy

import (
	"encoding/json"
	"testing"

	"github.com/deungjaho/hydra/internal/ir"
)

// TestIRAdapter_ResponseEncoding verifies that IR-based response encoding
// produces valid output for all three protocols.
func TestIRAdapter_ResponseEncoding(t *testing.T) {
	geminiResp := map[string]any{
		"response": map[string]any{
			"candidates": []any{
				map[string]any{
					"content": map[string]any{
						"role": "model",
						"parts": []any{
							map[string]any{"text": "Hello!"},
						},
					},
					"finishReason": "STOP",
				},
			},
			"usageMetadata": map[string]any{
				"promptTokenCount":     float64(10),
				"candidatesTokenCount": float64(5),
			},
		},
	}

	// OpenAI
	openaiResp := irDecodeGeminiResponse(geminiResp, "openai", "gemini-2.5-flash")
	if openaiResp["object"] != "chat.completion" {
		t.Errorf("openai object = %v", openaiResp["object"])
	}
	choices, _ := openaiResp["choices"].([]any)
	if len(choices) != 1 {
		t.Fatalf("openai choices = %d", len(choices))
	}

	// Anthropic
	anthropicResp := irDecodeGeminiResponse(geminiResp, "anthropic", "claude-sonnet-4-6")
	if anthropicResp["type"] != "message" {
		t.Errorf("anthropic type = %v", anthropicResp["type"])
	}
	blocks, _ := anthropicResp["content"].([]any)
	if len(blocks) != 1 {
		t.Fatalf("anthropic blocks = %d", len(blocks))
	}

	// Responses
	responsesResp := irDecodeGeminiResponse(geminiResp, "responses", "gpt-4")
	if responsesResp["object"] != "response" {
		t.Errorf("responses object = %v", responsesResp["object"])
	}
	output, _ := responsesResp["output"].([]any)
	if len(output) != 1 {
		t.Fatalf("responses output = %d", len(output))
	}
}

// TestIRAdapter_ToolCallRoundTrip verifies tool calls are preserved.
func TestIRAdapter_ToolCallRoundTrip(t *testing.T) {
	geminiResp := map[string]any{
		"response": map[string]any{
			"candidates": []any{
				map[string]any{
					"content": map[string]any{
						"parts": []any{
							map[string]any{
								"functionCall": map[string]any{
									"name": "get_weather",
									"args": map[string]any{"city": "SF"},
									"id":   "call_1",
								},
							},
						},
					},
					"finishReason": "STOP",
				},
			},
		},
	}

	// OpenAI should have tool_calls in the message.
	openaiResp := irDecodeGeminiResponse(geminiResp, "openai", "gpt-4")
	choices, _ := openaiResp["choices"].([]any)
	choice, _ := choices[0].(map[string]any)
	msg, _ := choice["message"].(map[string]any)
	tcs, _ := msg["tool_calls"].([]any)
	if len(tcs) != 1 {
		t.Fatalf("tool_calls = %d", len(tcs))
	}
	if choice["finish_reason"] != "tool_calls" {
		t.Errorf("finish = %v, want tool_calls", choice["finish_reason"])
	}

	// Anthropic should have tool_use block.
	anthropicResp := irDecodeGeminiResponse(geminiResp, "anthropic", "claude-sonnet-4-6")
	if anthropicResp["stop_reason"] != "tool_use" {
		t.Errorf("stop_reason = %v, want tool_use", anthropicResp["stop_reason"])
	}
	blocks, _ := anthropicResp["content"].([]any)
	block, _ := blocks[0].(map[string]any)
	if block["type"] != "tool_use" {
		t.Errorf("block type = %v, want tool_use", block["type"])
	}

	// Responses should have function_call item.
	responsesResp := irDecodeGeminiResponse(geminiResp, "responses", "gpt-4")
	output, _ := responsesResp["output"].([]any)
	item, _ := output[0].(map[string]any)
	if item["type"] != "function_call" {
		t.Errorf("item type = %v, want function_call", item["type"])
	}
}

// TestIRAdapter_ThinkingRoundTrip verifies thinking content is preserved.
func TestIRAdapter_ThinkingRoundTrip(t *testing.T) {
	geminiResp := map[string]any{
		"response": map[string]any{
			"candidates": []any{
				map[string]any{
					"content": map[string]any{
						"parts": []any{
							map[string]any{"text": "Let me think...", "thought": true, "thoughtSignature": "sig123"},
							map[string]any{"text": "The answer is 42."},
						},
					},
					"finishReason": "STOP",
				},
			},
		},
	}

	// OpenAI should have reasoning_content.
	openaiResp := irDecodeGeminiResponse(geminiResp, "openai", "gemini-2.5-pro")
	choices, _ := openaiResp["choices"].([]any)
	choice, _ := choices[0].(map[string]any)
	msg, _ := choice["message"].(map[string]any)
	if rc, _ := msg["reasoning_content"].(string); rc != "Let me think..." {
		t.Errorf("reasoning_content = %v", msg["reasoning_content"])
	}
	if c, _ := msg["content"].(string); c != "The answer is 42." {
		t.Errorf("content = %v", msg["content"])
	}

	// Anthropic should have thinking block with signature.
	anthropicResp := irDecodeGeminiResponse(geminiResp, "anthropic", "claude-sonnet-4-6")
	blocks, _ := anthropicResp["content"].([]any)
	if len(blocks) != 2 {
		t.Fatalf("blocks = %d, want 2", len(blocks))
	}
	thinkBlock, _ := blocks[0].(map[string]any)
	if thinkBlock["type"] != "thinking" {
		t.Errorf("block0 type = %v, want thinking", thinkBlock["type"])
	}
	if thinkBlock["signature"] != "sig123" {
		t.Errorf("signature = %v, want sig123", thinkBlock["signature"])
	}
}

// TestIRAdapter_UsageExtraction verifies usage tokens are extracted.
func TestIRAdapter_UsageExtraction(t *testing.T) {
	geminiResp := map[string]any{
		"response": map[string]any{
			"candidates": []any{
				map[string]any{
					"content":      map[string]any{"parts": []any{map[string]any{"text": "hi"}}},
					"finishReason": "STOP",
				},
			},
			"usageMetadata": map[string]any{
				"promptTokenCount":        float64(100),
				"candidatesTokenCount":    float64(50),
				"cachedContentTokenCount": float64(20),
				"thoughtsTokenCount":      float64(10),
			},
		},
	}

	prompt, completion, cached, thought := irExtractUsage(geminiResp)
	if prompt != 100 {
		t.Errorf("prompt = %d, want 100", prompt)
	}
	if completion != 50 {
		t.Errorf("completion = %d, want 50", completion)
	}
	if cached != 20 {
		t.Errorf("cached = %d, want 20", cached)
	}
	if thought != 10 {
		t.Errorf("thought = %d, want 10", thought)
	}
}

// TestIRAdapter_OpenAISSEChunk verifies IR-based OpenAI SSE chunk encoding.
func TestIRAdapter_OpenAISSEChunk(t *testing.T) {
	ev := ir.StreamEvent{Type: ir.StreamTextDelta, Delta: "Hello"}
	chunk := irEncodeOpenAIStreamChunk(ev, "chatcmpl-123", 1700000000, "gpt-4", true)
	if chunk == "" {
		t.Fatal("empty chunk")
	}
	var parsed map[string]any
	if err := json.Unmarshal([]byte(chunk[6:]), &parsed); err != nil {
		t.Fatalf("parse: %v", err)
	}
	if parsed["object"] != "chat.completion.chunk" {
		t.Errorf("object = %v", parsed["object"])
	}
	choices, _ := parsed["choices"].([]any)
	choice, _ := choices[0].(map[string]any)
	delta, _ := choice["delta"].(map[string]any)
	if delta["role"] != "assistant" {
		t.Errorf("role = %v, want assistant", delta["role"])
	}
	if delta["content"] != "Hello" {
		t.Errorf("content = %v, want Hello", delta["content"])
	}
}
