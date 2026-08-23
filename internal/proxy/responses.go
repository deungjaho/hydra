package proxy

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// responsesRequestToOpenAI converts a Responses API request body into a
// Chat Completions format that TransformRequest can consume.
func responsesRequestToOpenAI(req map[string]any) map[string]any {
	out := make(map[string]any, len(req)+4)

	// Copy pass-through fields.
	for _, k := range []string{"model", "stream", "temperature", "top_p",
		"parallel_tool_calls", "tool_choice", "user", "metadata"} {
		if v, ok := req[k]; ok {
			out[k] = v
		}
	}

	// max_output_tokens → max_tokens
	if v, ok := req["max_output_tokens"]; ok {
		out["max_tokens"] = v
	}

	// text.format → response_format
	if text, ok := req["text"].(map[string]any); ok {
		if format, ok := text["format"]; ok {
			out["response_format"] = format
		}
	}

	// reasoning.effort → reasoning_effort (TransformRequest reads this)
	if reasoning, ok := req["reasoning"].(map[string]any); ok {
		if effort, ok := reasoning["effort"]; ok {
			out["reasoning_effort"] = effort
		}
	}

	// instructions → system message (prepended to messages)
	var messages []any
	if instr, ok := req["instructions"].(string); ok && instr != "" {
		messages = append(messages, map[string]any{
			"role":    "system",
			"content": instr,
		})
	}

	// input → messages
	input := req["input"]
	switch v := input.(type) {
	case string:
		messages = append(messages, map[string]any{
			"role":    "user",
			"content": v,
		})
	case []any:
		for _, item := range v {
			msg := responsesItemToMessage(item)
			if msg != nil {
				messages = append(messages, msg)
			}
		}
	}

	if len(messages) > 0 {
		out["messages"] = messages
	}

	// tools: Responses API uses {type:"function", name:"...", ...} directly
	// without the nested {function:{...}} wrapper. Normalize.
	if tools, ok := req["tools"].([]any); ok {
		out["tools"] = normalizeResponsesTools(tools)
	}

	return out
}

// responsesItemToMessage converts a Responses API input item to a
// Chat Completions message.
func responsesItemToMessage(item any) map[string]any {
	m, ok := item.(map[string]any)
	if !ok {
		return nil
	}

	itemType, _ := m["type"].(string)

	switch itemType {
	case "message", "":
		// Message item: {type:"message", role:"user|assistant|system|developer", content:[...]}
		role, _ := m["role"].(string)
		if role == "" {
			role = "user"
		}
		content := responsesContentToOpenAI(m["content"])
		return map[string]any{
			"role":    role,
			"content": content,
		}

	case "function_call", "custom_tool_call":
		// Tool call from assistant: convert to assistant message with tool_calls
		name, _ := m["name"].(string)
		args, _ := m["arguments"].(string)
		callID, _ := m["call_id"].(string)
		if callID == "" {
			callID, _ = m["id"].(string)
		}
		return map[string]any{
			"role": "assistant",
			"tool_calls": []any{
				map[string]any{
					"id":   callID,
					"type": "function",
					"function": map[string]any{
						"name":      name,
						"arguments": args,
					},
				},
			},
		}

	case "function_call_output", "custom_tool_call_output":
		// Tool result: {type:"function_call_output", call_id:"...", output:"..."}
		callID, _ := m["call_id"].(string)
		if callID == "" {
			callID, _ = m["id"].(string)
		}
		output := m["output"]
		var content string
		switch v := output.(type) {
		case string:
			content = v
		default:
			b, _ := json.Marshal(output)
			content = string(b)
		}
		return map[string]any{
			"role":         "tool",
			"tool_call_id": callID,
			"content":      content,
		}

	case "reasoning":
		// Reasoning item: {type:"reasoning", content:[{type:"reasoning_text",text:"..."}]}
		// Convert to assistant message with reasoning_content.
		var reasoningParts []string
		if content, ok := m["content"].([]any); ok {
			for _, c := range content {
				cm, ok := c.(map[string]any)
				if !ok {
					continue
				}
				if t, _ := cm["type"].(string); t == "reasoning_text" || t == "input_text" || t == "text" {
					if text, ok := cm["text"].(string); ok {
						reasoningParts = append(reasoningParts, text)
					}
				}
			}
		}
		if len(reasoningParts) > 0 {
			return map[string]any{
				"role":              "assistant",
				"reasoning_content": strings.Join(reasoningParts, "\n"),
			}
		}
		return nil

	case "web_search_call", "image_generation_call", "compaction",
		"compaction_trigger", "context_compaction", "tool_search_call",
		"tool_search_output", "additional_tools", "agent_message",
		"local_shell_call":
		// These are Codex-internal items that don't map to chat messages.
		// Skip them — they're not needed for upstream generation.
		return nil

	default:
		// Unknown item type — try to extract role+content as a message.
		role, _ := m["role"].(string)
		if role == "" {
			return nil
		}
		content := responsesContentToOpenAI(m["content"])
		return map[string]any{
			"role":    role,
			"content": content,
		}
	}
}

// responsesContentToOpenAI converts Responses API content (string or array
// of typed parts) to Chat Completions content format.
func responsesContentToOpenAI(content any) any {
	switch v := content.(type) {
	case string:
		return v
	case []any:
		var parts []any
		for _, part := range v {
			pm, ok := part.(map[string]any)
			if !ok {
				continue
			}
			ptype, _ := pm["type"].(string)
			switch ptype {
			case "input_text", "output_text", "text":
				text, _ := pm["text"].(string)
				parts = append(parts, map[string]any{
					"type": "text",
					"text": text,
				})
			case "input_image":
				url, _ := pm["image_url"].(string)
				parts = append(parts, map[string]any{
					"type": "image_url",
					"image_url": map[string]any{
						"url": url,
					},
				})
			}
		}
		if len(parts) > 0 {
			return parts
		}
		return ""
	default:
		return ""
	}
}

// normalizeResponsesTools converts Responses API tool definitions to
// Chat Completions format. Responses API uses {type:"function", name:"...", ...}
// while Chat Completions uses {type:"function", function:{name:"...", ...}}.
func normalizeResponsesTools(tools []any) []any {
	out := make([]any, 0, len(tools))
	for _, tool := range tools {
		tm, ok := tool.(map[string]any)
		if !ok {
			continue
		}
		ttype, _ := tm["type"].(string)
		if ttype == "function" {
			// Already has nested function? Keep as-is.
			if _, ok := tm["function"].(map[string]any); ok {
				out = append(out, tm)
				continue
			}
			// Responses API flat format → wrap in function.
			fn := make(map[string]any)
			for _, k := range []string{"name", "description", "parameters",
				"strict"} {
				if v, ok := tm[k]; ok {
					fn[k] = v
				}
			}
			out = append(out, map[string]any{
				"type":     "function",
				"function": fn,
			})
		} else {
			out = append(out, tm)
		}
	}
	return out
}

// Legacy response transformation functions (transformResponsesResponse,
// responsesPartsToOutputItems, responsesUsageFromAGY, responsesFinishReason,
// responsesIncompleteReason) have been removed. The IR layer (internal/ir)
// now handles non-streaming response translation for the Antigravity/AGY
// path. See ir_adapter.go. The streaming state machine and utility
// functions below are still active.
// responsesStopReason maps AGY finishReason to a stop reason string
// that Codex expects in response.completed events.
func responsesStopReason(fr string) string {
	switch strings.ToUpper(fr) {
	case "STOP":
		return "stop"
	case "MAX_TOKENS":
		return "max_output_tokens"
	case "SAFETY", "RECITATION":
		return "content_filter"
	default:
		return "stop"
	}
}

// responsesErrorBody creates a Responses API error response.
func responsesErrorBody(status int, message string) map[string]any {
	return map[string]any{
		"id":         "resp_" + compactUUID(),
		"object":     "response",
		"created_at": time.Now().Unix(),
		"status":     "failed",
		"error": map[string]any{
			"code":    fmt.Sprintf("http_%d", status),
			"message": message,
		},
		"output": []any{},
		"usage": map[string]any{
			"input_tokens":  0,
			"output_tokens": 0,
			"total_tokens":  0,
		},
	}
}

// responsesStreamState tracks the state of a Responses API SSE stream.
type responsesStreamState struct {
	respID  string
	model   string
	created int64
	seq     int64 // sequence number for events

	// Item tracking
	messageItemID string // current message item ID
	reasoningItemID string // current reasoning item ID
	outputIndex   int    // current output item index
	contentIndex  int    // current content part index

	// Block tracking — which type of content we're currently streaming
	currentType string // "text", "reasoning", "function_call"

	// Whether we've sent the initial events
	createdSent    bool
	messageAdded   bool
	reasoningAdded bool

	// Accumulated content for done events
	accumulatedText   string // accumulated text deltas
	accumulatedReason string // accumulated reasoning summary
	outputItems       []any  // accumulated output items for completed event

	// Usage
	totalPrompt, totalCompletion, totalCached, totalThought int64

	// Finish reason
	finishReason  string
	completedSent bool
}

func newResponsesStreamState(respID, model string, created int64) *responsesStreamState {
	return &responsesStreamState{
		respID:      respID,
		model:       model,
		created:     created,
		outputIndex: -1,
	}
}

func (st *responsesStreamState) nextSeq() int64 {
	st.seq++
	return st.seq
}

// startMessage ensures response.created and the message output item exist.
func (st *responsesStreamState) startMessage() []string {
	var events []string
	if !st.createdSent {
		events = append(events, st.createdEvent())
		st.createdSent = true
	}
	if !st.messageAdded {
		// Close reasoning item if open.
		if st.reasoningAdded {
			events = append(events, st.reasoningSummaryTextDoneEvent())
			events = append(events, st.reasoningSummaryPartDoneEvent())
			events = append(events, st.reasoningItemDoneEvent())
			st.reasoningAdded = false
		}
		st.messageItemID = "msg_" + compactUUID()
		st.outputIndex++
		st.contentIndex = 0
		events = append(events, st.outputItemAddedEvent("message"))
		events = append(events, st.contentPartAddedEvent("output_text"))
		st.messageAdded = true
	}
	return events
}

// startReasoning ensures response.created and a reasoning output item exist.
func (st *responsesStreamState) startReasoning() []string {
	var events []string
	if !st.createdSent {
		events = append(events, st.createdEvent())
		st.createdSent = true
	}
	if !st.reasoningAdded {
		// Close message item if open.
		if st.messageAdded {
			events = append(events, st.outputTextDoneEvent())
			events = append(events, st.contentPartDoneEvent())
			events = append(events, st.outputItemDoneEvent())
			st.messageAdded = false
		}
		st.reasoningItemID = "rs_" + compactUUID()
		st.outputIndex++
		events = append(events, st.reasoningItemAddedEvent())
		events = append(events, st.reasoningSummaryPartAddedEvent())
		st.reasoningAdded = true
	}
	return events
}

func (st *responsesStreamState) createdEvent() string {
	ev := map[string]any{
		"type":            "response.created",
		"sequence_number": st.nextSeq(),
		"response": map[string]any{
			"id":         st.respID,
			"object":     "response",
			"created_at": st.created,
			"model":      st.model,
			"status":     "in_progress",
			"output":     []any{},
		},
	}
	return sseEvent("response.created", ev)
}

func (st *responsesStreamState) outputItemAddedEvent(itemType string) string {
	ev := map[string]any{
		"type":            "response.output_item.added",
		"sequence_number": st.nextSeq(),
		"output_index":    st.outputIndex,
		"item": map[string]any{
			"id":      st.messageItemID,
			"type":    itemType,
			"status":  "in_progress",
			"role":    "assistant",
			"content": []any{},
		},
	}
	return sseEvent("response.output_item.added", ev)
}

func (st *responsesStreamState) contentPartAddedEvent(partType string) string {
	ev := map[string]any{
		"type":            "response.content_part.added",
		"sequence_number": st.nextSeq(),
		"item_id":         st.messageItemID,
		"output_index":    st.outputIndex,
		"content_index":   st.contentIndex,
		"part": map[string]any{
			"type": partType,
			"text": "",
		},
	}
	return sseEvent("response.content_part.added", ev)
}

func (st *responsesStreamState) textDeltaEvent(delta string) string {
	ev := map[string]any{
		"type":            "response.output_text.delta",
		"sequence_number": st.nextSeq(),
		"item_id":         st.messageItemID,
		"output_index":    st.outputIndex,
		"content_index":   st.contentIndex,
		"delta":           delta,
	}
	return sseEvent("response.output_text.delta", ev)
}

func (st *responsesStreamState) reasoningDeltaEvent(delta string) string {
	ev := map[string]any{
		"type":            "response.reasoning_summary_text.delta",
		"sequence_number": st.nextSeq(),
		"item_id":         st.reasoningItemID,
		"output_index":    st.outputIndex,
		"summary_index":   0,
		"delta":           delta,
	}
	return sseEvent("response.reasoning_summary_text.delta", ev)
}

func (st *responsesStreamState) reasoningItemAddedEvent() string {
	ev := map[string]any{
		"type":            "response.output_item.added",
		"sequence_number": st.nextSeq(),
		"output_index":    st.outputIndex,
		"item": map[string]any{
			"id":     st.reasoningItemID,
			"type":   "reasoning",
			"status": "in_progress",
		},
	}
	return sseEvent("response.output_item.added", ev)
}

func (st *responsesStreamState) reasoningSummaryPartAddedEvent() string {
	ev := map[string]any{
		"type":            "response.reasoning_summary_part.added",
		"sequence_number": st.nextSeq(),
		"item_id":         st.reasoningItemID,
		"output_index":    st.outputIndex,
		"summary_index":   0,
		"part": map[string]any{
			"type": "summary_text",
			"text": "",
		},
	}
	return sseEvent("response.reasoning_summary_part.added", ev)
}

func (st *responsesStreamState) reasoningSummaryTextDoneEvent() string {
	ev := map[string]any{
		"type":            "response.reasoning_summary_text.done",
		"sequence_number": st.nextSeq(),
		"item_id":         st.reasoningItemID,
		"output_index":    st.outputIndex,
		"summary_index":   0,
		"text":            st.accumulatedReason,
	}
	return sseEvent("response.reasoning_summary_text.done", ev)
}

func (st *responsesStreamState) reasoningSummaryPartDoneEvent() string {
	ev := map[string]any{
		"type":            "response.reasoning_summary_part.done",
		"sequence_number": st.nextSeq(),
		"item_id":         st.reasoningItemID,
		"output_index":    st.outputIndex,
		"summary_index":   0,
		"part": map[string]any{
			"type": "summary_text",
			"text": st.accumulatedReason,
		},
	}
	return sseEvent("response.reasoning_summary_part.done", ev)
}

func (st *responsesStreamState) reasoningItemDoneEvent() string {
	ev := map[string]any{
		"type":            "response.output_item.done",
		"sequence_number": st.nextSeq(),
		"output_index":    st.outputIndex,
		"item": map[string]any{
			"id":     st.reasoningItemID,
			"type":   "reasoning",
			"status": "completed",
			"summary": []any{
				map[string]any{
					"type": "summary_text",
					"text": st.accumulatedReason,
				},
			},
		},
	}
	return sseEvent("response.output_item.done", ev)
}

func (st *responsesStreamState) functionCallArgsDeltaEvent(callID, name, delta string) string {
	ev := map[string]any{
		"type":            "response.function_call_arguments.delta",
		"sequence_number": st.nextSeq(),
		"item_id":         callID,
		"output_index":    st.outputIndex,
		"call_id":         callID,
		"name":            name,
		"delta":           delta,
	}
	return sseEvent("response.function_call_arguments.delta", ev)
}

func (st *responsesStreamState) functionCallDoneEvent(callID, name, args string) string {
	ev := map[string]any{
		"type":            "response.function_call_arguments.done",
		"sequence_number": st.nextSeq(),
		"item_id":         callID,
		"output_index":    st.outputIndex,
		"call_id":         callID,
		"name":            name,
		"arguments":       args,
	}
	return sseEvent("response.function_call_arguments.done", ev)
}

func (st *responsesStreamState) functionCallItemAddedEvent(callID, name string) string {
	item := map[string]any{
		"id":      callID,
		"type":    "function_call",
		"status":  "in_progress",
		"call_id": callID,
		"name":    name,
	}
	ev := map[string]any{
		"type":            "response.output_item.added",
		"sequence_number": st.nextSeq(),
		"output_index":    st.outputIndex,
		"item":            item,
	}
	return sseEvent("response.output_item.added", ev)
}

func (st *responsesStreamState) functionCallItemDoneEvent(callID, name, args string) string {
	item := map[string]any{
		"id":        callID,
		"type":      "function_call",
		"status":    "completed",
		"call_id":   callID,
		"name":      name,
		"arguments": args,
	}
	st.outputItems = append(st.outputItems, item)
	ev := map[string]any{
		"type":            "response.output_item.done",
		"sequence_number": st.nextSeq(),
		"output_index":    st.outputIndex,
		"item":            item,
	}
	return sseEvent("response.output_item.done", ev)
}

func (st *responsesStreamState) outputItemDoneEvent() string {
	item := map[string]any{
		"id":     st.messageItemID,
		"type":   "message",
		"status": "completed",
		"role":   "assistant",
		"content": []any{
			map[string]any{
				"type":        "output_text",
				"text":        st.accumulatedText,
				"annotations": []any{},
			},
		},
	}
	// Track for completed event
	st.outputItems = append(st.outputItems, item)
	ev := map[string]any{
		"type":            "response.output_item.done",
		"sequence_number": st.nextSeq(),
		"output_index":    st.outputIndex,
		"item":            item,
	}
	return sseEvent("response.output_item.done", ev)
}

func (st *responsesStreamState) outputTextDoneEvent() string {
	ev := map[string]any{
		"type":            "response.output_text.done",
		"sequence_number": st.nextSeq(),
		"item_id":         st.messageItemID,
		"output_index":    st.outputIndex,
		"content_index":   st.contentIndex,
		"text":            st.accumulatedText,
	}
	return sseEvent("response.output_text.done", ev)
}

func (st *responsesStreamState) contentPartDoneEvent() string {
	ev := map[string]any{
		"type":            "response.content_part.done",
		"sequence_number": st.nextSeq(),
		"item_id":         st.messageItemID,
		"output_index":    st.outputIndex,
		"content_index":   st.contentIndex,
		"part": map[string]any{
			"type": "output_text",
			"text": st.accumulatedText,
		},
	}
	return sseEvent("response.content_part.done", ev)
}

func (st *responsesStreamState) completedEvent() string {
	status := "completed"
	incompleteDetails := any(nil)
	if responsesStopReason(st.finishReason) == "max_output_tokens" {
		status = "incomplete"
		incompleteDetails = map[string]any{"reason": "max_output_tokens"}
	}

	ev := map[string]any{
		"type":            "response.completed",
		"sequence_number": st.nextSeq(),
		"response": map[string]any{
			"id":                 st.respID,
			"object":             "response",
			"created_at":         st.created,
			"model":              st.model,
			"status":             status,
			"error":              nil,
			"incomplete_details": incompleteDetails,
			"output":             st.outputItems,
			"usage": map[string]any{
				"input_tokens":  st.totalPrompt,
				"output_tokens": st.totalCompletion,
				"total_tokens":  st.totalPrompt + st.totalCompletion,
				"input_tokens_details": map[string]any{
					"cached_tokens": st.totalCached,
				},
				"output_tokens_details": map[string]any{
					"reasoning_tokens": st.totalThought,
				},
			},
		},
	}
	return sseEvent("response.completed", ev)
}

// processGeminiChunk processes one AGY SSE chunk and returns Responses API
// SSE events to write.
func (st *responsesStreamState) processGeminiChunk(inner map[string]any) string {
	var events []string

	// Extract usage.
	if usage, ok := inner["usageMetadata"].(map[string]any); ok {
		if v := int64Or(usage, "promptTokenCount", 0); v != 0 {
			st.totalPrompt = v
		}
		if v := int64Or(usage, "candidatesTokenCount", 0); v != 0 {
			st.totalCompletion = v
		}
		if v := int64Or(usage, "cachedContentTokenCount", 0); v != 0 {
			st.totalCached = v
		}
		if v := int64Or(usage, "thoughtsTokenCount", 0); v != 0 {
			st.totalThought = v
		}
	}

	// Extract parts.
	candidates, _ := inner["candidates"].([]any)
	if len(candidates) == 0 {
		// Maybe just a finishReason.
		return st.checkFinishReason(inner)
	}
	candidate, _ := candidates[0].(map[string]any)
	if candidate == nil {
		return ""
	}

	// Check finish reason.
	if fr, ok := candidate["finishReason"].(string); ok && fr != "" {
		st.finishReason = fr
	}

	contentMap, _ := candidate["content"].(map[string]any)
	var parts []any
	if contentMap != nil {
		parts, _ = contentMap["parts"].([]any)
	}

	for _, partAny := range parts {
		part, ok := partAny.(map[string]any)
		if !ok {
			continue
		}

		// Thought part → reasoning summary delta
		if thought, _ := part["thought"].(bool); thought {
			if text, ok := part["text"].(string); ok && text != "" {
				events = append(events, st.startReasoning()...)
				st.currentType = "reasoning"
				st.accumulatedReason += text
				events = append(events, st.reasoningDeltaEvent(text))
			}
			continue
		}

		// Text part → output_text delta
		if text, ok := part["text"].(string); ok && text != "" {
			events = append(events, st.startMessage()...)
			if st.currentType == "reasoning" {
				st.currentType = "text"
			}
			st.accumulatedText += text
			events = append(events, st.textDeltaEvent(text))
		}

		// Function call → function_call item
		if fc, ok := part["functionCall"].(map[string]any); ok {
			name, _ := fc["name"].(string)
			args, _ := json.Marshal(fc["args"])
			callID, _ := fc["id"].(string)
			if callID == "" {
				callID = "fc_" + compactUUID()
			}
			// Flush reasoning item if open.
			if st.reasoningAdded {
				events = append(events, st.reasoningSummaryTextDoneEvent())
				events = append(events, st.reasoningSummaryPartDoneEvent())
				events = append(events, st.reasoningItemDoneEvent())
				st.reasoningAdded = false
			}
			// Flush message item if open.
			if st.messageAdded {
				events = append(events, st.outputTextDoneEvent())
				events = append(events, st.contentPartDoneEvent())
				events = append(events, st.outputItemDoneEvent())
				st.messageAdded = false
			}
			if !st.createdSent {
				events = append(events, st.createdEvent())
				st.createdSent = true
			}
			st.outputIndex++
			st.messageItemID = callID
			st.currentType = "function_call"
			// Emit output_item.added with type "function_call"
			events = append(events, st.functionCallItemAddedEvent(callID, name))
			// Emit arguments delta and done
			events = append(events, st.functionCallArgsDeltaEvent(callID, name, string(args)))
			events = append(events, st.functionCallDoneEvent(callID, name, string(args)))
			// Emit output_item.done with the function call item
			events = append(events, st.functionCallItemDoneEvent(callID, name, string(args)))
		}
	}

	// Check finish reason after parts.
	if st.finishReason != "" {
		events = append(events, st.checkFinishReason(inner))
	}

	return strings.Join(events, "")
}

func (st *responsesStreamState) checkFinishReason(inner map[string]any) string {
	if st.finishReason == "" {
		// Check if finishReason is in the inner response directly.
		if candidates, ok := inner["candidates"].([]any); ok && len(candidates) > 0 {
			if cand, ok := candidates[0].(map[string]any); ok {
				if fr, ok := cand["finishReason"].(string); ok {
					st.finishReason = fr
				}
			}
		}
	}
	if st.finishReason == "" {
		return ""
	}
	if st.completedSent {
		return ""
	}
	var events []string
	// Close any open reasoning item with proper lifecycle events.
	if st.reasoningAdded {
		events = append(events, st.reasoningSummaryTextDoneEvent())
		events = append(events, st.reasoningSummaryPartDoneEvent())
		events = append(events, st.reasoningItemDoneEvent())
		st.reasoningAdded = false
	}
	// Close any open message item with proper lifecycle events.
	if st.messageAdded {
		events = append(events, st.outputTextDoneEvent())
		events = append(events, st.contentPartDoneEvent())
		events = append(events, st.outputItemDoneEvent())
		st.messageAdded = false
	}
	st.completedSent = true
	events = append(events, st.completedEvent())
	return strings.Join(events, "")
}

// sseEvent formats a typed SSE event.
func sseEvent(eventType string, data map[string]any) string {
	b, _ := json.Marshal(data)
	return "event: " + eventType + "\ndata: " + string(b) + "\n\n"
}
