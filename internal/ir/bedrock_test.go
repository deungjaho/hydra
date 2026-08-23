package ir

import "testing"

func TestDecodeBedrock_BasicRequest(t *testing.T) {
	req := map[string]any{
		"modelId": "anthropic.claude-3-sonnet-20240229-v1:0",
		"inferenceConfig": map[string]any{
			"maxTokens":     float64(1024),
			"temperature":   float64(0.5),
			"topP":          float64(0.9),
			"stopSequences": []any{"END"},
		},
		"system": []any{
			map[string]any{"text": "You are a helpful assistant."},
		},
		"messages": []any{
			map[string]any{
				"role": "user",
				"content": []any{
					map[string]any{"type": "text", "text": "Hello, world!"},
				},
			},
		},
	}
	r := DecodeBedrock(req)
	if r.Model != "anthropic.claude-3-sonnet-20240229-v1:0" {
		t.Errorf("model = %s, want anthropic.claude-3-sonnet-20240229-v1:0", r.Model)
	}
	if r.MaxTokens != 1024 {
		t.Errorf("maxTokens = %d, want 1024", r.MaxTokens)
	}
	if r.Temperature == nil || *r.Temperature != 0.5 {
		t.Errorf("temperature = %v, want 0.5", r.Temperature)
	}
	if r.TopP == nil || *r.TopP != 0.9 {
		t.Errorf("topP = %v, want 0.9", r.TopP)
	}
	if len(r.StopSequences) != 1 || r.StopSequences[0] != "END" {
		t.Errorf("stopSequences = %v, want [END]", r.StopSequences)
	}
	if r.System != "You are a helpful assistant." {
		t.Errorf("system = %q, want %q", r.System, "You are a helpful assistant.")
	}
	if len(r.Messages) != 1 {
		t.Fatalf("messages = %d, want 1", len(r.Messages))
	}
	if r.Messages[0].Role != "user" {
		t.Errorf("role = %s, want user", r.Messages[0].Role)
	}
	if len(r.Messages[0].Content) != 1 {
		t.Fatalf("content blocks = %d, want 1", len(r.Messages[0].Content))
	}
	if r.Messages[0].Content[0].Type != ContentText {
		t.Errorf("content type = %v, want ContentText", r.Messages[0].Content[0].Type)
	}
	if r.Messages[0].Content[0].Text != "Hello, world!" {
		t.Errorf("text = %q, want %q", r.Messages[0].Content[0].Text, "Hello, world!")
	}
}

func TestDecodeBedrock_ToolUse(t *testing.T) {
	req := map[string]any{
		"modelId": "anthropic.claude-3-sonnet-20240229-v1:0",
		"messages": []any{
			map[string]any{
				"role": "assistant",
				"content": []any{
					map[string]any{
						"type": "toolUse",
						"toolUse": map[string]any{
							"toolUseId": "tool-123",
							"name":      "get_weather",
							"input":     map[string]any{"city": "SF"},
						},
					},
				},
			},
			map[string]any{
				"role": "user",
				"content": []any{
					map[string]any{
						"type": "toolResult",
						"toolResult": map[string]any{
							"toolUseId": "tool-123",
							"content": []any{
								map[string]any{"type": "text", "text": "Sunny, 72F"},
							},
						},
					},
				},
			},
		},
		"toolConfig": map[string]any{
			"tools": []any{
				map[string]any{
					"toolSpec": map[string]any{
						"name":        "get_weather",
						"description": "Get weather for a city",
						"inputSchema": map[string]any{
							"json": map[string]any{
								"type": "object",
								"properties": map[string]any{
									"city": map[string]any{"type": "string"},
								},
							},
						},
					},
				},
			},
		},
	}
	r := DecodeBedrock(req)

	// Check tool definition.
	if len(r.Tools) != 1 {
		t.Fatalf("tools = %d, want 1", len(r.Tools))
	}
	if r.Tools[0].Name != "get_weather" {
		t.Errorf("tool name = %s, want get_weather", r.Tools[0].Name)
	}
	if r.Tools[0].Description != "Get weather for a city" {
		t.Errorf("tool desc = %s", r.Tools[0].Description)
	}
	if r.Tools[0].Schema == nil {
		t.Error("tool schema should not be nil")
	}

	// Check toolUse in assistant message.
	if len(r.Messages) != 2 {
		t.Fatalf("messages = %d, want 2", len(r.Messages))
	}
	assistantMsg := r.Messages[0]
	if assistantMsg.Role != "assistant" {
		t.Errorf("role = %s, want assistant", assistantMsg.Role)
	}
	if len(assistantMsg.Content) != 1 {
		t.Fatalf("content = %d, want 1", len(assistantMsg.Content))
	}
	tc := assistantMsg.Content[0].ToolCall
	if tc == nil {
		t.Fatal("toolCall is nil")
	}
	if tc.ID != "tool-123" {
		t.Errorf("toolCall ID = %s, want tool-123", tc.ID)
	}
	if tc.Name != "get_weather" {
		t.Errorf("toolCall name = %s, want get_weather", tc.Name)
	}
	if tc.Args["city"] != "SF" {
		t.Errorf("toolCall args = %v, want {city: SF}", tc.Args)
	}

	// Check toolResult in user message.
	userMsg := r.Messages[1]
	if len(userMsg.Content) != 1 {
		t.Fatalf("content = %d, want 1", len(userMsg.Content))
	}
	tr := userMsg.Content[0].ToolResult
	if tr == nil {
		t.Fatal("toolResult is nil")
	}
	if tr.ID != "tool-123" {
		t.Errorf("toolResult ID = %s, want tool-123", tr.ID)
	}
	if tr.Name != "get_weather" {
		t.Errorf("toolResult name = %s, want get_weather (recovered from map)", tr.Name)
	}
	if tr.Content != "Sunny, 72F" {
		t.Errorf("toolResult content = %q, want %q", tr.Content, "Sunny, 72F")
	}
}

func TestDecodeBedrock_ImageContent(t *testing.T) {
	req := map[string]any{
		"modelId": "anthropic.claude-3-sonnet-20240229-v1:0",
		"messages": []any{
			map[string]any{
				"role": "user",
				"content": []any{
					map[string]any{
						"type": "image",
						"image": map[string]any{
							"format": "png",
							"source": map[string]any{
								"bytes": "iVBORw0KGgo=",
							},
						},
					},
				},
			},
		},
	}
	r := DecodeBedrock(req)
	if len(r.Messages) != 1 || len(r.Messages[0].Content) != 1 {
		t.Fatalf("expected 1 message with 1 content block")
	}
	c := r.Messages[0].Content[0]
	if c.Type != ContentImage {
		t.Errorf("type = %v, want ContentImage", c.Type)
	}
	if c.Image == nil {
		t.Fatal("image is nil")
	}
	if c.Image.MimeType != "image/png" {
		t.Errorf("mime = %s, want image/png", c.Image.MimeType)
	}
	if c.Image.Data != "iVBORw0KGgo=" {
		t.Errorf("data = %s", c.Image.Data)
	}
}

func TestEncodeBedrock_BasicResponse(t *testing.T) {
	resp := &Response{
		ID:    "msg_test123",
		Model: "anthropic.claude-3-sonnet-20240229-v1:0",
		Content: []Content{
			{Type: ContentText, Text: "Hello from Bedrock!"},
		},
		FinishReason: "stop",
		Usage:        Usage{Prompt: 10, Completion: 5},
	}
	out := EncodeBedrock(resp)

	output, ok := out["output"].(map[string]any)
	if !ok {
		t.Fatal("output missing")
	}
	msg, ok := output["message"].(map[string]any)
	if !ok {
		t.Fatal("message missing")
	}
	content, ok := msg["content"].([]any)
	if !ok || len(content) != 1 {
		t.Fatalf("content = %v, want 1 block", content)
	}
	block := content[0].(map[string]any)
	if block["type"] != "text" {
		t.Errorf("type = %s, want text", block["type"])
	}
	if block["text"] != "Hello from Bedrock!" {
		t.Errorf("text = %s", block["text"])
	}
	if out["stopReason"] != "end_turn" {
		t.Errorf("stopReason = %s, want end_turn", out["stopReason"])
	}
	usage := out["usage"].(map[string]any)
	if usage["inputTokens"] != int64(10) {
		t.Errorf("inputTokens = %v, want 10", usage["inputTokens"])
	}
	if usage["outputTokens"] != int64(5) {
		t.Errorf("outputTokens = %v, want 5", usage["outputTokens"])
	}
	if usage["totalTokens"] != int64(15) {
		t.Errorf("totalTokens = %v, want 15", usage["totalTokens"])
	}
}

func TestEncodeBedrock_ToolUse(t *testing.T) {
	resp := &Response{
		ID:    "msg_test456",
		Model: "anthropic.claude-3-sonnet-20240229-v1:0",
		Content: []Content{
			{Type: ContentToolCall, ToolCall: &ToolCall{
				ID:   "tooluse_abc",
				Name: "get_weather",
				Args: map[string]any{"city": "SF"},
			}},
		},
		FinishReason: "tool_calls",
	}
	out := EncodeBedrock(resp)
	if out["stopReason"] != "tool_use" {
		t.Errorf("stopReason = %s, want tool_use", out["stopReason"])
	}
	output := out["output"].(map[string]any)
	msg := output["message"].(map[string]any)
	content := msg["content"].([]any)
	block := content[0].(map[string]any)
	if block["type"] != "toolUse" {
		t.Errorf("type = %s, want toolUse", block["type"])
	}
	toolUse := block["toolUse"].(map[string]any)
	if toolUse["toolUseId"] != "tooluse_abc" {
		t.Errorf("toolUseId = %s", toolUse["toolUseId"])
	}
	if toolUse["name"] != "get_weather" {
		t.Errorf("name = %s", toolUse["name"])
	}
}

func TestEncodeBedrock_EmptyContent(t *testing.T) {
	resp := &Response{
		ID:           "msg_empty",
		Model:        "test",
		Content:      []Content{},
		FinishReason: "stop",
	}
	out := EncodeBedrock(resp)
	output := out["output"].(map[string]any)
	msg := output["message"].(map[string]any)
	content := msg["content"].([]any)
	if len(content) != 1 {
		t.Fatalf("content = %d, want 1 (empty text block)", len(content))
	}
	block := content[0].(map[string]any)
	if block["type"] != "text" {
		t.Errorf("type = %s, want text", block["type"])
	}
}

func TestEncodeBedrock_CachedTokens(t *testing.T) {
	resp := &Response{
		ID:           "msg_cached",
		Model:        "test",
		Content:      []Content{{Type: ContentText, Text: "hi"}},
		FinishReason: "stop",
		Usage:        Usage{Prompt: 10, Completion: 5, Cached: 3},
	}
	out := EncodeBedrock(resp)
	usage := out["usage"].(map[string]any)
	if usage["cacheReadInputTokens"] != int64(3) {
		t.Errorf("cacheReadInputTokens = %v, want 3", usage["cacheReadInputTokens"])
	}
}

func TestDecodeBedrock_ToolChoice(t *testing.T) {
	tests := []struct {
		name   string
		choice any
		want   any
	}{
		{
			"auto",
			map[string]any{"type": "auto"},
			"auto",
		},
		{
			"any",
			map[string]any{"type": "any"},
			"required",
		},
		{
			"specific",
			map[string]any{
				"type": "tool",
				"tool": map[string]any{"name": "get_weather"},
			},
			map[string]any{
				"type":     "function",
				"function": map[string]any{"name": "get_weather"},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := map[string]any{
				"modelId": "test",
				"toolConfig": map[string]any{
					"toolChoice": tt.choice,
				},
			}
			r := DecodeBedrock(req)
			got := r.ToolChoice
			switch want := tt.want.(type) {
			case string:
				if g, ok := got.(string); !ok || g != want {
					t.Errorf("toolChoice = %v, want %s", got, want)
				}
			case map[string]any:
				g, ok := got.(map[string]any)
				if !ok {
					t.Errorf("toolChoice = %v, want %v", got, want)
					return
				}
				if g["type"] != want["type"] {
					t.Errorf("type = %v, want %v", g["type"], want["type"])
				}
			}
		})
	}
}

func TestEncodeBedrockStreamEvents_TextDelta(t *testing.T) {
	events := []StreamEvent{
		{Type: StreamTextDelta, Delta: "Hello"},
		{Type: StreamTextDelta, Delta: " world"},
		{Type: StreamDone, FinishReason: "stop"},
	}
	out := EncodeBedrockStreamEvents(events, "msg_stream1", "test-model")
	if len(out) < 4 {
		t.Fatalf("events = %d, want at least 4", len(out))
	}
	// First event should be messageStart.
	if !contains(out[0], "messageStart") {
		t.Errorf("first event = %s, want messageStart", out[0])
	}
	// Should contain contentBlockStart for text.
	foundStart := false
	for _, e := range out {
		if contains(e, "contentBlockStart") {
			foundStart = true
			break
		}
	}
	if !foundStart {
		t.Error("missing contentBlockStart event")
	}
	// Should contain messageStop.
	foundStop := false
	for _, e := range out {
		if contains(e, "messageStop") {
			foundStop = true
			break
		}
	}
	if !foundStop {
		t.Error("missing messageStop event")
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && indexOf(s, substr) >= 0
}

func indexOf(s, substr string) int {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return i
		}
	}
	return -1
}
