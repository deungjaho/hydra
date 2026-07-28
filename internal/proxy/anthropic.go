package proxy

import (
	"encoding/json"
	"strconv"
	"strings"
)

// AnthropicTransformRequest transforms an Anthropic /v1/messages request body
// into the Gemini v1internal envelope.
func AnthropicTransformRequest(req map[string]any, projectID, sessionID string, requestN uint64) map[string]any {
	originalModel := strOr(req, "model", "gemini-2.5-flash")
	mappedModel := MapModel(originalModel)

	// --- system prompt → systemInstruction ---
	var systemParts []any
	if systemAny, ok := req["system"]; ok {
		switch system := systemAny.(type) {
		case string:
			if strings.TrimSpace(system) != "" {
				systemParts = append(systemParts, map[string]any{"text": system})
			}
		case []any:
			for _, blockAny := range system {
				block, _ := blockAny.(map[string]any)
				if block == nil {
					continue
				}
				if t, ok := block["text"].(string); ok && strings.TrimSpace(t) != "" {
					systemParts = append(systemParts, map[string]any{"text": t})
				}
			}
		}
	}

	// --- messages → contents ---
	messages, _ := req["messages"].([]any)
	toolNameMap := buildToolNameMap(messages)

	var contents []any
	for _, msgAny := range messages {
		msg, _ := msgAny.(map[string]any)
		if msg == nil {
			continue
		}
		contents = append(contents, anthropicMessageToContent(msg, toolNameMap))
	}
	contents = mergeConsecutiveRoles(contents)

	requestBody := map[string]any{"contents": contents}
	if len(systemParts) > 0 {
		requestBody["systemInstruction"] = map[string]any{
			"role":  "user",
			"parts": systemParts,
		}
	}

	// --- generationConfig ---
	// Cap maxOutputTokens to the model's upstream limit. The streaming
	// endpoint rejects values above this limit with 400 INVALID_ARGUMENT.
	maxTokens := uint64Or(req, "max_tokens", 8192)
	if cap := maxOutputTokensCap(mappedModel); maxTokens > uint64(cap) {
		maxTokens = uint64(cap)
	}
	genConfig := map[string]any{
		"temperature":     orDefault(req, "temperature", 1.0),
		"topP":            orDefault(req, "top_p", 1.0),
		"topK":            orDefault(req, "top_k", 40),
		"maxOutputTokens": maxTokens,
	}

	// thinkingConfig
	isThinking := isThinkingModel(mappedModel)
	if thinkingAny, ok := req["thinking"].(map[string]any); ok {
		if t, _ := thinkingAny["type"].(string); t == "enabled" || t == "adaptive" {
			isThinking = true
		}
	}
	if isThinking {
		budget := uint64(10000)
		if thinkingAny, ok := req["thinking"].(map[string]any); ok {
			if b := uint64Or(thinkingAny, "budget_tokens", 10000); b != 0 {
				budget = b
			}
		}
		// Ensure budget < maxOutputTokens (Anthropic requirement).
		if budget >= maxTokens {
			if maxTokens > 256 {
				budget = maxTokens - 256
			} else {
				budget = 128
			}
		}
		if budget < 128 {
			budget = 128
		}
		genConfig["thinkingConfig"] = map[string]any{
			"includeThoughts": true,
			"thinkingBudget":  budget,
		}
	}

	// stop_sequences
	if ss, ok := req["stop_sequences"].([]any); ok && len(ss) > 0 {
		genConfig["stopSequences"] = ss
	}
	requestBody["generationConfig"] = genConfig

	// safetySettings — disable all (matches Antigravity desktop).
	requestBody["safetySettings"] = []any{
		map[string]any{"category": "HARM_CATEGORY_HARASSMENT", "threshold": "OFF"},
		map[string]any{"category": "HARM_CATEGORY_HATE_SPEECH", "threshold": "OFF"},
		map[string]any{"category": "HARM_CATEGORY_SEXUALLY_EXPLICIT", "threshold": "OFF"},
		map[string]any{"category": "HARM_CATEGORY_DANGEROUS_CONTENT", "threshold": "OFF"},
	}
	requestBody["sessionId"] = sessionID

	// tools → functionDeclarations
	if tools, ok := req["tools"].([]any); ok {
		var funcs []any
		for _, tAny := range tools {
			t, _ := tAny.(map[string]any)
			if t == nil {
				continue
			}
			funcs = append(funcs, transformToolAnthropic(t))
		}
		if len(funcs) > 0 {
			requestBody["tools"] = []any{
				map[string]any{"functionDeclarations": funcs},
			}
			requestBody["toolConfig"] = map[string]any{
				"functionCallingConfig": map[string]any{"mode": "AUTO"},
			}
		}
	}

	return map[string]any{
		"project":            projectID,
		"request":            requestBody,
		"model":              mappedModel,
		"userAgent":          "antigravity",
		"requestType":        "agent",
		"enabledCreditTypes": []string{"GOOGLE_ONE_AI"},
		"requestId":          "agent/antigravity/" + sessionID + "/" + itoaUint64(requestN),
	}
}

func anthropicMessageToContent(msg map[string]any, toolNameMap map[string]string) map[string]any {
	role := strOr(msg, "role", "user")
	geminiRole := "user"
	if role == "assistant" {
		geminiRole = "model"
	}

	var parts []any
	switch content := msg["content"].(type) {
	case string:
		if strings.TrimSpace(content) != "" {
			parts = append(parts, map[string]any{"text": content})
		}
	case []any:
		for _, blockAny := range content {
			block, _ := blockAny.(map[string]any)
			if block == nil {
				continue
			}
			btype := strOr(block, "type", "text")
			switch btype {
			case "text":
				if t, ok := block["text"].(string); ok && strings.TrimSpace(t) != "" {
					parts = append(parts, map[string]any{"text": t})
				}
			case "thinking":
				if t, ok := block["thinking"].(string); ok && strings.TrimSpace(t) != "" {
					// Pass through the real signature from the client.
					// For Claude models served natively by Antigravity,
					// the upstream (Anthropic API) validates signatures,
					// so we must forward the original. For Gemini models,
					// "skip_thought_signature_validator" skips validation.
					sig := "skip_thought_signature_validator"
					if s, ok := block["signature"].(string); ok && s != "" {
						sig = s
					}
					part := map[string]any{
						"text":             t,
						"thought":          true,
						"thoughtSignature": sig,
					}
					parts = append(parts, part)
				}
			case "redacted_thinking":
				if data, ok := block["data"].(string); ok {
					parts = append(parts, map[string]any{
						"text":    "[redacted thinking: " + data + "]",
						"thought": true,
					})
				}
			case "image":
				if src, ok := block["source"].(map[string]any); ok {
					mediaType := strOr(src, "media_type", "image/png")
					data, _ := src["data"].(string)
					if data != "" {
						parts = append(parts, map[string]any{
							"inlineData": map[string]any{"mimeType": mediaType, "data": data},
						})
					}
				}
			case "tool_use":
				id := strOr(block, "id", "")
				name := strOr(block, "name", "")
				input := block["input"]
				if input == nil {
					input = map[string]any{}
				}
				sig := "skip_thought_signature_validator"
				if s, ok := block["signature"].(string); ok {
					sig = s
				}
				parts = append(parts, map[string]any{
					"functionCall":    map[string]any{"name": name, "args": input, "id": id},
					"thoughtSignature": sig,
				})
			case "tool_result":
				toolUseID := strOr(block, "tool_use_id", "")
				isError, _ := block["is_error"].(bool)
				contentText := extractToolResultContent(block["content"])
				var result any
				if isError {
					result = map[string]any{"error": true, "result": contentText}
				} else {
					result = map[string]any{"result": contentText}
				}
				funcName, ok := toolNameMap[toolUseID]
				if !ok {
					funcName = toolUseID
				}
				parts = append(parts, map[string]any{
					"functionResponse": map[string]any{
						"name":     funcName,
						"response": result,
						"id":       toolUseID,
					},
					"thoughtSignature": "skip_thought_signature_validator",
				})
			}
		}
	}

	if len(parts) == 0 {
		parts = append(parts, map[string]any{"text": ""})
	}
	return map[string]any{"role": geminiRole, "parts": parts}
}

func buildToolNameMap(messages []any) map[string]string {
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
			if strOr(block, "type", "") == "tool_use" {
				id := strOr(block, "id", "")
				name := strOr(block, "name", "")
				if id != "" {
					m[id] = name
				}
			}
		}
	}
	return m
}

func extractToolResultContent(content any) string {
	switch c := content.(type) {
	case string:
		return c
	case []any:
		var texts []string
		for _, bAny := range c {
			b, _ := bAny.(map[string]any)
			if b == nil {
				continue
			}
			if strOr(b, "type", "") == "text" {
				if t, ok := b["text"].(string); ok {
					texts = append(texts, t)
				}
			}
		}
		return strings.Join(texts, "\n")
	}
	return ""
}

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

func transformToolAnthropic(tool map[string]any) map[string]any {
	name := strOr(tool, "name", "")
	desc := strOr(tool, "description", "")
	params, _ := tool["input_schema"].(map[string]any)
	if params == nil {
		params = map[string]any{"type": "object", "properties": map[string]any{}}
	}
	normalizeSchemaTypesAnthropic(params)
	return map[string]any{
		"name":        name,
		"description": desc,
		"parameters":  params,
	}
}

func normalizeSchemaTypesAnthropic(schema map[string]any) {
	// OpenAI/JSON Schema allows type to be an array like
	// ["string","null"] for nullable fields. Gemini proto requires a
	// single type string. Convert array → first non-null type +
	// nullable: true.
	if typeArr, ok := schema["type"].([]any); ok {
		var primary string
		nullable := false
		for _, tAny := range typeArr {
			t, _ := tAny.(string)
			if t == "null" {
				nullable = true
				continue
			}
			if primary == "" {
				primary = t
			}
		}
		if primary == "" {
			primary = "string"
		}
		schema["type"] = primary
		if nullable {
			schema["nullable"] = true
		}
	}
	if t, ok := schema["type"].(string); ok {
		switch t {
		case "object":
			schema["type"] = "OBJECT"
		case "string":
			schema["type"] = "STRING"
		case "number":
			schema["type"] = "NUMBER"
		case "integer":
			schema["type"] = "INTEGER"
		case "boolean":
			schema["type"] = "BOOLEAN"
		case "array":
			schema["type"] = "ARRAY"
		}
	}
	for _, k := range []string{
		"format", "strict", "$schema", "definitions",
		"exclusiveMinimum", "exclusiveMaximum", "default", "examples",
		"pattern", "multipleOf", "minLength", "maxLength",
		"minItems", "maxItems", "minProperties", "maxProperties",
		"uniqueItems", "const", "enum", "title", "$ref",
		"additionalProperties", "propertyNames",
		"oneOf", "anyOf", "allOf", "not",
	} {
		delete(schema, k)
	}
	if props, ok := schema["properties"].(map[string]any); ok {
		for _, vAny := range props {
			if v, ok := vAny.(map[string]any); ok {
				normalizeSchemaTypesAnthropic(v)
			}
		}
	}
	if items, ok := schema["items"].(map[string]any); ok {
		normalizeSchemaTypesAnthropic(items)
	}
}

// ---------------------------------------------------------------------------
// Response conversion: Gemini → Anthropic
// ---------------------------------------------------------------------------

// AnthropicTransformResponse transforms a non-streaming Gemini response into
// an Anthropic /v1/messages response.
func AnthropicTransformResponse(geminiResp map[string]any, model string) map[string]any {
	inner := innerResponse(geminiResp)

	candidates, _ := inner["candidates"].([]any)
	var candidate map[string]any
	if len(candidates) > 0 {
		candidate, _ = candidates[0].(map[string]any)
	}
	if candidate == nil {
		candidate = map[string]any{}
	}

	contentMap, _ := candidate["content"].(map[string]any)
	var parts []any
	if contentMap != nil {
		parts, _ = contentMap["parts"].([]any)
	}

	finishReason := strOr(candidate, "finishReason", "STOP")

	var contentBlocks []any
	hasToolUse := false

	for _, partAny := range parts {
		part, _ := partAny.(map[string]any)
		if part == nil {
			continue
		}
		if thought, _ := part["thought"].(bool); thought {
			if t, ok := part["text"].(string); ok && t != "" {
				block := map[string]any{"type": "thinking", "thinking": t}
				if sig, ok := part["thoughtSignature"].(string); ok {
					block["signature"] = sig
				}
				contentBlocks = append(contentBlocks, block)
			}
			continue
		}
		if t, ok := part["text"].(string); ok && t != "" {
			contentBlocks = append(contentBlocks, map[string]any{"type": "text", "text": t})
		}
		if fc, ok := part["functionCall"].(map[string]any); ok {
			hasToolUse = true
			id := strOr(fc, "id", "")
			if id == "" {
				name := strOr(fc, "name", "tool")
				id = "toolu_" + name + "_" + compactUUID()[:8]
			}
			name := strOr(fc, "name", "")
			input := fc["args"]
			if input == nil {
				input = map[string]any{}
			}
			contentBlocks = append(contentBlocks, map[string]any{
				"type":  "tool_use",
				"id":    id,
				"name":  name,
				"input": input,
			})
		}
	}

	if len(contentBlocks) == 0 {
		contentBlocks = append(contentBlocks, map[string]any{"type": "text", "text": ""})
	}

	stopReason := "end_turn"
	if hasToolUse {
		stopReason = "tool_use"
	} else if finishReason == "MAX_TOKENS" {
		stopReason = "max_tokens"
	}

	usage := buildAnthropicUsage(inner)

	return map[string]any{
		"id":            "msg_" + compactUUID()[:12],
		"type":          "message",
		"role":          "assistant",
		"model":         model,
		"content":       contentBlocks,
		"stop_reason":   stopReason,
		"stop_sequence": nil,
		"usage":         usage,
	}
}

func buildAnthropicUsage(inner map[string]any) map[string]any {
	usage, _ := inner["usageMetadata"].(map[string]any)
	if usage == nil {
		usage = map[string]any{}
	}
	prompt := int64Or(usage, "promptTokenCount", 0)
	completion := int64Or(usage, "candidatesTokenCount", 0)
	cached := int64Or(usage, "cachedContentTokenCount", 0)
	out := map[string]any{
		"input_tokens":                prompt,
		"output_tokens":               completion,
		"cache_creation_input_tokens": 0,
	}
	if cached > 0 {
		out["cache_read_input_tokens"] = cached
	} else {
		out["cache_read_input_tokens"] = 0
	}
	return out
}

// ---------------------------------------------------------------------------
// Streaming: Gemini SSE → Anthropic SSE
// ---------------------------------------------------------------------------

// AnthropicStreamState is the state machine for converting Gemini SSE into
// Anthropic SSE events.
type AnthropicStreamState struct {
	msgID           string
	model           string
	blockIndex      int
	currentBlock    *anthropicBlockType
	messageStartSet bool
	usedTool        bool
	inputTokens     int64
	outputTokens    int64
	cachedTokens    int64
}

type anthropicBlockType int

const (
	blockNone anthropicBlockType = iota
	blockText
	blockThinking
	blockToolUse
)

// NewAnthropicStreamState creates a fresh state machine.
func NewAnthropicStreamState(model string) *AnthropicStreamState {
	return &AnthropicStreamState{
		msgID: "msg_" + compactUUID()[:12],
		model: model,
	}
}

func (s *AnthropicStreamState) sse(event string, data any) string {
	b, _ := json.Marshal(data)
	return "event: " + event + "\ndata: " + string(b) + "\n\n"
}

func (s *AnthropicStreamState) ensureMessageStart() string {
	if s.messageStartSet {
		return ""
	}
	s.messageStartSet = true
	return s.sse("message_start", map[string]any{
		"type": "message_start",
		"message": map[string]any{
			"id":            s.msgID,
			"type":          "message",
			"role":          "assistant",
			"content":       []any{},
			"model":         s.model,
			"stop_reason":   nil,
			"stop_sequence": nil,
			"usage": map[string]any{
				"input_tokens":  s.inputTokens,
				"output_tokens": 0,
			},
		},
	})
}

func (s *AnthropicStreamState) startBlock(bt anthropicBlockType, contentBlock any) string {
	out := s.sse("content_block_start", map[string]any{
		"type":          "content_block_start",
		"index":         s.blockIndex,
		"content_block": contentBlock,
	})
	s.currentBlock = &bt
	return out
}

func (s *AnthropicStreamState) delta(deltaType string, payload map[string]any) string {
	payload["type"] = deltaType
	return s.sse("content_block_delta", map[string]any{
		"type":  "content_block_delta",
		"index": s.blockIndex,
		"delta": payload,
	})
}

func (s *AnthropicStreamState) endBlock() string {
	out := s.sse("content_block_stop", map[string]any{
		"type":  "content_block_stop",
		"index": s.blockIndex,
	})
	s.blockIndex++
	s.currentBlock = nil
	return out
}

// ProcessChunk processes one Gemini SSE chunk (parsed inner JSON) and emits
// Anthropic SSE event lines.
func (s *AnthropicStreamState) ProcessChunk(inner map[string]any) []string {
	var out []string

	if msg := s.ensureMessageStart(); msg != "" {
		out = append(out, msg)
	}

	if usage, ok := inner["usageMetadata"].(map[string]any); ok {
		if v := int64Or(usage, "promptTokenCount", 0); v != 0 {
			s.inputTokens = v
		}
		if v := int64Or(usage, "candidatesTokenCount", 0); v != 0 {
			s.outputTokens = v
		}
		if v := int64Or(usage, "cachedContentTokenCount", 0); v != 0 {
			s.cachedTokens = v
		}
	}

	candidates, _ := inner["candidates"].([]any)
	var candidate map[string]any
	if len(candidates) > 0 {
		candidate, _ = candidates[0].(map[string]any)
	}
	var parts []any
	if candidate != nil {
		if content, ok := candidate["content"].(map[string]any); ok {
			parts, _ = content["parts"].([]any)
		}
	}

	for _, partAny := range parts {
		part, _ := partAny.(map[string]any)
		if part == nil {
			continue
		}
		if thought, _ := part["thought"].(bool); thought {
			if t, ok := part["text"].(string); ok && t != "" {
				if s.currentBlock == nil || *s.currentBlock != blockThinking {
					if s.currentBlock != nil {
						out = append(out, s.endBlock())
					}
					out = append(out, s.startBlock(blockThinking, map[string]any{"type": "thinking", "thinking": ""}))
				}
				out = append(out, s.delta("thinking_delta", map[string]any{"thinking": t}))
			}
			if sig, ok := part["thoughtSignature"].(string); ok {
				out = append(out, s.delta("signature_delta", map[string]any{"signature": sig}))
			}
			continue
		}
		if t, ok := part["text"].(string); ok && t != "" {
			if s.currentBlock == nil || *s.currentBlock != blockText {
				if s.currentBlock != nil {
					out = append(out, s.endBlock())
				}
				out = append(out, s.startBlock(blockText, map[string]any{"type": "text", "text": ""}))
			}
			out = append(out, s.delta("text_delta", map[string]any{"text": t}))
			continue
		}
		if fc, ok := part["functionCall"].(map[string]any); ok {
			s.usedTool = true
			if s.currentBlock != nil {
				out = append(out, s.endBlock())
			}
			id := strOr(fc, "id", "")
			if id == "" {
				name := strOr(fc, "name", "tool")
				id = "toolu_" + name + "_" + compactUUID()[:8]
			}
			name := strOr(fc, "name", "")
			input := fc["args"]
			if input == nil {
				input = map[string]any{}
			}
			inputBytes, _ := json.Marshal(input)
			if inputBytes == nil {
				inputBytes = []byte("{}")
			}
			out = append(out, s.startBlock(blockToolUse, map[string]any{
				"type":  "tool_use",
				"id":    id,
				"name":  name,
				"input": map[string]any{},
			}))
			out = append(out, s.delta("input_json_delta", map[string]any{"partial_json": string(inputBytes)}))
			out = append(out, s.endBlock())
		}
	}

	// Check finishReason on the last chunk.
	var finishReason string
	if candidate != nil {
		finishReason = strOr(candidate, "finishReason", "")
	}
	if finishReason != "" {
		if s.currentBlock != nil {
			out = append(out, s.endBlock())
		}
		stopReason := "end_turn"
		if s.usedTool {
			stopReason = "tool_use"
		} else if finishReason == "MAX_TOKENS" {
			stopReason = "max_tokens"
		}
		out = append(out, s.sse("message_delta", map[string]any{
			"type":  "message_delta",
			"delta": map[string]any{"stop_reason": stopReason, "stop_sequence": nil},
			"usage": map[string]any{
				"input_tokens":  s.inputTokens,
				"output_tokens": s.outputTokens,
			},
		}))
		out = append(out, s.sse("message_stop", map[string]any{"type": "message_stop"}))
	}

	return out
}

func uint64Or(m map[string]any, key string, def uint64) uint64 {
	switch v := m[key].(type) {
	case float64:
		return uint64(v)
	case int64:
		return uint64(v)
	case int:
		return uint64(v)
	}
	return def
}

func itoaUint64(v uint64) string {
	return strconv.FormatUint(v, 10)
}
