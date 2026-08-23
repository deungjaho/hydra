package ir

import (
	"encoding/json"
	"testing"
)

func TestDecodeOpenAIChat_BasicRequest(t *testing.T) {
	req := map[string]any{
		"model":       "gpt-4",
		"max_tokens":  float64(1000),
		"temperature": float64(0.7),
		"stream":      true,
		"messages": []any{
			map[string]any{"role": "system", "content": "You are helpful."},
			map[string]any{"role": "user", "content": "Hello"},
		},
	}
	r := DecodeOpenAIChat(req)
	if r.Model != "gpt-4" {
		t.Errorf("model = %s, want gpt-4", r.Model)
	}
	if r.MaxTokens != 1000 {
		t.Errorf("max_tokens = %d, want 1000", r.MaxTokens)
	}
	if r.Temperature == nil || *r.Temperature != 0.7 {
		t.Errorf("temperature = %v, want 0.7", r.Temperature)
	}
	if !r.Stream {
		t.Error("stream should be true")
	}
	if r.System != "You are helpful." {
		t.Errorf("system = %q, want %q", r.System, "You are helpful.")
	}
	if len(r.Messages) != 1 {
		t.Fatalf("messages = %d, want 1", len(r.Messages))
	}
	if r.Messages[0].Role != "user" {
		t.Errorf("role = %s, want user", r.Messages[0].Role)
	}
	if len(r.Messages[0].Content) != 1 || r.Messages[0].Content[0].Type != ContentText || r.Messages[0].Content[0].Text != "Hello" {
		t.Errorf("content mismatch: %+v", r.Messages[0].Content)
	}
}

func TestDecodeOpenAIChat_ToolCalls(t *testing.T) {
	req := map[string]any{
		"model": "gpt-4",
		"messages": []any{
			map[string]any{
				"role": "assistant",
				"tool_calls": []any{
					map[string]any{
						"id":   "call_123",
						"type": "function",
						"function": map[string]any{
							"name":      "get_weather",
							"arguments": `{"city":"SF"}`,
						},
					},
				},
			},
			map[string]any{
				"role":         "tool",
				"tool_call_id": "call_123",
				"content":      "Sunny 72F",
			},
		},
	}
	r := DecodeOpenAIChat(req)
	if len(r.Messages) != 2 {
		t.Fatalf("messages = %d, want 2", len(r.Messages))
	}
	// First message: assistant with tool call
	if r.Messages[0].Role != "assistant" {
		t.Errorf("msg0 role = %s, want assistant", r.Messages[0].Role)
	}
	if len(r.Messages[0].Content) != 1 || r.Messages[0].Content[0].Type != ContentToolCall {
		t.Fatalf("msg0 content mismatch: %+v", r.Messages[0].Content)
	}
	tc := r.Messages[0].Content[0].ToolCall
	if tc.ID != "call_123" || tc.Name != "get_weather" {
		t.Errorf("tool call = %+v", tc)
	}
	if tc.Args["city"] != "SF" {
		t.Errorf("args city = %v, want SF", tc.Args["city"])
	}
	// Second message: tool result
	if r.Messages[1].Role != "tool" {
		t.Errorf("msg1 role = %s, want tool", r.Messages[1].Role)
	}
	if len(r.Messages[1].Content) != 1 || r.Messages[1].Content[0].Type != ContentToolResult {
		t.Fatalf("msg1 content mismatch: %+v", r.Messages[1].Content)
	}
	tr := r.Messages[1].Content[0].ToolResult
	if tr.ID != "call_123" || tr.Content != "Sunny 72F" {
		t.Errorf("tool result = %+v", tr)
	}
}

func TestDecodeOpenAIChat_ImageContent(t *testing.T) {
	req := map[string]any{
		"model": "gpt-4",
		"messages": []any{
			map[string]any{
				"role": "user",
				"content": []any{
					map[string]any{"type": "text", "text": "What's this?"},
					map[string]any{
						"type": "image_url",
						"image_url": map[string]any{
							"url": "data:image/png;base64,iVBORw0KGgo=",
						},
					},
				},
			},
		},
	}
	r := DecodeOpenAIChat(req)
	if len(r.Messages) != 1 || len(r.Messages[0].Content) != 2 {
		t.Fatalf("content count = %d", len(r.Messages[0].Content))
	}
	if r.Messages[0].Content[0].Type != ContentText || r.Messages[0].Content[0].Text != "What's this?" {
		t.Errorf("part0: %+v", r.Messages[0].Content[0])
	}
	img := r.Messages[0].Content[1]
	if img.Type != ContentImage || img.Image == nil {
		t.Fatalf("part1 not image: %+v", img)
	}
	if img.Image.MimeType != "image/png" || img.Image.Data != "iVBORw0KGgo=" {
		t.Errorf("image: %+v", img.Image)
	}
}

func TestEncodeGeminiRequest_Basic(t *testing.T) {
	r := &Request{
		Model:  "gemini-3-pro",
		System: "Be helpful.",
		Messages: []Message{
			{Role: "user", Content: []Content{{Type: ContentText, Text: "Hi"}}},
		},
		MaxTokens: 4096,
	}
	env := EncodeGeminiRequest(r, "my-project", "session-1", 1)
	body, _ := env["request"].(map[string]any)
	if body == nil {
		t.Fatal("no request body in envelope")
	}
	if body["model"] != "gemini-3-pro" {
		t.Errorf("model = %v", body["model"])
	}
	sysInst, _ := body["systemInstruction"].(map[string]any)
	if sysInst == nil {
		t.Fatal("no systemInstruction")
	}
	parts, _ := sysInst["parts"].([]any)
	if len(parts) != 1 {
		t.Fatalf("system parts = %d", len(parts))
	}
	if parts[0].(map[string]any)["text"] != "Be helpful." {
		t.Errorf("system text mismatch")
	}
	contents, _ := body["contents"].([]any)
	if len(contents) != 1 {
		t.Fatalf("contents = %d", len(contents))
	}
	genConfig, _ := body["generationConfig"].(map[string]any)
	if genConfig["maxOutputTokens"] != int64(4096) {
		t.Errorf("maxOutputTokens = %v", genConfig["maxOutputTokens"])
	}
}

func TestEncodeGeminiRequest_ToolCall(t *testing.T) {
	r := &Request{
		Model: "gemini-3-pro",
		Messages: []Message{
			{
				Role: "assistant",
				Content: []Content{{
					Type: ContentToolCall,
					ToolCall: &ToolCall{
						ID:   "call_1",
						Name: "get_weather",
						Args: map[string]any{"city": "SF"},
					},
				}},
			},
			{
				Role: "tool",
				Content: []Content{{
					Type: ContentToolResult,
					ToolResult: &ToolResult{
						ID:      "call_1",
						Name:    "get_weather",
						Content: "Sunny",
					},
				}},
			},
		},
	}
	env := EncodeGeminiRequest(r, "proj", "sess", 1)
	body, _ := env["request"].(map[string]any)
	contents, _ := body["contents"].([]any)
	if len(contents) != 2 {
		t.Fatalf("contents = %d", len(contents))
	}
	// First content: model role with functionCall
	c0, _ := contents[0].(map[string]any)
	if c0["role"] != "model" {
		t.Errorf("c0 role = %v, want model", c0["role"])
	}
	parts0, _ := c0["parts"].([]any)
	fc, _ := parts0[0].(map[string]any)["functionCall"].(map[string]any)
	if fc["name"] != "get_weather" {
		t.Errorf("functionCall name = %v", fc["name"])
	}
	// Second content: user role with functionResponse
	c1, _ := contents[1].(map[string]any)
	if c1["role"] != "user" {
		t.Errorf("c1 role = %v, want user", c1["role"])
	}
	parts1, _ := c1["parts"].([]any)
	fr, _ := parts1[0].(map[string]any)["functionResponse"].(map[string]any)
	if fr["name"] != "get_weather" {
		t.Errorf("functionResponse name = %v", fr["name"])
	}
}

func TestEncodeGeminiRequest_LocalShellTool(t *testing.T) {
	r := &Request{
		Model: "gemini-2.5-flash",
		Tools: []Tool{
			{Kind: ToolLocalShell, Name: "local_shell", Description: "Run shell commands"},
		},
		Messages: []Message{
			{Role: "user", Content: []Content{{Type: ContentText, Text: "list files"}}},
		},
	}
	env := EncodeGeminiRequest(r, "proj", "sess", 1)
	body, _ := env["request"].(map[string]any)
	tools, _ := body["tools"].([]any)
	if len(tools) != 1 {
		t.Fatalf("tools = %d, want 1", len(tools))
	}
	tool, _ := tools[0].(map[string]any)
	funcDecls, _ := tool["functionDeclarations"].([]any)
	if len(funcDecls) != 1 {
		t.Fatalf("functionDeclarations = %d, want 1", len(funcDecls))
	}
	decl, _ := funcDecls[0].(map[string]any)
	if decl["name"] != "local_shell" {
		t.Errorf("name = %v, want local_shell", decl["name"])
	}
	params, _ := decl["parameters"].(map[string]any)
	if params["type"] != "OBJECT" {
		t.Errorf("parameters type = %v, want OBJECT", params["type"])
	}
	props, _ := params["properties"].(map[string]any)
	if _, ok := props["action"]; !ok {
		t.Error("missing 'action' property in local_shell parameters")
	}
}

func TestDecodeGeminiResponse_TextAndToolCall(t *testing.T) {
	geminiResp := map[string]any{
		"response": map[string]any{
			"candidates": []any{
				map[string]any{
					"content": map[string]any{
						"role": "model",
						"parts": []any{
							map[string]any{"text": "Hello!"},
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
			"usageMetadata": map[string]any{
				"promptTokenCount":     float64(10),
				"candidatesTokenCount": float64(20),
			},
		},
	}
	resp := DecodeGeminiResponse(geminiResp, "gemini-3-pro")
	if len(resp.Content) != 2 {
		t.Fatalf("content count = %d, want 2", len(resp.Content))
	}
	if resp.Content[0].Type != ContentText || resp.Content[0].Text != "Hello!" {
		t.Errorf("content0: %+v", resp.Content[0])
	}
	if resp.Content[1].Type != ContentToolCall {
		t.Fatalf("content1 not tool call: %+v", resp.Content[1])
	}
	tc := resp.Content[1].ToolCall
	if tc.Name != "get_weather" || tc.ID != "call_1" {
		t.Errorf("tool call: %+v", tc)
	}
	if resp.FinishReason != "stop" {
		t.Errorf("finish = %s, want stop", resp.FinishReason)
	}
	if resp.Usage.Prompt != 10 || resp.Usage.Completion != 20 {
		t.Errorf("usage: %+v", resp.Usage)
	}
}

func TestDecodeGeminiResponse_Thinking(t *testing.T) {
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
	resp := DecodeGeminiResponse(geminiResp, "gemini-3-pro")
	if len(resp.Content) != 2 {
		t.Fatalf("content count = %d", len(resp.Content))
	}
	if resp.Content[0].Type != ContentThinking {
		t.Fatalf("content0 not thinking: %+v", resp.Content[0])
	}
	if resp.Content[0].Thinking.Text != "Let me think..." {
		t.Errorf("thinking text: %s", resp.Content[0].Thinking.Text)
	}
	if resp.Content[0].Thinking.Signature != "sig123" {
		t.Errorf("thinking sig: %s", resp.Content[0].Thinking.Signature)
	}
	if resp.Content[1].Type != ContentText || resp.Content[1].Text != "The answer is 42." {
		t.Errorf("content1: %+v", resp.Content[1])
	}
}

func TestEncodeOpenAIChat_Response(t *testing.T) {
	resp := &Response{
		ID:    "chatcmpl-123",
		Model: "gemini-3-pro",
		Content: []Content{
			{Type: ContentText, Text: "Hello!"},
		},
		FinishReason: "stop",
		Usage:        Usage{Prompt: 10, Completion: 20},
	}
	out := EncodeOpenAIChat(resp)
	if out["object"] != "chat.completion" {
		t.Errorf("object = %v", out["object"])
	}
	choices, _ := out["choices"].([]any)
	if len(choices) != 1 {
		t.Fatalf("choices = %d", len(choices))
	}
	choice, _ := choices[0].(map[string]any)
	msg, _ := choice["message"].(map[string]any)
	if msg["content"] != "Hello!" {
		t.Errorf("content = %v", msg["content"])
	}
	if choice["finish_reason"] != "stop" {
		t.Errorf("finish = %v", choice["finish_reason"])
	}
}

func TestEncodeOpenAIChat_ToolCallResponse(t *testing.T) {
	resp := &Response{
		ID:    "chatcmpl-123",
		Model: "gemini-3-pro",
		Content: []Content{{
			Type: ContentToolCall,
			ToolCall: &ToolCall{
				ID:   "call_1",
				Name: "get_weather",
				Args: map[string]any{"city": "SF"},
			},
		}},
		FinishReason: "stop",
	}
	out := EncodeOpenAIChat(resp)
	choices, _ := out["choices"].([]any)
	choice, _ := choices[0].(map[string]any)
	if choice["finish_reason"] != "tool_calls" {
		t.Errorf("finish = %v, want tool_calls", choice["finish_reason"])
	}
	msg, _ := choice["message"].(map[string]any)
	tcs, _ := msg["tool_calls"].([]any)
	if len(tcs) != 1 {
		t.Fatalf("tool_calls = %d", len(tcs))
	}
	tc, _ := tcs[0].(map[string]any)
	fn, _ := tc["function"].(map[string]any)
	if fn["name"] != "get_weather" {
		t.Errorf("name = %v", fn["name"])
	}
}

func TestDecodeAnthropic_Basic(t *testing.T) {
	req := map[string]any{
		"model":      "claude-sonnet-4-6",
		"max_tokens": float64(1024),
		"system":     "Be helpful.",
		"messages": []any{
			map[string]any{
				"role":    "user",
				"content": "Hello",
			},
		},
	}
	r := DecodeAnthropic(req)
	if r.Model != "claude-sonnet-4-6" {
		t.Errorf("model = %s", r.Model)
	}
	if r.MaxTokens != 1024 {
		t.Errorf("max_tokens = %d", r.MaxTokens)
	}
	if r.System != "Be helpful." {
		t.Errorf("system = %s", r.System)
	}
	if len(r.Messages) != 1 {
		t.Fatalf("messages = %d", len(r.Messages))
	}
}

func TestDecodeAnthropic_ToolUse(t *testing.T) {
	req := map[string]any{
		"model":      "claude-sonnet-4-6",
		"max_tokens": float64(1024),
		"messages": []any{
			map[string]any{
				"role": "assistant",
				"content": []any{
					map[string]any{
						"type":  "tool_use",
						"id":    "toolu_1",
						"name":  "get_weather",
						"input": map[string]any{"city": "SF"},
					},
				},
			},
			map[string]any{
				"role": "user",
				"content": []any{
					map[string]any{
						"type":        "tool_result",
						"tool_use_id": "toolu_1",
						"content":     "Sunny 72F",
					},
				},
			},
		},
	}
	r := DecodeAnthropic(req)
	if len(r.Messages) != 2 {
		t.Fatalf("messages = %d", len(r.Messages))
	}
	// First: assistant with tool_use
	if r.Messages[0].Content[0].Type != ContentToolCall {
		t.Fatalf("content0 not tool call: %+v", r.Messages[0].Content[0])
	}
	tc := r.Messages[0].Content[0].ToolCall
	if tc.ID != "toolu_1" || tc.Name != "get_weather" {
		t.Errorf("tool call: %+v", tc)
	}
	// Second: user with tool_result
	if r.Messages[1].Content[0].Type != ContentToolResult {
		t.Fatalf("content1 not tool result: %+v", r.Messages[1].Content[0])
	}
	tr := r.Messages[1].Content[0].ToolResult
	if tr.ID != "toolu_1" || tr.Content != "Sunny 72F" {
		t.Errorf("tool result: %+v", tr)
	}
}

func TestEncodeAnthropic_Response(t *testing.T) {
	resp := &Response{
		ID:    "msg_123",
		Model: "claude-sonnet-4-6",
		Content: []Content{
			{Type: ContentText, Text: "Hello!"},
		},
		FinishReason: "stop",
		Usage:        Usage{Prompt: 10, Completion: 20},
	}
	out := EncodeAnthropic(resp)
	if out["type"] != "message" {
		t.Errorf("type = %v", out["type"])
	}
	blocks, _ := out["content"].([]any)
	if len(blocks) != 1 {
		t.Fatalf("blocks = %d", len(blocks))
	}
	block, _ := blocks[0].(map[string]any)
	if block["type"] != "text" || block["text"] != "Hello!" {
		t.Errorf("block: %+v", block)
	}
	if out["stop_reason"] != "end_turn" {
		t.Errorf("stop_reason = %v", out["stop_reason"])
	}
}

func TestDecodeResponses_Basic(t *testing.T) {
	req := map[string]any{
		"model":             "gpt-4",
		"max_output_tokens": float64(1000),
		"instructions":      "Be helpful.",
		"input":             "Hello",
	}
	r := DecodeResponses(req)
	if r.Model != "gpt-4" {
		t.Errorf("model = %s", r.Model)
	}
	if r.MaxTokens != 1000 {
		t.Errorf("max_tokens = %d", r.MaxTokens)
	}
	if r.System != "Be helpful." {
		t.Errorf("system = %s", r.System)
	}
	if len(r.Messages) != 1 {
		t.Fatalf("messages = %d", len(r.Messages))
	}
	if r.Messages[0].Role != "user" {
		t.Errorf("role = %s", r.Messages[0].Role)
	}
}

func TestDecodeResponses_LocalShellCall(t *testing.T) {
	req := map[string]any{
		"model": "codex-1",
		"input": []any{
			map[string]any{
				"type":    "local_shell_call",
				"call_id": "call_1",
				"action": map[string]any{
					"type":    "exec",
					"command": []any{"cat", "file.txt"},
				},
			},
			map[string]any{
				"type":    "local_shell_call_output",
				"call_id": "call_1",
				"output":  "file contents here",
			},
		},
	}
	r := DecodeResponses(req)
	if len(r.Messages) != 2 {
		t.Fatalf("messages = %d, want 2", len(r.Messages))
	}
	// First: assistant with local_shell tool call
	if r.Messages[0].Role != "assistant" {
		t.Errorf("msg0 role = %s, want assistant", r.Messages[0].Role)
	}
	if r.Messages[0].Content[0].Type != ContentToolCall {
		t.Fatalf("msg0 content not tool call: %+v", r.Messages[0].Content[0])
	}
	tc := r.Messages[0].Content[0].ToolCall
	if tc.Name != "local_shell" {
		t.Errorf("tool name = %s, want local_shell", tc.Name)
	}
	if tc.ID != "call_1" {
		t.Errorf("tool id = %s, want call_1", tc.ID)
	}
	// Second: tool with local_shell output
	if r.Messages[1].Role != "tool" {
		t.Errorf("msg1 role = %s, want tool", r.Messages[1].Role)
	}
	tr := r.Messages[1].Content[0].ToolResult
	if tr.ID != "call_1" || tr.Content != "file contents here" {
		t.Errorf("tool result: %+v", tr)
	}
}

func TestDecodeResponses_FunctionCallOutputNameRecovery(t *testing.T) {
	// Responses API function_call_output items only contain call_id,
	// not the function name. The decoder must recover the name from
	// the corresponding function_call item earlier in the input.
	req := map[string]any{
		"model": "gemini-2.5-flash",
		"input": []any{
			map[string]any{
				"role":    "user",
				"content": []any{map[string]any{"type": "input_text", "text": "What's the weather?"}},
			},
			map[string]any{
				"type":      "function_call",
				"call_id":   "call_abc",
				"name":      "get_weather",
				"arguments": `{"city":"SF"}`,
			},
			map[string]any{
				"type":    "function_call_output",
				"call_id": "call_abc",
				"output":  `{"temp":72}`,
			},
		},
	}
	r := DecodeResponses(req)
	if len(r.Messages) != 3 {
		t.Fatalf("messages = %d, want 3", len(r.Messages))
	}
	// Message 2 should be the tool result with recovered name.
	tr := r.Messages[2].Content[0].ToolResult
	if tr == nil {
		t.Fatalf("msg2 content is not a tool result")
	}
	if tr.ID != "call_abc" {
		t.Errorf("tool result ID = %s, want call_abc", tr.ID)
	}
	if tr.Name != "get_weather" {
		t.Errorf("tool result Name = %q, want get_weather (recovered from function_call)", tr.Name)
	}
	if tr.Content != `{"temp":72}` {
		t.Errorf("tool result Content = %s", tr.Content)
	}
}

func TestEncodeResponses_Response(t *testing.T) {
	resp := &Response{
		ID:    "resp_123",
		Model: "gpt-4",
		Content: []Content{
			{Type: ContentText, Text: "Hello!"},
		},
		FinishReason: "stop",
		Usage:        Usage{Prompt: 10, Completion: 20},
	}
	out := EncodeResponses(resp)
	if out["object"] != "response" {
		t.Errorf("object = %v", out["object"])
	}
	if out["status"] != "completed" {
		t.Errorf("status = %v", out["status"])
	}
	output, _ := out["output"].([]any)
	if len(output) != 1 {
		t.Fatalf("output = %d", len(output))
	}
	item, _ := output[0].(map[string]any)
	if item["type"] != "message" {
		t.Errorf("item type = %v", item["type"])
	}
}

func TestEncodeResponses_LocalShellCall(t *testing.T) {
	resp := &Response{
		ID:    "resp_123",
		Model: "codex-1",
		Content: []Content{{
			Type: ContentToolCall,
			ToolCall: &ToolCall{
				ID:   "call_1",
				Name: "local_shell",
				Args: map[string]any{"type": "exec", "command": []any{"cat", "file.txt"}},
			},
		}},
		FinishReason: "tool_calls",
	}
	out := EncodeResponses(resp)
	output, _ := out["output"].([]any)
	if len(output) != 1 {
		t.Fatalf("output = %d", len(output))
	}
	item, _ := output[0].(map[string]any)
	if item["type"] != "local_shell_call" {
		t.Errorf("item type = %v, want local_shell_call", item["type"])
	}
	if item["call_id"] != "call_1" {
		t.Errorf("call_id = %v", item["call_id"])
	}
}

func TestNormalizeSchemaForGemini(t *testing.T) {
	schema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"name": map[string]any{"type": "string", "description": "Name"},
			"age":  map[string]any{"type": []any{"integer", "null"}},
		},
		"required":             []any{"name"},
		"additionalProperties": false,
		"$schema":              "http://json-schema.org/draft-07/schema#",
		"title":                "Person",
	}
	out := NormalizeSchemaForGemini(schema)
	if out["type"] != "OBJECT" {
		t.Errorf("type = %v, want OBJECT", out["type"])
	}
	if _, ok := out["$schema"]; ok {
		t.Error("$schema should be removed")
	}
	if _, ok := out["title"]; ok {
		t.Error("title should be removed")
	}
	if _, ok := out["additionalProperties"]; ok {
		t.Error("additionalProperties should be removed")
	}
	props, _ := out["properties"].(map[string]any)
	nameSchema, _ := props["name"].(map[string]any)
	if nameSchema["type"] != "STRING" {
		t.Errorf("name type = %v, want STRING", nameSchema["type"])
	}
	ageSchema, _ := props["age"].(map[string]any)
	if ageSchema["type"] != "INTEGER" {
		t.Errorf("age type = %v, want INTEGER", ageSchema["type"])
	}
	if ageSchema["nullable"] != true {
		t.Errorf("age nullable = %v, want true", ageSchema["nullable"])
	}
}

func TestRoundTrip_OpenAIChatToGeminiToOpenAIChat(t *testing.T) {
	// OpenAI request → IR → Gemini request → Gemini response → IR → OpenAI response
	openaiReq := map[string]any{
		"model":       "gemini-3-pro",
		"max_tokens":  float64(1000),
		"temperature": float64(0.7),
		"messages": []any{
			map[string]any{"role": "system", "content": "Be helpful."},
			map[string]any{"role": "user", "content": "What is 2+2?"},
		},
	}

	// Decode OpenAI → IR
	irReq := DecodeOpenAIChat(openaiReq)

	// Encode IR → Gemini
	geminiReq := EncodeGeminiRequest(irReq, "test-project", "session-1", 1)
	body, _ := geminiReq["request"].(map[string]any)

	// Verify Gemini request structure
	contents, _ := body["contents"].([]any)
	if len(contents) != 1 {
		t.Fatalf("contents = %d, want 1", len(contents))
	}

	// Simulate Gemini response
	geminiResp := map[string]any{
		"response": map[string]any{
			"candidates": []any{
				map[string]any{
					"content": map[string]any{
						"role": "model",
						"parts": []any{
							map[string]any{"text": "2+2 equals 4."},
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

	// Decode Gemini → IR
	irResp := DecodeGeminiResponse(geminiResp, "gemini-3-pro")

	// Encode IR → OpenAI
	openaiResp := EncodeOpenAIChat(irResp)
	choices, _ := openaiResp["choices"].([]any)
	choice, _ := choices[0].(map[string]any)
	msg, _ := choice["message"].(map[string]any)
	if msg["content"] != "2+2 equals 4." {
		t.Errorf("content = %v, want '2+2 equals 4.'", msg["content"])
	}
	if choice["finish_reason"] != "stop" {
		t.Errorf("finish = %v", choice["finish_reason"])
	}
}

func TestRoundTrip_AnthropicToGeminiToAnthropic(t *testing.T) {
	anthropicReq := map[string]any{
		"model":      "claude-sonnet-4-6",
		"max_tokens": float64(1024),
		"system":     "Be helpful.",
		"messages": []any{
			map[string]any{"role": "user", "content": "Hi"},
		},
	}

	// Decode Anthropic → IR
	irReq := DecodeAnthropic(anthropicReq)

	// Encode IR → Gemini
	geminiReq := EncodeGeminiRequest(irReq, "proj", "sess", 1)
	body, _ := geminiReq["request"].(map[string]any)
	sysInst, _ := body["systemInstruction"].(map[string]any)
	if sysInst == nil {
		t.Fatal("no systemInstruction")
	}

	// Simulate Gemini response
	geminiResp := map[string]any{
		"response": map[string]any{
			"candidates": []any{
				map[string]any{
					"content": map[string]any{
						"parts": []any{
							map[string]any{"text": "Hello there!"},
						},
					},
					"finishReason": "STOP",
				},
			},
			"usageMetadata": map[string]any{
				"promptTokenCount":     float64(5),
				"candidatesTokenCount": float64(3),
			},
		},
	}

	// Decode Gemini → IR
	irResp := DecodeGeminiResponse(geminiResp, "claude-sonnet-4-6")

	// Encode IR → Anthropic
	anthropicResp := EncodeAnthropic(irResp)
	if anthropicResp["type"] != "message" {
		t.Errorf("type = %v", anthropicResp["type"])
	}
	blocks, _ := anthropicResp["content"].([]any)
	block, _ := blocks[0].(map[string]any)
	if block["text"] != "Hello there!" {
		t.Errorf("text = %v", block["text"])
	}
	if anthropicResp["stop_reason"] != "end_turn" {
		t.Errorf("stop_reason = %v", anthropicResp["stop_reason"])
	}
}

func TestRoundTrip_ResponsesLocalShell(t *testing.T) {
	// This is the critical test: Codex local_shell should round-trip
	// through IR without losing the local_shell semantics.
	responsesReq := map[string]any{
		"model": "codex-1",
		"input": []any{
			map[string]any{
				"type": "message",
				"role": "user",
				"content": []any{
					map[string]any{"type": "input_text", "text": "Read the file"},
				},
			},
			map[string]any{
				"type":    "local_shell_call",
				"call_id": "call_1",
				"action": map[string]any{
					"type":    "exec",
					"command": []any{"cat", "file.txt"},
				},
			},
			map[string]any{
				"type":    "local_shell_call_output",
				"call_id": "call_1",
				"output":  "file contents",
			},
		},
		"tools": []any{
			map[string]any{"type": "local_shell"},
		},
	}

	// Decode Responses → IR
	irReq := DecodeResponses(responsesReq)

	// Verify local_shell tool call was decoded
	foundLocalShellCall := false
	for _, msg := range irReq.Messages {
		for _, c := range msg.Content {
			if c.Type == ContentToolCall && c.ToolCall.Name == "local_shell" {
				foundLocalShellCall = true
				if c.ToolCall.ID != "call_1" {
					t.Errorf("call ID = %s, want call_1", c.ToolCall.ID)
				}
			}
		}
	}
	if !foundLocalShellCall {
		t.Error("local_shell_call not found in IR messages")
	}

	// Verify local_shell tool result was decoded
	foundLocalShellResult := false
	for _, msg := range irReq.Messages {
		for _, c := range msg.Content {
			if c.Type == ContentToolResult && c.ToolResult.ID == "call_1" {
				foundLocalShellResult = true
				if c.ToolResult.Content != "file contents" {
					t.Errorf("result content = %s", c.ToolResult.Content)
				}
			}
		}
	}
	if !foundLocalShellResult {
		t.Error("local_shell_call_output not found in IR messages")
	}

	// Verify local_shell tool definition was decoded
	foundLocalShellTool := false
	for _, tool := range irReq.Tools {
		if tool.Kind == ToolLocalShell {
			foundLocalShellTool = true
		}
	}
	if !foundLocalShellTool {
		t.Error("local_shell tool definition not found in IR tools")
	}

	// Encode IR → Gemini (should work as function call)
	geminiReq := EncodeGeminiRequest(irReq, "proj", "sess", 1)
	body, _ := geminiReq["request"].(map[string]any)
	contents, _ := body["contents"].([]any)
	if len(contents) < 3 {
		t.Errorf("contents = %d, want >= 3", len(contents))
	}

	// Simulate Gemini response with function call back
	geminiResp := map[string]any{
		"response": map[string]any{
			"candidates": []any{
				map[string]any{
					"content": map[string]any{
						"parts": []any{
							map[string]any{
								"functionCall": map[string]any{
									"name": "local_shell",
									"args": map[string]any{
										"type":    "exec",
										"command": []any{"ls", "-la"},
									},
									"id": "call_2",
								},
							},
						},
					},
					"finishReason": "STOP",
				},
			},
		},
	}

	// Decode Gemini → IR
	irResp := DecodeGeminiResponse(geminiResp, "codex-1")

	// Encode IR → Responses (should produce local_shell_call item)
	responsesResp := EncodeResponses(irResp)
	output, _ := responsesResp["output"].([]any)
	if len(output) != 1 {
		t.Fatalf("output = %d, want 1", len(output))
	}
	item, _ := output[0].(map[string]any)
	if item["type"] != "local_shell_call" {
		t.Errorf("item type = %v, want local_shell_call", item["type"])
	}
	if item["call_id"] != "call_2" {
		t.Errorf("call_id = %v, want call_2", item["call_id"])
	}
	// Verify action is embedded
	action, _ := item["action"].(map[string]any)
	if action == nil {
		t.Error("no action in local_shell_call output")
	} else {
		if action["type"] != "exec" {
			t.Errorf("action type = %v, want exec", action["type"])
		}
	}
}

func TestJSONRoundTrip(t *testing.T) {
	// Verify that our IR types can be JSON-serialized without errors.
	r := &Request{
		Model:  "test",
		System: "system",
		Messages: []Message{
			{Role: "user", Content: []Content{{Type: ContentText, Text: "hi"}}},
		},
	}
	b, err := json.Marshal(r)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if len(b) == 0 {
		t.Error("empty JSON output")
	}
}
