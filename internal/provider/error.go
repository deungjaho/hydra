package provider

import (
	"fmt"
	"time"
)

// ProviderError is the unified error type. All providers MUST map their
// upstream errors to this. This rejects LiteLLM's string-pattern matching
// approach in favor of structured fields.
type ProviderError struct {
	Provider     string        // provider ID
	HTTPStatus   int           // raw HTTP status (0 for non-HTTP errors)
	Code         ErrorCode     // normalized code
	Message      string        // human-readable message (safe for client)
	Retryable    RetryDecision // retry policy
	RetryAfter   time.Duration // 0 if not applicable
	UpstreamType string        // provider's native error type/status (diagnostics only)
	UpstreamBody string        // raw upstream error body (diagnostics only, NOT leaked to client)
}

func (e *ProviderError) Error() string {
	return fmt.Sprintf("provider %s: %s (status %d, code %s): %s",
		e.Provider, e.Code, e.HTTPStatus, e.Code, e.Message)
}

// ErrorCode is the normalized error code across all providers.
type ErrorCode string

const (
	ErrBadRequest     ErrorCode = "bad_request"      // 400
	ErrAuthentication ErrorCode = "authentication"   // 401
	ErrPermission     ErrorCode = "permission"       // 403
	ErrNotFound       ErrorCode = "not_found"        // 404
	ErrRateLimit      ErrorCode = "rate_limit"       // 429
	ErrServerError    ErrorCode = "server_error"     // 500
	ErrOverloaded     ErrorCode = "overloaded"       // 503 / Anthropic 529
	ErrGateway        ErrorCode = "gateway"          // 502 / 504
	ErrTimeout        ErrorCode = "timeout"
	ErrConnectFailure ErrorCode = "connect_failure"  // TCP-level (Envoy-style)
	ErrStreamReset    ErrorCode = "stream_reset"     // mid-stream reset
	ErrContextWindow  ErrorCode = "context_window"   // 400 context-window-exceeded
	ErrContentFilter  ErrorCode = "content_filter"   // safety / recitation
)

// RetryDecision tells the router whether and how to retry.
type RetryDecision int

const (
	// RetryNone: do not retry (e.g. 400, 401 auth, 403 permission).
	RetryNone RetryDecision = iota
	// RetryImmediately: retry on next account/provider now (e.g. 404, 502, 503, 504).
	RetryImmediately
	// RetryAfterBackoff: retry after exponential backoff (e.g. 500).
	RetryAfterBackoff
	// RetryAfterDuration: retry after RetryAfter duration (e.g. 429 with retry-after).
	RetryAfterDuration
)

// ClassifyHTTPError maps an HTTP status code to a ProviderError with the
// appropriate ErrorCode and RetryDecision. This is the default classifier;
// providers can override for provider-specific nuances (e.g. Anthropic 529).
func ClassifyHTTPError(providerID string, status int, body string) *ProviderError {
	pe := &ProviderError{
		Provider:   providerID,
		HTTPStatus: status,
		UpstreamBody: body,
	}
	switch status {
	case 400:
		pe.Code = ErrBadRequest
		pe.Retryable = RetryNone
	case 401:
		pe.Code = ErrAuthentication
		pe.Retryable = RetryNone
	case 403:
		pe.Code = ErrPermission
		pe.Retryable = RetryNone
	case 404:
		// 404 is retryable across providers — rejects LiteLLM #21377 bypass.
		pe.Code = ErrNotFound
		pe.Retryable = RetryImmediately
	case 429:
		pe.Code = ErrRateLimit
		pe.Retryable = RetryAfterBackoff
	case 500:
		pe.Code = ErrServerError
		pe.Retryable = RetryAfterBackoff
	case 502, 503, 504:
		pe.Code = ErrGateway
		pe.Retryable = RetryImmediately
	case 529:
		// Anthropic overloaded.
		pe.Code = ErrOverloaded
		pe.Retryable = RetryAfterBackoff
	default:
		if status >= 500 {
			pe.Code = ErrServerError
			pe.Retryable = RetryAfterBackoff
		} else if status >= 400 {
			pe.Code = ErrBadRequest
			pe.Retryable = RetryNone
		} else {
			pe.Code = ErrServerError
			pe.Retryable = RetryNone
		}
	}
	pe.Message = fmt.Sprintf("upstream returned HTTP %d", status)
	return pe
}
