package proxy

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/deungjaho/hydra/internal/account"
	"github.com/deungjaho/hydra/internal/config"
)

// ProxyServer is the HTTP server that exposes the OpenAI/Anthropic-compatible
// API.
type ProxyServer struct {
	Config *config.AppConfig
	State  *ProxyState
	HTTP   *http.Client // uTLS client for Gemini upstream
	OAuth  *http.Client // standard client for OAuth/quota (no uTLS)
}

// NewProxyServer builds a ProxyServer with a uTLS-backed upstream client
// and a standard HTTP client for OAuth token refresh / quota fetch.
func NewProxyServer(cfg *config.AppConfig, state *ProxyState) *ProxyServer {
	return &ProxyServer{
		Config: cfg,
		State:  state,
		HTTP:   NewUTLSClient(300*time.Second, cfg.Proxy.UpstreamProxy),
		OAuth:  NewHTTPClient(60*time.Second, cfg.Proxy.UpstreamProxy),
	}
}

// Serve starts the HTTP listener and blocks until the server stops.
func (s *ProxyServer) Serve() error {
	addr := fmt.Sprintf("%s:%d", s.Config.Proxy.Bind, s.Config.Proxy.Port)

	// Background loops: token refresher (1-min) + quota refresher (5-min)
	// + health check (configurable interval, default 2-min).
	go s.tokenRefresherLoop()
	go s.quotaRefresherLoop()
	if s.Config.HealthCheck.Enabled {
		go s.healthCheckLoop()
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", s.handleHealthz)
	mux.HandleFunc("/v1/models", s.handleListModels)
	mux.HandleFunc("/v1/chat/completions", s.handleChatCompletions)
	mux.HandleFunc("/v1/messages", s.handleAnthropicMessages)
	mux.HandleFunc("/v1/messages/count_tokens", s.handleAnthropicCountTokens)
	mux.HandleFunc("/metrics", s.handleMetrics)

	srv := &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadHeaderTimeout: 30 * time.Second,
		ReadTimeout:       60 * time.Second,
		WriteTimeout:      600 * time.Second, // long for streaming
		MaxHeaderBytes:    1 << 20,           // 1MB headers
	}
	log.Printf("hydra proxy listening on http://%s", addr)
	return srv.ListenAndServe()
}

func (s *ProxyServer) handleHealthz(w http.ResponseWriter, r *http.Request) {
	_, _ = w.Write([]byte("ok"))
}

func (s *ProxyServer) handleListModels(w http.ResponseWriter, r *http.Request) {
	accounts, _ := account.ListAccounts(s.State.DB)
	models := DynamicModelList(accounts)
	data := make([]any, 0, len(models))
	for _, id := range models {
		data = append(data, map[string]any{
			"id":       id,
			"object":   "model",
			"owned_by": "antigravity",
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"object": "list", "data": data})
}

// isRetryableStatus returns true for status codes that warrant a
// failover to another account (quota exhausted, capacity unavailable).
// 401 is NOT retryable — it's a token refresh issue, not an account
// exhaustion issue, and retrying on another account doesn't help.
func isRetryableStatus(code int) bool {
	return code == 429 || code == 503
}

// ---------------------------------------------------------------------------
// OpenAI ChatCompletions
// ---------------------------------------------------------------------------

func (s *ProxyServer) handleChatCompletions(w http.ResponseWriter, r *http.Request) {
	startTime := time.Now()
	defer func() { s.State.metrics.observeDuration(time.Since(startTime).Seconds()) }()
	apiKeyID, ok := s.checkAuth(r)
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 32<<20)) // 32MB
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

	accounts, err := account.ListAccounts(s.State.DB)
	if err != nil {
		log.Printf("db list_accounts failed: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	sessionID, _ := openaiReq["user"].(string)

	// Failover loop: try accounts until one succeeds or all are exhausted.
	tried := make(map[int64]bool)
	for attempt := 0; ; attempt++ {
		acc := SelectAccount(
			accounts,
			s.State.RateLimiter,
			s.State.Sticky,
			s.Config.Scheduling.Mode,
			mappedModel,
			sessionID,
			s.Config.QuotaProtection.Enabled,
		)
		if acc == nil {
			http.Error(w,
				"no available accounts (all disabled, rate-limited, "+
					"or quota-protected)",
				http.StatusServiceUnavailable)
			return
		}
		if tried[acc.ID] {
			// Already tried this account — all accounts exhausted.
			if attempt > 0 {
				// Return the last error response we got.
				http.Error(w,
					"all accounts exhausted (quota or capacity)",
					http.StatusServiceUnavailable)
			} else {
				http.Error(w,
					"no available accounts (all disabled, rate-limited, "+
						"or quota-protected)",
					http.StatusServiceUnavailable)
			}
			return
		}
		tried[acc.ID] = true
		if sessionID != "" {
			s.State.Sticky.Bind(sessionID, acc.ID)
		}

		sessionUUID := strings.ReplaceAll(uuid.NewString(), "-", "")
		requestN := s.State.NextRequestN()

		accessToken, ok := s.ensureFreshToken(
			acc, mappedModel, originalModel, clientIP, apiKeyID, w)
		if !ok {
			return
		}

		available := acc.AvailableModels()
		effectiveModel := mappedModel
		if len(available) > 0 {
			effectiveModel = ResolveModelForAccount(
				mappedModel, available)
		}

		upstreamBody := TransformRequest(
			openaiReq, acc.ProjectID, sessionUUID, requestN)
		upstreamBody["model"] = effectiveModel

		bodyBytes, _ := json.Marshal(upstreamBody)
		resp, err := SendRequest(
			s.HTTP, accessToken, acc.ProjectID, bodyBytes, stream)
		if err != nil {
			log.Printf("upstream request failed: %v", err)
			_ = account.LogRequest(s.State.DB, account.LogRequestParams{
				HasAccountID: true, AccountID: acc.ID,
				HasModel: true, Model: originalModel,
				Status: 502, HasClientIP: clientIP != "",
				ClientIP: clientIP,
				HasError: true, Error: err.Error(),
				HasAPIKeyID: apiKeyID != nil,
				APIKeyID:   derefInt64(apiKeyID),
			})
			http.Error(w, "upstream request failed",
				http.StatusBadGateway)
			return
		}

		// Retryable: 429 (quota) or 503 (capacity) → failover.
		if isRetryableStatus(resp.StatusCode) {
			bodyText, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			s.State.RateLimiter.SetCooldown(
				acc.ID, mappedModel, 60)
			_ = account.MarkError(s.State.DB, acc.ID,
				string(bodyText), false)
			_ = account.LogRequest(s.State.DB, account.LogRequestParams{
				HasAccountID: true, AccountID: acc.ID,
				HasModel: true, Model: originalModel,
				Status: int64(resp.StatusCode),
				HasClientIP: clientIP != "", ClientIP: clientIP,
				HasError: true, Error: string(bodyText),
				HasAPIKeyID: apiKeyID != nil,
				APIKeyID:   derefInt64(apiKeyID),
			})
			if sessionID != "" {
				s.State.Sticky.Unbind(sessionID)
			}
			log.Printf("failover: account %s got %d for %s, "+
				"trying next", acc.Email, resp.StatusCode,
				originalModel)
			continue
		}

		// 401: token issue — set longer cooldown, no failover.
		if resp.StatusCode == 401 {
			bodyText, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			s.State.RateLimiter.SetCooldown(
				acc.ID, mappedModel, 300)
			_ = account.MarkError(s.State.DB, acc.ID,
				string(bodyText), true)
			_ = account.LogRequest(s.State.DB, account.LogRequestParams{
				HasAccountID: true, AccountID: acc.ID,
				HasModel: true, Model: originalModel,
				Status: int64(resp.StatusCode),
				HasClientIP: clientIP != "", ClientIP: clientIP,
				HasError: true, Error: string(bodyText),
				HasAPIKeyID: apiKeyID != nil,
				APIKeyID:   derefInt64(apiKeyID),
			})
			w.WriteHeader(resp.StatusCode)
			_, _ = w.Write(bodyText)
			return
		}

		// Other non-2xx: return to client, no failover.
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			bodyText, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			_ = account.LogRequest(s.State.DB, account.LogRequestParams{
				HasAccountID: true, AccountID: acc.ID,
				HasModel: true, Model: originalModel,
				Status: int64(resp.StatusCode),
				HasClientIP: clientIP != "", ClientIP: clientIP,
				HasError: true, Error: string(bodyText),
				HasAPIKeyID: apiKeyID != nil,
				APIKeyID:   derefInt64(apiKeyID),
			})
			w.WriteHeader(resp.StatusCode)
			_, _ = w.Write(bodyText)
			return
		}

		// Success path.
		_ = account.MarkUsed(s.State.DB, acc.ID, 0, false)
		s.State.RateLimiter.Clear(acc.ID)

		if stream {
			chatID := "chatcmpl-" + compactUUID()
			created := time.Now().Unix()
			s.streamOpenAISSE(w, resp.Body, chatID, created,
				originalModel, acc.ID, apiKeyID, clientIP)
			resp.Body.Close()
			return
		}

		bodyBytes2, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			http.Error(w, "read upstream body: "+err.Error(),
				http.StatusBadGateway)
			return
		}
		var geminiResp map[string]any
		if err := json.Unmarshal(bodyBytes2, &geminiResp); err != nil {
			http.Error(w, fmt.Sprintf(
				"invalid upstream JSON: %v | body: %s",
				err, string(bodyBytes2)),
				http.StatusBadGateway)
			return
		}

		openaiResp := TransformResponse(geminiResp, originalModel)
		usage, _ := openaiResp["usage"].(map[string]any)
		promptTokens := int64Or(usage, "prompt_tokens", 0)
		completionTokens := int64Or(usage, "completion_tokens", 0)
		cachedTokens := int64Or(usage, "cached_tokens", 0)
		thoughtTokens := int64Or(usage, "thought_tokens", 0)
		cost := ComputeCost(originalModel, promptTokens,
			completionTokens, cachedTokens)
		_ = account.LogRequest(s.State.DB, account.LogRequestParams{
			HasAccountID: true, AccountID: acc.ID,
			HasModel: true, Model: originalModel,
			HasPromptTokens: true, PromptTokens: promptTokens,
			HasCompletion: true, CompletionTokens: completionTokens,
			HasCached: true, CachedTokens: cachedTokens,
			HasThought: true, ThoughtTokens: thoughtTokens,
			Status: 200,
			HasClientIP: clientIP != "", ClientIP: clientIP,
			HasCost: true, CostUSD: cost,
			HasAPIKeyID: apiKeyID != nil,
			APIKeyID:   derefInt64(apiKeyID),
		})
		writeJSON(w, http.StatusOK, openaiResp)
		return
	} // end failover loop
}

// ---------------------------------------------------------------------------
// Anthropic Messages
// ---------------------------------------------------------------------------

func (s *ProxyServer) handleAnthropicMessages(w http.ResponseWriter, r *http.Request) {
	startTime := time.Now()
	defer func() { s.State.metrics.observeDuration(time.Since(startTime).Seconds()) }()
	apiKeyID, ok := s.checkAuth(r)
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 32<<20)) // 32MB
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

	// Failover loop: try accounts until one succeeds or all are exhausted.
	tried := make(map[int64]bool)
	for attempt := 0; ; attempt++ {
		acc := SelectAccount(
			accounts,
			s.State.RateLimiter,
			s.State.Sticky,
			s.Config.Scheduling.Mode,
			mappedModel,
			sessionID,
			s.Config.QuotaProtection.Enabled,
		)
		if acc == nil {
			http.Error(w,
				"no available accounts (all disabled, rate-limited, "+
					"or quota-protected)",
				http.StatusServiceUnavailable)
			return
		}
		if tried[acc.ID] {
			if attempt > 0 {
				http.Error(w,
					"all accounts exhausted (quota or capacity)",
					http.StatusServiceUnavailable)
			} else {
				http.Error(w,
					"no available accounts (all disabled, rate-limited, "+
						"or quota-protected)",
					http.StatusServiceUnavailable)
			}
			return
		}
		tried[acc.ID] = true
		if sessionID != "" {
			s.State.Sticky.Bind(sessionID, acc.ID)
		}

		sessionUUID := strings.ReplaceAll(uuid.NewString(), "-", "")
		requestN := s.State.NextRequestN()

		accessToken, ok := s.ensureFreshToken(
			acc, mappedModel, originalModel, clientIP, apiKeyID, w)
		if !ok {
			return
		}

		available := acc.AvailableModels()
		effectiveModel := mappedModel
		if len(available) > 0 {
			effectiveModel = ResolveModelForAccount(
				mappedModel, available)
		}

		upstreamBody := AnthropicTransformRequest(
			anthropicReq, acc.ProjectID, sessionUUID, requestN)
		upstreamBody["model"] = effectiveModel

		bodyBytes, _ := json.Marshal(upstreamBody)
		resp, err := SendRequest(
			s.HTTP, accessToken, acc.ProjectID, bodyBytes, stream)
		if err != nil {
			log.Printf("upstream request failed: %v", err)
			_ = account.LogRequest(s.State.DB, account.LogRequestParams{
				HasAccountID: true, AccountID: acc.ID,
				HasModel: true, Model: originalModel,
				Status: 502, HasClientIP: clientIP != "",
				ClientIP: clientIP,
				HasError: true, Error: err.Error(),
				HasAPIKeyID: apiKeyID != nil,
				APIKeyID:   derefInt64(apiKeyID),
			})
			http.Error(w, "upstream request failed",
				http.StatusBadGateway)
			return
		}

		// Retryable: 429 (quota) or 503 (capacity) → failover.
		if isRetryableStatus(resp.StatusCode) {
			bodyText, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			s.State.RateLimiter.SetCooldown(
				acc.ID, mappedModel, 60)
			_ = account.MarkError(s.State.DB, acc.ID,
				string(bodyText), false)
			_ = account.LogRequest(s.State.DB, account.LogRequestParams{
				HasAccountID: true, AccountID: acc.ID,
				HasModel: true, Model: originalModel,
				Status: int64(resp.StatusCode),
				HasClientIP: clientIP != "", ClientIP: clientIP,
				HasError: true, Error: string(bodyText),
				HasAPIKeyID: apiKeyID != nil,
				APIKeyID:   derefInt64(apiKeyID),
			})
			if sessionID != "" {
				s.State.Sticky.Unbind(sessionID)
			}
			log.Printf("failover: account %s got %d for %s, "+
				"trying next", acc.Email, resp.StatusCode,
				originalModel)
			continue
		}

		// 401: token issue — set longer cooldown, no failover.
		if resp.StatusCode == 401 {
			bodyText, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			s.State.RateLimiter.SetCooldown(
				acc.ID, mappedModel, 300)
			_ = account.MarkError(s.State.DB, acc.ID,
				string(bodyText), true)
			_ = account.LogRequest(s.State.DB, account.LogRequestParams{
				HasAccountID: true, AccountID: acc.ID,
				HasModel: true, Model: originalModel,
				Status: int64(resp.StatusCode),
				HasClientIP: clientIP != "", ClientIP: clientIP,
				HasError: true, Error: string(bodyText),
				HasAPIKeyID: apiKeyID != nil,
				APIKeyID:   derefInt64(apiKeyID),
			})
			w.WriteHeader(resp.StatusCode)
			_, _ = w.Write(bodyText)
			return
		}

		// Other non-2xx: return to client, no failover.
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			bodyText, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			_ = account.LogRequest(s.State.DB, account.LogRequestParams{
				HasAccountID: true, AccountID: acc.ID,
				HasModel: true, Model: originalModel,
				Status: int64(resp.StatusCode),
				HasClientIP: clientIP != "", ClientIP: clientIP,
				HasError: true, Error: string(bodyText),
				HasAPIKeyID: apiKeyID != nil,
				APIKeyID:   derefInt64(apiKeyID),
			})
			w.WriteHeader(resp.StatusCode)
			_, _ = w.Write(bodyText)
			return
		}

		// Success path.
		_ = account.MarkUsed(s.State.DB, acc.ID, 0, false)
		s.State.RateLimiter.Clear(acc.ID)

		if stream {
			s.streamAnthropicSSE(w, resp.Body, originalModel,
				acc.ID, apiKeyID, clientIP)
			resp.Body.Close()
			return
		}

		bodyBytes2, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			http.Error(w, "read upstream body: "+err.Error(),
				http.StatusBadGateway)
			return
		}
		var geminiResp map[string]any
		if err := json.Unmarshal(bodyBytes2, &geminiResp); err != nil {
			http.Error(w, fmt.Sprintf(
				"invalid upstream JSON: %v | body: %s",
				err, string(bodyBytes2)),
				http.StatusBadGateway)
			return
		}

		anthropicResp := AnthropicTransformResponse(
			geminiResp, originalModel)
		usage, _ := anthropicResp["usage"].(map[string]any)
		promptTokens := int64Or(usage, "input_tokens", 0)
		completionTokens := int64Or(usage, "output_tokens", 0)
		cachedTokens := int64Or(usage, "cache_read_input_tokens", 0)
		cost := ComputeCost(originalModel, promptTokens,
			completionTokens, cachedTokens)
		_ = account.LogRequest(s.State.DB, account.LogRequestParams{
			HasAccountID: true, AccountID: acc.ID,
			HasModel: true, Model: originalModel,
			HasPromptTokens: true, PromptTokens: promptTokens,
			HasCompletion: true, CompletionTokens: completionTokens,
			HasCached: true, CachedTokens: cachedTokens,
			Status: 200,
			HasClientIP: clientIP != "", ClientIP: clientIP,
			HasCost: true, CostUSD: cost,
			HasAPIKeyID: apiKeyID != nil,
			APIKeyID:   derefInt64(apiKeyID),
		})
		writeJSON(w, http.StatusOK, anthropicResp)
		return
	} // end failover loop
}

func (s *ProxyServer) handleAnthropicCountTokens(w http.ResponseWriter, r *http.Request) {
	apiKeyID, ok := s.checkAuth(r)
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

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

	acc := SelectAccount(
		accounts,
		s.State.RateLimiter,
		s.State.Sticky,
		s.Config.Scheduling.Mode,
		mappedModel,
		sessionID,
		s.Config.QuotaProtection.Enabled,
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

	upstreamBody := AnthropicTransformRequest(anthropicReq, acc.ProjectID, sessionUUID, requestN)
	bodyBytes, _ := json.Marshal(upstreamBody)
	resp, err := SendRequest(s.HTTP, accessToken, acc.ProjectID, bodyBytes, false)
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
	_ = account.LogRequest(s.State.DB, account.LogRequestParams{
		HasAccountID: true, AccountID: acc.ID,
		HasModel: true, Model: originalModel,
		HasPromptTokens: true, PromptTokens: promptTokens,
		Status: 200,
		HasAPIKeyID: apiKeyID != nil, APIKeyID: derefInt64(apiKeyID),
	})
	writeJSON(w, http.StatusOK, map[string]any{"input_tokens": promptTokens})
}

// ---------------------------------------------------------------------------
// Helpers shared by both endpoints
// ---------------------------------------------------------------------------

// ensureFreshToken refreshes the access token if needed. On failure it writes
// the error response and returns ok=false.
func (s *ProxyServer) ensureFreshToken(
	acc *account.Account,
	mappedModel, originalModel, clientIP string,
	apiKeyID *int64,
	w http.ResponseWriter,
) (string, bool) {
	if !account.NeedsRefresh(acc.ExpiresAt) {
		return acc.AccessToken, true
	}
	tok, expiresAt, err := account.RefreshToken(s.OAuth, acc.RefreshToken)
	if err != nil {
		log.Printf("refresh token failed for account %d: %v", acc.ID, err)
		// Only disable on invalid_grant (refresh token revoked/expired).
		// Network errors (uTLS handshake EOF, timeout, etc.) are transient
		// — just set a cooldown so the account is retried later.
		disable := strings.Contains(err.Error(), "invalid_grant")
		secs := 300
		if disable {
			secs = 3600
		}
		s.State.RateLimiter.SetCooldown(acc.ID, mappedModel, secs)
		_ = account.MarkError(s.State.DB, acc.ID, err.Error(), disable)
		_ = account.LogRequest(s.State.DB, account.LogRequestParams{
			HasAccountID: true, AccountID: acc.ID,
			HasModel: true, Model: originalModel,
			Status: 401, HasClientIP: clientIP != "", ClientIP: clientIP,
			HasError: true, Error: err.Error(),
			HasAPIKeyID: apiKeyID != nil, APIKeyID: derefInt64(apiKeyID),
		})
		if disable {
			http.Error(w, "account token refresh failed (invalid_grant)",
				http.StatusBadGateway)
		} else {
			http.Error(w, "account token refresh failed (transient)",
				http.StatusBadGateway)
		}
		return "", false
	}
	if err := account.UpdateTokens(s.State.DB, acc.ID, tok, expiresAt, ""); err != nil {
		log.Printf("persist refreshed token failed: %v", err)
	}
	return tok, true
}

// checkAuth authenticates the request against DB-managed API keys.
// Returns:
//   - (some, true)  — matched a DB API key. The pointer holds the key id.
//   - (nil,  true)  — open access (no keys configured yet).
//   - (nil,  false) — auth failed.
func (s *ProxyServer) checkAuth(r *http.Request) (*int64, bool) {
	var provided string
	if h := r.Header.Get("Authorization"); h != "" {
		if rest, ok := strings.CutPrefix(h, "Bearer "); ok {
			provided = rest
		} else {
			provided = h
		}
	}
	if provided == "" {
		provided = r.Header.Get("X-API-Key")
	}
	if provided == "" {
		provided = r.Header.Get("X-Goog-Api-Key")
	}

	if provided == "" {
		// No key presented → only allow if no keys configured at all (first run).
		keys, _ := account.ListAPIKeys(s.State.DB)
		if len(keys) == 0 {
			return nil, true // open access until keys are created
		}
		return nil, false
	}

	// Check DB keys only.
	if k, err := account.FindAPIKey(s.State.DB, provided); err == nil && k != nil {
		id := k.ID
		return &id, true
	}
	return nil, false
}

func clientIPFrom(r *http.Request) string {
	// Use RemoteAddr as the source of truth. X-Forwarded-For is only
	// used if RemoteAddr is localhost (behind a known reverse proxy).
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	// If the request came from localhost, trust X-Forwarded-For.
	if host == "127.0.0.1" || host == "::1" || host == "localhost" {
		if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
			// Take the leftmost (original client) IP.
			if i := strings.Index(xff, ","); i >= 0 {
				return strings.TrimSpace(xff[:i])
			}
			return strings.TrimSpace(xff)
		}
	}
	return host
}

func derefInt64(p *int64) int64 {
	if p == nil {
		return 0
	}
	return *p
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	_ = enc.Encode(v)
}

// ---------------------------------------------------------------------------
// SSE streaming
// ---------------------------------------------------------------------------

// streamOpenAISSE transforms a Gemini SSE byte stream into an OpenAI SSE byte
// stream and writes it to w.
func (s *ProxyServer) streamOpenAISSE(
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
	// SSE chunks can be large; raise the per-line buffer cap.
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)

	firstChunk := true
	var totalPrompt, totalCompletion, totalCached, totalThought int64

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
		chunk := buildOpenAISSEChunk(inner, chatID, created, model, firstChunk)
		firstChunk = false
		if chunk != "" {
			_, _ = io.WriteString(w, chunk)
			if flusher != nil {
				flusher.Flush()
			}
		}
	}
	_, _ = io.WriteString(w, "data: [DONE]\n\n")
	if flusher != nil {
		flusher.Flush()
	}

	cost := ComputeCost(model, totalPrompt, totalCompletion, totalCached)
	_ = account.LogRequest(s.State.DB, account.LogRequestParams{
		HasAccountID: true, AccountID: accountID,
		HasModel: true, Model: model,
		HasPromptTokens: true, PromptTokens: totalPrompt,
		HasCompletion: true, CompletionTokens: totalCompletion,
		HasCached: true, CachedTokens: totalCached,
		HasThought: true, ThoughtTokens: totalThought,
		Status: 200,
		HasCost: true, CostUSD: cost,
		HasAPIKeyID: apiKeyID != nil, APIKeyID: derefInt64(apiKeyID),
	})
}

func buildOpenAISSEChunk(geminiJSON map[string]any, chatID string, created int64, model string, isFirst bool) string {
	candidates, _ := geminiJSON["candidates"].([]any)
	if len(candidates) == 0 {
		return ""
	}
	candidate, _ := candidates[0].(map[string]any)
	if candidate == nil {
		return ""
	}
	contentMap, _ := candidate["content"].(map[string]any)
	var parts []any
	if contentMap != nil {
		parts, _ = contentMap["parts"].([]any)
	}
	if parts == nil {
		// No parts — but we may still have a finishReason to emit.
		parts = []any{}
	}

	delta := map[string]any{}
	if isFirst {
		delta["role"] = "assistant"
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
				"index": 0,
				"id":    id,
				"type":  "function",
				"function": map[string]any{
					"name":      name,
					"arguments": string(argsBytes),
				},
			})
		}
	}

	if content != "" {
		delta["content"] = content
	}
	if reasoning != "" {
		delta["reasoning_content"] = reasoning
	}
	if len(toolCalls) > 0 {
		delta["tool_calls"] = toolCalls
	}

	var finishReasonMapped any
	if fr, ok := candidate["finishReason"].(string); ok && fr != "" {
		finishReasonMapped = mapFinishReason(fr)
	}

	chunk := map[string]any{
		"id":      chatID,
		"object":  "chat.completion.chunk",
		"created": created,
		"model":   model,
		"choices": []any{
			map[string]any{
				"index":         0,
				"delta":         delta,
				"finish_reason": finishReasonMapped,
			},
		},
	}
	b, _ := json.Marshal(chunk)
	return "data: " + string(b) + "\n\n"
}

// streamAnthropicSSE transforms a Gemini SSE byte stream into an Anthropic SSE
// byte stream and writes it to w.
func (s *ProxyServer) streamAnthropicSSE(
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
		cost := ComputeCost(model, totalPrompt, totalCompletion, totalCached)
		_ = account.LogRequest(s.State.DB, account.LogRequestParams{
			HasAccountID: true, AccountID: accountID,
			HasModel: true, Model: model,
			HasPromptTokens: true, PromptTokens: totalPrompt,
			HasCompletion: true, CompletionTokens: totalCompletion,
			HasCached: true, CachedTokens: totalCached,
			Status: 200,
			HasClientIP: clientIP != "", ClientIP: clientIP,
			HasCost: true, CostUSD: cost,
			HasAPIKeyID: apiKeyID != nil, APIKeyID: derefInt64(apiKeyID),
		})
	}
}

func setSSEHeaders(w http.ResponseWriter) {
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	h.Set("Connection", "keep-alive")
}

// ---------------------------------------------------------------------------
// Background quota refresher
// ---------------------------------------------------------------------------

func (s *ProxyServer) quotaRefresherLoop() {
	// Run once on boot, then every 5 minutes.
	RefreshAllQuotas(s)
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()
	for range ticker.C {
		if err := RefreshAllQuotas(s); err != nil {
			log.Printf("background quota refresh failed: %v", err)
		}
	}
}

// tokenRefresherLoop proactively refreshes access tokens that are within
// oauthRefreshSkew (15 min) of expiry, so requests don't hit a stale token.
func (s *ProxyServer) tokenRefresherLoop() {
	ticker := time.NewTicker(1 * time.Minute)
	defer ticker.Stop()
	for range ticker.C {
		s.refreshExpiringTokens()
	}
}

func (s *ProxyServer) refreshExpiringTokens() {
	accounts, err := account.ListAccounts(s.State.DB)
	if err != nil {
		return
	}
	for _, acc := range accounts {
		if acc.Disabled || !account.NeedsRefresh(acc.ExpiresAt) {
			continue
		}
		tok, expiresAt, err := account.RefreshToken(s.OAuth, acc.RefreshToken)
		if err != nil {
			log.Printf("proactive token refresh failed for %s: %v", acc.Email, err)
			continue
		}
		if err := account.UpdateTokens(s.State.DB, acc.ID, tok, expiresAt, ""); err != nil {
			log.Printf("persist proactively refreshed token failed for %s: %v", acc.Email, err)
			continue
		}
		log.Printf("proactively refreshed token for %s (expires in %dm)",
			acc.Email, (expiresAt-time.Now().Unix())/60)
	}
}

// RefreshAllQuotas refreshes quota for every bound account.
func RefreshAllQuotas(s *ProxyServer) error {
	accounts, err := account.ListAccounts(s.State.DB)
	if err != nil {
		return err
	}
	if len(accounts) == 0 {
		return nil
	}
	threshold := int32(s.Config.QuotaProtection.ThresholdPercentage)
	monitored := s.Config.QuotaProtection.MonitoredModels
	protectionEnabled := s.Config.QuotaProtection.Enabled

	for _, acc := range accounts {
		if acc.Disabled {
			continue
		}
		accessToken := acc.AccessToken
		if account.NeedsRefresh(acc.ExpiresAt) {
			tok, expiresAt, err := account.RefreshToken(s.OAuth, acc.RefreshToken)
			if err != nil {
				log.Printf("refresh token failed for %s: %v", acc.Email, err)
				continue
			}
			if err := account.UpdateTokens(s.State.DB, acc.ID, tok, expiresAt, ""); err != nil {
				log.Printf("persist refreshed token failed for %s: %v", acc.Email, err)
			}
			accessToken = tok
		}
		fetched, err := account.FetchQuota(s.OAuth, accessToken, acc.ProjectID)
		if err != nil {
			log.Printf("fetch quota failed for %s: %v", acc.Email, err)
			continue
		}
		newProtected := acc.ProtectedModels
		if protectionEnabled {
			newProtected = account.ComputeProtectedModels(
				acc.ProtectedModels,
				fetched.ModelPercentages,
				monitored,
				threshold,
			)
		}
		if err := account.UpdateQuota(
			s.State.DB, acc.ID,
			fetched.JSONBlob, fetched.SummaryBlob,
			fetched.MaxPercentage, fetched.HasMaxPercentage,
			newProtected,
		); err != nil {
			log.Printf("persist quota failed for %s: %v", acc.Email, err)
			continue
		}
	log.Printf("refreshed quota for %s (max=%d%%, protected=%v)", acc.Email, fetched.MaxPercentage, newProtected)
	}
	return nil
}

// ---------------------------------------------------------------------------
// Health check (background connectivity probe)
// ---------------------------------------------------------------------------

// healthCheckLoop runs a periodic connectivity probe against every
// non-disabled account. It calls fetchAvailableModels (lightweight, no
// quota cost) to verify token validity + upstream reachability.
//
// On failure:
//   - increments the account's consecutive-failure counter
//   - fires a Notifier notification
//   - after FailureThreshold consecutive failures, auto-disables the
//     account (protects against repeatedly hitting dead accounts)
//
// On success:
//   - resets the failure counter
//   - if the account was previously unhealthy, fires a recovery notification
func (s *ProxyServer) healthCheckLoop() {
	interval := time.Duration(s.Config.HealthCheck.IntervalSeconds) * time.Second
	if interval < 10*time.Second {
		interval = 10 * time.Second
	}
	threshold := s.Config.HealthCheck.FailureThreshold
	if threshold < 1 {
		threshold = 3
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	// Run once immediately at startup, then on every tick.
	s.runHealthCheck(threshold)
	for range ticker.C {
		s.runHealthCheck(threshold)
	}
}

func (s *ProxyServer) runHealthCheck(threshold int) {
	accounts, err := account.ListAccounts(s.State.DB)
	if err != nil {
		log.Printf("health check: list accounts failed: %v", err)
		return
	}

	var allDown []AccountHealth
	for _, acc := range accounts {
		if acc.Disabled {
			continue
		}
		healthy, reason := s.probeAccount(acc)
		ah := AccountHealth{
			Email:    acc.Email,
			Healthy:  healthy,
			Reason:   reason,
			Disabled: acc.Disabled,
		}

		if healthy {
			s.State.healthFailures.Delete(acc.ID)
			// Check if it was previously unhealthy (notifier tracks state).
			s.State.Notifier.NotifyAccountRecovered(acc.Email)
			continue
		}

		// Unhealthy — increment failure counter.
		count := s.incrementHealthFailure(acc.ID)
		s.State.Notifier.NotifyAccountUnhealthy(acc.Email, reason)

		if count >= threshold {
			log.Printf("health check: %s failed %d times, auto-disabling",
				acc.Email, count)
			_ = account.MarkError(s.State.DB, acc.ID,
				"health check: "+reason, true)
			s.State.healthFailures.Delete(acc.ID)
			s.State.RateLimiter.Clear(acc.ID)
		}
		allDown = append(allDown, ah)
	}

	// If every non-disabled account is down, fire the all-down alert.
	if len(allDown) > 0 && len(allDown) == countNonDisabled(accounts) {
		s.State.Notifier.NotifyAllAccountsDown(allDown)
	}
}

// probeAccount does the actual connectivity test. It first ensures
// the token is fresh, then calls ProbeAccount (fetchAvailableModels).
func (s *ProxyServer) probeAccount(acc *account.Account) (bool, string) {
	accessToken := acc.AccessToken
	if account.NeedsRefresh(acc.ExpiresAt) {
		tok, expiresAt, err := account.RefreshToken(s.OAuth, acc.RefreshToken)
		if err != nil {
			return false, fmt.Sprintf("token refresh: %v", err)
		}
		if err := account.UpdateTokens(s.State.DB, acc.ID, tok, expiresAt, ""); err != nil {
			log.Printf("health check: persist token for %s: %v", acc.Email, err)
		}
		accessToken = tok
	}

	if err := account.ProbeAccount(s.OAuth, accessToken, acc.ProjectID); err != nil {
		return false, err.Error()
	}
	return true, ""
}

func (s *ProxyServer) incrementHealthFailure(id int64) int {
	v, _ := s.State.healthFailures.Load(id)
	count := 0
	if v != nil {
		count = v.(int)
	}
	count++
	s.State.healthFailures.Store(id, count)
	return count
}

func countNonDisabled(accounts []*account.Account) int {
	n := 0
	for _, a := range accounts {
		if !a.Disabled {
			n++
		}
	}
	return n
}
