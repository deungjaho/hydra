package provider

import "time"

// Usage is the normalized token-usage record. All providers map their
// native usage fields to this.
//
// OpenAI: usage.prompt_tokens / completion_tokens / total_tokens
// Anthropic: usage.input_tokens / output_tokens / cache_creation_input_tokens / cache_read_input_tokens
// Gemini: usageMetadata.promptTokenCount / candidatesTokenCount / cachedContentTokenCount / thoughtsTokenCount
type Usage struct {
	PromptTokens        int64
	CompletionTokens    int64
	CachedTokens        int64 // OpenAI cached_tokens, Anthropic cache_read, Gemini cachedContent
	ThoughtTokens       int64 // Gemini thoughtsTokenCount (thinking budget)
	CacheCreationTokens int64 // Anthropic cache_creation_input_tokens
}

// QuotaState is the per-provider quota state (optional, for providers with
// account pools like Google Cloud Code). Providers with static API keys
// return nil from FetchQuota.
type QuotaState struct {
	// Per-account quota, by account ID (no PII — ID only).
	Accounts map[int64]AccountQuota
}

// AccountQuota describes one account's quota state.
type AccountQuota struct {
	Available      bool
	QuotaRemaining int64     // percent or absolute, provider-specific
	RateLimited    bool
	RateLimitUntil time.Time
}
