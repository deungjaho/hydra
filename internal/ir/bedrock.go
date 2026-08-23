// Package ir provides the intermediate representation for protocol translation.
//
// This file implements the AWS Bedrock Converse codec: decoding Bedrock
// Converse requests into IR and encoding IR responses back to Bedrock
// Converse format.
//
// Bedrock Converse API reference:
//   - Request:  POST /model/{modelId}/converse
//   - Response: { output, stopReason, usage, metrics }
//
// Key mappings:
//   - Bedrock "text"         → IR ContentText
//   - Bedrock "image"        → IR ContentImage
//   - Bedrock "toolUse"      → IR ContentToolCall
//   - Bedrock "toolResult"   → IR ContentToolResult
//   - Bedrock inferenceConfig → IR MaxTokens/Temperature/TopP/StopSequences
//   - Bedrock system[]       → IR System (concatenated text blocks)
//   - Bedrock toolConfig     → IR Tools
package ir

import (
	"encoding/json"
	"fmt"
)

// DecodeBedrock transforms a Bedrock Converse API request into IR.
func DecodeBedrock(req map[string]any) *Request {
	r := &Request{
		Extra: make(map[string]any),
	}

	if m, ok := req["modelId"].(string); ok {
		r.Model = m
	}
	if s, ok := req["stream"].(bool); ok {
		r.Stream = s
	}

	// inferenceConfig → IR fields.
	if ic, ok := req["inferenceConfig"].(map[string]any); ok {
		if mt, ok := ic["maxTokens"].(float64); ok {
			r.MaxTokens = int64(mt)
		}
		if temp, ok := ic["temperature"].(float64); ok {
			t := temp
			r.Temperature = &t
		}
		if topP, ok := ic["topP"].(float64); ok {
			p := topP
			r.TopP = &p
		}
		if stops, ok := ic["stopSequences"].([]any); ok {
			for _, s := range stops {
				if str, ok := s.(string); ok {
					r.StopSequences = append(r.StopSequences, str)
				}
			}
		}
	}

	// system — array of SystemContentBlock (text only in IR).
	if sysArr, ok := req["system"].([]any); ok {
		var parts []string
		for _, blockAny := range sysArr {
			block, _ := blockAny.(map[string]any)
			if block == nil {
				continue
			}
			if t, ok := block["text"].(string); ok {
				parts = append(parts, t)
			}
		}
		r.System = joinStrings(parts, "\n")
	}

	// Messages — build toolUseId → name map for toolResult recovery.
	var msgs []any
	if m, ok := req["messages"].([]any); ok {
		msgs = m
	}
	toolNameMap := buildBedrockToolNameMap(msgs)

	for _, msgAny := range msgs {
		msg, _ := msgAny.(map[string]any)
		if msg == nil {
			continue
		}
		r.Messages = append(r.Messages, decodeBedrockMessage(msg, toolNameMap))
	}

	// Tools — Bedrock uses toolConfig.tools[].toolSpec.
	if tc, ok := req["toolConfig"].(map[string]any); ok {
		if tools, ok := tc["tools"].([]any); ok {
			for _, toolAny := range tools {
				tool, _ := toolAny.(map[string]any)
				if tool == nil {
					continue
				}
				t := decodeBedrockTool(tool)
				r.Tools = append(r.Tools, t)
			}
		}
		// toolChoice
		if choice, ok := tc["toolChoice"]; ok {
			r.ToolChoice = mapBedrockToolChoice(choice)
		}
	}

	// Pass-through extras.
	for _, key := range []string{"additionalModelRequestFields", "requestMetadata", "guardrailConfig"} {
		if v, ok := req[key]; ok {
			r.Extra[key] = v
		}
	}

	return r
}

// decodeBedrockMessage converts a Bedrock message to IR.
func decodeBedrockMessage(msg map[string]any, toolNameMap map[string]string) Message {
	m := Message{}
	role, _ := msg["role"].(string)
	m.Role = role

	if arr, ok := msg["content"].([]any); ok {
		for _, blockAny := range arr {
			block, _ := blockAny.(map[string]any)
			if block == nil {
				continue
			}
			c := decodeBedrockBlock(block, toolNameMap)
			m.Content = append(m.Content, c)
		}
	}
	return m
}

// buildBedrockToolNameMap builds a map from toolUseId to tool name.
func buildBedrockToolNameMap(messages []any) map[string]string {
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
			if bt, _ := block["type"].(string); bt == "toolUse" {
				toolUse, _ := block["toolUse"].(map[string]any)
				if toolUse == nil {
					continue
				}
				id, _ := toolUse["toolUseId"].(string)
				name, _ := toolUse["name"].(string)
				if id != "" && name != "" {
					m[id] = name
				}
			}
		}
	}
	return m
}

// decodeBedrockBlock converts a Bedrock ContentBlock to IR.
func decodeBedrockBlock(block map[string]any, toolNameMap map[string]string) Content {
	bt, _ := block["type"].(string)
	switch bt {
	case "text":
		t, _ := block["text"].(string)
		return Content{Type: ContentText, Text: t}

	case "image":
		if src, ok := block["image"].(map[string]any); ok {
			format, _ := src["format"].(string)
			if source, ok := src["source"].(map[string]any); ok {
				if data, ok := source["bytes"].(string); ok {
					return Content{Type: ContentImage, Image: &Image{
						MimeType: bedrockImageMime(format),
						Data:     data,
					}}
				}
			}
		}
		return Content{Type: ContentText}

	case "toolUse":
		toolUse, _ := block["toolUse"].(map[string]any)
		if toolUse == nil {
			return Content{Type: ContentText}
		}
		id, _ := toolUse["toolUseId"].(string)
		name, _ := toolUse["name"].(string)
		var args map[string]any
		if input, ok := toolUse["input"].(map[string]any); ok {
			args = input
		}
		return Content{
			Type:     ContentToolCall,
			ToolCall: &ToolCall{ID: id, Name: name, Args: args},
		}

	case "toolResult":
		toolResult, _ := block["toolResult"].(map[string]any)
		if toolResult == nil {
			return Content{Type: ContentText}
		}
		id, _ := toolResult["toolUseId"].(string)
		content := extractBedrockToolResultText(toolResult["content"])
		name, _ := toolNameMap[id]
		if name == "" {
			name = id
		}
		return Content{
			Type: ContentToolResult,
			ToolResult: &ToolResult{
				ID:      id,
				Name:    name,
				Content: content,
			},
		}
	}
	return Content{Type: ContentText}
}

// extractBedrockToolResultText extracts text from a toolResult content array.
func extractBedrockToolResultText(content any) string {
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

// decodeBedrockTool converts a Bedrock tool definition to IR.
// Bedrock wraps the spec in { toolSpec: { name, description, inputSchema: { json: ... } } }.
func decodeBedrockTool(tool map[string]any) Tool {
	t := Tool{}
	spec, _ := tool["toolSpec"].(map[string]any)
	if spec == nil {
		return t
	}
	t.Name, _ = spec["name"].(string)
	t.Description, _ = spec["description"].(string)
	if schemaWrapper, ok := spec["inputSchema"].(map[string]any); ok {
		if jsonSchema, ok := schemaWrapper["json"].(map[string]any); ok {
			t.Schema = jsonSchema
		}
	}
	return t
}

// mapBedrockToolChoice converts Bedrock toolChoice to IR ToolChoice.
func mapBedrockToolChoice(choice any) any {
	m, ok := choice.(map[string]any)
	if !ok {
		return choice
	}
	ct, _ := m["type"].(string)
	switch ct {
	case "auto":
		return "auto"
	case "any":
		return "required"
	case "tool":
		if spec, ok := m["tool"].(map[string]any); ok {
			if name, ok := spec["name"].(string); ok {
				return map[string]any{
					"type":     "function",
					"function": map[string]any{"name": name},
				}
			}
		}
	}
	return choice
}

// bedrockImageMime converts a Bedrock image format to a MIME type.
func bedrockImageMime(format string) string {
	switch format {
	case "png":
		return "image/png"
	case "jpeg":
		return "image/jpeg"
	case "gif":
		return "image/gif"
	case "webp":
		return "image/webp"
	}
	return "image/" + format
}

// EncodeBedrock transforms an IR Response into a Bedrock Converse
// API response.
func EncodeBedrock(resp *Response) map[string]any {
	var contentBlocks []any
	hasToolUse := false

	for _, c := range resp.Content {
		switch c.Type {
		case ContentText:
			if c.Text != "" {
				contentBlocks = append(contentBlocks, map[string]any{
					"type": "text",
					"text": c.Text,
				})
			}
		case ContentToolCall:
			hasToolUse = true
			tc := c.ToolCall
			id := tc.ID
			if id == "" {
				id = "tooluse_" + compactUUID()[:12]
			}
			input := tc.Args
			if input == nil {
				input = map[string]any{}
			}
			contentBlocks = append(contentBlocks, map[string]any{
				"type": "toolUse",
				"toolUse": map[string]any{
					"toolUseId": id,
					"name":      tc.Name,
					"input":     input,
				},
			})
		}
	}

	if len(contentBlocks) == 0 {
		contentBlocks = []any{map[string]any{"type": "text", "text": ""}}
	}

	stopReason := mapIRToBedrockStop(resp.FinishReason)
	if hasToolUse {
		stopReason = "tool_use"
	}

	id := resp.ID
	if id == "" {
		id = "msg_" + compactUUID()[:12]
	}

	usage := map[string]any{
		"inputTokens":  resp.Usage.Prompt,
		"outputTokens": resp.Usage.Completion,
		"totalTokens":  resp.Usage.Prompt + resp.Usage.Completion,
	}
	if resp.Usage.Cached > 0 {
		usage["cacheReadInputTokens"] = resp.Usage.Cached
	}

	return map[string]any{
		"output": map[string]any{
			"message": map[string]any{
				"role":    "assistant",
				"content": contentBlocks,
			},
		},
		"stopReason": stopReason,
		"usage":      usage,
		"metrics":    map[string]any{"latencyMs": 0},
	}
}

// mapIRToBedrockStop maps IR finish reasons to Bedrock stop reasons.
func mapIRToBedrockStop(fr string) string {
	switch fr {
	case "stop":
		return "end_turn"
	case "length":
		return "max_tokens"
	case "tool_calls":
		return "tool_use"
	case "content_filter":
		return "content_filtered"
	}
	return "end_turn"
}

// EncodeBedrockStreamEvents encodes IR stream events as Bedrock
// ConverseStream SSE events.
//
// Bedrock ConverseStream event types:
//   - messageStart
//   - contentBlockStart  (with contentBlockIndex)
//   - contentBlockDelta  (text, toolUse input)
//   - contentBlockStop
//   - messageStop
//   - metadata
func EncodeBedrockStreamEvents(events []StreamEvent, respID, model string) []string {
	var out []string
	blockIdx := -1
	currentBlockType := ""

	// messageStart
	out = append(out, bedrockSSE("messageStart", map[string]any{
		"role":      "assistant",
		"messageId": respID,
	}))

	for _, ev := range events {
		switch ev.Type {
		case StreamTextDelta:
			if currentBlockType != "text" {
				if currentBlockType != "" {
					out = append(out, bedrockSSE("contentBlockStop", map[string]any{
						"contentBlockIndex": blockIdx,
					}))
				}
				blockIdx++
				currentBlockType = "text"
				out = append(out, bedrockSSE("contentBlockStart", map[string]any{
					"contentBlockIndex": blockIdx,
					"start":             map[string]any{"type": "text", "text": ""},
				}))
			}
			out = append(out, bedrockSSE("contentBlockDelta", map[string]any{
				"contentBlockIndex": blockIdx,
				"delta":             map[string]any{"type": "text", "text": ev.Delta},
			}))

		case StreamToolCallDone:
			if currentBlockType != "" {
				out = append(out, bedrockSSE("contentBlockStop", map[string]any{
					"contentBlockIndex": blockIdx,
				}))
			}
			blockIdx++
			currentBlockType = "toolUse"
			tc := ev.ToolCall
			id := tc.ID
			if id == "" {
				id = "tooluse_" + compactUUID()[:12]
			}
			out = append(out, bedrockSSE("contentBlockStart", map[string]any{
				"contentBlockIndex": blockIdx,
				"start": map[string]any{
					"type":      "toolUse",
					"toolUseId": id,
					"name":      tc.Name,
				},
			}))
			argsJSON := tc.RawArgs
			if argsJSON == "" {
				if b, err := json.Marshal(tc.Args); err == nil {
					argsJSON = string(b)
				}
			}
			if argsJSON != "" {
				out = append(out, bedrockSSE("contentBlockDelta", map[string]any{
					"contentBlockIndex": blockIdx,
					"delta":             map[string]any{"type": "toolUseInput", "input": argsJSON},
				}))
			}
			out = append(out, bedrockSSE("contentBlockStop", map[string]any{
				"contentBlockIndex": blockIdx,
			}))
			currentBlockType = ""

		case StreamUsage:
			// Collected in metadata event at the end.

		case StreamDone:
			if currentBlockType != "" {
				out = append(out, bedrockSSE("contentBlockStop", map[string]any{
					"contentBlockIndex": blockIdx,
				}))
			}
			stopReason := mapIRToBedrockStop(ev.FinishReason)
			out = append(out, bedrockSSE("messageStop", map[string]any{
				"stopReason": stopReason,
			}))
		}
	}

	return out
}

// bedrockSSE formats a Bedrock ConverseStream SSE event.
func bedrockSSE(eventType string, data any) string {
	b, _ := json.Marshal(data)
	return fmt.Sprintf("event: %s\ndata: %s\n\n", eventType, string(b))
}
