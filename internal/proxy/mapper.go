package proxy

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/deungjaho/hydra/internal/account"
)

// SupportedModels is the canonical list orbit advertises via /v1/models.
// Order matters — it determines display order in the TUI and /v1/models.
var SupportedModels = []string{
	// Gemini family
	"gemini-2.5-flash",
	"gemini-2.5-flash-thinking",
	"gemini-3-flash",
	"gemini-3-pro",
	"gemini-3-pro-preview",
	"gemini-3-pro-low",
	"gemini-3-pro-high",
	"gemini-3.1-pro-preview",
	"gemini-3-pro-image",
	// Claude family (served natively by Antigravity)
	"claude-sonnet-4-6",
	"claude-sonnet-4-6-thinking",
	"claude-opus-4-6",
	"claude-opus-4-6-thinking",
}

// MapModel maps an incoming OpenAI model name to the Antigravity/Gemini model id.
//
// Strategy:
//  1. Exact alias lookup (handles deprecated/legacy names → current model).
//  2. gemini-* / claude-* passthrough (supports unreleased model IDs).
//  3. Unknown → passthrough as-is (lets the upstream reject if invalid).
func MapModel(openaiModel string) string {
	m := strings.ToLower(strings.TrimSpace(openaiModel))
	switch m {
	// OpenAI names → Gemini equivalents
	case "gpt-4", "gpt-4o", "gpt-4-turbo", "gpt-4.1", "gpt-4.1-turbo",
		"gpt-4-turbo-preview", "gpt-4-0125-preview", "gpt-4-1106-preview",
		"gpt-4-0613", "gpt-4o-2024-05-13", "gpt-4o-2024-08-06",
		"gpt-4o-mini", "gpt-4o-mini-2024-07-18",
		"gpt-3.5-turbo", "gpt-3.5-turbo-16k", "gpt-3.5-turbo-0125",
		"gpt-3.5-turbo-1106", "gpt-3.5-turbo-0613":
		return "gemini-2.5-flash"
	case "gpt-5":
		return "gemini-3-pro-preview"

	// Claude legacy/alias → current
	case "claude-sonnet-4-5":
		return "claude-sonnet-4-6"
	case "claude-sonnet-4-5-thinking":
		return "claude-sonnet-4-6-thinking"
	case "claude-sonnet-4-5-20250929":
		return "claude-sonnet-4-6-thinking"
	case "claude-3-5-sonnet-20241022", "claude-3-5-sonnet-20240620":
		return "claude-sonnet-4-6"
	case "claude-opus-4", "claude-opus-4-5-thinking", "claude-opus-4-5-20251101":
		return "claude-opus-4-6-thinking"
	case "claude-opus-4.6", "claude-opus-4.6-thinking", "claude-opus-4-6-20260201":
		return "claude-opus-4-6-thinking"
	case "claude-haiku-4", "claude-3-haiku-20240307", "claude-haiku-4-5-20251001":
		return "claude-sonnet-4-6"

	// Gemini aliases
	case "gemini-2.5-flash-lite":
		return "gemini-2.5-flash"
	case "gemini-3.1-pro":
		return "gemini-3.1-pro-preview"
	case "gemini-3-pro":
		return "gemini-3-pro-preview"
	case "gemini-3.1-pro-high", "gemini-3-pro-high":
		return "gemini-pro-agent"

	// Background-task virtual id
	case "internal-background-task":
		return "gemini-2.5-flash"
	}
	// Passthrough: gemini-*, claude-* (supports unreleased IDs).
	if strings.HasPrefix(m, "gemini-") || strings.HasPrefix(m, "claude-") {
		return m
	}
	return m
}

// TransformRequest transforms an OpenAI ChatCompletion request into the
// Antigravity v1internal envelope.
func TransformRequest(openaiReq map[string]any, projectID, sessionID string, requestN uint64) map[string]any {
	originalModel := strOr(openaiReq, "model", "gemini-2.5-flash")
	mappedModel := MapModel(originalModel)

	messages, _ := openaiReq["messages"].([]any)

	// Build a tool_call_id → function_name map so that tool response
	// messages (which often omit the function name in OpenAI format)
	// can recover the name for Gemini's functionResponse.name field.
	toolNameMap := buildOpenAIToolNameMap(messages)

	var systemParts []any
	var contents []any
	for _, msgAny := range messages {
		msg, _ := msgAny.(map[string]any)
		if msg == nil {
			continue
		}
		role := strOr(msg, "role", "user")
		switch role {
		case "system", "developer":
			if text := textOfMessage(msg); text != "" {
				systemParts = append(systemParts, map[string]any{"text": text})
			}
		default:
			contents = append(contents, openAIMessageToContent(msg, toolNameMap))
		}
	}

	requestBody := map[string]any{
		"contents": contents,
	}
	if len(systemParts) > 0 {
		requestBody["systemInstruction"] = map[string]any{
			"role":  "system",
			"parts": systemParts,
		}
	}

	genConfig := map[string]any{
		"temperature":     orDefault(openaiReq, "temperature", 1.0),
		"topP":            orDefault(openaiReq, "top_p", 1.0),
		"topK":            40,
		"maxOutputTokens": orDefault(openaiReq, "max_tokens", 65536),
	}
	if isThinkingModel(mappedModel) {
		maxOut := toInt64(orDefault(openaiReq, "max_tokens", 65536), 65536)
		// thinkingBudget must be < maxOutputTokens (Anthropic requirement).
		var budget int64 = 10000
		if budget >= maxOut {
			if maxOut > 384 {
				budget = maxOut - 256
			} else if maxOut > 256 {
				budget = 128
			} else {
				budget = maxOut / 2
				if budget < 1 {
					budget = 1
				}
			}
		}
		genConfig["thinkingConfig"] = map[string]any{
			"includeThoughts": true,
			"thinkingBudget":  budget,
		}
	}
	requestBody["generationConfig"] = genConfig

	// response_format: json_object → tell Gemini to output JSON.
	if rf, ok := openaiReq["response_format"].(map[string]any); ok {
		if t, _ := rf["type"].(string); t == "json_object" {
			genConfig["responseMimeType"] = "application/json"
			if schema, ok := rf["json_schema"].(map[string]any); ok {
				if s, ok := schema["schema"].(map[string]any); ok {
					genConfig["responseSchema"] = s
				}
			}
		}
	}

	// safetySettings — disable all safety filters (matches Antigravity desktop).
	requestBody["safetySettings"] = []any{
		map[string]any{"category": "HARM_CATEGORY_HARASSMENT", "threshold": "OFF"},
		map[string]any{"category": "HARM_CATEGORY_HATE_SPEECH", "threshold": "OFF"},
		map[string]any{"category": "HARM_CATEGORY_SEXUALLY_EXPLICIT", "threshold": "OFF"},
		map[string]any{"category": "HARM_CATEGORY_DANGEROUS_CONTENT", "threshold": "OFF"},
	}

	requestBody["sessionId"] = sessionID

	if tools, ok := openaiReq["tools"].([]any); ok {
		var funcs []any
		for _, tAny := range tools {
			t, _ := tAny.(map[string]any)
			if t == nil {
				continue
			}
			if fn, ok := t["function"]; ok {
				funcs = append(funcs, transformToolOpenAI(fn))
			}
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
		"requestId":          fmt.Sprintf("agent/antigravity/%s/%d", sessionID, requestN),
	}
}

// IsThinkingModelPub is a public wrapper for use by the Anthropic mapper.
func IsThinkingModelPub(model string) bool { return isThinkingModel(model) }

func isThinkingModel(model string) bool {
	return strings.Contains(model, "-thinking") ||
		(strings.HasPrefix(model, "gemini-") && (strings.Contains(model, "-pro") || strings.Contains(model, "gemini-3")))
}

// buildOpenAIToolNameMap scans all messages and builds a map from
// tool_call_id → function name, by looking at assistant messages that
// contain tool_calls. This lets tool response messages (which often
// omit the name field in OpenAI format) recover the function name.
func buildOpenAIToolNameMap(messages []any) map[string]string {
	m := make(map[string]string)
	for _, msgAny := range messages {
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
			id := strOr(tc, "id", "")
			fn, _ := tc["function"].(map[string]any)
			name := ""
			if fn != nil {
				name = strOr(fn, "name", "")
			}
			if id != "" && name != "" {
				m[id] = name
			}
		}
	}
	return m
}

func openAIMessageToContent(msg map[string]any, toolNameMap map[string]string) map[string]any {
	role := strOr(msg, "role", "user")
	geminiRole := "user"
	switch role {
	case "assistant":
		geminiRole = "model"
	case "tool", "function":
		geminiRole = "user"
	}

	var parts []any

	// tool_calls → functionCall
	if toolCalls, ok := msg["tool_calls"].([]any); ok {
		for _, tcAny := range toolCalls {
			tc, _ := tcAny.(map[string]any)
			if tc == nil {
				continue
			}
			id := strOr(tc, "id", "")
			fnAny, _ := tc["function"].(map[string]any)
			var fn map[string]any
			if fnAny != nil {
				fn = fnAny
			} else {
				fn = map[string]any{}
			}
			name := strOr(fn, "name", "")
			argsStr := strOr(fn, "arguments", "{}")
			var args any
			if err := json.Unmarshal([]byte(argsStr), &args); err != nil {
				args = map[string]any{}
			}
			parts = append(parts, map[string]any{
				"functionCall":    map[string]any{"name": name, "args": args, "id": id},
				"thoughtSignature": "skip_thought_signature_validator",
			})
		}
	}

	// tool message → functionResponse
	if role == "tool" {
		name := strOr(msg, "name", "")
		toolCallID := strOr(msg, "tool_call_id", "")
		// OpenAI tool messages often omit the function name; recover it
		// from the corresponding assistant tool_call via the ID map.
		if name == "" && toolCallID != "" {
			if mapped, ok := toolNameMap[toolCallID]; ok {
				name = mapped
			}
		}
		content := textOfMessage(msg)
		parts = append(parts, map[string]any{
			"functionResponse": map[string]any{
				"name":     name,
				"response": map[string]any{"result": content},
				"id":       toolCallID,
			},
		})
	}

	// reasoning_content → thought part
	if rc, ok := msg["reasoning_content"].(string); ok && rc != "" {
		parts = append(parts, map[string]any{"text": rc, "thought": true})
	}

	// text content / multimodal content
	if contentAny, ok := msg["content"]; ok {
		switch content := contentAny.(type) {
		case string:
			if content != "" {
				parts = append(parts, map[string]any{"text": content})
			}
		case []any:
			for _, partAny := range content {
				part, _ := partAny.(map[string]any)
				if part == nil {
					continue
				}
				if t, ok := part["text"].(string); ok {
					parts = append(parts, map[string]any{"text": t})
				}
				if img, ok := part["image_url"].(map[string]any); ok {
					if urlStr, ok := img["url"].(string); ok {
						if rest, ok := strings.CutPrefix(urlStr, "data:"); ok {
							if mime, data, ok := strings.Cut(rest, ","); ok {
								// mime is like "image/png;base64" — strip the ";base64" suffix
								if i := strings.Index(mime, ";"); i >= 0 {
									mime = mime[:i]
								}
								parts = append(parts, map[string]any{
									"inlineData": map[string]any{"mimeType": mime, "data": data},
								})
							}
						}
					}
				}
				if audio, ok := part["input_audio"].(map[string]any); ok {
					data, _ := audio["data"].(string)
					format, _ := audio["format"].(string)
					if data != "" {
						mimeType := "audio/wav"
						switch format {
						case "mp3", "mpeg":
							mimeType = "audio/mpeg"
						case "webm":
							mimeType = "audio/webm"
						case "mp4", "m4a":
							mimeType = "audio/mp4"
						}
						parts = append(parts, map[string]any{
							"inlineData": map[string]any{"mimeType": mimeType, "data": data},
						})
					}
				}
			}
		}
	}

	if len(parts) == 0 {
		parts = append(parts, map[string]any{"text": ""})
	}

	return map[string]any{"role": geminiRole, "parts": parts}
}

func textOfMessage(msg map[string]any) string {
	contentAny, ok := msg["content"]
	if !ok {
		return ""
	}
	switch content := contentAny.(type) {
	case string:
		return content
	case []any:
		var texts []string
		for _, partAny := range content {
			part, _ := partAny.(map[string]any)
			if part == nil {
				continue
			}
			if t, ok := part["text"].(string); ok {
				texts = append(texts, t)
			}
		}
		return strings.Join(texts, "\n")
	}
	return ""
}

func transformToolOpenAI(fnAny any) map[string]any {
	fn, _ := fnAny.(map[string]any)
	if fn == nil {
		fn = map[string]any{}
	}
	name := valueOr(fn, "name", "")
	desc := valueOr(fn, "description", "")
	params, _ := fn["parameters"].(map[string]any)
	if params == nil {
		params = map[string]any{"type": "OBJECT", "properties": map[string]any{}}
	}
	normalizeSchemaTypes(params)
	return map[string]any{
		"name":        name,
		"description": desc,
		"parameters":  params,
	}
}

func normalizeSchemaTypes(schema map[string]any) {
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
	// Remove fields Gemini rejects.
	for _, k := range []string{"format", "strict", "$schema", "definitions"} {
		delete(schema, k)
	}
	if props, ok := schema["properties"].(map[string]any); ok {
		for _, vAny := range props {
			if v, ok := vAny.(map[string]any); ok {
				normalizeSchemaTypes(v)
			}
		}
	}
	if items, ok := schema["items"].(map[string]any); ok {
		normalizeSchemaTypes(items)
	}
}

// TransformResponse transforms a non-streaming Gemini response into an
// OpenAI ChatCompletion response.
func TransformResponse(geminiResp map[string]any, model string) map[string]any {
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

	var content, reasoning string
	var toolCalls []any
	for _, partAny := range parts {
		part, _ := partAny.(map[string]any)
		if part == nil {
			continue
		}
		if t, ok := part["text"].(string); ok {
			if thought, _ := part["thought"].(bool); thought {
				reasoning += t
			} else {
				content += t
			}
		}
		if fc, ok := part["functionCall"].(map[string]any); ok {
			id := strOr(fc, "id", "call_0")
			name := strOr(fc, "name", "")
			args := fc["args"]
			if args == nil {
				args = map[string]any{}
			}
			argsBytes, _ := json.Marshal(args)
			if argsBytes == nil {
				argsBytes = []byte("{}")
			}
			toolCalls = append(toolCalls, map[string]any{
				"id":   id,
				"type": "function",
				"function": map[string]any{
					"name":      name,
					"arguments": string(argsBytes),
				},
			})
		}
	}

	finishReason := strOr(candidate, "finishReason", "")
	finishReasonMapped := mapFinishReason(finishReason)
	if finishReasonMapped == "" {
		finishReasonMapped = "stop"
	}

	usage, _ := inner["usageMetadata"].(map[string]any)
	if usage == nil {
		usage = map[string]any{}
	}
	promptTokens := int64Or(usage, "promptTokenCount", 0)
	completionTokens := int64Or(usage, "candidatesTokenCount", 0)
	cachedTokens := int64Or(usage, "cachedContentTokenCount", 0)
	thoughtTokens := int64Or(usage, "thoughtsTokenCount", 0)
	totalTokens := promptTokens + completionTokens

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

	return map[string]any{
		"id":      "chatcmpl-" + compactUUID(),
		"object":  "chat.completion",
		"created": nowUnix(),
		"model":   model,
		"choices": []any{
			map[string]any{
				"index":         0,
				"message":       message,
				"finish_reason": finishReasonMapped,
			},
		},
		"usage": map[string]any{
			"prompt_tokens":     promptTokens,
			"completion_tokens": completionTokens,
			"total_tokens":      totalTokens,
			"cached_tokens":     cachedTokens,
			"thought_tokens":    thoughtTokens,
		},
	}
}

func mapFinishReason(gemini string) string {
	switch gemini {
	case "STOP":
		return "stop"
	case "MAX_TOKENS":
		return "length"
	case "SAFETY", "RECITATION":
		return "content_filter"
	}
	return "stop"
}

// ---------------------------------------------------------------------------
// Dynamic-Model-Rewrite
// ---------------------------------------------------------------------------

func buildDynamicModelCandidates(modelName string) []string {
	model := strings.ToLower(strings.TrimSpace(modelName))
	if model == "" {
		return nil
	}
	proImage := []string{"gemini-3-pro-image", "gemini-3.1-pro-image"}
	flashImage := []string{"gemini-3-flash-image", "gemini-3.1-flash-image"}
	isProImage := contains(proImage, model)
	isFlashImage := contains(flashImage, model)

	var out []string
	seen := make(map[string]struct{})
	push := func(c string) {
		if _, ok := seen[c]; ok {
			return
		}
		seen[c] = struct{}{}
		out = append(out, c)
	}

	if isProImage || isFlashImage {
		push(model)
		if isProImage {
			push("gemini-3.1-pro-image")
			push("gemini-3-pro-image")
		} else {
			push("gemini-3.1-flash-image")
			push("gemini-3-flash-image")
		}
		return out
	}

	proFamily := []string{
		"gemini-3-pro", "gemini-3-pro-preview", "gemini-3-pro-high", "gemini-3-pro-low",
		"gemini-3.1-pro", "gemini-3.1-pro-preview", "gemini-3.1-pro-high", "gemini-3.1-pro-low",
		"gemini-pro-agent",
	}
	if !contains(proFamily, model) {
		return nil
	}

	push(model)
	push("gemini-pro-agent")
	push("gemini-3.1-pro-preview")
	push("gemini-3-pro-preview")
	push("gemini-3.1-pro-high")
	push("gemini-3-pro-high")
	push("gemini-3.1-pro-low")
	push("gemini-3-pro-low")
	return out
}

// ResolveModelForAccount picks the model to actually send to upstream for a
// specific account. If the account's quota lists the requested model, use it
// as-is. Otherwise, walk the candidate list and pick the first one the account
// has. If no candidate matches, return the original model unchanged.
func ResolveModelForAccount(mappedModel string, available map[string]struct{}) string {
	candidates := buildDynamicModelCandidates(mappedModel)
	if len(candidates) == 0 {
		return mappedModel
	}
	for _, c := range candidates {
		if _, ok := available[strings.ToLower(c)]; ok {
			if strings.ToLower(c) != strings.ToLower(mappedModel) {
				// log info would go here; we skip to avoid importing log.
			}
			return c
		}
	}
	return mappedModel
}

// DynamicModelList returns the union of routable models available across the
// given accounts, fetched dynamically from Google's fetchAvailableModels API.
func DynamicModelList(accounts []*account.Account) []string {
	set := make(map[string]struct{})
	for _, a := range accounts {
		for m := range a.AvailableModels() {
			if isRoutableModel(m) {
				set[m] = struct{}{}
			}
		}
	}
	if len(set) == 0 {
		// Fallback to hardcoded list if no accounts have quota data yet.
		for _, m := range SupportedModels {
			set[m] = struct{}{}
		}
	}
	out := make([]string, 0, len(set))
	for m := range set {
		out = append(out, m)
	}
	// Sort for stable display.
	sortStrings(out)
	return out
}

func isRoutableModel(name string) bool {
	m := strings.ToLower(name)
	// Exclude internal/non-routable models (chat_*, tab_*, etc.)
	if strings.HasPrefix(m, "chat_") || strings.HasPrefix(m, "tab_") {
		return false
	}
	return strings.HasPrefix(m, "gemini-") ||
		strings.HasPrefix(m, "claude-") ||
		strings.HasPrefix(m, "gpt-") ||
		strings.HasPrefix(m, "deepseek-")
}

// --- helpers ---

func innerResponse(geminiResp map[string]any) map[string]any {
	if inner, ok := geminiResp["response"].(map[string]any); ok {
		return inner
	}
	return geminiResp
}

func strOr(m map[string]any, key, def string) string {
	if v, ok := m[key].(string); ok {
		return v
	}
	return def
}

func int64Or(m map[string]any, key string, def int64) int64 {
	switch v := m[key].(type) {
	case float64:
		return int64(v)
	case int64:
		return v
	case int:
		return int64(v)
	case json.Number:
		if n, err := v.Int64(); err == nil {
			return n
		}
	}
	return def
}

func valueOr(m map[string]any, key, def string) any {
	if v, ok := m[key]; ok {
		return v
	}
	return def
}

func orDefault(m map[string]any, key string, def any) any {
	if v, ok := m[key]; ok {
		return v
	}
	return def
}

// toInt64 safely converts any JSON number type (float64, int64, json.Number)
// to int64, returning def on failure.
func toInt64(v any, def int64) int64 {
	switch n := v.(type) {
	case float64:
		return int64(n)
	case int64:
		return n
	case int:
		return int64(n)
	case json.Number:
		if i, err := n.Int64(); err == nil {
			return i
		}
	}
	return def
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

func sortStrings(s []string) {
	sort.Strings(s)
}
func compactUUID() string {
	return strings.ReplaceAll(uuid.NewString(), "-", "")
}

func nowUnix() int64 {
	return time.Now().Unix()
}
