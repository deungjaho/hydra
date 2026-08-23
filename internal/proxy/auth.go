package proxy

import (
	"log"
	"net"
	"net/http"
	"strings"

	"github.com/deungjaho/hydra/internal/account"
)

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
		secs := cooldownTransientToken
		if disable {
			secs = cooldownInvalidGrant
		}
		s.State.RateLimiter.SetCooldown(acc.ID, mappedModel, secs)
		logErr(account.MarkError(s.State.DB, acc.ID, err.Error(), disable))
		logErr(account.LogRequest(s.State.DB, account.LogRequestParams{
			AccountID: &acc.ID,
			Model:     &originalModel,
			Status:    401, ClientIP: pstrIf(clientIP != "", clientIP),
			Error:    pstr(err.Error()),
			APIKeyID: apiKeyID,
		}))
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
	k, ok := s.checkAuthFull(r)
	if !ok || k == nil {
		return nil, ok
	}
	id := k.ID
	return &id, true
}

// checkAuthFull is like checkAuth but returns the full ApiKey (with scheduling
// override fields). Used by handleChatCompletions / handleResponses to apply
// per-key scheduling_mode and no_sticky.
func (s *ProxyServer) checkAuthFull(r *http.Request) (*account.ApiKey, bool) {
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
		// DB error → fail-closed (deny all) rather than degrading to open access.
		keys, err := account.ListAPIKeys(s.State.DB)
		if err != nil {
			log.Printf("auth: ListAPIKeys failed (fail-closed): %v", err)
			return nil, false
		}
		if len(keys) == 0 {
			return nil, true // open access until keys are created
		}
		return nil, false
	}

	// Check DB keys only.
	k, err := account.FindAPIKey(s.State.DB, provided)
	if err != nil {
		log.Printf("auth: FindAPIKey failed (fail-closed): %v", err)
		return nil, false
	}
	if k == nil {
		return nil, false // key not found
	}
	return k, true
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

func pi64(v int64) *int64     { return &v }
func pstr(v string) *string   { return &v }
func pf64(v float64) *float64 { return &v }

func pstrIf(cond bool, v string) *string {
	if !cond {
		return nil
	}
	return &v
}

// keyIDPtr extracts the API key ID as a pointer for logging.
// Returns nil for open-access (no key matched).
func keyIDPtr(k *account.ApiKey) *int64 {
	if k == nil {
		return nil
	}
	id := k.ID
	return &id
}
