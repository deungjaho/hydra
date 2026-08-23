package proxy

import (
	"encoding/json"
	"io"
	"log"
	"net/http"
	"time"

	"github.com/deungjaho/hydra/internal/account"
)

func (s *ProxyServer) handleChatCompletions(w http.ResponseWriter, r *http.Request) {
	startTime := time.Now()
	defer func() { s.State.metrics.observeDuration(time.Since(startTime).Seconds()) }()
	apiKey, ok := s.checkAuthFull(r)
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	apiKeyID := keyIDPtr(apiKey)

	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxRequestBody)) // 32MB
	if err != nil {
		http.Error(w, "read body: "+err.Error(), http.StatusBadRequest)
		return
	}

	var openaiReq map[string]any
	if err := json.Unmarshal(body, &openaiReq); err != nil {
		http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}

	stream, _ := openaiReq["stream"].(bool)
	originalModel := strOr(openaiReq, "model", "gemini-2.5-flash")
	mappedModel := MapModel(originalModel)
	clientIP := clientIPFrom(r)

	// API key provider passthrough (DeepSeek, Zhipu, etc.).
	if providers := s.resolveAPIProviders(originalModel); len(providers) > 0 {
		s.forwardChatCompletions(w, r, body, providers, originalModel, apiKeyID, clientIP)
		return
	}

	accounts, err := account.ListAccounts(s.State.DB)
	if err != nil {
		log.Printf("db list_accounts failed: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	sessionID, _ := openaiReq["user"].(string)
	schedMode, noSticky := resolveScheduling(s, apiKey, r)

	s.failoverLoop(w, accounts, failoverConfig{
		originalModel: originalModel,
		mappedModel:   mappedModel,
		sessionID:     sessionID,
		stream:        stream,
		clientIP:      clientIP,
		apiKeyID:      apiKeyID,
		schedMode:     schedMode,
		noSticky:      noSticky,
		writeErr: func(status int, msg string) {
			http.Error(w, msg, status)
		},
		writeRawErr: func(status int, body []byte) {
			w.WriteHeader(status)
			_, _ = w.Write(body)
		},
		buildBody: func(acc *account.Account, sessionUUID string, requestN uint64, effectiveModel string) ([]byte, error) {
			upstreamBody := irEncodeGeminiRequest(
				openaiReq, "openai", acc.ProjectID, sessionUUID, requestN)
			upstreamBody["model"] = effectiveModel
			return json.Marshal(upstreamBody)
		},
		handleSuccess: func(w http.ResponseWriter, resp *http.Response, acc *account.Account) {
			// Streaming success.
			chatID := "chatcmpl-" + compactUUID()
			created := time.Now().Unix()
			s.streamOpenAISSEIR(w, resp.Body, chatID, created,
				originalModel, acc.ID, apiKeyID, clientIP)
			resp.Body.Close()
		},
		handleSuccessNonStream: func(w http.ResponseWriter, resp *http.Response, acc *account.Account) {
			geminiResp := parseGeminiSuccessBody(w, resp, func(status int, msg string) {
				http.Error(w, msg, status)
			})
			if geminiResp == nil {
				return
			}
			openaiResp := irDecodeGeminiResponse(geminiResp, "openai", originalModel)
			usage, _ := openaiResp["usage"].(map[string]any)
			promptTokens := int64Or(usage, "prompt_tokens", 0)
			completionTokens := int64Or(usage, "completion_tokens", 0)
			cachedTokens := int64Or(usage, "cached_tokens", 0)
			thoughtTokens := int64Or(usage, "thought_tokens", 0)
			s.failoverLogSuccess(acc, originalModel,
				promptTokens, completionTokens, cachedTokens, thoughtTokens,
				clientIP, apiKeyID)
			writeJSON(w, http.StatusOK, openaiResp)
		},
	})
}
