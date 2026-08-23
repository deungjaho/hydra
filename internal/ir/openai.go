// Package ir provides the intermediate representation for protocol translation.
//
// This file implements the OpenAI Chat Completions codec: decoding OpenAI
// chat requests into IR and encoding IR responses back to OpenAI format.
package ir

import (
	"encoding/json"
	"fmt"
	"strings"
)

// DecodeOpenAIChat transforms an OpenAI Chat Completions request into IR.
func DecodeOpenAIChat(req map[string]any) *Request {
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
	if stops, ok := req["stop"].([]any); ok {
		for _, s := range stops {
			if str, ok := s.(string); ok {
				r.StopSequences = append(r.StopSequences, str)
			}
		}
	}
	if str, ok := req["stop"].(string); ok {
		r.StopSequences = append(r.StopSequences, str)
	}

	// Reasoning effort.
	if re, ok := req["reasoning_effort"].(string); ok {
		r.Reasoning = &Reasoning{Effort: re}
	}

	// Response format.
	if rf, ok := req["response_format"].(map[string]any); ok {
		rfType, _ := rf["type"].(string)
		schema, _ := rf["json_schema"].(map[string]any)
		var schemaMap map[string]any
		if schema != nil {
			schemaMap, _ = schema["schema"].(map[string]any)
		}
		r.ResponseFormat = &ResponseFormat{Type: rfType, Schema: schemaMap}
	}

	// Build tool_call_id → function_name map so that tool response
	// messages (which often omit the function name in OpenAI format)
	// can recover the name for Gemini's functionResponse.name field.
	toolNameMap := buildOpenAIToolNameMap(req["messages"])

	// Messages.
	if msgs, ok := req["messages"].([]any); ok {
		for _, msgAny := range msgs {
			msg, _ := msgAny.(map[string]any)
			if msg == nil {
				continue
			}
			m := decodeOpenAIMessage(msg, toolNameMap)
			if m.Role == "system" || m.Role == "developer" {
				if r.System == "" {
					r.System = m.Content[0].Text
				} else {
					r.System += "\n" + m.Content[0].Text
				}
				continue
			}
			r.Messages = append(r.Messages, m)
		}
	}

	// Tools.
	if tools, ok := req["tools"].([]any); ok {
		for _, toolAny := range tools {
			tool, _ := toolAny.(map[string]any)
			if tool == nil {
				continue
			}
			t := decodeOpenAITool(tool)
			r.Tools = append(r.Tools, t)
		}
	}

	// Tool choice.
	if tc, ok := req["tool_choice"]; ok {
		r.ToolChoice = tc
	}

	// Pass-through extras.
	for _, key := range []string{"parallel_tool_calls", "user", "metadata", "seed", "n"} {
		if v, ok := req[key]; ok {
			r.Extra[key] = v
		}
	}

	return r
}

// buildOpenAIToolNameMap builds a map from tool_call_id to function name
// by scanning assistant messages for tool_calls.
func buildOpenAIToolNameMap(messagesAny any) map[string]string {
	m := make(map[string]string)
	msgs, ok := messagesAny.([]any)
	if !ok {
		return m
	}
	for _, msgAny := range msgs {
		msg, _ := msgAny.(map[string]any)
		if msg == nil {
			continue
		}
		toolCalls, ok := msg["tool_calls"].([]any)
		if !ok {
			continue
		}
		for _, tcAny := range toolCalls {
			tc, _ := tcAny.(map[string]any)
			if tc == nil {
				continue
			}
			id, _ := tc["id"].(string)
			fn, _ := tc["function"].(map[string]any)
			name := ""
			if fn != nil {
				name, _ = fn["name"].(string)
			}
			if id != "" && name != "" {
				m[id] = name
			}
		}
	}
	return m
}

// decodeOpenAIMessage converts an OpenAI message to IR.
func decodeOpenAIMessage(msg map[string]any, toolNameMap map[string]string) Message {
	m := Message{}
	role, _ := msg["role"].(string)
	m.Role = role

	// Tool response (role: "tool") — handle before generic content.
	if role == "tool" {
		content, _ := msg["content"].(string)
		toolCallID, _ := msg["tool_call_id"].(string)
		name, _ := msg["name"].(string)
		// OpenAI tool messages often omit the function name; recover it
		// from the corresponding assistant tool_call via the ID map.
		if name == "" && toolCallID != "" {
			if mapped, ok := toolNameMap[toolCallID]; ok {
				name = mapped
			}
		}
		m.Content = []Content{{
			Type: ContentToolResult,
			ToolResult: &ToolResult{
				ID:      toolCallID,
				Name:    name,
				Content: content,
			},
		}}
		return m
	}

	// String content.
	if s, ok := msg["content"].(string); ok {
		m.Content = []Content{{Type: ContentText, Text: s}}
		return m
	}

	// Array content.
	if arr, ok := msg["content"].([]any); ok {
		for _, partAny := range arr {
			part, _ := partAny.(map[string]any)
			if part == nil {
				continue
			}
			c := decodeOpenAIContentPart(part)
			m.Content = append(m.Content, c)
		}
	}

	// Reasoning content.
	if rc, ok := msg["reasoning_content"].(string); ok && rc != "" {
		m.Content = append(m.Content, Content{
			Type:     ContentThinking,
			Thinking: &Thinking{Text: rc},
		})
	}

	// Tool calls.
	if tcs, ok := msg["tool_calls"].([]any); ok {
		for _, tcAny := range tcs {
			tc, _ := tcAny.(map[string]any)
			if tc == nil {
				continue
			}
			call := decodeOpenAIToolCall(tc)
			m.Content = append(m.Content, Content{Type: ContentToolCall, ToolCall: call})
		}
	}

	return m
}

// decodeOpenAIContentPart converts an OpenAI content part to IR.
func decodeOpenAIContentPart(part map[string]any) Content {
	pt, _ := part["type"].(string)
	switch pt {
	case "text":
		t, _ := part["text"].(string)
		return Content{Type: ContentText, Text: t}

	case "image_url":
		if imgURL, ok := part["image_url"].(map[string]any); ok {
			url, _ := imgURL["url"].(string)
			if strings.HasPrefix(url, "data:") {
				mime, data := parseDataURL(url)
				return Content{Type: ContentImage, Image: &Image{MimeType: mime, Data: data}}
			}
			return Content{Type: ContentImage, Image: &Image{URL: url}}
		}

	case "input_audio":
		if audio, ok := part["input_audio"].(map[string]any); ok {
			data, _ := audio["data"].(string)
			format, _ := audio["format"].(string)
			mime := "audio/" + format
			if format == "mp3" {
				mime = "audio/mpeg"
			}
			return Content{Type: ContentAudio, Audio: &Audio{MimeType: mime, Data: data}}
		}
	}
	return Content{Type: ContentText}
}

// decodeOpenAIToolCall converts an OpenAI tool_call to IR.
func decodeOpenAIToolCall(tc map[string]any) *ToolCall {
	id, _ := tc["id"].(string)
	fn, _ := tc["function"].(map[string]any)
	if fn == nil {
		return &ToolCall{ID: id}
	}
	name, _ := fn["name"].(string)
	argsStr, _ := fn["arguments"].(string)
	var args map[string]any
	if argsStr != "" {
		_ = json.Unmarshal([]byte(argsStr), &args)
	}
	return &ToolCall{
		ID:      id,
		Name:    name,
		Args:    args,
		RawArgs: argsStr,
	}
}

// decodeOpenAITool converts an OpenAI tool definition to IR.
func decodeOpenAITool(tool map[string]any) Tool {
	t := Tool{}
	tType, _ := tool["type"].(string)
	if tType != "function" && tType != "" {
		// Non-function tool types (web_search, file_search, etc.)
		t.Kind = openAIToolKind(tType)
		t.Name = tType
		return t
	}
	fn, _ := tool["function"].(map[string]any)
	if fn != nil {
		t.Name, _ = fn["name"].(string)
		t.Description, _ = fn["description"].(string)
		if params, ok := fn["parameters"].(map[string]any); ok {
			t.Schema = params
		}
	}
	return t
}

// openAIToolKind maps OpenAI tool type strings to IR ToolKind.
func openAIToolKind(t string) ToolKind {
	switch t {
	case "local_shell":
		return ToolLocalShell
	case "shell":
		return ToolShell
	case "apply_patch":
		return ToolApplyPatch
	case "mcp":
		return ToolMCP
	case "computer":
		return ToolComputer
	case "web_search":
		return ToolWebSearch
	case "file_search":
		return ToolFileSearch
	case "code_interpreter":
		return ToolCodeInterpreter
	}
	return ToolFunction
}

// parseDataURL extracts mime type and base64 data from a data: URL.
func parseDataURL(url string) (mime, data string) {
	if !strings.HasPrefix(url, "data:") {
		return "", url
	}
	rest := url[5:]
	semicolon := strings.Index(rest, ";")
	comma := strings.Index(rest, ",")
	if comma < 0 {
		return "", rest
	}
	if semicolon >= 0 && semicolon < comma {
		mime = rest[:semicolon]
	} else {
		mime = rest[:comma]
	}
	data = rest[comma+1:]
	return mime, data
}

// EncodeOpenAIChat transforms an IR Response into an OpenAI Chat
// Completions response.
func EncodeOpenAIChat(resp *Response) map[string]any {
	var content string
	var reasoning string
	var toolCalls []any

	for _, c := range resp.Content {
		switch c.Type {
		case ContentText:
			content += c.Text
		case ContentThinking:
			reasoning += c.Thinking.Text
		case ContentToolCall:
			tc := c.ToolCall
			args := tc.RawArgs
			if args == "" {
				if b, err := json.Marshal(tc.Args); err == nil {
					args = string(b)
				} else {
					args = "{}"
				}
			}
			toolCalls = append(toolCalls, map[string]any{
				"id":   tc.ID,
				"type": "function",
				"function": map[string]any{
					"name":      tc.Name,
					"arguments": args,
				},
			})
		}
	}

	finishReason := resp.FinishReason
	if finishReason == "" {
		finishReason = "stop"
	}
	if len(toolCalls) > 0 && finishReason == "stop" {
		finishReason = "tool_calls"
	}

	message := map[string]any{
		"role": "assistant",
	}
	if content == "" {
		message["content"] = nil
	} else {
		message["content"] = content
	}
	if reasoning != "" {
		message["reasoning_content"] = reasoning
	}
	if len(toolCalls) > 0 {
		message["tool_calls"] = toolCalls
	}

	id := resp.ID
	if id == "" {
		id = "chatcmpl-" + compactUUID()
	}
	created := resp.Created
	if created == 0 {
		created = nowUnix()
	}

	return map[string]any{
		"id":      id,
		"object":  "chat.completion",
		"created": created,
		"model":   resp.Model,
		"choices": []any{
			map[string]any{
				"index":         0,
				"message":       message,
				"finish_reason": finishReason,
			},
		},
		"usage": map[string]any{
			"prompt_tokens":     resp.Usage.Prompt,
			"completion_tokens": resp.Usage.Completion,
			"total_tokens":      resp.Usage.Prompt + resp.Usage.Completion,
			"cached_tokens":     resp.Usage.Cached,
			"thought_tokens":    resp.Usage.Thought,
		},
	}
}

// EncodeOpenAIChatStreamChunk encodes one IR stream event as an OpenAI
// SSE chunk. Returns empty string for events that don't map to OpenAI
// streaming (e.g. usage-only events).
func EncodeOpenAIChatStreamChunk(ev StreamEvent, chatID string, created int64, model string, isFirst bool) string {
	delta := map[string]any{}
	if isFirst {
		delta["role"] = "assistant"
	}

	switch ev.Type {
	case StreamTextDelta:
		delta["content"] = ev.Delta
	case StreamThinkingDelta:
		delta["reasoning_content"] = ev.Delta
	case StreamToolCallDelta, StreamToolCallDone:
		if ev.ToolCall != nil {
			args := ev.ToolCall.RawArgs
			if args == "" {
				if b, err := json.Marshal(ev.ToolCall.Args); err == nil {
					args = string(b)
				} else {
					args = "{}"
				}
			}
			delta["tool_calls"] = []any{
				map[string]any{
					"index": 0,
					"id":    ev.ToolCall.ID,
					"type":  "function",
					"function": map[string]any{
						"name":      ev.ToolCall.Name,
						"arguments": args,
					},
				},
			}
		}
	case StreamDone:
		chunk := map[string]any{
			"id":      chatID,
			"object":  "chat.completion.chunk",
			"created": created,
			"model":   model,
			"choices": []any{
				map[string]any{
					"index":         0,
					"delta":         map[string]any{},
					"finish_reason": ev.FinishReason,
				},
			},
		}
		return sseData(chunk)
	case StreamUsage:
		return "" // OpenAI doesn't emit usage in stream chunks
	}

	if len(delta) == 0 {
		return ""
	}

	chunk := map[string]any{
		"id":      chatID,
		"object":  "chat.completion.chunk",
		"created": created,
		"model":   model,
		"choices": []any{
			map[string]any{
				"index":         0,
				"delta":         delta,
				"finish_reason": nil,
			},
		},
	}
	return sseData(chunk)
}

// sseData formats a JSON object as an SSE data line.
func sseData(v any) string {
	b, _ := json.Marshal(v)
	return fmt.Sprintf("data: %s\n\n", string(b))
}
