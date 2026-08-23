package proxy

import (
	"bufio"
	"encoding/json"
	"io"
	"net/http"

	"github.com/deungjaho/hydra/internal/account"
)

// streamOpenAISSEIR transforms a Gemini SSE byte stream into an OpenAI SSE
// byte stream using the IR pipeline. This is the IR-based replacement for
// streamOpenAISSE.
func (s *ProxyServer) streamOpenAISSEIR(
	w http.ResponseWriter,
	body io.Reader,
	chatID string,
	created int64,
	model string,
	accountID int64,
	apiKeyID *int64,
	clientIP string,
) {
	flusher, _ := w.(http.Flusher)
	setSSEHeaders(w)
	if flusher != nil {
		flusher.Flush()
	}

	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)

	firstChunk := true
	var totalPrompt, totalCompletion, totalCached, totalThought int64

	for scanner.Scan() {
		line := scanner.Text()
		m := irParseGeminiSSELine(line)
		if m == nil {
			continue
		}
		// Extract usage from the raw Gemini chunk before IR decoding,
		// since IR DecodeGeminiStreamChunk may not emit a usage event.
		inner := innerResponse(m)
		if usage, ok := inner["usageMetadata"].(map[string]any); ok {
			if v := int64Or(usage, "promptTokenCount", 0); v != 0 {
				totalPrompt = v
			}
			if v := int64Or(usage, "candidatesTokenCount", 0); v != 0 {
				totalCompletion = v
			}
			if v := int64Or(usage, "cachedContentTokenCount", 0); v != 0 {
				totalCached = v
			}
			if v := int64Or(usage, "thoughtsTokenCount", 0); v != 0 {
				totalThought = v
			}
		}

		events := irStreamGeminiChunk(m)
		for _, ev := range events {
			chunk := irEncodeOpenAIStreamChunk(ev, chatID, created, model, firstChunk)
			if chunk != "" {
				firstChunk = false
				_, _ = io.WriteString(w, chunk)
				if flusher != nil {
					flusher.Flush()
				}
			}
		}
	}

	// Ensure we always emit a [DONE] marker.
	_, _ = io.WriteString(w, "data: [DONE]\n\n")
	if flusher != nil {
		flusher.Flush()
	}

	logErr(account.LogRequest(s.State.DB, account.LogRequestParams{
		AccountID:        &accountID,
		Model:            &model,
		PromptTokens:     &totalPrompt,
		CompletionTokens: &totalCompletion,
		CachedTokens:     &totalCached,
		ThoughtTokens:    &totalThought,
		Status:           200,
		APIKeyID:         apiKeyID,
	}))
}

// streamAnthropicSSEIR transforms a Gemini SSE byte stream into an Anthropic
// SSE byte stream using the IR pipeline. This is the IR-based replacement
// for streamAnthropicSSE.
func (s *ProxyServer) streamAnthropicSSEIR(
	w http.ResponseWriter,
	body io.Reader,
	model string,
	accountID int64,
	apiKeyID *int64,
	clientIP string,
) {
	flusher, _ := w.(http.Flusher)
	setSSEHeaders(w)
	if flusher != nil {
		flusher.Flush()
	}

	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)

	state := NewAnthropicStreamState(model)
	var totalPrompt, totalCompletion, totalCached int64

	for scanner.Scan() {
		line := scanner.Text()
		m := irParseGeminiSSELine(line)
		if m == nil {
			continue
		}
		inner := innerResponse(m)
		if usage, ok := inner["usageMetadata"].(map[string]any); ok {
			if v := int64Or(usage, "promptTokenCount", 0); v != 0 {
				totalPrompt = v
			}
			if v := int64Or(usage, "candidatesTokenCount", 0); v != 0 {
				totalCompletion = v
			}
			if v := int64Or(usage, "cachedContentTokenCount", 0); v != 0 {
				totalCached = v
			}
		}
		for _, out := range state.ProcessChunk(inner) {
			_, _ = io.WriteString(w, out)
		}
		if flusher != nil {
			flusher.Flush()
		}
	}

	if totalPrompt != 0 || totalCompletion != 0 {
		logErr(account.LogRequest(s.State.DB, account.LogRequestParams{
			AccountID:        &accountID,
			Model:            &model,
			PromptTokens:     &totalPrompt,
			CompletionTokens: &totalCompletion,
			CachedTokens:     &totalCached,
			Status:           200,
			ClientIP:         pstrIf(clientIP != "", clientIP),
			APIKeyID:         apiKeyID,
		}))
	}
}

// streamResponsesSSEIR transforms a Gemini SSE byte stream into a Responses
// API SSE byte stream using the existing responsesStreamState (which is
// already well-tested). The IR pipeline for Responses streaming is not yet
// complete enough to replace the state machine.
func (s *ProxyServer) streamResponsesSSEIR(
	w http.ResponseWriter,
	body io.Reader,
	respID string,
	created int64,
	model string,
	accountID int64,
	apiKeyID *int64,
	clientIP string,
) {
	// Delegate to the existing implementation for now.
	s.streamResponsesSSE(w, body, respID, created, model, accountID, apiKeyID, clientIP)
}

// jsonMarshal is a helper to avoid importing json in multiple places.
func jsonMarshal(v any) []byte {
	b, _ := json.Marshal(v)
	return b
}
