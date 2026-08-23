package proxy

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestResponsesRequestToOpenAI_StringInput(t *testing.T) {
	req := map[string]any{
		"model":  "gemini-3-flash",
		"input":  "Hello, world",
		"stream": true,
	}
	out := responsesRequestToOpenAI(req)

	if out["model"] != "gemini-3-flash" {
		t.Errorf("model = %v, want gemini-3-flash", out["model"])
	}
	if out["stream"] != true {
		t.Errorf("stream = %v, want true", out["stream"])
	}

	messages, ok := out["messages"].([]any)
	if !ok || len(messages) != 1 {
		t.Fatalf("messages = %v, want 1 entry", out["messages"])
	}
	msg, _ := messages[0].(map[string]any)
	if msg["role"] != "user" {
		t.Errorf("role = %v, want user", msg["role"])
	}
	if msg["content"] != "Hello, world" {
		t.Errorf("content = %v, want 'Hello, world'", msg["content"])
	}
}

func TestResponsesRequestToOpenAI_ArrayInputWithInstructions(t *testing.T) {
	req := map[string]any{
		"model":        "gemini-3-flash",
		"instructions": "You are a helpful assistant.",
		"input": []any{
			map[string]any{
				"type":    "message",
				"role":    "user",
				"content": "Hello!",
			},
			map[string]any{
				"type":    "message",
				"role":    "assistant",
				"content": "Hi there!",
			},
		},
		"max_output_tokens": float64(4096),
	}
	out := responsesRequestToOpenAI(req)

	messages, ok := out["messages"].([]any)
	if !ok || len(messages) != 3 {
		t.Fatalf("messages len = %d, want 3", len(messages))
	}

	// First message should be system from instructions.
	sys, _ := messages[0].(map[string]any)
	if sys["role"] != "system" {
		t.Errorf("first message role = %v, want system", sys["role"])
	}
	if sys["content"] != "You are a helpful assistant." {
		t.Errorf("first message content = %v", sys["content"])
	}

	// Second message should be user.
	user, _ := messages[1].(map[string]any)
	if user["role"] != "user" {
		t.Errorf("second message role = %v, want user", user["role"])
	}

	// max_output_tokens → max_tokens
	if out["max_tokens"] != float64(4096) {
		t.Errorf("max_tokens = %v, want 4096", out["max_tokens"])
	}
}

func TestResponsesRequestToOpenAI_FunctionCallItem(t *testing.T) {
	req := map[string]any{
		"model": "gemini-3-flash",
		"input": []any{
			map[string]any{
				"type":    "message",
				"role":    "user",
				"content": "What's the weather?",
			},
			map[string]any{
				"type":      "function_call",
				"call_id":   "call_123",
				"name":      "get_weather",
				"arguments": `{"city":"SF"}`,
			},
			map[string]any{
				"type":    "function_call_output",
				"call_id": "call_123",
				"output":  "Sunny, 72F",
			},
		},
	}
	out := responsesRequestToOpenAI(req)
	messages, _ := out["messages"].([]any)
	if len(messages) != 3 {
		t.Fatalf("messages len = %d, want 3", len(messages))
	}

	// Second message should be assistant with tool_calls.
	assistant, _ := messages[1].(map[string]any)
	if assistant["role"] != "assistant" {
		t.Errorf("second message role = %v, want assistant", assistant["role"])
	}
	toolCalls, _ := assistant["tool_calls"].([]any)
	if len(toolCalls) != 1 {
		t.Fatalf("tool_calls len = %d, want 1", len(toolCalls))
	}
	tc, _ := toolCalls[0].(map[string]any)
	fn, _ := tc["function"].(map[string]any)
	if fn["name"] != "get_weather" {
		t.Errorf("function name = %v, want get_weather", fn["name"])
	}
	if fn["arguments"] != `{"city":"SF"}` {
		t.Errorf("arguments = %v", fn["arguments"])
	}

	// Third message should be tool response.
	tool, _ := messages[2].(map[string]any)
	if tool["role"] != "tool" {
		t.Errorf("third message role = %v, want tool", tool["role"])
	}
	if tool["tool_call_id"] != "call_123" {
		t.Errorf("tool_call_id = %v, want call_123", tool["tool_call_id"])
	}
}

func TestResponsesRequestToOpenAI_ToolsNormalization(t *testing.T) {
	req := map[string]any{
		"model": "gemini-3-flash",
		"input": "test",
		"tools": []any{
			map[string]any{
				"type":        "function",
				"name":        "get_weather",
				"description": "Get weather",
				"parameters":  map[string]any{"type": "object"},
			},
		},
	}
	out := responsesRequestToOpenAI(req)
	tools, _ := out["tools"].([]any)
	if len(tools) != 1 {
		t.Fatalf("tools len = %d, want 1", len(tools))
	}
	tool, _ := tools[0].(map[string]any)
	fn, _ := tool["function"].(map[string]any)
	if fn["name"] != "get_weather" {
		t.Errorf("function name = %v, want get_weather", fn["name"])
	}
}

func TestResponsesRequestToOpenAI_ReasoningEffort(t *testing.T) {
	req := map[string]any{
		"model": "gemini-3-flash",
		"input": "test",
		"reasoning": map[string]any{
			"effort": "high",
		},
	}
	out := responsesRequestToOpenAI(req)
	if out["reasoning_effort"] != "high" {
		t.Errorf("reasoning_effort = %v, want high", out["reasoning_effort"])
	}
}

func TestResponsesStreamState_TextDelta(t *testing.T) {
	st := newResponsesStreamState("resp_test", "gemini-3-flash", 1000)

	inner := map[string]any{
		"candidates": []any{
			map[string]any{
				"content": map[string]any{
					"parts": []any{
						map[string]any{"text": "Hello"},
					},
				},
			},
		},
	}

	events := st.processGeminiChunk(inner)
	if !strings.Contains(events, "response.created") {
		t.Errorf("missing response.created event")
	}
	if !strings.Contains(events, "response.output_text.delta") {
		t.Errorf("missing output_text.delta event")
	}
	if !strings.Contains(events, `"delta":"Hello"`) {
		t.Errorf("missing delta text 'Hello'")
	}
}

func TestResponsesStreamState_ReasoningThenText(t *testing.T) {
	st := newResponsesStreamState("resp_test", "gemini-3-flash", 1000)

	// First chunk: reasoning
	inner1 := map[string]any{
		"candidates": []any{
			map[string]any{
				"content": map[string]any{
					"parts": []any{
						map[string]any{"text": "Let me think...", "thought": true},
					},
				},
			},
		},
	}
	events1 := st.processGeminiChunk(inner1)
	if !strings.Contains(events1, "response.reasoning_summary_text.delta") {
		t.Errorf("missing reasoning_summary_text.delta in first chunk")
	}

	// Second chunk: text
	inner2 := map[string]any{
		"candidates": []any{
			map[string]any{
				"content": map[string]any{
					"parts": []any{
						map[string]any{"text": "Here's the answer."},
					},
				},
				"finishReason": "STOP",
			},
		},
	}
	events2 := st.processGeminiChunk(inner2)
	if !strings.Contains(events2, "response.output_text.delta") {
		t.Errorf("missing output_text.delta in second chunk")
	}
	if !strings.Contains(events2, "response.completed") {
		t.Errorf("missing response.completed event")
	}
}

func TestResponsesStreamState_FunctionCall(t *testing.T) {
	st := newResponsesStreamState("resp_test", "gemini-3-flash", 1000)

	inner := map[string]any{
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
	}

	events := st.processGeminiChunk(inner)
	if !strings.Contains(events, "response.function_call_arguments.delta") {
		t.Errorf("missing function_call_arguments.delta event")
	}
	if !strings.Contains(events, "response.function_call_arguments.done") {
		t.Errorf("missing function_call_arguments.done event")
	}
	if !strings.Contains(events, "response.completed") {
		t.Errorf("missing response.completed event")
	}
}

func TestResponsesErrorBody(t *testing.T) {
	body := responsesErrorBody(500, "internal error")
	if body["status"] != "failed" {
		t.Errorf("status = %v, want failed", body["status"])
	}
	err, _ := body["error"].(map[string]any)
	if err["message"] != "internal error" {
		t.Errorf("error message = %v", err["message"])
	}
}

func TestSSEEvent(t *testing.T) {
	ev := sseEvent("response.created", map[string]any{
		"type": "response.created",
	})
	if !strings.HasPrefix(ev, "event: response.created\n") {
		t.Errorf("missing event header: %s", ev)
	}
	if !strings.Contains(ev, "data: ") {
		t.Errorf("missing data line: %s", ev)
	}
	// Verify it's valid JSON in the data line.
	lines := strings.Split(ev, "\n")
	for _, line := range lines {
		if strings.HasPrefix(line, "data: ") {
			data := line[len("data: "):]
			var m map[string]any
			if err := json.Unmarshal([]byte(data), &m); err != nil {
				t.Errorf("invalid JSON in data: %v", err)
			}
		}
	}
}
