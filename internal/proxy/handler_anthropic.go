package proxy

import (
	"encoding/json"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/deungjaho/hydra/internal/account"
)

func (s *ProxyServer) handleAnthropicMessages(w http.ResponseWriter, r *http.Request) {
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

	var anthropicReq map[string]any
	if err := json.Unmarshal(body, &anthropicReq); err != nil {
		http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}

	stream, _ := anthropicReq["stream"].(bool)
	originalModel := strOr(anthropicReq, "model", "gemini-2.5-flash")
	mappedModel := MapModel(originalModel)
	clientIP := clientIPFrom(r)

	// API key provider passthrough (DeepSeek, Zhipu, etc.).
	if providers := s.resolveAPIProviders(originalModel); len(providers) > 0 {
		s.forwardAnthropicMessages(w, r, anthropicReq, providers, originalModel, apiKeyID, clientIP)
		return
	}

	accounts, err := account.ListAccounts(s.State.DB)
	if err != nil {
		log.Printf("db list_accounts failed: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	var sessionID string
	if md, ok := anthropicReq["metadata"].(map[string]any); ok {
		sessionID, _ = md["user_id"].(string)
	}
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
				anthropicReq, "anthropic", acc.ProjectID, sessionUUID, requestN)
			upstreamBody["model"] = effectiveModel
			return json.Marshal(upstreamBody)
		},
		handleSuccess: func(w http.ResponseWriter, resp *http.Response, acc *account.Account) {
			// Streaming success.
			s.streamAnthropicSSEIR(w, resp.Body, originalModel,
				acc.ID, apiKeyID, clientIP)
			resp.Body.Close()
		},
		handleSuccessNonStream: func(w http.ResponseWriter, resp *http.Response, acc *account.Account) {
			geminiResp := parseGeminiSuccessBody(w, resp, func(status int, msg string) {
				http.Error(w, msg, status)
			})
			if geminiResp == nil {
				return
			}
			anthropicResp := irDecodeGeminiResponse(
				geminiResp, "anthropic", originalModel)
			usage, _ := anthropicResp["usage"].(map[string]any)
			promptTokens := int64Or(usage, "input_tokens", 0)
			completionTokens := int64Or(usage, "output_tokens", 0)
			cachedTokens := int64Or(usage, "cache_read_input_tokens", 0)
			s.failoverLogSuccess(acc, originalModel,
				promptTokens, completionTokens, cachedTokens, 0,
				clientIP, apiKeyID)
			writeJSON(w, http.StatusOK, anthropicResp)
		},
	})
}

func (s *ProxyServer) handleAnthropicCountTokens(w http.ResponseWriter, r *http.Request) {
	apiKey, ok := s.checkAuthFull(r)
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	apiKeyID := keyIDPtr(apiKey)

	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "read body: "+err.Error(), http.StatusBadRequest)
		return
	}

	var anthropicReq map[string]any
	if err := json.Unmarshal(body, &anthropicReq); err != nil {
		http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}

	originalModel := strOr(anthropicReq, "model", "gemini-2.5-flash")
	mappedModel := MapModel(originalModel)

	accounts, err := account.ListAccounts(s.State.DB)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	var sessionID string
	if md, ok := anthropicReq["metadata"].(map[string]any); ok {
		sessionID, _ = md["user_id"].(string)
	}
	schedMode, noSticky := resolveScheduling(s, apiKey, r)

	acc := SelectAccount(
		accounts,
		s.State.RateLimiter,
		s.State.Sticky,
		schedMode,
		mappedModel,
		sessionID,
		s.Config.QuotaProtection.Enabled,
		s.Config.Scheduling.StickySessions,
		noSticky,
		s.State.Concurrency,
		int32(s.Config.Scheduling.QuotaWarnPercentage),
	)
	if acc == nil {
		http.Error(w, "no available accounts", http.StatusServiceUnavailable)
		return
	}

	sessionUUID := strings.ReplaceAll(uuid.NewString(), "-", "")
	requestN := s.State.NextRequestN()

	accessToken, ok := s.ensureFreshToken(acc, mappedModel, originalModel, "", nil, w)
	if !ok {
		return
	}

	upstreamBody := irEncodeGeminiRequest(anthropicReq, "anthropic", acc.ProjectID, sessionUUID, requestN)
	bodyBytes, _ := json.Marshal(upstreamBody)
	resp, err := SendRequest(s.HTTP, accessToken, acc.ProjectID, bodyBytes, false, acc.MachineID)
	if err != nil {
		http.Error(w, "upstream failed", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		bodyText, _ := io.ReadAll(resp.Body)
		w.WriteHeader(resp.StatusCode)
		_, _ = w.Write(bodyText)
		return
	}

	bodyBytes2, err := io.ReadAll(resp.Body)
	if err != nil {
		http.Error(w, "read body: "+err.Error(), http.StatusBadGateway)
		return
	}
	var geminiResp map[string]any
	if err := json.Unmarshal(bodyBytes2, &geminiResp); err != nil {
		http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadGateway)
		return
	}

	inner := innerResponse(geminiResp)
	usage, _ := inner["usageMetadata"].(map[string]any)
	promptTokens := int64Or(usage, "promptTokenCount", 0)
	logErr(account.LogRequest(s.State.DB, account.LogRequestParams{
		AccountID:    &acc.ID,
		Model:        &originalModel,
		PromptTokens: &promptTokens,
		Status:       200,
		APIKeyID:     apiKeyID,
	}))
	writeJSON(w, http.StatusOK, map[string]any{"input_tokens": promptTokens})
}
