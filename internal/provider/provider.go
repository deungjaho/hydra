// Package provider defines the multi-provider adapter interface for Hydra.
//
// A Provider encapsulates: auth, request/response/streaming translation,
// capability declaration, error normalization, and (optionally) account
// pool + quota management for one upstream.
//
// This is a spike package (spike-hydra-mpg-001) — it is NOT yet wired
// into the live proxy server. See inbox/hydra.md REFERENCE_REVIEW
// RR-hydra-001 for the full design rationale and precedent analysis.
package provider

import (
	"context"
)

// Provider is the interface every upstream adapter must implement.
type Provider interface {
	// Identity
	ID() string          // "google-cloud-code", "openai", "anthropic", ...
	DisplayName() string // human-readable

	// Capability declaration (static, from adapter Config)
	Capabilities() CapabilitySet

	// Auth
	AuthStrategy() AuthStrategy

	// Model resolution
	// ResolveModel maps a client-requested model name to this provider's
	// internal model id. Returns (modelID, ok). ok=false means this
	// provider cannot serve this model.
	ResolveModel(clientModel string) (string, bool)

	// AvailableModels returns models currently available on this provider
	// (may be runtime-discovered or static from Config).
	AvailableModels(ctx context.Context) ([]string, error)

	// Chat completions (non-streaming)
	ChatCompletions(ctx context.Context, req *Request) (*Response, error)

	// Chat completions (streaming)
	StreamChatCompletions(ctx context.Context, req *Request) (Stream, error)

	// Quota / accounting (optional per provider)
	// Providers without a quota concept (e.g. static API key) return nil.
	FetchQuota(ctx context.Context) (*QuotaState, error)

	// Health
	HealthCheck(ctx context.Context) error
}

// RequestFormat indicates the wire format of the client request body.
type RequestFormat string

const (
	FormatOpenAI    RequestFormat = "openai"    // /v1/chat/completions
	FormatAnthropic RequestFormat = "anthropic" // /v1/messages
)

// Request is the normalized request passed to a provider.
type Request struct {
	Model                string
	Stream               bool
	RequiredCapabilities CapabilitySet
	// RawBody is the original client request body (OpenAI or Anthropic
	// format, depending on Format). Providers translate from this.
	RawBody map[string]any
	Format  RequestFormat
}

// Response is the normalized non-streaming response from a provider.
type Response struct {
	Model         string
	Content       string
	ToolCalls     []ToolCall
	Usage         *Usage
	FinishReason  FinishReason
	// RawBody is the normalized response body in the client's requested
	// format (OpenAI or Anthropic). Providers that do format translation
	// populate this for passthrough.
	RawBody map[string]any
}

// ToolCall represents a single tool/function call in a response.
type ToolCall struct {
	ID   string
	Name string
	Args string // JSON-encoded arguments
}
