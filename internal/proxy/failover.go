package proxy

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/deungjaho/hydra/internal/account"
	"github.com/deungjaho/hydra/internal/config"
)

// failoverConfig configures a failover loop run. Each protocol handler
// constructs one with protocol-specific callbacks.
type failoverConfig struct {
	originalModel string // model name as received from the client
	mappedModel   string // model name after MapModel
	sessionID     string // sticky-session key (OpenAI "user" field)
	stream        bool   // whether this is a streaming request
	clientIP      string // client IP for logging
	apiKeyID      *int64 // API key ID for logging
	schedMode     config.SchedulingMode
	noSticky      bool

	// writeErr sends a protocol-specific error response to the client.
	writeErr func(status int, msg string)

	// writeRawErr sends a raw (passthrough) error response with the
	// original status code and body bytes.
	writeRawErr func(status int, body []byte)

	// buildBody constructs the upstream request body for one account.
	// Returns the marshalled JSON bytes ready to send.
	buildBody func(acc *account.Account, sessionUUID string, requestN uint64, effectiveModel string) ([]byte, error)

	// handleSuccess processes a successful (2xx) upstream response.
	// It must close resp.Body.
	handleSuccess func(w http.ResponseWriter, resp *http.Response, acc *account.Account)

	// handleSuccessNonStream processes a successful non-streaming
	// response. It reads and closes resp.Body, then writes the
	// protocol-specific response to w. If nil, handleSuccess is used.
	handleSuccessNonStream func(w http.ResponseWriter, resp *http.Response, acc *account.Account)
}

// failoverLoop runs the account failover loop. It tries accounts in
// sequence until one succeeds or all are exhausted. The behaviour
// matches the original inline loops in the three protocol handlers:
//
//   - 429/503 → cooldown + failover
//   - 400 location error → failover (with one retry round)
//   - 400 other → return to client
//   - 401 → long cooldown, return to client
//   - other non-2xx → return to client
//   - 2xx → handleSuccess
//
// The caller is responsible for listing accounts before calling.
func (s *ProxyServer) failoverLoop(
	w http.ResponseWriter,
	accounts []*account.Account,
	cfg failoverConfig,
) {
	tried := make(map[int64]bool)
	locationFailures := 0
	locationRetries := 0

	for attempt := 0; ; attempt++ {
		acc := SelectAccount(
			accounts,
			s.State.RateLimiter,
			s.State.Sticky,
			cfg.schedMode,
			cfg.mappedModel,
			cfg.sessionID,
			s.Config.QuotaProtection.Enabled,
			s.Config.Scheduling.StickySessions,
			cfg.noSticky,
			s.State.Concurrency,
			int32(s.Config.Scheduling.QuotaWarnPercentage),
		)
		if acc == nil {
			cfg.writeErr(http.StatusServiceUnavailable,
				"no available accounts (all disabled, rate-limited, "+
					"or quota-protected)")
			return
		}
		if tried[acc.ID] {
			if locationFailures > 0 && locationFailures == len(tried) && locationRetries < 1 {
				locationRetries++
				time.Sleep(locationRetryDelay)
				tried = make(map[int64]bool)
				locationFailures = 0
				continue
			}
			if attempt > 0 {
				cfg.writeErr(http.StatusServiceUnavailable,
					"all accounts exhausted (quota or capacity)")
			} else {
				cfg.writeErr(http.StatusServiceUnavailable,
					"no available accounts (all disabled, rate-limited, "+
						"or quota-protected)")
			}
			return
		}
		tried[acc.ID] = true
		if cfg.sessionID != "" && !cfg.noSticky {
			s.State.Sticky.Bind(cfg.sessionID, acc.ID)
		}

		// Track in-flight request for concurrency control.
		s.State.Concurrency.Acquire(acc.ID)
		released := false
		releaseOnce := func() {
			if !released {
				s.State.Concurrency.Release(acc.ID)
				released = true
			}
		}

		sessionUUID := strings.ReplaceAll(uuid.NewString(), "-", "")
		requestN := s.State.NextRequestN()

		accessToken, ok := s.ensureFreshToken(
			acc, cfg.mappedModel, cfg.originalModel, cfg.clientIP, cfg.apiKeyID, w)
		if !ok {
			releaseOnce()
			return
		}

		available := acc.AvailableModels()
		effectiveModel := cfg.mappedModel
		if len(available) > 0 {
			effectiveModel = ResolveModelForAccount(cfg.mappedModel, available)
		}

		bodyBytes, err := cfg.buildBody(acc, sessionUUID, requestN, effectiveModel)
		if err != nil {
			releaseOnce()
			cfg.writeErr(http.StatusInternalServerError,
				"marshal request: "+err.Error())
			return
		}

		resp, err := SendRequest(
			s.HTTP, accessToken, acc.ProjectID, bodyBytes, cfg.stream, acc.MachineID)
		if err != nil {
			log.Printf("upstream request failed: %v", err)
			releaseOnce()
			logErr(account.LogRequest(s.State.DB, account.LogRequestParams{
				AccountID: &acc.ID,
				Model:     &cfg.originalModel,
				Status:    502, ClientIP: pstrIf(cfg.clientIP != "", cfg.clientIP),
				Error:    pstr(err.Error()),
				APIKeyID: cfg.apiKeyID,
			}))
			cfg.writeErr(http.StatusBadGateway, "upstream request failed")
			return
		}

		// Retryable: 429 (quota) or 503 (capacity) → failover.
		if isRetryableStatus(resp.StatusCode) {
			bodyText, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			releaseOnce()
			s.State.RateLimiter.SetCooldown(
				acc.ID, cfg.mappedModel, cooldownRetryable)
			logErr(account.MarkError(s.State.DB, acc.ID,
				string(bodyText), false))
			logErr(account.LogRequest(s.State.DB, account.LogRequestParams{
				AccountID: &acc.ID,
				Model:     &cfg.originalModel,
				Status:    int64(resp.StatusCode),
				ClientIP:  pstrIf(cfg.clientIP != "", cfg.clientIP),
				Error:     pstr(string(bodyText)),
				APIKeyID:  cfg.apiKeyID,
			}))
			if cfg.sessionID != "" {
				s.State.Sticky.Unbind(cfg.sessionID)
			}
			log.Printf("failover: account %s got %d for %s, "+
				"trying next", acc.Email, resp.StatusCode,
				cfg.originalModel)
			continue
		}

		// 400 with "User location is not supported" → failover.
		if resp.StatusCode == 400 {
			bodyText, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			if isLocationError(bodyText) {
				releaseOnce()
				locationFailures++
				logErr(account.LogRequest(s.State.DB, account.LogRequestParams{
					AccountID: &acc.ID,
					Model:     &cfg.originalModel,
					Status:    400,
					ClientIP:  pstrIf(cfg.clientIP != "", cfg.clientIP),
					Error:     pstr(string(bodyText)),
					APIKeyID:  cfg.apiKeyID,
				}))
				if cfg.sessionID != "" {
					s.State.Sticky.Unbind(cfg.sessionID)
				}
				log.Printf("failover: account %s got 400 location "+
					"error for %s, trying next",
					acc.Email, cfg.originalModel)
				continue
			}
			// Other 400 errors — return to client, no failover.
			logErr(account.LogRequest(s.State.DB, account.LogRequestParams{
				AccountID: &acc.ID,
				Model:     &cfg.originalModel,
				Status:    400,
				ClientIP:  pstrIf(cfg.clientIP != "", cfg.clientIP),
				Error:     pstr(string(bodyText)),
				APIKeyID:  cfg.apiKeyID,
			}))
			releaseOnce()
			cfg.writeRawErr(400, bodyText)
			return
		}

		// 401: token issue — set longer cooldown, no failover.
		if resp.StatusCode == 401 {
			bodyText, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			releaseOnce()
			s.State.RateLimiter.SetCooldown(
				acc.ID, cfg.mappedModel, cooldownTokenError)
			logErr(account.MarkError(s.State.DB, acc.ID,
				string(bodyText), true))
			logErr(account.LogRequest(s.State.DB, account.LogRequestParams{
				AccountID: &acc.ID,
				Model:     &cfg.originalModel,
				Status:    int64(resp.StatusCode),
				ClientIP:  pstrIf(cfg.clientIP != "", cfg.clientIP),
				Error:     pstr(string(bodyText)),
				APIKeyID:  cfg.apiKeyID,
			}))
			cfg.writeRawErr(resp.StatusCode, bodyText)
			return
		}

		// Other non-2xx: return to client, no failover.
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			bodyText, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			releaseOnce()
			logErr(account.LogRequest(s.State.DB, account.LogRequestParams{
				AccountID: &acc.ID,
				Model:     &cfg.originalModel,
				Status:    int64(resp.StatusCode),
				ClientIP:  pstrIf(cfg.clientIP != "", cfg.clientIP),
				Error:     pstr(string(bodyText)),
				APIKeyID:  cfg.apiKeyID,
			}))
			cfg.writeRawErr(resp.StatusCode, bodyText)
			return
		}

		// Success path.
		logErr(account.MarkUsed(s.State.DB, acc.ID, 0, false))
		s.State.RateLimiter.Clear(acc.ID)

		if cfg.stream {
			cfg.handleSuccess(w, resp, acc)
			releaseOnce()
			return
		}
		if cfg.handleSuccessNonStream != nil {
			cfg.handleSuccessNonStream(w, resp, acc)
			releaseOnce()
			return
		}
		cfg.handleSuccess(w, resp, acc)
		releaseOnce()
		return
	}
}

// resolveScheduling determines the scheduling mode and no-sticky flag
// from the API key and request headers.
func resolveScheduling(s *ProxyServer, apiKey *account.ApiKey, r *http.Request) (config.SchedulingMode, bool) {
	schedMode := s.Config.Scheduling.Mode
	if apiKey != nil && apiKey.SchedulingMode != "" {
		schedMode = config.SchedulingMode(apiKey.SchedulingMode)
	}
	noSticky := r.Header.Get("X-Hydra-No-Sticky") == "true" ||
		r.Header.Get("X-Hydra-No-Sticky") == "1" ||
		(apiKey != nil && apiKey.NoSticky)
	return schedMode, noSticky
}

// writeRawHTTP writes a raw status code and body bytes to the response,
// preserving the original content type from the upstream.
func writeRawHTTP(w http.ResponseWriter, status int, body []byte) {
	w.WriteHeader(status)
	_, _ = w.Write(body)
}

// writeOpenAIError writes an OpenAI-format error JSON response.
func writeOpenAIError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, openAIErrorBody(status, message))
}

// writeAnthropicError writes an Anthropic-format error JSON response.
func writeAnthropicError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, anthropicErrorBody(status, message))
}

// openAIErrorBody builds an OpenAI-format error body.
func openAIErrorBody(status int, message string) map[string]any {
	return map[string]any{
		"error": map[string]any{
			"message": message,
			"type":    errorType(status),
			"code":    nil,
		},
	}
}

// anthropicErrorBody builds an Anthropic-format error body.
func anthropicErrorBody(status int, message string) map[string]any {
	return map[string]any{
		"type": "error",
		"error": map[string]any{
			"type":    errorType(status),
			"message": message,
		},
	}
}

// errorType maps an HTTP status code to an OpenAI/Anthropic error type string.
func errorType(status int) string {
	switch {
	case status == 401 || status == 403:
		return "authentication_error"
	case status == 400:
		return "invalid_request_error"
	case status == 429:
		return "rate_limit_error"
	case status == 503:
		return "overloaded_error"
	default:
		return "api_error"
	}
}

// failoverLogSuccess logs a successful request with usage.
func (s *ProxyServer) failoverLogSuccess(
	acc *account.Account,
	originalModel string,
	promptTokens, completionTokens, cachedTokens, thoughtTokens int64,
	clientIP string,
	apiKeyID *int64,
) {
	logErr(account.LogRequest(s.State.DB, account.LogRequestParams{
		AccountID:        &acc.ID,
		Model:            &originalModel,
		PromptTokens:     &promptTokens,
		CompletionTokens: &completionTokens,
		CachedTokens:     &cachedTokens,
		ThoughtTokens:    &thoughtTokens,
		Status:           200,
		ClientIP:         pstrIf(clientIP != "", clientIP),
		APIKeyID:         apiKeyID,
	}))
}

// parseGeminiSuccessBody reads, closes, and parses a successful upstream
// response body into a map[string]any. On failure it writes an error to w
// and returns nil.
func parseGeminiSuccessBody(w http.ResponseWriter, resp *http.Response, writeErr func(int, string)) map[string]any {
	bodyBytes2, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil {
		writeErr(http.StatusBadGateway, "read upstream body: "+err.Error())
		return nil
	}
	var geminiResp map[string]any
	if err := json.Unmarshal(bodyBytes2, &geminiResp); err != nil {
		writeErr(http.StatusBadGateway,
			fmt.Sprintf("invalid upstream JSON: %v | body: %s",
				err, string(bodyBytes2)))
		return nil
	}
	return geminiResp
}
