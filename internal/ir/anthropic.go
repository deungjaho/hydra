// Package ir provides the intermediate representation for protocol translation.
//
// This file implements the Anthropic Messages codec: decoding Anthropic
// requests into IR and encoding IR responses back to Anthropic format.
package ir

import (
	"encoding/json"
	"fmt"
)

// DecodeAnthropic transforms an Anthropic Messages API request into IR.
func DecodeAnthropic(req map[string]any) *Request {
	r := &Request{
		Extra: make(map[string]any),
	}

	if m, ok := req["model"].(string); ok {
		r.Model = m
	}
	if s, ok := req["stream"].(bool); ok {
		r.Stream = s
	}
	if mt, ok := req["max_tokens"].(float64); ok {
		r.MaxTokens = int64(mt)
	}
	if temp, ok := req["temperature"].(float64); ok {
		t := temp
		r.Temperature = &t
	}
	if topP, ok := req["top_p"].(float64); ok {
		p := topP
		r.TopP = &p
	}
	if topK, ok := req["top_k"].(float64); ok {
		k := int64(topK)
		r.TopK = &k
	}
	if stops, ok := req["stop_sequences"].([]any); ok {
		for _, s := range stops {
			if str, ok := s.(string); ok {
				r.StopSequences = append(r.StopSequences, str)
			}
		}
	}

	// System prompt (string or array of text blocks).
	if sys, ok := req["system"].(string); ok {
		r.System = sys
	} else if sysArr, ok := req["system"].([]any); ok {
		var parts []string
		for _, blockAny := range sysArr {
			block, _ := blockAny.(map[string]any)
			if block == nil {
				continue
			}
			if bt, _ := block["type"].(string); bt == "text" {
				if t, ok := block["text"].(string); ok {
					parts = append(parts, t)
				}
			}
		}
		r.System = fmt.Sprintf("%s", joinStrings(parts, "\n"))
	}

	// Thinking config.
	if thinking, ok := req["thinking"].(map[string]any); ok {
		effort := ""
		if et, _ := thinking["type"].(string); et == "enabled" {
			effort = "high"
		} else if et == "adaptive" {
			effort = "medium"
		}
		budget := int64(0)
		if bt, ok := thinking["budget_tokens"].(float64); ok {
			budget = int64(bt)
		}
		r.Reasoning = &Reasoning{Effort: effort, BudgetTokens: budget}
	}

	// Messages — build tool_use_id → name map for tool_result recovery.
	var msgs []any
	if m, ok := req["messages"].([]any); ok {
		msgs = m
	}
	toolNameMap := buildAnthropicToolNameMap(msgs)

	for _, msgAny := range msgs {
		msg, _ := msgAny.(map[string]any)
		if msg == nil {
			continue
		}
		r.Messages = append(r.Messages, decodeAnthropicMessage(msg, toolNameMap))
	}

	// Tools.
	if tools, ok := req["tools"].([]any); ok {
		for _, toolAny := range tools {
			tool, _ := toolAny.(map[string]any)
			if tool == nil {
				continue
			}
			t := decodeAnthropicTool(tool)
			r.Tools = append(r.Tools, t)
		}
	}

	// Tool choice.
	if tc, ok := req["tool_choice"]; ok {
		r.ToolChoice = tc
	}

	// Pass-through extras.
	for _, key := range []string{"metadata", "user_id"} {
		if v, ok := req[key]; ok {
			r.Extra[key] = v
		}
	}

	return r
}

// decodeAnthropicMessage converts an Anthropic message to IR.
func decodeAnthropicMessage(msg map[string]any, toolNameMap map[string]string) Message {
	m := Message{}
	role, _ := msg["role"].(string)
	m.Role = role

	// String content.
	if s, ok := msg["content"].(string); ok {
		m.Content = []Content{{Type: ContentText, Text: s}}
		return m
	}

	// Array content (content blocks).
	if arr, ok := msg["content"].([]any); ok {
		for _, blockAny := range arr {
			block, _ := blockAny.(map[string]any)
			if block == nil {
				continue
			}
			c := decodeAnthropicBlock(block, toolNameMap)
			m.Content = append(m.Content, c)
		}
	}

	return m
}

// buildAnthropicToolNameMap builds a map from tool_use_id to function name.
func buildAnthropicToolNameMap(messages []any) map[string]string {
	m := make(map[string]string)
	for _, msgAny := range messages {
		msg, _ := msgAny.(map[string]any)
		if msg == nil {
			continue
		}
		arr, _ := msg["content"].([]any)
		for _, blockAny := range arr {
			block, _ := blockAny.(map[string]any)
			if block == nil {
				continue
			}
			if bt, _ := block["type"].(string); bt == "tool_use" {
				id, _ := block["id"].(string)
				name, _ := block["name"].(string)
				if id != "" {
					m[id] = name
				}
			}
		}
	}
	return m
}

// decodeAnthropicBlock converts an Anthropic content block to IR.
func decodeAnthropicBlock(block map[string]any, toolNameMap map[string]string) Content {
	bt, _ := block["type"].(string)
	switch bt {
	case "text":
		t, _ := block["text"].(string)
		return Content{Type: ContentText, Text: t}

	case "thinking":
		t, _ := block["thinking"].(string)
		sig := "skip_thought_signature_validator"
		if s, ok := block["signature"].(string); ok && s != "" {
			sig = s
		}
		return Content{
			Type:     ContentThinking,
			Thinking: &Thinking{Text: t, Signature: sig},
		}

	case "redacted_thinking":
		// Redacted thinking has no readable text; preserve a placeholder.
		data, _ := block["data"].(string)
		return Content{
			Type:     ContentThinking,
			Thinking: &Thinking{Text: "[redacted thinking: " + data + "]", Redacted: true},
		}

	case "image":
		if src, ok := block["source"].(map[string]any); ok {
			srcType, _ := src["type"].(string)
			if srcType == "base64" {
				mime, _ := src["media_type"].(string)
				data, _ := src["data"].(string)
				return Content{Type: ContentImage, Image: &Image{MimeType: mime, Data: data}}
			}
		}

	case "tool_use":
		id, _ := block["id"].(string)
		name, _ := block["name"].(string)
		var args map[string]any
		if input, ok := block["input"].(map[string]any); ok {
			args = input
		}
		sig := "skip_thought_signature_validator"
		if s, ok := block["thoughtSignature"].(string); ok && s != "" {
			sig = s
		}
		if s, ok := block["signature"].(string); ok && s != "" {
			sig = s
		}
		return Content{
			Type:     ContentToolCall,
			ToolCall: &ToolCall{ID: id, Name: name, Args: args, Signature: sig},
		}

	case "tool_result":
		id, _ := block["tool_use_id"].(string)
		isError, _ := block["is_error"].(bool)
		content := extractToolResultText(block["content"])
		sig, _ := block["thoughtSignature"].(string)
		// Recover function name from the tool_use_id via the name map.
		name, _ := toolNameMap[id]
		if name == "" {
			name = id
		}
		return Content{
			Type: ContentToolResult,
			ToolResult: &ToolResult{
				ID:        id,
				Name:      name,
				Content:   content,
				IsError:   isError,
				Signature: sig,
			},
		}
	}
	return Content{Type: ContentText}
}

// extractToolResultText extracts text from a tool_result content field.
func extractToolResultText(content any) string {
	if s, ok := content.(string); ok {
		return s
	}
	if arr, ok := content.([]any); ok {
		var parts []string
		for _, blockAny := range arr {
			block, _ := blockAny.(map[string]any)
			if block == nil {
				continue
			}
			if bt, _ := block["type"].(string); bt == "text" {
				if t, ok := block["text"].(string); ok {
					parts = append(parts, t)
				}
			}
		}
		return joinStrings(parts, "\n")
	}
	return ""
}

// decodeAnthropicTool converts an Anthropic tool definition to IR.
func decodeAnthropicTool(tool map[string]any) Tool {
	t := Tool{}
	t.Name, _ = tool["name"].(string)
	t.Description, _ = tool["description"].(string)
	if schema, ok := tool["input_schema"].(map[string]any); ok {
		t.Schema = schema
	}
	// Check for special tool types.
	if tt, ok := tool["type"].(string); ok && tt != "" {
		t.Kind = anthropicToolKind(tt)
	}
	return t
}

// anthropicToolKind maps Anthropic tool type strings to IR ToolKind.
func anthropicToolKind(t string) ToolKind {
	switch t {
	case "computer_20241022", "computer":
		return ToolComputer
	case "web_search_20250305", "web_search":
		return ToolWebSearch
	case "mcp":
		return ToolMCP
	}
	return ToolFunction
}

// EncodeAnthropic transforms an IR Response into an Anthropic Messages
// API response.
func EncodeAnthropic(resp *Response) map[string]any {
	var blocks []any
	hasToolUse := false

	for _, c := range resp.Content {
		switch c.Type {
		case ContentText:
			if c.Text != "" {
				blocks = append(blocks, map[string]any{
					"type": "text",
					"text": c.Text,
				})
			}
		case ContentThinking:
			block := map[string]any{
				"type":     "thinking",
				"thinking": c.Thinking.Text,
			}
			if c.Thinking.Signature != "" {
				block["signature"] = c.Thinking.Signature
			}
			blocks = append(blocks, block)
		case ContentToolCall:
			hasToolUse = true
			tc := c.ToolCall
			id := tc.ID
			if id == "" {
				id = "toolu_" + tc.Name + "_" + compactUUID()[:8]
			}
			input := tc.Args
			if input == nil {
				input = map[string]any{}
			}
			block := map[string]any{
				"type":  "tool_use",
				"id":    id,
				"name":  tc.Name,
				"input": input,
			}
			blocks = append(blocks, block)
		}
	}

	if len(blocks) == 0 {
		blocks = []any{map[string]any{"type": "text", "text": ""}}
	}

	stopReason := mapIRToAnthropicStop(resp.FinishReason)
	if hasToolUse {
		stopReason = "tool_use"
	}

	id := resp.ID
	if id == "" {
		id = "msg_" + compactUUID()[:12]
	}

	usage := map[string]any{
		"input_tokens":                resp.Usage.Prompt,
		"output_tokens":               resp.Usage.Completion,
		"cache_creation_input_tokens": 0,
	}
	if resp.Usage.Cached > 0 {
		usage["cache_read_input_tokens"] = resp.Usage.Cached
	} else {
		usage["cache_read_input_tokens"] = 0
	}

	return map[string]any{
		"id":            id,
		"type":          "message",
		"role":          "assistant",
		"model":         resp.Model,
		"content":       blocks,
		"stop_reason":   stopReason,
		"stop_sequence": nil,
		"usage":         usage,
	}
}

// mapIRToAnthropicStop maps IR finish reasons to Anthropic stop reasons.
func mapIRToAnthropicStop(fr string) string {
	switch fr {
	case "stop":
		return "end_turn"
	case "length":
		return "max_tokens"
	case "tool_calls":
		return "tool_use"
	case "content_filter":
		return "end_turn"
	}
	return "end_turn"
}

// joinStrings joins strings with a separator.
func joinStrings(parts []string, sep string) string {
	if len(parts) == 0 {
		return ""
	}
	result := parts[0]
	for i := 1; i < len(parts); i++ {
		result += sep + parts[i]
	}
	return result
}

// EncodeAnthropicStreamEvents encodes IR stream events as Anthropic SSE.
// This is a simplified version; the full implementation requires a
// state machine to manage content_block_start/stop lifecycle.
func EncodeAnthropicStreamEvents(events []StreamEvent, respID, model string) []string {
	var out []string
	blockIdx := -1
	currentBlockType := ""

	// message_start
	startData := map[string]any{
		"type": "message_start",
		"message": map[string]any{
			"id":            respID,
			"type":          "message",
			"role":          "assistant",
			"content":       []any{},
			"model":         model,
			"stop_reason":   nil,
			"stop_sequence": nil,
			"usage": map[string]any{
				"input_tokens":  0,
				"output_tokens": 0,
			},
		},
	}
	out = append(out, sseEvent("message_start", startData))

	for _, ev := range events {
		switch ev.Type {
		case StreamTextDelta:
			if currentBlockType != "text" {
				if currentBlockType != "" {
					out = append(out, sseEvent("content_block_stop", map[string]any{
						"type":  "content_block_stop",
						"index": blockIdx,
					}))
				}
				blockIdx++
				currentBlockType = "text"
				out = append(out, sseEvent("content_block_start", map[string]any{
					"type":  "content_block_start",
					"index": blockIdx,
					"content_block": map[string]any{
						"type": "text",
						"text": "",
					},
				}))
			}
			out = append(out, sseEvent("content_block_delta", map[string]any{
				"type":  "content_block_delta",
				"index": blockIdx,
				"delta": map[string]any{"type": "text_delta", "text": ev.Delta},
			}))

		case StreamThinkingDelta:
			if currentBlockType != "thinking" {
				if currentBlockType != "" {
					out = append(out, sseEvent("content_block_stop", map[string]any{
						"type":  "content_block_stop",
						"index": blockIdx,
					}))
				}
				blockIdx++
				currentBlockType = "thinking"
				out = append(out, sseEvent("content_block_start", map[string]any{
					"type":  "content_block_start",
					"index": blockIdx,
					"content_block": map[string]any{
						"type":     "thinking",
						"thinking": "",
					},
				}))
			}
			out = append(out, sseEvent("content_block_delta", map[string]any{
				"type":  "content_block_delta",
				"index": blockIdx,
				"delta": map[string]any{"type": "thinking_delta", "thinking": ev.Delta},
			}))

		case StreamToolCallDone:
			if currentBlockType != "" {
				out = append(out, sseEvent("content_block_stop", map[string]any{
					"type":  "content_block_stop",
					"index": blockIdx,
				}))
			}
			blockIdx++
			currentBlockType = "tool_use"
			tc := ev.ToolCall
			out = append(out, sseEvent("content_block_start", map[string]any{
				"type":  "content_block_start",
				"index": blockIdx,
				"content_block": map[string]any{
					"type":  "tool_use",
					"id":    tc.ID,
					"name":  tc.Name,
					"input": map[string]any{},
				},
			}))
			argsJSON := tc.RawArgs
			if argsJSON == "" {
				if b, err := json.Marshal(tc.Args); err == nil {
					argsJSON = string(b)
				}
			}
			if argsJSON != "" {
				out = append(out, sseEvent("content_block_delta", map[string]any{
					"type":  "content_block_delta",
					"index": blockIdx,
					"delta": map[string]any{"type": "input_json_delta", "partial_json": argsJSON},
				}))
			}
			out = append(out, sseEvent("content_block_stop", map[string]any{
				"type":  "content_block_stop",
				"index": blockIdx,
			}))
			currentBlockType = ""

		case StreamUsage:
			// Update usage for final message_delta.

		case StreamDone:
			if currentBlockType != "" {
				out = append(out, sseEvent("content_block_stop", map[string]any{
					"type":  "content_block_stop",
					"index": blockIdx,
				}))
			}
			stopReason := mapIRToAnthropicStop(ev.FinishReason)
			out = append(out, sseEvent("message_delta", map[string]any{
				"type": "message_delta",
				"delta": map[string]any{
					"stop_reason":   stopReason,
					"stop_sequence": nil,
				},
				"usage": map[string]any{
					"output_tokens": 0,
				},
			}))
			out = append(out, sseEvent("message_stop", map[string]any{
				"type": "message_stop",
			}))
		}
	}

	return out
}

// sseEvent formats a typed SSE event.
func sseEvent(eventType string, data any) string {
	b, _ := json.Marshal(data)
	return fmt.Sprintf("event: %s\ndata: %s\n\n", eventType, string(b))
}
