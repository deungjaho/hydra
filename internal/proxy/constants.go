package proxy

import "time"

// Timeouts for the HTTP server and upstream clients.
const (
	readHeaderTimeout = 30 * time.Second
	readTimeout       = 60 * time.Second
	writeTimeout      = 600 * time.Second // long for streaming responses
	maxHeaderBytes    = 1 << 20           // 1 MB

	upstreamTimeout = 300 * time.Second // uTLS client for Gemini API
	oauthTimeout    = 60 * time.Second  // OAuth/quota fetch
	minProbeTimeout = 5 * time.Second
	defProbeTimeout = 15 * time.Second

	shutdownTimeout     = 30 * time.Second
	loopJoinTimeout     = 45 * time.Second
	locationRetryDelay  = 2 * time.Second
	stickyCleanupPeriod = 30 * time.Minute
)

// Request body size limit (32 MB).
const maxRequestBody = 32 << 20

// Cooldown durations (seconds) for the per-account rate limiter.
const (
	cooldownRetryable      = 60   // 429/503 — quota or capacity
	cooldownTokenError     = 300  // 401 — token refresh issue
	cooldownInvalidGrant   = 3600 // invalid_grant — refresh token revoked
	cooldownTransientToken = 300  // network error during token refresh
)

// Default context window when a registered model has none configured.
const defaultContextWindow = 128000

// Default max output tokens when AGY metadata is unavailable.
const defaultMaxOutputTokens = 65536
