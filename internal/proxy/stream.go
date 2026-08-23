package proxy

import (
	"bufio"
	"encoding/json"
	"io"
	"net/http"
	"strings"

	"github.com/deungjaho/hydra/internal/account"
)

// Legacy streaming functions (streamOpenAISSE, buildOpenAISSEChunk,
// streamAnthropicSSE) have been removed. The IR-based streaming
// functions (streamOpenAISSEIR, streamAnthropicSSEIR) in ir_stream.go
// now handle these paths. streamResponsesSSE below is still used by
// streamResponsesSSEIR (which delegates to it).

// streamResponsesSSE transforms a Gemini SSE byte stream into a Responses API
// SSE byte stream and writes it to w.
func (s *ProxyServer) streamResponsesSSE(
	w http.ResponseWriter,
	body io.Reader,
	respID string,
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

	st := newResponsesStreamState(respID, model, created)

	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		data := strings.TrimSpace(line[len("data: "):])
		if data == "" {
			continue
		}
		var geminiJSON map[string]any
		if err := json.Unmarshal([]byte(data), &geminiJSON); err != nil {
			continue
		}
		inner := innerResponse(geminiJSON)
		events := st.processGeminiChunk(inner)
		if events != "" {
			_, _ = io.WriteString(w, events)
			if flusher != nil {
				flusher.Flush()
			}
		}
	}

	// Ensure we send response.completed if not already sent.
	if st.finishReason == "" {
		st.finishReason = "STOP"
	}
	if !st.createdSent {
		// No data chunks at all — send empty completed response.
		_, _ = io.WriteString(w, st.createdEvent())
	}
	if !st.completedSent {
		// Close any open message item with proper lifecycle events.
		if st.messageAdded {
			_, _ = io.WriteString(w, st.outputTextDoneEvent())
			_, _ = io.WriteString(w, st.contentPartDoneEvent())
			_, _ = io.WriteString(w, st.outputItemDoneEvent())
			st.messageAdded = false
		}
		st.completedSent = true
		_, _ = io.WriteString(w, st.completedEvent())
	}
	if flusher != nil {
		flusher.Flush()
	}

	logErr(account.LogRequest(s.State.DB, account.LogRequestParams{
		AccountID:        &accountID,
		Model:            &model,
		PromptTokens:     &st.totalPrompt,
		CompletionTokens: &st.totalCompletion,
		CachedTokens:     &st.totalCached,
		ThoughtTokens:    &st.totalThought,
		Status:           200,
		APIKeyID:         apiKeyID,
	}))
}

// int64Or0 extracts an int64 from a nested map field, returning 0 if absent.
func int64Or0(m map[string]any, key string) int64 {
	if m == nil {
		return 0
	}
	return int64Or(m, key, 0)
}

// responsesUsageThoughtTokens extracts reasoning_tokens from Responses usage.
func responsesUsageThoughtTokens(usage map[string]any) int64 {
	if usage == nil {
		return 0
	}
	if details, ok := usage["output_tokens_details"].(map[string]any); ok {
		return int64Or(details, "reasoning_tokens", 0)
	}
	return 0
}

func setSSEHeaders(w http.ResponseWriter) {
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	h.Set("Connection", "keep-alive")
}
