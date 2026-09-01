// protocol_matrix_test.go — Exhaustive protocol feature coverage.
//
// This file tests EVERY feature of EVERY client protocol (OpenAI Chat,
// Anthropic Messages, OpenAI Responses) through the full Hydra IR
// pipeline: Decode → IR → EncodeGeminiRequest → (simulated Gemini
// response) → DecodeGeminiResponse → Encode back to client protocol.
//
// The goal is to verify that each feature survives the round trip
// or is explicitly documented as dropped.

package ir

import (
	"encoding/json"
	"testing"
)

// =====================================================================
// HELPERS
// =====================================================================

// roundTripOpenAI sends an OpenAI request through the full pipeline
// and returns the OpenAI response.
func roundTripOpenAI(t *testing.T, openaiReq map[string]any, geminiResp map[string]any) map[string]any {
	t.Helper()
	irReq := DecodeOpenAIChat(openaiReq)
	if m, ok := openaiReq["model"].(string); ok {
		irReq.Model = m
	} else {
		irReq.Model = "gemini-2.5-flash"
	}
	geminiBody := EncodeGeminiRequest(irReq, "test-project", "test-session", 1)
	_ = geminiBody // would be sent to upstream
	irResp := DecodeGeminiResponse(geminiResp, irReq.Model)
	return EncodeOpenAIChat(irResp)
}

// roundTripAnthropic sends an Anthropic request through the full pipeline.
func roundTripAnthropic(t *testing.T, anthropicReq map[string]any, geminiResp map[string]any) map[string]any {
	t.Helper()
	irReq := DecodeAnthropic(anthropicReq)
	if m, ok := anthropicReq["model"].(string); ok {
		irReq.Model = m
	} else {
		irReq.Model = "gemini-2.5-flash"
	}
	_ = EncodeGeminiRequest(irReq, "test-project", "test-session", 1)
	irResp := DecodeGeminiResponse(geminiResp, irReq.Model)
	return EncodeAnthropic(irResp)
}

// roundTripResponses sends a Responses API request through the full pipeline.
func roundTripResponses(t *testing.T, responsesReq map[string]any, geminiResp map[string]any) map[string]any {
	t.Helper()
	irReq := DecodeResponses(responsesReq)
	if m, ok := responsesReq["model"].(string); ok {
		irReq.Model = m
	} else {
		irReq.Model = "gemini-2.5-flash"
	}
	_ = EncodeGeminiRequest(irReq, "test-project", "test-session", 1)
	irResp := DecodeGeminiResponse(geminiResp, irReq.Model)
	return EncodeResponses(irResp)
}

// simpleGeminiTextResponse builds a minimal Gemini response with text.
func simpleGeminiTextResponse(text string) map[string]any {
	return map[string]any{
		"response": map[string]any{
			"candidates": []any{
				map[string]any{
					"content": map[string]any{
						"parts": []any{
							map[string]any{"text": text},
						},
					},
					"finishReason": "STOP",
				},
			},
			"usageMetadata": map[string]any{
				"promptTokenCount":     float64(10),
				"candidatesTokenCount": float64(20),
			},
		},
	}
}

// geminiToolCallResponse builds a Gemini response with a function call.
func geminiToolCallResponse(name string, args map[string]any) map[string]any {
	return map[string]any{
		"response": map[string]any{
			"candidates": []any{
				map[string]any{
					"content": map[string]any{
						"parts": []any{
							map[string]any{
								"functionCall": map[string]any{
									"name": name,
									"args": args,
									"id":   "call_123",
								},
							},
						},
					},
					"finishReason": "STOP",
				},
			},
		},
	}
}

// geminiThinkingResponse builds a Gemini response with thought text.
func geminiThinkingResponse(thought, text string) map[string]any {
	return map[string]any{
		"response": map[string]any{
			"candidates": []any{
				map[string]any{
					"content": map[string]any{
						"parts": []any{
							map[string]any{"text": thought, "thought": true},
							map[string]any{"text": text},
						},
					},
					"finishReason": "STOP",
				},
			},
		},
	}
}

// geminiGroundingResponse builds a Gemini response with grounding metadata.
func geminiGroundingResponse(text string, sources []map[string]any) map[string]any {
	chunks := make([]any, len(sources))
	for i, s := range sources {
		chunks[i] = map[string]any{"web": s}
	}
	return map[string]any{
		"response": map[string]any{
			"candidates": []any{
				map[string]any{
					"content": map[string]any{
						"parts": []any{map[string]any{"text": text}},
					},
					"finishReason": "STOP",
					"groundingMetadata": map[string]any{
						"webSearchQueries": []any{"test query"},
						"groundingChunks":  chunks,
					},
				},
			},
		},
	}
}

// =====================================================================
// SECTION 1: OpenAI Chat Completions — Request Field Decode
// =====================================================================

func TestMatrix_OpenAI_BasicFields(t *testing.T) {
	req := map[string]any{
		"model":       "gpt-4",
		"max_tokens":  float64(1000),
		"temperature": float64(0.7),
		"top_p":       float64(0.9),
		"stop":        []any{"stop1", "stop2"},
		"stream":      true,
		"messages": []any{
			map[string]any{"role": "system", "content": "You are helpful."},
			map[string]any{"role": "user", "content": "Hello"},
		},
	}
	r := DecodeOpenAIChat(req)
	assertEqual(t, "model", r.Model, "gpt-4")
	assertEqual(t, "max_tokens", r.MaxTokens, int64(1000))
	if r.Temperature == nil || *r.Temperature != 0.7 {
		t.Errorf("temperature = %v, want 0.7", r.Temperature)
	}
	if r.TopP == nil || *r.TopP != 0.9 {
		t.Errorf("top_p = %v, want 0.9", r.TopP)
	}
	assertEqual(t, "stop len", len(r.StopSequences), 2)
	assertEqual(t, "stop[0]", r.StopSequences[0], "stop1")
	if !r.Stream {
		t.Error("stream should be true")
	}
	assertEqual(t, "system", r.System, "You are helpful.")
	assertEqual(t, "messages", len(r.Messages), 1)
}

func TestMatrix_OpenAI_StopAsString(t *testing.T) {
	req := map[string]any{
		"model":    "gpt-4",
		"stop":     "END",
		"messages": []any{map[string]any{"role": "user", "content": "hi"}},
	}
	r := DecodeOpenAIChat(req)
	assertEqual(t, "stop len", len(r.StopSequences), 1)
	assertEqual(t, "stop[0]", r.StopSequences[0], "END")
}

func TestMatrix_OpenAI_ReasoningEffort(t *testing.T) {
	req := map[string]any{
		"model":            "o1",
		"reasoning_effort": "high",
		"messages":         []any{map[string]any{"role": "user", "content": "hi"}},
	}
	r := DecodeOpenAIChat(req)
	if r.Reasoning == nil {
		t.Fatal("Reasoning is nil")
	}
	assertEqual(t, "effort", r.Reasoning.Effort, "high")
}

func TestMatrix_OpenAI_ResponseFormat_JSONObject(t *testing.T) {
	req := map[string]any{
		"model":           "gpt-4",
		"response_format": map[string]any{"type": "json_object"},
		"messages":        []any{map[string]any{"role": "user", "content": "hi"}},
	}
	r := DecodeOpenAIChat(req)
	if r.ResponseFormat == nil {
		t.Fatal("ResponseFormat is nil")
	}
	assertEqual(t, "type", r.ResponseFormat.Type, "json_object")
}

func TestMatrix_OpenAI_ResponseFormat_JSONSchema(t *testing.T) {
	schema := map[string]any{"type": "object", "properties": map[string]any{"name": map[string]any{"type": "string"}}}
	req := map[string]any{
		"model": "gpt-4",
		"response_format": map[string]any{
			"type":        "json_schema",
			"json_schema": map[string]any{"name": "my_schema", "schema": schema},
		},
		"messages": []any{map[string]any{"role": "user", "content": "hi"}},
	}
	r := DecodeOpenAIChat(req)
	if r.ResponseFormat == nil {
		t.Fatal("ResponseFormat is nil")
	}
	assertEqual(t, "type", r.ResponseFormat.Type, "json_schema")
	if r.ResponseFormat.Schema == nil {
		t.Fatal("Schema is nil")
	}
}

func TestMatrix_OpenAI_PassthroughExtras(t *testing.T) {
	req := map[string]any{
		"model":               "gpt-4",
		"parallel_tool_calls": true,
		"user":                "user-123",
		"metadata":            map[string]any{"session": "abc"},
		"seed":                float64(42),
		"n":                   float64(3),
		"messages":            []any{map[string]any{"role": "user", "content": "hi"}},
	}
	r := DecodeOpenAIChat(req)
	if r.Extra["parallel_tool_calls"] != true {
		t.Error("parallel_tool_calls not passed through")
	}
	if r.Extra["user"] != "user-123" {
		t.Error("user not passed through")
	}
	if r.Extra["seed"] != float64(42) {
		t.Error("seed not passed through")
	}
	if r.Extra["n"] != float64(3) {
		t.Error("n not passed through")
	}
}

func TestMatrix_OpenAI_ToolChoice(t *testing.T) {
	req := map[string]any{
		"model":       "gpt-4",
		"tool_choice": "auto",
		"tools": []any{
			map[string]any{"type": "function", "function": map[string]any{"name": "get_weather", "parameters": map[string]any{}}},
		},
		"messages": []any{map[string]any{"role": "user", "content": "hi"}},
	}
	r := DecodeOpenAIChat(req)
	if r.ToolChoice == nil {
		t.Fatal("ToolChoice is nil")
	}
	assertEqual(t, "tool_choice", r.ToolChoice, "auto")
}

func TestMatrix_OpenAI_ImageContent(t *testing.T) {
	req := map[string]any{
		"model": "gpt-4",
		"messages": []any{
			map[string]any{
				"role": "user",
				"content": []any{
					map[string]any{"type": "text", "text": "What's in this image?"},
					map[string]any{"type": "image_url", "image_url": map[string]any{"url": "data:image/png;base64,iVBORw0KG="}},
				},
			},
		},
	}
	r := DecodeOpenAIChat(req)
	if len(r.Messages) != 1 || len(r.Messages[0].Content) != 2 {
		t.Fatalf("content = %d items, want 2", len(r.Messages[0].Content))
	}
	if r.Messages[0].Content[1].Type != ContentImage {
		t.Errorf("content[1] type = %v, want ContentImage", r.Messages[0].Content[1].Type)
	}
	if r.Messages[0].Content[1].Image.MimeType != "image/png" {
		t.Errorf("mime = %s, want image/png", r.Messages[0].Content[1].Image.MimeType)
	}
}

func TestMatrix_OpenAI_AudioContent(t *testing.T) {
	req := map[string]any{
		"model": "gpt-4",
		"messages": []any{
			map[string]any{
				"role": "user",
				"content": []any{
					map[string]any{"type": "input_audio", "input_audio": map[string]any{"data": "base64data", "format": "mp3"}},
				},
			},
		},
	}
	r := DecodeOpenAIChat(req)
	if r.Messages[0].Content[0].Type != ContentAudio {
		t.Errorf("type = %v, want ContentAudio", r.Messages[0].Content[0].Type)
	}
	assertEqual(t, "mime", r.Messages[0].Content[0].Audio.MimeType, "audio/mpeg")
}

func TestMatrix_OpenAI_ToolCallInMessage(t *testing.T) {
	req := map[string]any{
		"model": "gpt-4",
		"messages": []any{
			map[string]any{
				"role": "assistant",
				"tool_calls": []any{
					map[string]any{
						"id": "call_1",
						"function": map[string]any{
							"name":      "get_weather",
							"arguments": `{"city":"SF"}`,
						},
					},
				},
			},
			map[string]any{
				"role":         "tool",
				"tool_call_id": "call_1",
				"name":         "get_weather",
				"content":      "Sunny 72F",
			},
		},
	}
	r := DecodeOpenAIChat(req)
	// Assistant message should have a tool call.
	if len(r.Messages) != 2 {
		t.Fatalf("messages = %d, want 2", len(r.Messages))
	}
	if r.Messages[0].Content[0].Type != ContentToolCall {
		t.Errorf("msg[0] type = %v, want ContentToolCall", r.Messages[0].Content[0].Type)
	}
	tc := r.Messages[0].Content[0].ToolCall
	assertEqual(t, "id", tc.ID, "call_1")
	assertEqual(t, "name", tc.Name, "get_weather")
	assertEqual(t, "raw_args", tc.RawArgs, `{"city":"SF"}`)
	// Tool message should have a tool result.
	if r.Messages[1].Content[0].Type != ContentToolResult {
		t.Errorf("msg[1] type = %v, want ContentToolResult", r.Messages[1].Content[0].Type)
	}
	tr := r.Messages[1].Content[0].ToolResult
	assertEqual(t, "result id", tr.ID, "call_1")
	assertEqual(t, "result name", tr.Name, "get_weather")
	assertEqual(t, "result content", tr.Content, "Sunny 72F")
}

func TestMatrix_OpenAI_ToolNameRecoveryFromID(t *testing.T) {
	req := map[string]any{
		"model": "gpt-4",
		"messages": []any{
			map[string]any{
				"role": "assistant",
				"tool_calls": []any{
					map[string]any{
						"id":       "call_42",
						"function": map[string]any{"name": "search", "arguments": "{}"},
					},
				},
			},
			map[string]any{
				"role":         "tool",
				"tool_call_id": "call_42",
				"content":      "results",
			},
		},
	}
	r := DecodeOpenAIChat(req)
	// Tool message omits name; should be recovered from tool_calls.
	tr := r.Messages[1].Content[0].ToolResult
	assertEqual(t, "recovered name", tr.Name, "search")
}

func TestMatrix_OpenAI_ReasoningContentInMessage(t *testing.T) {
	// When content is a string, the decoder returns early and does NOT
	// process reasoning_content. This is a known limitation.
	// Use array content to get both reasoning + text.
	req := map[string]any{
		"model": "o1",
		"messages": []any{
			map[string]any{
				"role":              "assistant",
				"reasoning_content": "Let me think about this...",
				"content": []any{
					map[string]any{"type": "text", "text": "The answer is 42."},
				},
			},
		},
	}
	r := DecodeOpenAIChat(req)
	// Should have both thinking and text content.
	foundThinking := false
	foundText := false
	for _, c := range r.Messages[0].Content {
		if c.Type == ContentThinking && c.Thinking.Text == "Let me think about this..." {
			foundThinking = true
		}
		if c.Type == ContentText && c.Text == "The answer is 42." {
			foundText = true
		}
	}
	if !foundThinking {
		t.Error("thinking content not found")
	}
	if !foundText {
		t.Error("text content not found")
	}
}

// =====================================================================
// SECTION 2: OpenAI Chat — All Tool Types
// =====================================================================

func TestMatrix_OpenAI_AllToolTypes(t *testing.T) {
	tools := []any{
		map[string]any{"type": "function", "function": map[string]any{"name": "my_func"}},
		map[string]any{"type": "local_shell"},
		map[string]any{"type": "shell"},
		map[string]any{"type": "apply_patch"},
		map[string]any{"type": "mcp"},
		map[string]any{"type": "computer"},
		map[string]any{"type": "web_search"},
		map[string]any{"type": "file_search"},
		map[string]any{"type": "code_interpreter"},
	}
	req := map[string]any{
		"model":    "gpt-4",
		"tools":    tools,
		"messages": []any{map[string]any{"role": "user", "content": "hi"}},
	}
	r := DecodeOpenAIChat(req)
	if len(r.Tools) != 9 {
		t.Fatalf("tools = %d, want 9", len(r.Tools))
	}
	expectedKinds := []ToolKind{
		ToolFunction, ToolLocalShell, ToolShell, ToolApplyPatch,
		ToolMCP, ToolComputer, ToolWebSearch, ToolFileSearch, ToolCodeInterpreter,
	}
	for i, want := range expectedKinds {
		if r.Tools[i].Kind != want {
			t.Errorf("tool[%d] kind = %v, want %v", i, r.Tools[i].Kind, want)
		}
	}
}

// =====================================================================
// SECTION 3: OpenAI Chat — Response Encode
// =====================================================================

func TestMatrix_OpenAI_Response_Text(t *testing.T) {
	resp := roundTripOpenAI(t,
		map[string]any{"model": "gpt-4", "messages": []any{map[string]any{"role": "user", "content": "hi"}}},
		simpleGeminiTextResponse("Hello!"),
	)
	choices, _ := resp["choices"].([]any)
	choice, _ := choices[0].(map[string]any)
	msg, _ := choice["message"].(map[string]any)
	assertEqual(t, "content", msg["content"], "Hello!")
	assertEqual(t, "finish_reason", choice["finish_reason"], "stop")
}

func TestMatrix_OpenAI_Response_ToolCall(t *testing.T) {
	resp := roundTripOpenAI(t,
		map[string]any{"model": "gpt-4", "messages": []any{map[string]any{"role": "user", "content": "weather?"}}},
		geminiToolCallResponse("get_weather", map[string]any{"city": "SF"}),
	)
	choices, _ := resp["choices"].([]any)
	choice, _ := choices[0].(map[string]any)
	msg, _ := choice["message"].(map[string]any)
	toolCalls, _ := msg["tool_calls"].([]any)
	if len(toolCalls) != 1 {
		t.Fatalf("tool_calls = %d, want 1", len(toolCalls))
	}
	tc, _ := toolCalls[0].(map[string]any)
	fn, _ := tc["function"].(map[string]any)
	assertEqual(t, "name", fn["name"], "get_weather")
	assertEqual(t, "finish_reason", choice["finish_reason"], "tool_calls")
}

func TestMatrix_OpenAI_Response_ReasoningContent(t *testing.T) {
	resp := roundTripOpenAI(t,
		map[string]any{"model": "o1", "messages": []any{map[string]any{"role": "user", "content": "think"}}},
		geminiThinkingResponse("Let me think...", "The answer is 42."),
	)
	choices, _ := resp["choices"].([]any)
	choice, _ := choices[0].(map[string]any)
	msg, _ := choice["message"].(map[string]any)
	assertEqual(t, "reasoning_content", msg["reasoning_content"], "Let me think...")
	assertEqual(t, "content", msg["content"], "The answer is 42.")
}

func TestMatrix_OpenAI_Response_Usage(t *testing.T) {
	resp := roundTripOpenAI(t,
		map[string]any{"model": "gpt-4", "messages": []any{map[string]any{"role": "user", "content": "hi"}}},
		simpleGeminiTextResponse("Hello!"),
	)
	usage, _ := resp["usage"].(map[string]any)
	assertEqual(t, "prompt_tokens", int64FromAny(usage["prompt_tokens"]), int64(10))
	assertEqual(t, "completion_tokens", int64FromAny(usage["completion_tokens"]), int64(20))
	assertEqual(t, "total_tokens", int64FromAny(usage["total_tokens"]), int64(30))
}

func TestMatrix_OpenAI_Response_WebSearch(t *testing.T) {
	resp := roundTripOpenAI(t,
		map[string]any{"model": "gpt-4", "messages": []any{map[string]any{"role": "user", "content": "search"}}},
		geminiGroundingResponse("Here are results.", []map[string]any{
			{"uri": "https://example.com", "title": "Example"},
		}),
	)
	choices, _ := resp["choices"].([]any)
	choice, _ := choices[0].(map[string]any)
	msg, _ := choice["message"].(map[string]any)
	content, _ := msg["content"].(string)
	if content == "" {
		t.Fatal("content is empty")
	}
	// OpenAI has no native web_search format; sources appended as text.
	if !contains(content, "Web search results:") {
		t.Errorf("content should contain 'Web search results:', got: %s", content[:minInt(80, len(content))])
	}
	if !contains(content, "https://example.com") {
		t.Errorf("content should contain URL")
	}
}

// =====================================================================
// SECTION 4: Anthropic Messages — Request Field Decode
// =====================================================================

func TestMatrix_Anthropic_BasicFields(t *testing.T) {
	req := map[string]any{
		"model":          "claude-3",
		"max_tokens":     float64(4096),
		"temperature":    float64(0.5),
		"top_p":          float64(0.95),
		"top_k":          float64(40),
		"stop_sequences": []any{"END"},
		"stream":         true,
		"system":         "You are helpful.",
		"messages": []any{
			map[string]any{"role": "user", "content": "Hello"},
		},
	}
	r := DecodeAnthropic(req)
	assertEqual(t, "model", r.Model, "claude-3")
	assertEqual(t, "max_tokens", r.MaxTokens, int64(4096))
	if r.Temperature == nil || *r.Temperature != 0.5 {
		t.Errorf("temperature = %v, want 0.5", r.Temperature)
	}
	if r.TopP == nil || *r.TopP != 0.95 {
		t.Errorf("top_p = %v, want 0.95", r.TopP)
	}
	if r.TopK == nil || *r.TopK != 40 {
		t.Errorf("top_k = %v, want 40", r.TopK)
	}
	assertEqual(t, "stop len", len(r.StopSequences), 1)
	assertEqual(t, "stop[0]", r.StopSequences[0], "END")
	if !r.Stream {
		t.Error("stream should be true")
	}
	assertEqual(t, "system", r.System, "You are helpful.")
}

func TestMatrix_Anthropic_SystemAsArray(t *testing.T) {
	req := map[string]any{
		"model":      "claude-3",
		"max_tokens": float64(100),
		"system": []any{
			map[string]any{"type": "text", "text": "Part 1"},
			map[string]any{"type": "text", "text": "Part 2"},
		},
		"messages": []any{map[string]any{"role": "user", "content": "hi"}},
	}
	r := DecodeAnthropic(req)
	assertEqual(t, "system", r.System, "Part 1\nPart 2")
}

func TestMatrix_Anthropic_ThinkingConfig(t *testing.T) {
	req := map[string]any{
		"model":      "claude-3",
		"max_tokens": float64(4096),
		"thinking": map[string]any{
			"type":          "enabled",
			"budget_tokens": float64(10000),
		},
		"messages": []any{map[string]any{"role": "user", "content": "hi"}},
	}
	r := DecodeAnthropic(req)
	if r.Reasoning == nil {
		t.Fatal("Reasoning is nil")
	}
	assertEqual(t, "effort", r.Reasoning.Effort, "high")
	assertEqual(t, "budget", r.Reasoning.BudgetTokens, int64(10000))
}

func TestMatrix_Anthropic_ThinkingAdaptive(t *testing.T) {
	req := map[string]any{
		"model":      "claude-3",
		"max_tokens": float64(4096),
		"thinking":   map[string]any{"type": "adaptive"},
		"messages":   []any{map[string]any{"role": "user", "content": "hi"}},
	}
	r := DecodeAnthropic(req)
	if r.Reasoning == nil {
		t.Fatal("Reasoning is nil")
	}
	assertEqual(t, "effort", r.Reasoning.Effort, "medium")
}

func TestMatrix_Anthropic_ToolUse(t *testing.T) {
	req := map[string]any{
		"model":      "claude-3",
		"max_tokens": float64(4096),
		"messages": []any{
			map[string]any{
				"role": "assistant",
				"content": []any{
					map[string]any{"type": "tool_use", "id": "toolu_1", "name": "search", "input": map[string]any{"q": "test"}},
				},
			},
			map[string]any{
				"role": "user",
				"content": []any{
					map[string]any{"type": "tool_result", "tool_use_id": "toolu_1", "content": "results here"},
				},
			},
		},
	}
	r := DecodeAnthropic(req)
	if r.Messages[0].Content[0].Type != ContentToolCall {
		t.Errorf("msg[0] type = %v, want ContentToolCall", r.Messages[0].Content[0].Type)
	}
	tc := r.Messages[0].Content[0].ToolCall
	assertEqual(t, "id", tc.ID, "toolu_1")
	assertEqual(t, "name", tc.Name, "search")
	if r.Messages[1].Content[0].Type != ContentToolResult {
		t.Errorf("msg[1] type = %v, want ContentToolResult", r.Messages[1].Content[0].Type)
	}
	tr := r.Messages[1].Content[0].ToolResult
	assertEqual(t, "result id", tr.ID, "toolu_1")
	assertEqual(t, "result content", tr.Content, "results here")
}

func TestMatrix_Anthropic_ThinkingBlock(t *testing.T) {
	req := map[string]any{
		"model":      "claude-3",
		"max_tokens": float64(4096),
		"messages": []any{
			map[string]any{
				"role": "assistant",
				"content": []any{
					map[string]any{"type": "thinking", "thinking": "My reasoning...", "signature": "sig_123"},
					map[string]any{"type": "text", "text": "The answer."},
				},
			},
		},
	}
	r := DecodeAnthropic(req)
	if r.Messages[0].Content[0].Type != ContentThinking {
		t.Errorf("type = %v, want ContentThinking", r.Messages[0].Content[0].Type)
	}
	assertEqual(t, "thinking text", r.Messages[0].Content[0].Thinking.Text, "My reasoning...")
	assertEqual(t, "signature", r.Messages[0].Content[0].Thinking.Signature, "sig_123")
}

func TestMatrix_Anthropic_RedactedThinking(t *testing.T) {
	req := map[string]any{
		"model":      "claude-3",
		"max_tokens": float64(4096),
		"messages": []any{
			map[string]any{
				"role": "assistant",
				"content": []any{
					map[string]any{"type": "redacted_thinking", "data": "encrypted_data_here"},
				},
			},
		},
	}
	r := DecodeAnthropic(req)
	if r.Messages[0].Content[0].Type != ContentThinking {
		t.Errorf("type = %v, want ContentThinking", r.Messages[0].Content[0].Type)
	}
	if !r.Messages[0].Content[0].Thinking.Redacted {
		t.Error("should be redacted")
	}
}

func TestMatrix_Anthropic_ImageBase64(t *testing.T) {
	req := map[string]any{
		"model":      "claude-3",
		"max_tokens": float64(4096),
		"messages": []any{
			map[string]any{
				"role": "user",
				"content": []any{
					map[string]any{
						"type": "image",
						"source": map[string]any{
							"type":       "base64",
							"media_type": "image/jpeg",
							"data":       "base64data",
						},
					},
				},
			},
		},
	}
	r := DecodeAnthropic(req)
	if r.Messages[0].Content[0].Type != ContentImage {
		t.Errorf("type = %v, want ContentImage", r.Messages[0].Content[0].Type)
	}
	assertEqual(t, "mime", r.Messages[0].Content[0].Image.MimeType, "image/jpeg")
	assertEqual(t, "data", r.Messages[0].Content[0].Image.Data, "base64data")
}

func TestMatrix_Anthropic_PassthroughExtras(t *testing.T) {
	req := map[string]any{
		"model":      "claude-3",
		"max_tokens": float64(100),
		"metadata":   map[string]any{"session": "abc"},
		"user_id":    "user-123",
		"messages":   []any{map[string]any{"role": "user", "content": "hi"}},
	}
	r := DecodeAnthropic(req)
	if r.Extra["metadata"] == nil {
		t.Error("metadata not passed through")
	}
	if r.Extra["user_id"] != "user-123" {
		t.Error("user_id not passed through")
	}
}

// =====================================================================
// SECTION 5: Anthropic — All Tool Types
// =====================================================================

func TestMatrix_Anthropic_AllToolTypes(t *testing.T) {
	tools := []any{
		map[string]any{"name": "my_func", "input_schema": map[string]any{}}, // function (no type)
		map[string]any{"type": "computer_20241022", "name": "computer"},
		map[string]any{"type": "web_search_20250305", "name": "web_search"},
		map[string]any{"type": "mcp", "name": "mcp_tool"},
	}
	req := map[string]any{
		"model":      "claude-3",
		"max_tokens": float64(100),
		"tools":      tools,
		"messages":   []any{map[string]any{"role": "user", "content": "hi"}},
	}
	r := DecodeAnthropic(req)
	if len(r.Tools) != 4 {
		t.Fatalf("tools = %d, want 4", len(r.Tools))
	}
	expectedKinds := []ToolKind{ToolFunction, ToolComputer, ToolWebSearch, ToolMCP}
	for i, want := range expectedKinds {
		if r.Tools[i].Kind != want {
			t.Errorf("tool[%d] kind = %v, want %v", i, r.Tools[i].Kind, want)
		}
	}
}

// =====================================================================
// SECTION 6: Anthropic — Response Encode
// =====================================================================

func TestMatrix_Anthropic_Response_Text(t *testing.T) {
	resp := roundTripAnthropic(t,
		map[string]any{"model": "claude-3", "max_tokens": float64(100), "messages": []any{map[string]any{"role": "user", "content": "hi"}}},
		simpleGeminiTextResponse("Hello!"),
	)
	blocks, _ := resp["content"].([]any)
	if len(blocks) != 1 {
		t.Fatalf("blocks = %d, want 1", len(blocks))
	}
	block, _ := blocks[0].(map[string]any)
	assertEqual(t, "type", block["type"], "text")
	assertEqual(t, "text", block["text"], "Hello!")
	assertEqual(t, "stop_reason", resp["stop_reason"], "end_turn")
}

func TestMatrix_Anthropic_Response_ToolUse(t *testing.T) {
	resp := roundTripAnthropic(t,
		map[string]any{"model": "claude-3", "max_tokens": float64(100), "messages": []any{map[string]any{"role": "user", "content": "weather?"}}},
		geminiToolCallResponse("get_weather", map[string]any{"city": "SF"}),
	)
	blocks, _ := resp["content"].([]any)
	found := false
	for _, bAny := range blocks {
		b, _ := bAny.(map[string]any)
		if b["type"] == "tool_use" {
			found = true
			assertEqual(t, "name", b["name"], "get_weather")
			input, _ := b["input"].(map[string]any)
			assertEqual(t, "city", input["city"], "SF")
		}
	}
	if !found {
		t.Error("tool_use block not found")
	}
	assertEqual(t, "stop_reason", resp["stop_reason"], "tool_use")
}

func TestMatrix_Anthropic_Response_Thinking(t *testing.T) {
	resp := roundTripAnthropic(t,
		map[string]any{"model": "claude-3", "max_tokens": float64(100), "messages": []any{map[string]any{"role": "user", "content": "think"}}},
		geminiThinkingResponse("My reasoning...", "The answer."),
	)
	blocks, _ := resp["content"].([]any)
	foundThinking := false
	foundText := false
	for _, bAny := range blocks {
		b, _ := bAny.(map[string]any)
		switch b["type"] {
		case "thinking":
			foundThinking = true
			assertEqual(t, "thinking", b["thinking"], "My reasoning...")
		case "text":
			foundText = true
			assertEqual(t, "text", b["text"], "The answer.")
		}
	}
	if !foundThinking {
		t.Error("thinking block not found")
	}
	if !foundText {
		t.Error("text block not found")
	}
}

func TestMatrix_Anthropic_Response_Usage(t *testing.T) {
	resp := roundTripAnthropic(t,
		map[string]any{"model": "claude-3", "max_tokens": float64(100), "messages": []any{map[string]any{"role": "user", "content": "hi"}}},
		simpleGeminiTextResponse("Hello!"),
	)
	usage, _ := resp["usage"].(map[string]any)
	assertEqual(t, "input_tokens", int64FromAny(usage["input_tokens"]), int64(10))
	assertEqual(t, "output_tokens", int64FromAny(usage["output_tokens"]), int64(20))
}

func TestMatrix_Anthropic_Response_WebSearchResult(t *testing.T) {
	resp := roundTripAnthropic(t,
		map[string]any{"model": "claude-3", "max_tokens": float64(100), "messages": []any{map[string]any{"role": "user", "content": "search"}}},
		geminiGroundingResponse("Here are results.", []map[string]any{
			{"uri": "https://example.com", "title": "Example"},
		}),
	)
	blocks, _ := resp["content"].([]any)
	found := false
	for _, bAny := range blocks {
		b, _ := bAny.(map[string]any)
		if b["type"] == "web_search_tool_result" {
			found = true
			results, _ := b["content"].([]any)
			if len(results) != 1 {
				t.Errorf("results = %d, want 1", len(results))
			}
			r, _ := results[0].(map[string]any)
			assertEqual(t, "url", r["url"], "https://example.com")
			assertEqual(t, "title", r["title"], "Example")
		}
	}
	if !found {
		t.Error("web_search_tool_result block not found")
	}
}

// =====================================================================
// SECTION 7: OpenAI Responses API — Request Field Decode
// =====================================================================

func TestMatrix_Responses_BasicFields(t *testing.T) {
	req := map[string]any{
		"model":             "o1",
		"max_output_tokens": float64(4096),
		"temperature":       float64(0.7),
		"top_p":             float64(0.9),
		"stream":            true,
		"instructions":      "You are helpful.",
		"input":             "Hello",
	}
	r := DecodeResponses(req)
	assertEqual(t, "model", r.Model, "o1")
	assertEqual(t, "max_tokens", r.MaxTokens, int64(4096))
	if r.Temperature == nil || *r.Temperature != 0.7 {
		t.Errorf("temperature = %v, want 0.7", r.Temperature)
	}
	if r.TopP == nil || *r.TopP != 0.9 {
		t.Errorf("top_p = %v, want 0.9", r.TopP)
	}
	if !r.Stream {
		t.Error("stream should be true")
	}
	assertEqual(t, "system", r.System, "You are helpful.")
	assertEqual(t, "messages", len(r.Messages), 1)
}

func TestMatrix_Responses_ReasoningEffort(t *testing.T) {
	req := map[string]any{
		"model":     "o1",
		"reasoning": map[string]any{"effort": "high"},
		"input":     "hi",
	}
	r := DecodeResponses(req)
	if r.Reasoning == nil {
		t.Fatal("Reasoning is nil")
	}
	assertEqual(t, "effort", r.Reasoning.Effort, "high")
}

func TestMatrix_Responses_TextFormat(t *testing.T) {
	req := map[string]any{
		"model": "o1",
		"text": map[string]any{
			"format": map[string]any{"type": "json_schema"},
		},
		"input": "hi",
	}
	r := DecodeResponses(req)
	if r.ResponseFormat == nil {
		t.Fatal("ResponseFormat is nil")
	}
	assertEqual(t, "type", r.ResponseFormat.Type, "json_schema")
}

func TestMatrix_Responses_ArrayInput(t *testing.T) {
	req := map[string]any{
		"model": "o1",
		"input": []any{
			map[string]any{
				"type":    "message",
				"role":    "user",
				"content": []any{map[string]any{"type": "input_text", "text": "Hello"}},
			},
		},
	}
	r := DecodeResponses(req)
	if len(r.Messages) != 1 {
		t.Fatalf("messages = %d, want 1", len(r.Messages))
	}
	assertEqual(t, "role", r.Messages[0].Role, "user")
	assertEqual(t, "text", r.Messages[0].Content[0].Text, "Hello")
}

func TestMatrix_Responses_FunctionCallItem(t *testing.T) {
	req := map[string]any{
		"model": "o1",
		"input": []any{
			map[string]any{"type": "message", "role": "user", "content": "weather?"},
			map[string]any{"type": "function_call", "call_id": "fc_1", "name": "get_weather", "arguments": `{"city":"SF"}`},
			map[string]any{"type": "function_call_output", "call_id": "fc_1", "output": "Sunny 72F"},
		},
	}
	r := DecodeResponses(req)
	if len(r.Messages) != 3 {
		t.Fatalf("messages = %d, want 3", len(r.Messages))
	}
	// Message 1: tool call
	if r.Messages[1].Content[0].Type != ContentToolCall {
		t.Errorf("msg[1] type = %v, want ContentToolCall", r.Messages[1].Content[0].Type)
	}
	tc := r.Messages[1].Content[0].ToolCall
	assertEqual(t, "id", tc.ID, "fc_1")
	assertEqual(t, "name", tc.Name, "get_weather")
	// Message 2: tool result
	if r.Messages[2].Content[0].Type != ContentToolResult {
		t.Errorf("msg[2] type = %v, want ContentToolResult", r.Messages[2].Content[0].Type)
	}
	tr := r.Messages[2].Content[0].ToolResult
	assertEqual(t, "id", tr.ID, "fc_1")
	assertEqual(t, "content", tr.Content, "Sunny 72F")
}

func TestMatrix_Responses_LocalShellCall(t *testing.T) {
	req := map[string]any{
		"model": "o1",
		"input": []any{
			map[string]any{"type": "message", "role": "user", "content": "run ls"},
			map[string]any{
				"type":    "local_shell_call",
				"call_id": "lsc_1",
				"action":  map[string]any{"type": "exec", "command": []any{"ls", "-la"}},
			},
			map[string]any{
				"type":    "local_shell_call_output",
				"call_id": "lsc_1",
				"output":  "file1.txt\nfile2.txt",
			},
		},
	}
	r := DecodeResponses(req)
	if len(r.Messages) != 3 {
		t.Fatalf("messages = %d, want 3", len(r.Messages))
	}
	if r.Messages[1].Content[0].Type != ContentToolCall {
		t.Errorf("msg[1] type = %v, want ContentToolCall", r.Messages[1].Content[0].Type)
	}
	assertEqual(t, "name", r.Messages[1].Content[0].ToolCall.Name, "local_shell")
	if r.Messages[2].Content[0].Type != ContentToolResult {
		t.Errorf("msg[2] type = %v, want ContentToolResult", r.Messages[2].Content[0].Type)
	}
	assertEqual(t, "name", r.Messages[2].Content[0].ToolResult.Name, "local_shell")
}

func TestMatrix_Responses_ReasoningItem(t *testing.T) {
	req := map[string]any{
		"model": "o1",
		"input": []any{
			map[string]any{
				"type": "reasoning",
				"content": []any{
					map[string]any{"type": "reasoning_text", "text": "Thinking..."},
				},
			},
		},
	}
	r := DecodeResponses(req)
	if len(r.Messages) != 1 {
		t.Fatalf("messages = %d, want 1", len(r.Messages))
	}
	if r.Messages[0].Content[0].Type != ContentThinking {
		t.Errorf("type = %v, want ContentThinking", r.Messages[0].Content[0].Type)
	}
	assertEqual(t, "text", r.Messages[0].Content[0].Thinking.Text, "Thinking...")
}

func TestMatrix_Responses_PassthroughExtras(t *testing.T) {
	req := map[string]any{
		"model":                "o1",
		"input":                "hi",
		"store":                false,
		"truncation":           "disabled",
		"previous_response_id": "resp_123",
		"parallel_tool_calls":  true,
		"metadata":             map[string]any{"session": "abc"},
	}
	r := DecodeResponses(req)
	if r.Extra["store"] != false {
		t.Error("store not passed through")
	}
	if r.Extra["truncation"] != "disabled" {
		t.Error("truncation not passed through")
	}
	if r.Extra["previous_response_id"] != "resp_123" {
		t.Error("previous_response_id not passed through")
	}
}

// =====================================================================
// SECTION 8: Responses API — All Tool Types
// =====================================================================

func TestMatrix_Responses_AllToolTypes(t *testing.T) {
	tools := []any{
		map[string]any{"type": "function", "name": "my_func"},
		map[string]any{"type": "local_shell"},
		map[string]any{"type": "shell"},
		map[string]any{"type": "apply_patch"},
		map[string]any{"type": "mcp", "server_label": "my_mcp"},
		map[string]any{"type": "web_search"},
		map[string]any{"type": "file_search"},
		map[string]any{"type": "computer_use"},
		map[string]any{"type": "code_interpreter"},
	}
	req := map[string]any{
		"model": "o1",
		"input": "hi",
		"tools": tools,
	}
	r := DecodeResponses(req)
	if len(r.Tools) != 9 {
		t.Fatalf("tools = %d, want 9", len(r.Tools))
	}
	expectedKinds := []ToolKind{
		ToolFunction, ToolLocalShell, ToolShell, ToolApplyPatch,
		ToolMCP, ToolWebSearch, ToolFileSearch, ToolComputer, ToolCodeInterpreter,
	}
	for i, want := range expectedKinds {
		if r.Tools[i].Kind != want {
			t.Errorf("tool[%d] kind = %v, want %v", i, r.Tools[i].Kind, want)
		}
	}
}

// =====================================================================
// SECTION 9: Responses API — Response Encode
// =====================================================================

func TestMatrix_Responses_Response_Text(t *testing.T) {
	resp := roundTripResponses(t,
		map[string]any{"model": "o1", "input": "hi"},
		simpleGeminiTextResponse("Hello!"),
	)
	output, _ := resp["output"].([]any)
	if len(output) != 1 {
		t.Fatalf("output = %d items, want 1", len(output))
	}
	item, _ := output[0].(map[string]any)
	assertEqual(t, "type", item["type"], "message")
	content, _ := item["content"].([]any)
	c, _ := content[0].(map[string]any)
	assertEqual(t, "text", c["text"], "Hello!")
	assertEqual(t, "status", resp["status"], "completed")
}

func TestMatrix_Responses_Response_FunctionCall(t *testing.T) {
	resp := roundTripResponses(t,
		map[string]any{"model": "o1", "input": "weather?"},
		geminiToolCallResponse("get_weather", map[string]any{"city": "SF"}),
	)
	output, _ := resp["output"].([]any)
	found := false
	for _, itemAny := range output {
		item, _ := itemAny.(map[string]any)
		if item["type"] == "function_call" {
			found = true
			assertEqual(t, "name", item["name"], "get_weather")
		}
	}
	if !found {
		t.Error("function_call item not found")
	}
}

func TestMatrix_Responses_Response_Reasoning(t *testing.T) {
	resp := roundTripResponses(t,
		map[string]any{"model": "o1", "input": "think"},
		geminiThinkingResponse("My reasoning...", "The answer."),
	)
	output, _ := resp["output"].([]any)
	foundReasoning := false
	foundMessage := false
	for _, itemAny := range output {
		item, _ := itemAny.(map[string]any)
		switch item["type"] {
		case "reasoning":
			foundReasoning = true
		case "message":
			foundMessage = true
		}
	}
	if !foundReasoning {
		t.Error("reasoning item not found")
	}
	if !foundMessage {
		t.Error("message item not found")
	}
}

func TestMatrix_Responses_Response_Usage(t *testing.T) {
	resp := roundTripResponses(t,
		map[string]any{"model": "o1", "input": "hi"},
		simpleGeminiTextResponse("Hello!"),
	)
	usage, _ := resp["usage"].(map[string]any)
	assertEqual(t, "input_tokens", int64FromAny(usage["input_tokens"]), int64(10))
	assertEqual(t, "output_tokens", int64FromAny(usage["output_tokens"]), int64(20))
}

// =====================================================================
// SECTION 10: Gemini Request Encode — All Features
// =====================================================================

func TestMatrix_Gemini_SystemInstruction(t *testing.T) {
	req := &Request{
		Model:  "gemini-2.5-flash",
		System: "You are a helpful assistant.",
		Messages: []Message{
			{Role: "user", Content: []Content{{Type: ContentText, Text: "hi"}}},
		},
	}
	body := encodeGeminiBody(req)
	sysInst, _ := body["systemInstruction"].(map[string]any)
	if sysInst == nil {
		t.Fatal("systemInstruction is nil")
	}
	parts, _ := sysInst["parts"].([]any)
	part, _ := parts[0].(map[string]any)
	assertEqual(t, "text", part["text"], "You are a helpful assistant.")
}

func TestMatrix_Gemini_GenerationConfig(t *testing.T) {
	temp := 0.5
	topP := 0.9
	topK := int64(20)
	req := &Request{
		Model:         "gemini-2.5-flash",
		MaxTokens:     4096,
		Temperature:   &temp,
		TopP:          &topP,
		TopK:          &topK,
		StopSequences: []string{"END"},
		Messages:      []Message{{Role: "user", Content: []Content{{Type: ContentText, Text: "hi"}}}},
	}
	body := encodeGeminiBody(req)
	genConfig, _ := body["generationConfig"].(map[string]any)
	assertEqual(t, "temperature", genConfig["temperature"], 0.5)
	assertEqual(t, "topP", genConfig["topP"], 0.9)
	assertEqual(t, "topK", genConfig["topK"], int64(20))
	assertEqual(t, "maxOutputTokens", genConfig["maxOutputTokens"], int64(4096))
	stopSeqs, _ := genConfig["stopSequences"].([]string)
	assertEqual(t, "stopSequences len", len(stopSeqs), 1)
}

func TestMatrix_Gemini_ThinkingConfig(t *testing.T) {
	req := &Request{
		Model:     "gemini-2.5-flash",
		Reasoning: &Reasoning{Effort: "high", BudgetTokens: 8000},
		Messages:  []Message{{Role: "user", Content: []Content{{Type: ContentText, Text: "hi"}}}},
	}
	body := encodeGeminiBody(req)
	genConfig, _ := body["generationConfig"].(map[string]any)
	thinkConfig, _ := genConfig["thinkingConfig"].(map[string]any)
	if thinkConfig == nil {
		t.Fatal("thinkingConfig is nil")
	}
	if thinkConfig["includeThoughts"] != true {
		t.Error("includeThoughts should be true")
	}
	assertEqual(t, "thinkingBudget", thinkConfig["thinkingBudget"], int64(8000))
}

func TestMatrix_Gemini_ResponseFormat_JSON(t *testing.T) {
	req := &Request{
		Model:          "gemini-2.5-flash",
		ResponseFormat: &ResponseFormat{Type: "json_object"},
		Messages:       []Message{{Role: "user", Content: []Content{{Type: ContentText, Text: "hi"}}}},
	}
	body := encodeGeminiBody(req)
	genConfig, _ := body["generationConfig"].(map[string]any)
	assertEqual(t, "responseMimeType", genConfig["responseMimeType"], "application/json")
}

func TestMatrix_Gemini_SafetySettings(t *testing.T) {
	req := &Request{
		Model:    "gemini-2.5-flash",
		Messages: []Message{{Role: "user", Content: []Content{{Type: ContentText, Text: "hi"}}}},
	}
	body := encodeGeminiBody(req)
	safety, _ := body["safetySettings"].([]any)
	if len(safety) != 4 {
		t.Fatalf("safetySettings = %d, want 4", len(safety))
	}
	for _, sAny := range safety {
		s, _ := sAny.(map[string]any)
		if s["threshold"] != "OFF" {
			t.Errorf("threshold = %v, want OFF", s["threshold"])
		}
	}
}

func TestMatrix_Gemini_ToolCallInMessage(t *testing.T) {
	req := &Request{
		Model: "gemini-2.5-flash",
		Messages: []Message{
			{Role: "assistant", Content: []Content{{
				Type:     ContentToolCall,
				ToolCall: &ToolCall{ID: "call_1", Name: "search", Args: map[string]any{"q": "test"}},
			}}},
		},
	}
	body := encodeGeminiBody(req)
	contents, _ := body["contents"].([]any)
	// ensureValidFirstTurn prepends a user pad when first message is model.
	if len(contents) != 2 {
		t.Fatalf("contents = %d, want 2 (pad + model)", len(contents))
	}
	c, _ := contents[1].(map[string]any)
	assertEqual(t, "role", c["role"], "model")
	parts, _ := c["parts"].([]any)
	part, _ := parts[0].(map[string]any)
	fc, _ := part["functionCall"].(map[string]any)
	assertEqual(t, "name", fc["name"], "search")
	args, _ := fc["args"].(map[string]any)
	assertEqual(t, "q", args["q"], "test")
}

func TestMatrix_Gemini_ToolResultInMessage(t *testing.T) {
	req := &Request{
		Model: "gemini-2.5-flash",
		Messages: []Message{
			{Role: "tool", Content: []Content{{
				Type:       ContentToolResult,
				ToolResult: &ToolResult{ID: "call_1", Name: "search", Content: "results"},
			}}},
		},
	}
	body := encodeGeminiBody(req)
	contents, _ := body["contents"].([]any)
	c, _ := contents[0].(map[string]any)
	// tool role maps to user in Gemini.
	assertEqual(t, "role", c["role"], "user")
	parts, _ := c["parts"].([]any)
	part, _ := parts[0].(map[string]any)
	fr, _ := part["functionResponse"].(map[string]any)
	assertEqual(t, "name", fr["name"], "search")
	resp, _ := fr["response"].(map[string]any)
	assertEqual(t, "output", resp["output"], "results")
}

func TestMatrix_Gemini_ToolResultError(t *testing.T) {
	req := &Request{
		Model: "gemini-2.5-flash",
		Messages: []Message{
			{Role: "tool", Content: []Content{{
				Type:       ContentToolResult,
				ToolResult: &ToolResult{ID: "call_1", Name: "search", Content: "not found", IsError: true},
			}}},
		},
	}
	body := encodeGeminiBody(req)
	contents, _ := body["contents"].([]any)
	c, _ := contents[0].(map[string]any)
	parts, _ := c["parts"].([]any)
	part, _ := parts[0].(map[string]any)
	fr, _ := part["functionResponse"].(map[string]any)
	resp, _ := fr["response"].(map[string]any)
	if _, ok := resp["error"]; !ok {
		t.Error("error field not set in functionResponse")
	}
}

func TestMatrix_Gemini_ImageInMessage(t *testing.T) {
	req := &Request{
		Model: "gemini-2.5-flash",
		Messages: []Message{
			{Role: "user", Content: []Content{{
				Type:  ContentImage,
				Image: &Image{MimeType: "image/png", Data: "base64data"},
			}}},
		},
	}
	body := encodeGeminiBody(req)
	contents, _ := body["contents"].([]any)
	c, _ := contents[0].(map[string]any)
	parts, _ := c["parts"].([]any)
	part, _ := parts[0].(map[string]any)
	inline, _ := part["inlineData"].(map[string]any)
	assertEqual(t, "mimeType", inline["mimeType"], "image/png")
	assertEqual(t, "data", inline["data"], "base64data")
}

func TestMatrix_Gemini_ThinkingInMessage(t *testing.T) {
	req := &Request{
		Model: "gemini-2.5-flash",
		Messages: []Message{
			{Role: "assistant", Content: []Content{{
				Type:     ContentThinking,
				Thinking: &Thinking{Text: "My thoughts...", Signature: "sig_123"},
			}}},
		},
	}
	body := encodeGeminiBody(req)
	contents, _ := body["contents"].([]any)
	// ensureValidFirstTurn prepends a user pad when first message is model.
	if len(contents) != 2 {
		t.Fatalf("contents = %d, want 2 (pad + model)", len(contents))
	}
	c, _ := contents[1].(map[string]any)
	parts, _ := c["parts"].([]any)
	part, _ := parts[0].(map[string]any)
	if part["thought"] != true {
		t.Error("thought should be true")
	}
	assertEqual(t, "text", part["text"], "My thoughts...")
	assertEqual(t, "thoughtSignature", part["thoughtSignature"], "sig_123")
}

func TestMatrix_Gemini_LocalShellTool(t *testing.T) {
	req := &Request{
		Model:    "gemini-2.5-flash",
		Tools:    []Tool{{Kind: ToolLocalShell, Name: "local_shell", Description: "Run shell"}},
		Messages: []Message{{Role: "user", Content: []Content{{Type: ContentText, Text: "hi"}}}},
	}
	body := encodeGeminiBody(req)
	tools, _ := body["tools"].([]any)
	entry, _ := tools[0].(map[string]any)
	funcDecls, _ := entry["functionDeclarations"].([]any)
	decl, _ := funcDecls[0].(map[string]any)
	assertEqual(t, "name", decl["name"], "local_shell")
}

func TestMatrix_Gemini_SafetySettingsAllOff(t *testing.T) {
	req := &Request{
		Model:    "gemini-2.5-flash",
		Messages: []Message{{Role: "user", Content: []Content{{Type: ContentText, Text: "hi"}}}},
	}
	body := encodeGeminiBody(req)
	safety, _ := body["safetySettings"].([]any)
	categories := map[string]bool{}
	for _, sAny := range safety {
		s, _ := sAny.(map[string]any)
		cat, _ := s["category"].(string)
		categories[cat] = true
		if s["threshold"] != "OFF" {
			t.Errorf("%s threshold = %v, want OFF", cat, s["threshold"])
		}
	}
	expected := []string{"HARM_CATEGORY_HARASSMENT", "HARM_CATEGORY_HATE_SPEECH", "HARM_CATEGORY_SEXUALLY_EXPLICIT", "HARM_CATEGORY_DANGEROUS_CONTENT"}
	for _, e := range expected {
		if !categories[e] {
			t.Errorf("category %s not found", e)
		}
	}
}

// =====================================================================
// SECTION 11: Gemini Response Decode — All Features
// =====================================================================

func TestMatrix_GeminiDecode_Text(t *testing.T) {
	resp := DecodeGeminiResponse(simpleGeminiTextResponse("Hello!"), "gemini-2.5-flash")
	if len(resp.Content) != 1 {
		t.Fatalf("content = %d, want 1", len(resp.Content))
	}
	assertEqual(t, "type", resp.Content[0].Type, ContentText)
	assertEqual(t, "text", resp.Content[0].Text, "Hello!")
	assertEqual(t, "finish", resp.FinishReason, "stop")
}

func TestMatrix_GeminiDecode_Thinking(t *testing.T) {
	resp := DecodeGeminiResponse(geminiThinkingResponse("Thinking...", "Answer."), "gemini-2.5-flash")
	if len(resp.Content) != 2 {
		t.Fatalf("content = %d, want 2", len(resp.Content))
	}
	assertEqual(t, "content[0] type", resp.Content[0].Type, ContentThinking)
	assertEqual(t, "thinking text", resp.Content[0].Thinking.Text, "Thinking...")
	assertEqual(t, "content[1] type", resp.Content[1].Type, ContentText)
	assertEqual(t, "text", resp.Content[1].Text, "Answer.")
}

func TestMatrix_GeminiDecode_ToolCall(t *testing.T) {
	resp := DecodeGeminiResponse(geminiToolCallResponse("search", map[string]any{"q": "test"}), "gemini-2.5-flash")
	if len(resp.Content) != 1 {
		t.Fatalf("content = %d, want 1", len(resp.Content))
	}
	assertEqual(t, "type", resp.Content[0].Type, ContentToolCall)
	tc := resp.Content[0].ToolCall
	assertEqual(t, "name", tc.Name, "search")
	assertEqual(t, "id", tc.ID, "call_123")
}

func TestMatrix_GeminiDecode_FinishReason_Length(t *testing.T) {
	geminiResp := map[string]any{
		"response": map[string]any{
			"candidates": []any{
				map[string]any{
					"content":      map[string]any{"parts": []any{map[string]any{"text": "partial"}}},
					"finishReason": "MAX_TOKENS",
				},
			},
		},
	}
	resp := DecodeGeminiResponse(geminiResp, "gemini-2.5-flash")
	assertEqual(t, "finish", resp.FinishReason, "length")
}

func TestMatrix_GeminiDecode_FinishReason_Safety(t *testing.T) {
	geminiResp := map[string]any{
		"response": map[string]any{
			"candidates": []any{
				map[string]any{
					"content":      map[string]any{"parts": []any{map[string]any{"text": ""}}},
					"finishReason": "SAFETY",
				},
			},
		},
	}
	resp := DecodeGeminiResponse(geminiResp, "gemini-2.5-flash")
	assertEqual(t, "finish", resp.FinishReason, "content_filter")
}

func TestMatrix_GeminiDecode_Usage(t *testing.T) {
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
	resp := DecodeGeminiResponse(geminiResp, "gemini-2.5-flash")
	assertEqual(t, "prompt", resp.Usage.Prompt, int64(100))
	assertEqual(t, "completion", resp.Usage.Completion, int64(50))
	assertEqual(t, "cached", resp.Usage.Cached, int64(20))
	assertEqual(t, "thought", resp.Usage.Thought, int64(10))
}

func TestMatrix_GeminiDecode_Grounding(t *testing.T) {
	resp := DecodeGeminiResponse(geminiGroundingResponse("Answer.", []map[string]any{
		{"uri": "https://a.com", "title": "A"},
		{"uri": "https://b.com", "title": "B"},
	}), "gemini-2.5-flash")
	// Should have text + web_search.
	if len(resp.Content) != 2 {
		t.Fatalf("content = %d, want 2", len(resp.Content))
	}
	assertEqual(t, "content[1] type", resp.Content[1].Type, ContentWebSearch)
	wsr := resp.Content[1].WebSearch
	if len(wsr.Sources) != 2 {
		t.Fatalf("sources = %d, want 2", len(wsr.Sources))
	}
	assertEqual(t, "source[0] uri", wsr.Sources[0].URI, "https://a.com")
	assertEqual(t, "source[0] title", wsr.Sources[0].Title, "A")
}

func TestMatrix_GeminiDecode_ImageResponse(t *testing.T) {
	geminiResp := map[string]any{
		"response": map[string]any{
			"candidates": []any{
				map[string]any{
					"content": map[string]any{
						"parts": []any{
							map[string]any{
								"inlineData": map[string]any{
									"mimeType": "image/png",
									"data":     "base64img",
								},
							},
						},
					},
					"finishReason": "STOP",
				},
			},
		},
	}
	resp := DecodeGeminiResponse(geminiResp, "gemini-2.5-flash")
	if len(resp.Content) != 1 {
		t.Fatalf("content = %d, want 1", len(resp.Content))
	}
	assertEqual(t, "type", resp.Content[0].Type, ContentImage)
	assertEqual(t, "mime", resp.Content[0].Image.MimeType, "image/png")
	assertEqual(t, "data", resp.Content[0].Image.Data, "base64img")
}

// =====================================================================
// SECTION 12: Cross-Protocol Round Trips
// =====================================================================

func TestMatrix_CrossProtocol_OpenAIToAnthropic(t *testing.T) {
	// OpenAI request → IR → Anthropic response
	openaiReq := map[string]any{
		"model":    "gpt-4",
		"messages": []any{map[string]any{"role": "user", "content": "Hello"}},
	}
	irReq := DecodeOpenAIChat(openaiReq)
	irReq.Model = "gemini-2.5-flash"
	irResp := DecodeGeminiResponse(simpleGeminiTextResponse("Hi there!"), irReq.Model)
	anthropicResp := EncodeAnthropic(irResp)

	blocks, _ := anthropicResp["content"].([]any)
	block, _ := blocks[0].(map[string]any)
	assertEqual(t, "type", block["type"], "text")
	assertEqual(t, "text", block["text"], "Hi there!")
}

func TestMatrix_CrossProtocol_AnthropicToOpenAI(t *testing.T) {
	// Anthropic request → IR → OpenAI response
	anthropicReq := map[string]any{
		"model":      "claude-3",
		"max_tokens": float64(100),
		"messages":   []any{map[string]any{"role": "user", "content": "Hello"}},
	}
	irReq := DecodeAnthropic(anthropicReq)
	irReq.Model = "gemini-2.5-flash"
	irResp := DecodeGeminiResponse(simpleGeminiTextResponse("Hi there!"), irReq.Model)
	openaiResp := EncodeOpenAIChat(irResp)

	choices, _ := openaiResp["choices"].([]any)
	choice, _ := choices[0].(map[string]any)
	msg, _ := choice["message"].(map[string]any)
	assertEqual(t, "content", msg["content"], "Hi there!")
}

func TestMatrix_CrossProtocol_ResponsesToOpenAI(t *testing.T) {
	// Responses request → IR → OpenAI response
	responsesReq := map[string]any{
		"model":        "o1",
		"input":        "Hello",
		"instructions": "Be helpful.",
	}
	irReq := DecodeResponses(responsesReq)
	irReq.Model = "gemini-2.5-flash"
	irResp := DecodeGeminiResponse(simpleGeminiTextResponse("Hi!"), irReq.Model)
	openaiResp := EncodeOpenAIChat(irResp)

	choices, _ := openaiResp["choices"].([]any)
	choice, _ := choices[0].(map[string]any)
	msg, _ := choice["message"].(map[string]any)
	assertEqual(t, "content", msg["content"], "Hi!")
}

// =====================================================================
// SECTION 13: Gemini Stream Chunk Decode
// =====================================================================

func TestMatrix_GeminiStream_TextDelta(t *testing.T) {
	chunk := map[string]any{
		"response": map[string]any{
			"candidates": []any{
				map[string]any{
					"content": map[string]any{
						"parts": []any{map[string]any{"text": "Hello "}},
					},
				},
			},
		},
	}
	events := DecodeGeminiStreamChunk(chunk)
	if len(events) != 1 {
		t.Fatalf("events = %d, want 1", len(events))
	}
	assertEqual(t, "type", events[0].Type, StreamTextDelta)
	assertEqual(t, "delta", events[0].Delta, "Hello ")
}

func TestMatrix_GeminiStream_ThinkingDelta(t *testing.T) {
	chunk := map[string]any{
		"response": map[string]any{
			"candidates": []any{
				map[string]any{
					"content": map[string]any{
						"parts": []any{map[string]any{"text": "Thinking...", "thought": true}},
					},
				},
			},
		},
	}
	events := DecodeGeminiStreamChunk(chunk)
	if len(events) != 1 {
		t.Fatalf("events = %d, want 1", len(events))
	}
	assertEqual(t, "type", events[0].Type, StreamThinkingDelta)
}

func TestMatrix_GeminiStream_ToolCallDelta(t *testing.T) {
	chunk := map[string]any{
		"response": map[string]any{
			"candidates": []any{
				map[string]any{
					"content": map[string]any{
						"parts": []any{
							map[string]any{
								"functionCall": map[string]any{
									"name": "search",
									"args": map[string]any{"q": "test"},
									"id":   "call_1",
								},
							},
						},
					},
				},
			},
		},
	}
	events := DecodeGeminiStreamChunk(chunk)
	// Should produce Delta + Done.
	if len(events) != 2 {
		t.Fatalf("events = %d, want 2", len(events))
	}
	assertEqual(t, "events[0] type", events[0].Type, StreamToolCallDelta)
	assertEqual(t, "events[1] type", events[1].Type, StreamToolCallDone)
}

func TestMatrix_GeminiStream_Usage(t *testing.T) {
	chunk := map[string]any{
		"response": map[string]any{
			"usageMetadata": map[string]any{
				"promptTokenCount":     float64(10),
				"candidatesTokenCount": float64(20),
			},
		},
	}
	events := DecodeGeminiStreamChunk(chunk)
	if len(events) != 1 {
		t.Fatalf("events = %d, want 1", len(events))
	}
	assertEqual(t, "type", events[0].Type, StreamUsage)
	assertEqual(t, "prompt", events[0].Usage.Prompt, int64(10))
}

func TestMatrix_GeminiStream_Done(t *testing.T) {
	chunk := map[string]any{
		"response": map[string]any{
			"candidates": []any{
				map[string]any{
					"content":      map[string]any{"parts": []any{}},
					"finishReason": "STOP",
				},
			},
		},
	}
	events := DecodeGeminiStreamChunk(chunk)
	if len(events) != 1 {
		t.Fatalf("events = %d, want 1", len(events))
	}
	assertEqual(t, "type", events[0].Type, StreamDone)
	assertEqual(t, "finish", events[0].FinishReason, "stop")
}

func TestMatrix_GeminiStream_Grounding(t *testing.T) {
	chunk := map[string]any{
		"response": map[string]any{
			"candidates": []any{
				map[string]any{
					"content":      map[string]any{"parts": []any{map[string]any{"text": "Answer"}}},
					"finishReason": "STOP",
					"groundingMetadata": map[string]any{
						"groundingChunks": []any{
							map[string]any{"web": map[string]any{"uri": "https://example.com", "title": "Example"}},
						},
					},
				},
			},
		},
	}
	events := DecodeGeminiStreamChunk(chunk)
	// Should have StreamDone + StreamWebSearch.
	foundWebSearch := false
	for _, ev := range events {
		if ev.Type == StreamWebSearch {
			foundWebSearch = true
			if ev.WebSearch == nil || len(ev.WebSearch.Sources) != 1 {
				t.Error("web search sources missing")
			}
		}
	}
	if !foundWebSearch {
		t.Error("StreamWebSearch event not found")
	}
}

// =====================================================================
// SECTION 14: OpenAI Stream Encode
// =====================================================================

func TestMatrix_OpenAIStream_TextDelta(t *testing.T) {
	ev := StreamEvent{Type: StreamTextDelta, Delta: "Hello"}
	chunk := EncodeOpenAIChatStreamChunk(ev, "chatcmpl-1", 1000, "gpt-4", true)
	if chunk == "" {
		t.Fatal("chunk is empty")
	}
	if !contains(chunk, `"content":"Hello"`) {
		t.Errorf("chunk should contain content delta: %s", chunk)
	}
	if !contains(chunk, `"role":"assistant"`) {
		t.Error("first chunk should have role")
	}
}

func TestMatrix_OpenAIStream_ThinkingDelta(t *testing.T) {
	ev := StreamEvent{Type: StreamThinkingDelta, Delta: "Thinking..."}
	chunk := EncodeOpenAIChatStreamChunk(ev, "chatcmpl-1", 1000, "o1", false)
	if !contains(chunk, `"reasoning_content":"Thinking..."`) {
		t.Errorf("chunk should contain reasoning_content: %s", chunk)
	}
}

func TestMatrix_OpenAIStream_ToolCallDelta(t *testing.T) {
	ev := StreamEvent{
		Type:     StreamToolCallDelta,
		ToolCall: &ToolCall{ID: "call_1", Name: "search", Args: map[string]any{"q": "test"}},
	}
	chunk := EncodeOpenAIChatStreamChunk(ev, "chatcmpl-1", 1000, "gpt-4", false)
	if !contains(chunk, `"tool_calls"`) {
		t.Errorf("chunk should contain tool_calls: %s", chunk)
	}
	if !contains(chunk, `"name":"search"`) {
		t.Error("chunk should contain function name")
	}
}

func TestMatrix_OpenAIStream_ToolCallDone(t *testing.T) {
	ev := StreamEvent{
		Type:     StreamToolCallDone,
		ToolCall: &ToolCall{ID: "call_1", Name: "search"},
	}
	chunk := EncodeOpenAIChatStreamChunk(ev, "chatcmpl-1", 1000, "gpt-4", false)
	if !contains(chunk, `"arguments":""`) {
		t.Errorf("Done should have empty arguments: %s", chunk)
	}
}

func TestMatrix_OpenAIStream_Done(t *testing.T) {
	ev := StreamEvent{Type: StreamDone, FinishReason: "stop"}
	chunk := EncodeOpenAIChatStreamChunk(ev, "chatcmpl-1", 1000, "gpt-4", false)
	if !contains(chunk, `"finish_reason":"stop"`) {
		t.Errorf("chunk should contain finish_reason: %s", chunk)
	}
}

// TestMatrix_OpenAIStream_MultiToolCallIndex verifies that multiple tool
// calls in a single response get distinct OpenAI indices so that clients
// can correctly associate delta chunks by index.
func TestMatrix_OpenAIStream_MultiToolCallIndex(t *testing.T) {
	ev1 := StreamEvent{
		Type:     StreamToolCallDelta,
		ToolCall: &ToolCall{ID: "call_a", Name: "search", Args: map[string]any{"q": "1"}, Index: 0},
	}
	ev2 := StreamEvent{
		Type:     StreamToolCallDelta,
		ToolCall: &ToolCall{ID: "call_b", Name: "fetch", Args: map[string]any{"url": "x"}, Index: 1},
	}
	chunk1 := EncodeOpenAIChatStreamChunk(ev1, "chatcmpl-1", 1000, "gpt-4", false)
	chunk2 := EncodeOpenAIChatStreamChunk(ev2, "chatcmpl-1", 1000, "gpt-4", false)
	if !contains(chunk1, `"index":0`) {
		t.Errorf("first tool call should have index 0: %s", chunk1)
	}
	if !contains(chunk2, `"index":1`) {
		t.Errorf("second tool call should have index 1: %s", chunk2)
	}
	if !contains(chunk1, `"id":"call_a"`) {
		t.Errorf("first chunk should have call_a: %s", chunk1)
	}
	if !contains(chunk2, `"id":"call_b"`) {
		t.Errorf("second chunk should have call_b: %s", chunk2)
	}
}

// TestMatrix_GeminiStream_MultiToolCallIndex verifies that Gemini stream
// chunks with multiple functionCall parts produce events with distinct
// tool call indices.
func TestMatrix_GeminiStream_MultiToolCallIndex(t *testing.T) {
	chunk := map[string]any{
		"response": map[string]any{
			"candidates": []any{
				map[string]any{
					"content": map[string]any{
						"parts": []any{
							map[string]any{"functionCall": map[string]any{"name": "search", "id": "call_a", "args": map[string]any{"q": "1"}}},
							map[string]any{"functionCall": map[string]any{"name": "fetch", "id": "call_b", "args": map[string]any{"url": "x"}}},
						},
					},
				},
			},
		},
	}
	events := DecodeGeminiStreamChunk(chunk)
	// Expect 4 events: Delta+Done for call_a, Delta+Done for call_b.
	if len(events) != 4 {
		t.Fatalf("events = %d, want 4", len(events))
	}
	// First tool call should have index 0.
	if events[0].ToolCall == nil || events[0].ToolCall.Index != 0 {
		t.Errorf("event[0] index = %v, want 0", idxOr(events[0].ToolCall))
	}
	// Third event (second tool call delta) should have index 1.
	if events[2].ToolCall == nil || events[2].ToolCall.Index != 1 {
		t.Errorf("event[2] index = %v, want 1", idxOr(events[2].ToolCall))
	}
	// IDs should be distinct.
	if events[0].ToolCall.ID == events[2].ToolCall.ID {
		t.Error("tool call IDs should be distinct")
	}
}

func idxOr(tc *ToolCall) int {
	if tc == nil {
		return -99
	}
	return tc.Index
}

func TestMatrix_OpenAIStream_WebSearch(t *testing.T) {
	ev := StreamEvent{
		Type: StreamWebSearch,
		WebSearch: &WebSearchResult{
			Sources: []WebSearchSource{{URI: "https://example.com", Title: "Example"}},
		},
	}
	chunk := EncodeOpenAIChatStreamChunk(ev, "chatcmpl-1", 1000, "gpt-4", false)
	if !contains(chunk, "Web search results:") {
		t.Errorf("chunk should contain web search text: %s", chunk)
	}
}

// =====================================================================
// SECTION 15: Anthropic Stream Encode
// =====================================================================

func TestMatrix_AnthropicStream_TextDelta(t *testing.T) {
	events := []StreamEvent{{Type: StreamTextDelta, Delta: "Hello"}}
	chunks := EncodeAnthropicStreamEvents(events, "msg_1", "claude-3")
	if len(chunks) == 0 {
		t.Fatal("no chunks")
	}
	// Should have message_start, content_block_start, content_block_delta, content_block_stop, message_delta, message_stop
	if !contains(chunks[0], "message_start") {
		t.Error("first chunk should be message_start")
	}
	foundDelta := false
	for _, c := range chunks {
		if contains(c, "content_block_delta") && contains(c, "text_delta") {
			foundDelta = true
		}
	}
	if !foundDelta {
		t.Error("text_delta not found")
	}
}

func TestMatrix_AnthropicStream_ThinkingDelta(t *testing.T) {
	events := []StreamEvent{{Type: StreamThinkingDelta, Delta: "Thinking..."}}
	chunks := EncodeAnthropicStreamEvents(events, "msg_1", "claude-3")
	foundThinking := false
	for _, c := range chunks {
		if contains(c, "thinking_delta") {
			foundThinking = true
		}
	}
	if !foundThinking {
		t.Error("thinking_delta not found")
	}
}

func TestMatrix_AnthropicStream_ToolCallDone(t *testing.T) {
	events := []StreamEvent{{
		Type:     StreamToolCallDone,
		ToolCall: &ToolCall{ID: "toolu_1", Name: "search", Args: map[string]any{"q": "test"}},
	}}
	chunks := EncodeAnthropicStreamEvents(events, "msg_1", "claude-3")
	foundToolUse := false
	for _, c := range chunks {
		if contains(c, "tool_use") {
			foundToolUse = true
		}
	}
	if !foundToolUse {
		t.Error("tool_use block not found in stream")
	}
}

func TestMatrix_AnthropicStream_WebSearch(t *testing.T) {
	events := []StreamEvent{{
		Type: StreamWebSearch,
		WebSearch: &WebSearchResult{
			Sources: []WebSearchSource{{URI: "https://example.com", Title: "Example"}},
		},
	}}
	chunks := EncodeAnthropicStreamEvents(events, "msg_1", "claude-3")
	foundWebSearch := false
	for _, c := range chunks {
		if contains(c, "web_search_tool_result") {
			foundWebSearch = true
		}
	}
	if !foundWebSearch {
		t.Error("web_search_tool_result not found in stream")
	}
}

func TestMatrix_AnthropicStream_Done(t *testing.T) {
	events := []StreamEvent{{Type: StreamDone, FinishReason: "stop"}}
	chunks := EncodeAnthropicStreamEvents(events, "msg_1", "claude-3")
	foundStop := false
	for _, c := range chunks {
		if contains(c, "message_stop") {
			foundStop = true
		}
	}
	if !foundStop {
		t.Error("message_stop not found")
	}
}

// =====================================================================
// SECTION 16: Schema Normalization
// =====================================================================

func TestMatrix_Schema_TypeNormalization(t *testing.T) {
	schema := map[string]any{
		"type": "string",
		"properties": map[string]any{
			"nested": map[string]any{"type": "integer"},
		},
	}
	out := NormalizeSchemaForGemini(schema)
	assertEqual(t, "type", out["type"], "STRING")
	props, _ := out["properties"].(map[string]any)
	nested, _ := props["nested"].(map[string]any)
	assertEqual(t, "nested type", nested["type"], "INTEGER")
}

func TestMatrix_Schema_NullableType(t *testing.T) {
	schema := map[string]any{
		"type": []any{"string", "null"},
	}
	out := NormalizeSchemaForGemini(schema)
	assertEqual(t, "type", out["type"], "STRING")
	if out["nullable"] != true {
		t.Error("nullable should be true")
	}
}

func TestMatrix_Schema_StripsUnsupported(t *testing.T) {
	schema := map[string]any{
		"type":        "object",
		"format":      "date-time",
		"strict":      true,
		"enum":        []any{"a", "b"},
		"minLength":   float64(1),
		"maxLength":   float64(100),
		"description": "kept",
		"required":    []string{"name"},
	}
	out := NormalizeSchemaForGemini(schema)
	// Supported fields should be kept.
	assertEqual(t, "type", out["type"], "OBJECT")
	assertEqual(t, "description", out["description"], "kept")
	// Unsupported fields should be stripped.
	for _, key := range []string{"format", "strict", "enum", "minLength", "maxLength"} {
		if _, exists := out[key]; exists {
			t.Errorf("%s should be stripped", key)
		}
	}
}

// =====================================================================
// SECTION 17: Edge Cases
// =====================================================================

func TestMatrix_EdgeCase_EmptyMessages(t *testing.T) {
	req := map[string]any{
		"model":    "gpt-4",
		"messages": []any{},
	}
	r := DecodeOpenAIChat(req)
	if len(r.Messages) != 0 {
		t.Errorf("messages = %d, want 0", len(r.Messages))
	}
}

func TestMatrix_EdgeCase_NilContent(t *testing.T) {
	req := map[string]any{
		"model": "gpt-4",
		"messages": []any{
			map[string]any{"role": "user", "content": nil},
		},
	}
	r := DecodeOpenAIChat(req)
	// Should not crash; content may be empty.
	if len(r.Messages) != 1 {
		t.Errorf("messages = %d, want 1", len(r.Messages))
	}
}

func TestMatrix_EdgeCase_DeveloperRole(t *testing.T) {
	req := map[string]any{
		"model": "o1",
		"messages": []any{
			map[string]any{"role": "developer", "content": "System message"},
			map[string]any{"role": "user", "content": "Hello"},
		},
	}
	r := DecodeOpenAIChat(req)
	// developer role should be treated as system.
	assertEqual(t, "system", r.System, "System message")
	assertEqual(t, "messages", len(r.Messages), 1)
}

func TestMatrix_EdgeCase_MultipleSystemMessages(t *testing.T) {
	req := map[string]any{
		"model": "gpt-4",
		"messages": []any{
			map[string]any{"role": "system", "content": "Part 1"},
			map[string]any{"role": "system", "content": "Part 2"},
			map[string]any{"role": "user", "content": "Hello"},
		},
	}
	r := DecodeOpenAIChat(req)
	assertEqual(t, "system", r.System, "Part 1\nPart 2")
}

func TestMatrix_EdgeCase_GeminiResponse_NoCandidates(t *testing.T) {
	geminiResp := map[string]any{
		"response": map[string]any{},
	}
	resp := DecodeGeminiResponse(geminiResp, "gemini-2.5-flash")
	if len(resp.Content) != 0 {
		t.Errorf("content = %d, want 0", len(resp.Content))
	}
}

func TestMatrix_EdgeCase_GeminiResponse_EmptyParts(t *testing.T) {
	geminiResp := map[string]any{
		"response": map[string]any{
			"candidates": []any{
				map[string]any{
					"content":      map[string]any{"parts": []any{}},
					"finishReason": "STOP",
				},
			},
		},
	}
	resp := DecodeGeminiResponse(geminiResp, "gemini-2.5-flash")
	if len(resp.Content) != 0 {
		t.Errorf("content = %d, want 0", len(resp.Content))
	}
}

// =====================================================================
// HELPERS
// =====================================================================

func assertEqual(t *testing.T, label string, got, want any) {
	t.Helper()
	gotJSON, _ := json.Marshal(got)
	wantJSON, _ := json.Marshal(want)
	if string(gotJSON) != string(wantJSON) {
		t.Errorf("%s = %v, want %v", label, got, want)
	}
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
