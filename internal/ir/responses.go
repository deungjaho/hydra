// Package ir provides the intermediate representation for protocol translation.
//
// This file implements the OpenAI Responses API codec: decoding Responses
// API requests into IR and encoding IR responses back to Responses API
// format. This includes handling of special item types like local_shell.
package ir

import (
	"encoding/json"
	"fmt"
)

// DecodeResponses transforms an OpenAI Responses API request into IR.
func DecodeResponses(req map[string]any) *Request {
	r := &Request{
		Extra: make(map[string]any),
	}

	if m, ok := req["model"].(string); ok {
		r.Model = m
	}
	if s, ok := req["stream"].(bool); ok {
		r.Stream = s
	}
	if mt, ok := req["max_output_tokens"].(float64); ok {
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

	// Reasoning.
	if reasoning, ok := req["reasoning"].(map[string]any); ok {
		effort, _ := reasoning["effort"].(string)
		r.Reasoning = &Reasoning{Effort: effort}
	}

	// Text format → response_format.
	if text, ok := req["text"].(map[string]any); ok {
		if format, ok := text["format"].(map[string]any); ok {
			ft, _ := format["type"].(string)
			var schema map[string]any
			if s, ok := format["schema"].(map[string]any); ok {
				schema = s
			}
			r.ResponseFormat = &ResponseFormat{Type: ft, Schema: schema}
		}
	}

	// Instructions → system prompt.
	if instr, ok := req["instructions"].(string); ok {
		r.System = instr
	}

	// Input → messages.
	if input, ok := req["input"].(string); ok {
		r.Messages = append(r.Messages, Message{
			Role:    "user",
			Content: []Content{{Type: ContentText, Text: input}},
		})
	} else if inputArr, ok := req["input"].([]any); ok {
		// Build a call_id → function_name map from function_call items
		// so that function_call_output items can recover the function
		// name (which Gemini requires but Responses API omits).
		callNameMap := make(map[string]string)
		for _, itemAny := range inputArr {
			item, _ := itemAny.(map[string]any)
			if item == nil {
				continue
			}
			itemType, _ := item["type"].(string)
			if itemType == "function_call" || itemType == "custom_tool_call" {
				callID, _ := item["call_id"].(string)
				name, _ := item["name"].(string)
				if callID != "" && name != "" {
					callNameMap[callID] = name
				}
			}
		}
		for _, itemAny := range inputArr {
			item, _ := itemAny.(map[string]any)
			if item == nil {
				continue
			}
			msgs := decodeResponsesItem(item, callNameMap)
			r.Messages = append(r.Messages, msgs...)
		}
	}

	// Tools.
	if tools, ok := req["tools"].([]any); ok {
		for _, toolAny := range tools {
			tool, _ := toolAny.(map[string]any)
			if tool == nil {
				continue
			}
			t := decodeResponsesTool(tool)
			r.Tools = append(r.Tools, t)
		}
	}

	// Tool choice.
	if tc, ok := req["tool_choice"]; ok {
		r.ToolChoice = tc
	}

	// Pass-through extras.
	for _, key := range []string{"parallel_tool_calls", "user", "metadata",
		"previous_response_id", "store", "truncation"} {
		if v, ok := req[key]; ok {
			r.Extra[key] = v
		}
	}

	return r
}

// decodeResponsesItem converts a Responses API input item to IR messages.
// Some items (like local_shell_call) produce multiple IR content pieces.
// callNameMap maps call_id → function_name from prior function_call items,
// used to recover the function name for function_call_output items.
func decodeResponsesItem(item map[string]any, callNameMap map[string]string) []Message {
	itemType, _ := item["type"].(string)

	switch itemType {
	case "message", "":
		role, _ := item["role"].(string)
		content := decodeResponsesContent(item["content"])
		return []Message{{Role: role, Content: content}}

	case "function_call", "custom_tool_call":
		id, _ := item["call_id"].(string)
		name, _ := item["name"].(string)
		argsStr, _ := item["arguments"].(string)
		var args map[string]any
		if argsStr != "" {
			_ = json.Unmarshal([]byte(argsStr), &args)
		}
		return []Message{{
			Role: "assistant",
			Content: []Content{{
				Type: ContentToolCall,
				ToolCall: &ToolCall{
					ID:      id,
					Name:    name,
					Args:    args,
					RawArgs: argsStr,
				},
			}},
		}}

	case "function_call_output", "custom_tool_call_output":
		callID, _ := item["call_id"].(string)
		output, _ := item["output"].(string)
		// Recover the function name from the call_id map (Gemini
		// requires functionResponse.name to be non-empty).
		name := ""
		if callID != "" {
			name = callNameMap[callID]
		}
		return []Message{{
			Role: "tool",
			Content: []Content{{
				Type: ContentToolResult,
				ToolResult: &ToolResult{
					ID:      callID,
					Name:    name,
					Content: output,
				},
			}},
		}}

	case "local_shell_call":
		// Codex local_shell_call: {type, action: {type, command, ...}}
		// Convert to a function call with name "local_shell".
		action, _ := item["action"].(map[string]any)
		if action == nil {
			return nil
		}
		id, _ := item["call_id"].(string)
		if id == "" {
			id, _ = item["id"].(string)
		}
		args := action
		argsStr := ""
		if b, err := json.Marshal(action); err == nil {
			argsStr = string(b)
		}
		return []Message{{
			Role: "assistant",
			Content: []Content{{
				Type: ContentToolCall,
				ToolCall: &ToolCall{
					ID:      id,
					Name:    "local_shell",
					Args:    args,
					RawArgs: argsStr,
				},
			}},
		}}

	case "local_shell_call_output":
		// Codex local_shell_call_output: {type, call_id, output}
		callID, _ := item["call_id"].(string)
		output, _ := item["output"].(string)
		return []Message{{
			Role: "tool",
			Content: []Content{{
				Type: ContentToolResult,
				ToolResult: &ToolResult{
					ID:      callID,
					Name:    "local_shell",
					Content: output,
				},
			}},
		}}

	case "reasoning":
		// Extract reasoning text from content parts.
		var text string
		if content, ok := item["content"].([]any); ok {
			for _, partAny := range content {
				part, _ := partAny.(map[string]any)
				if part == nil {
					continue
				}
				pt, _ := part["type"].(string)
				if pt == "reasoning_text" || pt == "input_text" || pt == "text" {
					if t, ok := part["text"].(string); ok {
						text += t
					}
				}
			}
		}
		// Fallback: summary field.
		if text == "" {
			if summary, ok := item["summary"].([]any); ok {
				for _, sAny := range summary {
					s, _ := sAny.(map[string]any)
					if s == nil {
						continue
					}
					if t, ok := s["text"].(string); ok {
						text += t
					}
				}
			}
		}
		return []Message{{
			Role: "assistant",
			Content: []Content{{
				Type:     ContentThinking,
				Thinking: &Thinking{Text: text},
			}},
		}}
	}

	// Unknown item types: try to extract role + content as fallback.
	role, _ := item["role"].(string)
	if role != "" {
		content := decodeResponsesContent(item["content"])
		if len(content) > 0 {
			return []Message{{Role: role, Content: content}}
		}
	}

	return nil
}

// decodeResponsesContent converts Responses API content to IR content.
func decodeResponsesContent(content any) []Content {
	if s, ok := content.(string); ok {
		return []Content{{Type: ContentText, Text: s}}
	}
	arr, ok := content.([]any)
	if !ok {
		return nil
	}
	var out []Content
	for _, partAny := range arr {
		part, _ := partAny.(map[string]any)
		if part == nil {
			continue
		}
		pt, _ := part["type"].(string)
		switch pt {
		case "input_text", "output_text", "text":
			t, _ := part["text"].(string)
			out = append(out, Content{Type: ContentText, Text: t})
		case "input_image":
			if src, ok := part["image_url"].(string); ok {
				out = append(out, Content{Type: ContentImage, Image: &Image{URL: src}})
			}
		}
	}
	return out
}

// decodeResponsesTool converts a Responses API tool definition to IR.
func decodeResponsesTool(tool map[string]any) Tool {
	t := Tool{}
	tType, _ := tool["type"].(string)

	switch tType {
	case "function":
		t.Name, _ = tool["name"].(string)
		t.Description, _ = tool["description"].(string)
		if params, ok := tool["parameters"].(map[string]any); ok {
			t.Schema = params
		}
		return t
	case "local_shell":
		t.Kind = ToolLocalShell
		t.Name = "local_shell"
		return t
	case "shell":
		t.Kind = ToolShell
		t.Name = "shell"
		return t
	case "apply_patch":
		t.Kind = ToolApplyPatch
		t.Name = "apply_patch"
		return t
	case "mcp":
		t.Kind = ToolMCP
		t.Name, _ = tool["server_label"].(string)
		return t
	case "web_search":
		t.Kind = ToolWebSearch
		t.Name = "web_search"
		return t
	case "file_search":
		t.Kind = ToolFileSearch
		t.Name = "file_search"
		return t
	case "computer_use":
		t.Kind = ToolComputer
		t.Name = "computer"
		return t
	case "code_interpreter":
		t.Kind = ToolCodeInterpreter
		t.Name = "code_interpreter"
		return t
	}

	// Default: treat as function.
	t.Name, _ = tool["name"].(string)
	if t.Name == "" {
		t.Name = tType
	}
	return t
}

// EncodeResponses transforms an IR Response into a Responses API response.
func EncodeResponses(resp *Response) map[string]any {
	output := make([]any, 0)
	var reasoningTexts []string

	for _, c := range resp.Content {
		switch c.Type {
		case ContentThinking:
			// Accumulate reasoning; flush before non-thought content.
			if c.Thinking.Text != "" {
				reasoningTexts = append(reasoningTexts, c.Thinking.Text)
			}

		case ContentText:
			// Flush accumulated reasoning.
			if len(reasoningTexts) > 0 {
				output = append(output, map[string]any{
					"id":      "rs_" + compactUUID(),
					"type":    "reasoning",
					"content": []any{},
					"summary": []any{
						map[string]any{
							"type": "summary_text",
							"text": joinStrings(reasoningTexts, "\n"),
						},
					},
				})
				reasoningTexts = nil
			}
			if c.Text != "" {
				output = append(output, map[string]any{
					"id":     "msg_" + compactUUID(),
					"type":   "message",
					"status": "completed",
					"role":   "assistant",
					"content": []any{
						map[string]any{
							"type":        "output_text",
							"text":        c.Text,
							"annotations": []any{},
						},
					},
				})
			}

		case ContentToolCall:
			// Flush accumulated reasoning.
			if len(reasoningTexts) > 0 {
				output = append(output, map[string]any{
					"id":      "rs_" + compactUUID(),
					"type":    "reasoning",
					"content": []any{},
					"summary": []any{
						map[string]any{
							"type": "summary_text",
							"text": joinStrings(reasoningTexts, "\n"),
						},
					},
				})
				reasoningTexts = nil
			}
			tc := c.ToolCall
			args := tc.RawArgs
			if args == "" {
				if b, err := json.Marshal(tc.Args); err == nil {
					args = string(b)
				} else {
					args = "{}"
				}
			}
			callID := tc.ID
			if callID == "" {
				callID = "fc_" + compactUUID()
			}
			// Determine if this is a local_shell call.
			itemType := "function_call"
			if tc.Name == "local_shell" {
				itemType = "local_shell_call"
			}
			item := map[string]any{
				"type":      itemType,
				"id":        callID,
				"call_id":   callID,
				"name":      tc.Name,
				"arguments": args,
				"status":    "completed",
			}
			// For local_shell, embed the action.
			if tc.Name == "local_shell" {
				var action map[string]any
				if json.Unmarshal([]byte(args), &action) == nil {
					item["action"] = action
				}
			}
			output = append(output, item)
		}
	}

	// Flush trailing reasoning.
	if len(reasoningTexts) > 0 {
		output = append(output, map[string]any{
			"id":      "rs_" + compactUUID(),
			"type":    "reasoning",
			"content": []any{},
			"summary": []any{
				map[string]any{
					"type": "summary_text",
					"text": joinStrings(reasoningTexts, "\n"),
				},
			},
		})
	}

	if output == nil {
		output = []any{}
	}

	status := "completed"
	incompleteDetails := map[string]any(nil)
	switch resp.FinishReason {
	case "length":
		status = "incomplete"
		incompleteDetails = map[string]any{"reason": "max_output_tokens"}
	case "content_filter":
		status = "incomplete"
		incompleteDetails = map[string]any{"reason": "content_filter"}
	}

	id := resp.ID
	if id == "" {
		id = "resp_" + compactUUID()
	}
	created := resp.Created
	if created == 0 {
		created = nowUnix()
	}

	return map[string]any{
		"id":                 id,
		"object":             "response",
		"created_at":         created,
		"model":              resp.Model,
		"status":             status,
		"error":              nil,
		"incomplete_details": incompleteDetails,
		"instructions":       nil,
		"output":             output,
		"usage": map[string]any{
			"input_tokens":  resp.Usage.Prompt,
			"output_tokens": resp.Usage.Completion,
			"total_tokens":  resp.Usage.Prompt + resp.Usage.Completion,
			"input_tokens_details": map[string]any{
				"cached_tokens": resp.Usage.Cached,
			},
			"output_tokens_details": map[string]any{
				"reasoning_tokens": resp.Usage.Thought,
			},
		},
		"parallel_tool_calls":  true,
		"temperature":          1.0,
		"top_p":                1.0,
		"max_output_tokens":    nil,
		"previous_response_id": nil,
		"reasoning":            nil,
		"text":                 nil,
		"truncation":           "disabled",
		"metadata":             map[string]any{},
		"store":                false,
		"service_tier":         "default",
	}
}

// EncodeResponsesStreamEvents encodes IR stream events as Responses API SSE.
// This is a simplified version; the full implementation requires a
// state machine to manage response.output_item.added/done lifecycle.
func EncodeResponsesStreamEvents(events []StreamEvent, respID, model string, created int64) []string {
	var out []string
	seq := int64(0)
	nextSeq := func() int64 { seq++; return seq }

	// response.created
	out = append(out, sseResponsesEvent("response.created", map[string]any{
		"type":            "response.created",
		"sequence_number": nextSeq(),
		"response": map[string]any{
			"id":         respID,
			"object":     "response",
			"created_at": created,
			"model":      model,
			"status":     "in_progress",
			"output":     []any{},
		},
	}))

	messageStarted := false
	for _, ev := range events {
		switch ev.Type {
		case StreamTextDelta:
			if !messageStarted {
				messageStarted = true
				out = append(out, sseResponsesEvent("response.output_item.added", map[string]any{
					"type":            "response.output_item.added",
					"sequence_number": nextSeq(),
					"output_index":    0,
					"item": map[string]any{
						"type":    "message",
						"id":      "msg_" + respID,
						"status":  "in_progress",
						"role":    "assistant",
						"content": []any{},
					},
				}))
				out = append(out, sseResponsesEvent("response.content_part.added", map[string]any{
					"type":            "response.content_part.added",
					"sequence_number": nextSeq(),
					"item_id":         "msg_" + respID,
					"output_index":    0,
					"content_index":   0,
					"part": map[string]any{
						"type": "output_text",
						"text": "",
					},
				}))
			}
			out = append(out, sseResponsesEvent("response.output_text.delta", map[string]any{
				"type":            "response.output_text.delta",
				"sequence_number": nextSeq(),
				"item_id":         "msg_" + respID,
				"output_index":    0,
				"content_index":   0,
				"delta":           ev.Delta,
			}))

		case StreamThinkingDelta:
			out = append(out, sseResponsesEvent("response.reasoning_summary.delta", map[string]any{
				"type":            "response.reasoning_summary.delta",
				"sequence_number": nextSeq(),
				"item_id":         "rs_" + respID,
				"output_index":    0,
				"delta":           ev.Delta,
			}))

		case StreamToolCallDone:
			if messageStarted {
				out = append(out, sseResponsesEvent("response.content_part.done", map[string]any{
					"type":            "response.content_part.done",
					"sequence_number": nextSeq(),
					"item_id":         "msg_" + respID,
					"output_index":    0,
					"content_index":   0,
					"part":            map[string]any{"type": "output_text", "text": ""},
				}))
				out = append(out, sseResponsesEvent("response.output_item.done", map[string]any{
					"type":            "response.output_item.done",
					"sequence_number": nextSeq(),
					"output_index":    0,
					"item":            map[string]any{"type": "message", "id": "msg_" + respID, "status": "completed", "role": "assistant"},
				}))
				messageStarted = false
			}
			tc := ev.ToolCall
			args := tc.RawArgs
			if args == "" {
				if b, err := json.Marshal(tc.Args); err == nil {
					args = string(b)
				}
			}
			itemType := "function_call"
			if tc.Name == "local_shell" {
				itemType = "local_shell_call"
			}
			item := map[string]any{
				"type":      itemType,
				"id":        "fc_" + tc.ID,
				"call_id":   tc.ID,
				"name":      tc.Name,
				"arguments": args,
				"status":    "completed",
			}
			out = append(out, sseResponsesEvent("response.output_item.added", map[string]any{
				"type":            "response.output_item.added",
				"sequence_number": nextSeq(),
				"output_index":    1,
				"item":            item,
			}))
			out = append(out, sseResponsesEvent("response.output_item.done", map[string]any{
				"type":            "response.output_item.done",
				"sequence_number": nextSeq(),
				"output_index":    1,
				"item":            item,
			}))

		case StreamDone:
			if messageStarted {
				out = append(out, sseResponsesEvent("response.content_part.done", map[string]any{
					"type":            "response.content_part.done",
					"sequence_number": nextSeq(),
					"item_id":         "msg_" + respID,
					"output_index":    0,
					"content_index":   0,
					"part":            map[string]any{"type": "output_text", "text": ""},
				}))
				out = append(out, sseResponsesEvent("response.output_item.done", map[string]any{
					"type":            "response.output_item.done",
					"sequence_number": nextSeq(),
					"output_index":    0,
					"item":            map[string]any{"type": "message", "id": "msg_" + respID, "status": "completed", "role": "assistant"},
				}))
				messageStarted = false
			}
			out = append(out, sseResponsesEvent("response.completed", map[string]any{
				"type":            "response.completed",
				"sequence_number": nextSeq(),
				"response": map[string]any{
					"id":         respID,
					"object":     "response",
					"created_at": created,
					"model":      model,
					"status":     "completed",
					"output":     []any{},
				},
			}))
		}
	}

	return out
}

// sseResponsesEvent formats a Responses API SSE event.
func sseResponsesEvent(eventType string, data any) string {
	b, _ := json.Marshal(data)
	return fmt.Sprintf("event: %s\ndata: %s\n\n", eventType, string(b))
}
