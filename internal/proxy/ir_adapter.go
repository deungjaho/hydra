package proxy

import (
	"encoding/json"
	"strings"

	"github.com/deungjaho/hydra/internal/ir"
)

// irEncodeGeminiRequest is the IR-based replacement for TransformRequest
// and AnthropicTransformRequest. It decodes an inbound protocol request
// (already parsed as map[string]any) into IR, applies Hydra's model
// mapping and generation-config adjustments, then encodes to the AGY
// v1internal envelope.
//
// protocol is "openai", "anthropic", or "responses".
func irEncodeGeminiRequest(req map[string]any, protocol, projectID, sessionID string, requestN uint64) map[string]any {
	var irReq *ir.Request
	switch protocol {
	case "openai":
		irReq = ir.DecodeOpenAIChat(req)
	case "anthropic":
		irReq = ir.DecodeAnthropic(req)
	case "responses":
		irReq = ir.DecodeResponses(req)
	default:
		irReq = ir.DecodeOpenAIChat(req)
	}

	originalModel := strOr(req, "model", "gemini-2.5-flash")
	mappedModel := MapModel(originalModel)
	irReq.Model = mappedModel

	// Cap maxOutputTokens to the model's upstream limit.
	cap := maxOutputTokensCap(mappedModel)
	if irReq.MaxTokens == 0 {
		irReq.MaxTokens = defaultMaxOutputTokens
	}
	if irReq.MaxTokens > cap {
		irReq.MaxTokens = cap
	}

	// Apply thinking config for thinking models.
	if isThinkingModel(mappedModel) {
		budget := thinkingBudgetFor(mappedModel, reasoningEffortFromIR(irReq))
		if budget >= irReq.MaxTokens {
			if irReq.MaxTokens > 384 {
				budget = irReq.MaxTokens - 256
			} else if irReq.MaxTokens > 256 {
				budget = 128
			} else {
				budget = irReq.MaxTokens / 2
				if budget < 1 {
					budget = 1
				}
			}
		}
		if irReq.Reasoning == nil {
			irReq.Reasoning = &ir.Reasoning{}
		}
		irReq.Reasoning.BudgetTokens = budget
	}

	return ir.EncodeGeminiRequest(irReq, projectID, sessionID, requestN)
}

// reasoningEffortFromIR extracts the reasoning effort string from IR.
func reasoningEffortFromIR(req *ir.Request) string {
	if req.Reasoning != nil {
		return req.Reasoning.Effort
	}
	return ""
}

// irDecodeGeminiResponse is the IR-based replacement for TransformResponse,
// AnthropicTransformResponse, and transformResponsesResponse. It decodes
// a Gemini/AGY response into IR, then encodes to the client protocol.
//
// protocol is "openai", "anthropic", or "responses".
func irDecodeGeminiResponse(geminiResp map[string]any, protocol, model string) map[string]any {
	irResp := ir.DecodeGeminiResponse(geminiResp, model)
	switch protocol {
	case "openai":
		return ir.EncodeOpenAIChat(irResp)
	case "anthropic":
		return ir.EncodeAnthropic(irResp)
	case "responses":
		return ir.EncodeResponses(irResp)
	default:
		return ir.EncodeOpenAIChat(irResp)
	}
}

// irExtractUsage extracts token usage from a Gemini response for request
// logging, regardless of which client protocol is used.
func irExtractUsage(geminiResp map[string]any) (prompt, completion, cached, thought int64) {
	irResp := ir.DecodeGeminiResponse(geminiResp, "")
	return irResp.Usage.Prompt, irResp.Usage.Completion, irResp.Usage.Cached, irResp.Usage.Thought
}

// irStreamGeminiChunk decodes a Gemini SSE chunk into IR stream events.
// This is a thin wrapper around ir.DecodeGeminiStreamChunk.
func irStreamGeminiChunk(chunk map[string]any) []ir.StreamEvent {
	return ir.DecodeGeminiStreamChunk(chunk)
}

// irEncodeOpenAIStreamChunk wraps ir.EncodeOpenAIChatStreamChunk.
func irEncodeOpenAIStreamChunk(ev ir.StreamEvent, chatID string, created int64, model string, isFirst bool) string {
	return ir.EncodeOpenAIChatStreamChunk(ev, chatID, created, model, isFirst)
}

// irParseGeminiSSELine parses one SSE data line into a map[string]any.
// Returns nil for non-data lines or parse errors.
func irParseGeminiSSELine(line string) map[string]any {
	if !strings.HasPrefix(line, "data: ") {
		return nil
	}
	data := strings.TrimSpace(line[len("data: "):])
	if data == "" {
		return nil
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(data), &m); err != nil {
		return nil
	}
	return m
}
