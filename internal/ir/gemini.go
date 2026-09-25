// Package ir provides the intermediate representation for protocol translation.
//
// This file implements the Gemini v1internal codec: decoding Gemini/AGY
// responses into IR and encoding IR requests into the AGY v1internal
// envelope.
package ir

import (
	"encoding/json"
	"fmt"
	"hash/fnv"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
)

func toAgySessionID(s string) string {
	if s == "" {
		s = uuid.NewString()
	}
	if _, err := strconv.ParseInt(s, 10, 64); err == nil {
		return s
	}
	h := fnv.New64a()
	h.Write([]byte(s))
	val := int64(h.Sum64())
	if val > 0 {
		val = -val
	}
	return strconv.FormatInt(val, 10)
}

// EncodeGeminiRequest transforms an IR Request into the AGY v1internal
// request envelope. The envelope wraps a generateContent body with
// project, request metadata, and client identity headers.
func EncodeGeminiRequest(req *Request, projectID, sessionID string, requestN uint64) map[string]any {
	body := encodeGeminiBody(req)
	if sessionID == "" {
		sessionID = uuid.NewString()
	}
	body["sessionId"] = toAgySessionID(sessionID)
	body["labels"] = map[string]any{
		"last_step_index":          fmt.Sprintf("%d", requestN),
		"model_enum":               "MODEL_PLACEHOLDER_M318",
		"request_id":               fmt.Sprintf("%s-%d", sessionID, requestN),
		"trajectory_id":            sessionID,
		"used_claude":              "false",
		"used_claude_conservative": "false",
		"used_non_gemini_model":    "false",
	}
	if projectID == "" || projectID == "test-project" || projectID == "project-id" {
		projectID = "aicode-consumers"
	}
	nowMs := time.Now().UnixMilli()
	return map[string]any{
		"project":     projectID,
		"request":     body,
		"model":       req.Model,
		"userAgent":   "antigravity",
		"requestType": "agent",
		"requestId":   fmt.Sprintf("agent/%s/%d/%s/%d", uuid.NewString(), nowMs, sessionID, requestN),
	}
}

// encodeGeminiBody builds the generateContent request body from IR.
func encodeGeminiBody(req *Request) map[string]any {
	body := map[string]any{}

	// System instruction.
	if req.System != "" {
		body["systemInstruction"] = map[string]any{
			"role": "user",
			"parts": []any{
				map[string]any{"text": SanitizeSystemPrompt(req.System)},
			},
		}
	}

	// Contents (messages).
	contents := make([]any, 0, len(req.Messages))
	for _, msg := range req.Messages {
		c := encodeGeminiMessage(msg)
		if c != nil {
			contents = append(contents, c)
		}
	}
	contents = mergeConsecutiveRoles(contents)
	contents = ensureValidFirstTurn(contents)
	if len(contents) > 0 {
		body["contents"] = contents
	}

	// Generation config matching native agy-cli.
	genConfig := map[string]any{}
	if req.Temperature != nil {
		genConfig["temperature"] = *req.Temperature
	}
	if req.TopP != nil {
		genConfig["topP"] = *req.TopP
	}
	if req.TopK != nil {
		genConfig["topK"] = *req.TopK
	}
	if req.MaxTokens > 0 {
		genConfig["maxOutputTokens"] = req.MaxTokens
	} else {
		genConfig["maxOutputTokens"] = 65536
	}
	if len(req.StopSequences) > 0 {
		genConfig["stopSequences"] = req.StopSequences
	}
	if req.Reasoning != nil {
		thinkConfig := map[string]any{"includeThoughts": true}
		if req.Reasoning.BudgetTokens != 0 {
			thinkConfig["thinkingBudget"] = req.Reasoning.BudgetTokens
		} else {
			thinkConfig["thinkingBudget"] = -1
		}
		genConfig["thinkingConfig"] = thinkConfig
	} else {
		genConfig["thinkingConfig"] = map[string]any{
			"includeThoughts": true,
			"thinkingBudget":  -1,
		}
	}
	if req.ResponseFormat != nil {
		switch req.ResponseFormat.Type {
		case "json_object":
			genConfig["responseMimeType"] = "application/json"
			if req.ResponseFormat.Schema != nil {
				genConfig["responseSchema"] = req.ResponseFormat.Schema
			}
		case "json_schema":
			genConfig["responseMimeType"] = "application/json"
			if req.ResponseFormat.Schema != nil {
				genConfig["responseSchema"] = req.ResponseFormat.Schema
			}
		}
	}
	if len(genConfig) > 0 {
		body["generationConfig"] = genConfig
	}

	// Tools.
	if len(req.Tools) > 0 {
		var funcDecls []any
		var toolsList []any
		for _, tool := range req.Tools {
			if tool.Kind == ToolWebSearch {
				if len(req.Tools) == 1 {
					toolsList = append(toolsList, map[string]any{"google_search": map[string]any{}})
					continue
				}
				// When mixed with other function declarations, Gemini rejects built-in
				// tools. Encode as a client-side functionDeclaration instead.
				if tool.Name == "" {
					tool.Name = "web_search"
				}
				if tool.Description == "" {
					tool.Description = "Search the web for up-to-date information"
				}
				if tool.Schema == nil {
					tool.Schema = map[string]any{
						"type": "object",
						"properties": map[string]any{
							"query": map[string]any{
								"type":        "string",
								"description": "The search query to execute",
							},
						},
						"required": []string{"query"},
					}
				}
				funcDecls = append(funcDecls, encodeGeminiTool(tool))
				continue
			}
			funcDecls = append(funcDecls, encodeGeminiTool(tool))
		}
		if len(funcDecls) > 0 {
			toolsList = append(toolsList, map[string]any{"functionDeclarations": funcDecls})
		}
		if len(toolsList) > 0 {
			body["tools"] = toolsList
		}
		if len(funcDecls) > 0 {
			body["toolConfig"] = map[string]any{
				"functionCallingConfig": map[string]any{"mode": "AUTO"},
			}
		}
	}

	return body
}

// encodeGeminiMessage converts an IR Message to a Gemini content entry.
func encodeGeminiMessage(msg Message) map[string]any {
	role := msg.Role
	switch role {
	case "assistant":
		role = "model"
	case "tool":
		role = "user"
	case "system", "developer":
		return nil // system is handled separately
	}

	parts := make([]any, 0, len(msg.Content))
	for _, c := range msg.Content {
		p := encodeGeminiPart(c)
		if p != nil {
			parts = append(parts, p)
		}
	}
	if len(parts) == 0 {
		return nil
	}
	return map[string]any{
		"role":  role,
		"parts": parts,
	}
}

// encodeGeminiPart converts an IR Content to a Gemini part.
func encodeGeminiPart(c Content) map[string]any {
	switch c.Type {
	case ContentText:
		return map[string]any{"text": SanitizeSystemPrompt(c.Text)}

	case ContentThinking:
		// strip thought parts from model turns: Code Assist requires thought_signature when
		// thought parts are echoed back, but rig drops them from history, causing 400 errors.
		// removing them entirely is accepted fine.
		return nil

	case ContentImage:
		if c.Image.Data != "" {
			return map[string]any{
				"inlineData": map[string]any{
					"mimeType": c.Image.MimeType,
					"data":     c.Image.Data,
				},
			}
		}
		return nil

	case ContentAudio:
		if c.Audio.Data != "" {
			return map[string]any{
				"inlineData": map[string]any{
					"mimeType": c.Audio.MimeType,
					"data":     c.Audio.Data,
				},
			}
		}
		return nil

	case ContentToolCall:
		args := c.ToolCall.Args
		if args == nil {
			args = map[string]any{}
		}
		part := map[string]any{
			"functionCall": map[string]any{
				"name": c.ToolCall.Name,
				"args": args,
				"id":   c.ToolCall.ID,
			},
		}
		sig := c.ToolCall.Signature
		if sig == "" {
			sig = "skip_thought_signature_validator"
		}
		part["thoughtSignature"] = sig
		return part

	case ContentToolResult:
		result := c.ToolResult.Content
		if result == "" {
			result = "{}"
		}
		// Keep tool output as text. Some upstream function-response
		// validators interpret JSON keys such as "$ref" as metadata.
		respObj := map[string]any{"output": result}
		if c.ToolResult.IsError {
			respObj = map[string]any{"error": result}
		}
		resp := map[string]any{
			"name":     c.ToolResult.Name,
			"response": respObj,
			"id":       c.ToolResult.ID,
		}
		part := map[string]any{"functionResponse": resp}
		sig := c.ToolResult.Signature
		if sig == "" {
			sig = "skip_thought_signature_validator"
		}
		part["thoughtSignature"] = sig
		return part
	}
	return nil
}

// encodeGeminiTool converts an IR Tool to a Gemini functionDeclaration.
// Special tool kinds (local_shell, shell, apply_patch) are converted to
// function declarations with appropriate names and schemas, since Gemini
// only supports functionDeclarations.
func encodeGeminiTool(tool Tool) map[string]any {
	// For special tool kinds, generate a function declaration with
	// a schema that matches the tool's semantics.
	switch tool.Kind {
	case ToolLocalShell:
		return map[string]any{
			"name":        "local_shell",
			"description": tool.Description,
			"parameters": map[string]any{
				"type": "OBJECT",
				"properties": map[string]any{
					"action": map[string]any{
						"type": "OBJECT",
						"properties": map[string]any{
							"type":    map[string]any{"type": "STRING"},
							"command": map[string]any{"type": "ARRAY", "items": map[string]any{"type": "STRING"}},
							"workdir": map[string]any{"type": "STRING"},
							"timeout": map[string]any{"type": "INTEGER"},
						},
						"required": []string{"type", "command"},
					},
				},
				"required": []string{"action"},
			},
		}
	case ToolShell:
		return map[string]any{
			"name":        "shell",
			"description": tool.Description,
			"parameters": map[string]any{
				"type": "OBJECT",
				"properties": map[string]any{
					"command": map[string]any{"type": "ARRAY", "items": map[string]any{"type": "STRING"}},
					"workdir": map[string]any{"type": "STRING"},
					"timeout": map[string]any{"type": "INTEGER"},
					"env":     map[string]any{"type": "OBJECT", "additionalProperties": map[string]any{"type": "STRING"}},
				},
				"required": []string{"command"},
			},
		}
	case ToolApplyPatch:
		return map[string]any{
			"name":        "apply_patch",
			"description": tool.Description,
			"parameters": map[string]any{
				"type": "OBJECT",
				"properties": map[string]any{
					"patch": map[string]any{"type": "STRING"},
				},
				"required": []string{"patch"},
			},
		}
	}

	decl := map[string]any{
		"name":        tool.Name,
		"description": tool.Description,
	}
	if tool.Schema != nil {
		decl["parameters"] = NormalizeSchemaForGemini(tool.Schema)
	} else {
		decl["parameters"] = map[string]any{"type": "OBJECT", "properties": map[string]any{}}
	}
	return decl
}

// NormalizeSchemaForGemini normalizes a JSON Schema for the Gemini API.
// Gemini requires uppercase type names and does not support many
// standard JSON Schema fields.
func NormalizeSchemaForGemini(schema map[string]any) map[string]any {
	if schema == nil {
		return nil
	}
	out := make(map[string]any, len(schema))

	// Type normalization.
	if t, ok := schema["type"]; ok {
		switch v := t.(type) {
		case string:
			out["type"] = strings.ToUpper(v)
		case []any:
			// type: ["string", "null"] → primary type + nullable
			primary := ""
			nullable := false
			for _, item := range v {
				if s, ok := item.(string); ok {
					if s == "null" {
						nullable = true
					} else if primary == "" {
						primary = s
					}
				}
			}
			if primary != "" {
				out["type"] = strings.ToUpper(primary)
			}
			if nullable {
				out["nullable"] = true
			}
		}
	}

	// Copy only supported fields (strict allowlist).
	// Gemini API rejects unknown fields such as $defs, definitions, $schema, etc.
	if val, ok := schema["description"]; ok {
		out["description"] = val
	}
	if val, ok := schema["nullable"]; ok {
		out["nullable"] = val
	}
	if val, ok := schema["required"]; ok {
		out["required"] = val
	}
	if val, ok := schema["items"]; ok {
		if sub, ok := val.(map[string]any); ok {
			out["items"] = NormalizeSchemaForGemini(sub)
		}
	}
	if val, ok := schema["properties"]; ok {
		if props, ok := val.(map[string]any); ok {
			normalized := make(map[string]any, len(props))
			for k, v := range props {
				if sub, ok := v.(map[string]any); ok {
					normalized[k] = NormalizeSchemaForGemini(sub)
				}
			}
			out["properties"] = normalized
		}
	}

	// Gemini requires every ARRAY type to declare an items schema.
	// If the source omitted items or used a non-object form (e.g.
	// items: true), normalize leaves it absent — supply a permissive
	// default so the request is not rejected.
	if out["type"] == "ARRAY" {
		if _, ok := out["items"].(map[string]any); !ok {
			out["items"] = map[string]any{"type": "STRING"}
		}
	}
	return out
}

// DecodeGeminiResponse transforms a Gemini/AGY response into IR.
func DecodeGeminiResponse(geminiResp map[string]any, model string) *Response {
	inner := extractInnerResponse(geminiResp)
	if inner == nil {
		return &Response{Model: model}
	}

	resp := &Response{Model: model}

	// Extract content from candidates.
	candidates, _ := inner["candidates"].([]any)
	if len(candidates) > 0 {
		candidate, _ := candidates[0].(map[string]any)
		if candidate != nil {
			contentMap, _ := candidate["content"].(map[string]any)
			if contentMap != nil {
				parts, _ := contentMap["parts"].([]any)
				for _, partAny := range parts {
					part, _ := partAny.(map[string]any)
					if part == nil {
						continue
					}
					c := decodeGeminiPart(part)
					if c.Type != ContentText || c.Text != "" {
						resp.Content = append(resp.Content, c)
					}
				}
			}
			if fr, ok := candidate["finishReason"].(string); ok {
				resp.FinishReason = mapGeminiFinishReason(fr)
			}

			// Extract grounding metadata from google_search tool.
			if gm, ok := candidate["groundingMetadata"].(map[string]any); ok {
				if wsr := decodeGroundingMetadata(gm); wsr != nil && len(wsr.Sources) > 0 {
					resp.Content = append(resp.Content, Content{
						Type:      ContentWebSearch,
						WebSearch: wsr,
					})
				}
			}
		}
	}

	// Extract usage.
	resp.Usage = decodeGeminiUsage(inner)

	return resp
}

// decodeGeminiPart converts a Gemini part to IR Content.
func decodeGeminiPart(part map[string]any) Content {
	// Thought text.
	if t, ok := part["text"].(string); ok {
		if thought, _ := part["thought"].(bool); thought {
			sig, _ := part["thoughtSignature"].(string)
			return Content{
				Type:     ContentThinking,
				Thinking: &Thinking{Text: t, Signature: sig},
			}
		}
		return Content{Type: ContentText, Text: t}
	}

	// Function call.
	if fc, ok := part["functionCall"].(map[string]any); ok {
		name, _ := fc["name"].(string)
		id, _ := fc["id"].(string)
		args, _ := fc["args"].(map[string]any)
		sig, _ := part["thoughtSignature"].(string)
		return Content{
			Type: ContentToolCall,
			ToolCall: &ToolCall{
				ID:        id,
				Name:      name,
				Args:      args,
				Signature: sig,
			},
		}
	}

	// Function response.
	if fr, ok := part["functionResponse"].(map[string]any); ok {
		name, _ := fr["name"].(string)
		id, _ := fr["id"].(string)
		legacyErr, _ := fr["error"].(bool)
		sig, _ := part["thoughtSignature"].(string)
		var contentStr string
		// The official Gemini FunctionResponse convention uses
		// response.error (key presence with a non-null value) to
		// signal an error. The legacy top-level functionResponse.error
		// bool is kept for backward compatibility with older
		// Hydra-encoded data; it is only consulted when the official
		// nested response.error marker is absent.
		// See: https://ai.google.dev/gemini-api/docs/function-calling
		isError := false
		nestedPresent := false
		if resp, ok := fr["response"]; ok {
			if b, err := json.Marshal(resp); err == nil {
				contentStr = string(b)
			}
			if respObj, ok := resp.(map[string]any); ok {
				if v, ok := respObj["error"]; ok && v != nil {
					nestedPresent = true
					isError = true
				}
			}
		}
		if !nestedPresent {
			isError = legacyErr
		}
		return Content{
			Type: ContentToolResult,
			ToolResult: &ToolResult{
				ID:        id,
				Name:      name,
				Content:   contentStr,
				IsError:   isError,
				Signature: sig,
			},
		}
	}

	// Inline data (image/audio).
	if inline, ok := part["inlineData"].(map[string]any); ok {
		mime, _ := inline["mimeType"].(string)
		data, _ := inline["data"].(string)
		if strings.HasPrefix(mime, "image/") {
			return Content{Type: ContentImage, Image: &Image{MimeType: mime, Data: data}}
		}
		if strings.HasPrefix(mime, "audio/") {
			return Content{Type: ContentAudio, Audio: &Audio{MimeType: mime, Data: data}}
		}
	}

	return Content{Type: ContentText}
}

// decodeGeminiUsage extracts usage metadata from a Gemini response.
func decodeGeminiUsage(inner map[string]any) Usage {
	usage, _ := inner["usageMetadata"].(map[string]any)
	if usage == nil {
		return Usage{}
	}
	return Usage{
		Prompt:     int64FromAny(usage["promptTokenCount"]),
		Completion: int64FromAny(usage["candidatesTokenCount"]),
		Cached:     int64FromAny(usage["cachedContentTokenCount"]),
		Thought:    int64FromAny(usage["thoughtsTokenCount"]),
	}
}

// decodeGroundingMetadata converts Gemini google_search grounding
// metadata into an IR WebSearchResult.
func decodeGroundingMetadata(gm map[string]any) *WebSearchResult {
	wsr := &WebSearchResult{}

	// Extract search queries.
	if queries, ok := gm["webSearchQueries"].([]any); ok {
		for _, q := range queries {
			if s, ok := q.(string); ok && s != "" {
				wsr.Query = s
				break
			}
		}
	}

	// Extract grounding chunks (web sources).
	chunks, _ := gm["groundingChunks"].([]any)
	seen := map[string]bool{}
	for _, chunkAny := range chunks {
		chunk, _ := chunkAny.(map[string]any)
		if chunk == nil {
			continue
		}
		web, _ := chunk["web"].(map[string]any)
		if web == nil {
			continue
		}
		uri, _ := web["uri"].(string)
		if uri == "" || seen[uri] {
			continue
		}
		seen[uri] = true
		title, _ := web["title"].(string)
		wsr.Sources = append(wsr.Sources, WebSearchSource{
			URI:   uri,
			Title: title,
		})
	}

	// Extract grounding supports for snippet text.
	supports, _ := gm["groundingSupports"].([]any)
	snippetByURI := map[string]string{}
	for _, supAny := range supports {
		sup, _ := supAny.(map[string]any)
		if sup == nil {
			continue
		}
		segment, _ := sup["segment"].(map[string]any)
		if segment == nil {
			continue
		}
		segText, _ := segment["text"].(string)
		indices, _ := sup["groundingChunkIndices"].([]any)
		for _, idxAny := range indices {
			idx, ok := idxAny.(float64)
			if !ok || int(idx) >= len(chunks) {
				continue
			}
			chunk, _ := chunks[int(idx)].(map[string]any)
			if chunk == nil {
				continue
			}
			web, _ := chunk["web"].(map[string]any)
			if web == nil {
				continue
			}
			uri, _ := web["uri"].(string)
			if uri != "" && segText != "" {
				if _, exists := snippetByURI[uri]; !exists {
					snippetByURI[uri] = segText
				}
			}
		}
	}
	for i := range wsr.Sources {
		if snip, ok := snippetByURI[wsr.Sources[i].URI]; ok {
			wsr.Sources[i].Snippet = snip
		}
	}

	return wsr
}

// extractInnerResponse unwraps the AGY envelope to get the inner response.
func extractInnerResponse(resp map[string]any) map[string]any {
	// AGY wraps the response in a "response" field.
	if inner, ok := resp["response"].(map[string]any); ok {
		return inner
	}
	// Direct Gemini response (no envelope).
	return resp
}

// mapGeminiFinishReason maps Gemini finish reasons to IR finish reasons.
func mapGeminiFinishReason(fr string) string {
	switch fr {
	case "STOP":
		return "stop"
	case "MAX_TOKENS":
		return "length"
	case "SAFETY", "RECITATION":
		return "content_filter"
	default:
		return "stop"
	}
}

// int64FromAny safely extracts an int64 from an any value.
func int64FromAny(v any) int64 {
	switch n := v.(type) {
	case float64:
		return int64(n)
	case int64:
		return n
	case int:
		return int64(n)
	case json.Number:
		i, _ := n.Int64()
		return i
	}
	return 0
}

// GeminiStreamState carries decoding state across the SSE chunks of one
// streamed response. Create one instance per response stream.
type GeminiStreamState struct {
	// toolCallIdx is the next positional index to assign to a tool call.
	// Gemini may deliver parallel function calls in separate chunks, so
	// the index must accumulate across the whole stream rather than
	// restart per chunk.
	toolCallIdx int
	hasThinking bool
	hasText     bool
	usedTool    bool
	lastThought string
}

// NeedsStitch returns true if the stream ended with thoughts but produced
// neither text nor tool calls. In this state, an Agent client would stall
// on an empty turn, so the proxy should transparently request continuation.
func (s *GeminiStreamState) NeedsStitch() bool {
	return s.hasThinking && !s.hasText && !s.usedTool
}

// LastThinking returns the accumulated thinking text.
func (s *GeminiStreamState) LastThinking() string {
	return s.lastThought
}

// DecodeChunk decodes one Gemini SSE chunk into zero or more IR stream
// events, advancing the stream state.
func (s *GeminiStreamState) DecodeChunk(chunk map[string]any) []StreamEvent {
	inner := extractInnerResponse(chunk)
	if inner == nil {
		return nil
	}

	var events []StreamEvent

	// Usage.
	usage := decodeGeminiUsage(inner)
	if usage.Prompt > 0 || usage.Completion > 0 {
		u := usage
		events = append(events, StreamEvent{Type: StreamUsage, Usage: &u})
	}

	// Parts.
	candidates, _ := inner["candidates"].([]any)
	if len(candidates) > 0 {
		candidate, _ := candidates[0].(map[string]any)
		if candidate != nil {
			contentMap, _ := candidate["content"].(map[string]any)
			if contentMap != nil {
				parts, _ := contentMap["parts"].([]any)
				for _, partAny := range parts {
					part, _ := partAny.(map[string]any)
					if part == nil {
						continue
					}
					evs := decodeGeminiPartToStream(part, s.toolCallIdx)
					for _, ev := range evs {
						switch ev.Type {
						case StreamThinkingDelta:
							s.hasThinking = true
							s.lastThought += ev.Delta
						case StreamTextDelta:
							s.hasText = true
						case StreamToolCallDelta, StreamToolCallDone:
							s.usedTool = true
						}
					}
					if _, ok := part["functionCall"].(map[string]any); ok {
						s.toolCallIdx++
					}
					events = append(events, evs...)
				}
			}
			if fr, ok := candidate["finishReason"].(string); ok && fr != "" {
				events = append(events, StreamEvent{
					Type:         StreamDone,
					FinishReason: mapGeminiFinishReason(fr),
				})
			}

			// Extract grounding metadata from google_search tool.
			if gm, ok := candidate["groundingMetadata"].(map[string]any); ok {
				if wsr := decodeGroundingMetadata(gm); wsr != nil && len(wsr.Sources) > 0 {
					events = append(events, StreamEvent{
						Type:      StreamWebSearch,
						WebSearch: wsr,
					})
				}
			}
		}
	}

	return events
}

// decodeGeminiPartToStream converts a Gemini part to stream events.
// toolCallIdx is the positional index of this tool call within the whole
// response turn so that parallel tool calls get distinct OpenAI indices.
func decodeGeminiPartToStream(part map[string]any, toolCallIdx int) []StreamEvent {
	// Thought text.
	if t, ok := part["text"].(string); ok {
		if thought, _ := part["thought"].(bool); thought {
			return []StreamEvent{{Type: StreamThinkingDelta, Delta: t}}
		}
		return []StreamEvent{{Type: StreamTextDelta, Delta: t}}
	}

	// Function call.
	if fc, ok := part["functionCall"].(map[string]any); ok {
		name, _ := fc["name"].(string)
		id, _ := fc["id"].(string)
		args, _ := fc["args"].(map[string]any)
		sig, _ := part["thoughtSignature"].(string)
		tc := &ToolCall{ID: id, Name: name, Args: args, Signature: sig, Index: toolCallIdx}
		return []StreamEvent{
			{Type: StreamToolCallDelta, ToolCall: tc},
			{Type: StreamToolCallDone, ToolCall: tc},
		}
	}

	return nil
}

// mergeConsecutiveRoles merges adjacent content entries with the same
// role into a single entry, concatenating their parts. Gemini does not
// allow consecutive messages with the same role.
func mergeConsecutiveRoles(contents []any) []any {
	var out []any
	for _, entryAny := range contents {
		entry, _ := entryAny.(map[string]any)
		if entry == nil {
			out = append(out, entryAny)
			continue
		}
		role, _ := entry["role"].(string)
		parts, _ := entry["parts"].([]any)
		if len(out) > 0 {
			last, _ := out[len(out)-1].(map[string]any)
			if last != nil {
				if lastRole, _ := last["role"].(string); lastRole == role {
					if lastParts, ok := last["parts"].([]any); ok {
						last["parts"] = append(lastParts, parts...)
						continue
					}
				}
			}
		}
		out = append(out, map[string]any{"role": role, "parts": parts})
	}
	return out
}

// ensureValidFirstTurn guarantees that the first content entry is a
// user turn. Gemini requires functionCall (model turn) to come after a
// user turn or functionResponse turn. If the client's history was
// truncated and the first message is a model turn containing a
// functionCall, Gemini returns 400 "Please ensure that function call
// turn comes immediately after a user turn or after a function
// response turn." We fix this by prepending a minimal user turn so
// the model turn is no longer first.
func ensureValidFirstTurn(contents []any) []any {
	if len(contents) == 0 {
		return contents
	}
	first, _ := contents[0].(map[string]any)
	if first == nil {
		return contents
	}
	role, _ := first["role"].(string)
	if role == "user" {
		return contents
	}
	// Prepend a minimal user turn to satisfy Gemini's ordering rule.
	pad := map[string]any{
		"role":  "user",
		"parts": []any{map[string]any{"text": ""}},
	}
	return append([]any{pad}, contents...)
}
