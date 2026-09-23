package proxy

import (
	"bufio"
	"encoding/json"
	"io"
	"log"
	"net/http"

	"github.com/deungjaho/hydra/internal/account"
	"github.com/deungjaho/hydra/internal/ir"
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
	stitchFn func(gstate *ir.GeminiStreamState) io.ReadCloser,
) {
	flusher, _ := w.(http.Flusher)
	setSSEHeaders(w)
	if flusher != nil {
		flusher.Flush()
	}

	firstChunk := true
	gstate := &ir.GeminiStreamState{}
	var totalPrompt, totalCompletion, totalCached, totalThought int64

	consumeStream := func(reader io.Reader) {
		scanner := bufio.NewScanner(reader)
		scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
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

			events := gstate.DecodeChunk(m)
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
	}

	consumeStream(body)

	// If the model output only thoughts and no text or tool calls, attempt
	// transparent continuation stitching.
	if gstate.NeedsStitch() && stitchFn != nil {
		log.Printf("stitch: openai %s on account %d ended thinking-only, requesting continuation",
			model, accountID)
		if nextBody := stitchFn(gstate); nextBody != nil {
			consumeStream(nextBody)
			nextBody.Close()
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
// SSE byte stream using the IR pipeline. If upstream Gemini finishes with
// thoughts only and no text or tool calls, it invokes stitchFn to transparently
// request continuation and stream the remaining output into the same SSE response.
func (s *ProxyServer) streamAnthropicSSEIR(
	w http.ResponseWriter,
	body io.Reader,
	model string,
	accountID int64,
	apiKeyID *int64,
	clientIP string,
	stitchFn func(state *AnthropicStreamState) io.ReadCloser,
) {
	flusher, _ := w.(http.Flusher)
	setSSEHeaders(w)
	if flusher != nil {
		flusher.Flush()
	}

	state := NewAnthropicStreamState(model)
	var totalPrompt, totalCompletion, totalCached int64

	consumeStream := func(r io.Reader) {
		scanner := bufio.NewScanner(r)
		scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
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
	}

	consumeStream(body)

	// If the model output only thoughts and no text or tool calls, attempt
	// transparent continuation stitching.
	if state.NeedsStitch() && stitchFn != nil {
		log.Printf("stitch: %s on account %d ended thinking-only, requesting continuation",
			model, accountID)
		if nextBody := stitchFn(state); nextBody != nil {
			consumeStream(nextBody)
			nextBody.Close()
		}
	}

	// Always guarantee terminal SSE events are emitted.
	for _, out := range state.ForceFinish() {
		_, _ = io.WriteString(w, out)
	}
	if flusher != nil {
		flusher.Flush()
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
	stitchFn func(st *responsesStreamState) io.ReadCloser,
) {
	// Delegate to the existing implementation for now.
	s.streamResponsesSSE(w, body, respID, created, model, accountID, apiKeyID, clientIP, stitchFn)
}

// jsonMarshal is a helper to avoid importing json in multiple places.
func jsonMarshal(v any) []byte {
	b, _ := json.Marshal(v)
	return b
}
