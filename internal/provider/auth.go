package provider

import (
	"context"
	"net/http"
)

// AuthStrategy describes how a provider authenticates upstream requests.
type AuthStrategy interface {
	// Type returns the auth scheme identifier.
	Type() AuthType
	// Apply attaches credentials to an outbound HTTP request.
	Apply(req *http.Request) error
	// Refresh refreshes credentials if applicable (no-op for static keys).
	// Must be safe for concurrent use (single-flight recommended).
	Refresh(ctx context.Context) error
	// NeedsRefresh returns true if credentials are near expiry.
	NeedsRefresh() bool
}

// AuthType identifies the authentication scheme a provider uses.
type AuthType string

const (
	AuthBearer      AuthType = "bearer"         // OpenAI: Authorization: Bearer $KEY
	AuthXAPIKey     AuthType = "x-api-key"      // Anthropic: x-api-key + anthropic-version
	AuthXGoogAPIKey AuthType = "x-goog-api-key" // Gemini REST static key
	AuthOAuthBearer AuthType = "oauth-bearer"   // Gemini OAuth / Google Cloud Code
	AuthNone        AuthType = "none"           // No auth (e.g. EchoProvider for testing)
)

// StaticKeyAuth is an AuthStrategy for providers that use a simple
// static API key with no refresh (OpenAI, Anthropic, Gemini REST static).
type StaticKeyAuth struct {
	Type_   AuthType
	Key     string
	Headers map[string]string // extra headers (e.g. anthropic-version)
}

func (s *StaticKeyAuth) Type() AuthType { return s.Type_ }
func (s *StaticKeyAuth) Apply(req *http.Request) error {
	switch s.Type_ {
	case AuthBearer:
		req.Header.Set("Authorization", "Bearer "+s.Key)
	case AuthXAPIKey:
		req.Header.Set("x-api-key", s.Key)
	case AuthXGoogAPIKey:
		req.Header.Set("x-goog-api-key", s.Key)
	}
	for k, v := range s.Headers {
		req.Header.Set(k, v)
	}
	return nil
}
func (s *StaticKeyAuth) Refresh(ctx context.Context) error { return nil }
func (s *StaticKeyAuth) NeedsRefresh() bool                { return false }

// NoAuth is an AuthStrategy for providers that require no authentication.
type NoAuth struct{}

func (NoAuth) Type() AuthType                          { return AuthNone }
func (NoAuth) Apply(req *http.Request) error           { return nil }
func (NoAuth) Refresh(ctx context.Context) error       { return nil }
func (NoAuth) NeedsRefresh() bool                      { return false }
