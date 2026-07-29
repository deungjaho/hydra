package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"github.com/deungjaho/hydra/internal/proxy"
)

// GoogleCloudCodeProvider wraps the existing Google Cloud Code upstream
// logic (internal/proxy package) behind the Provider interface.
//
// It calls existing exported functions — NO duplication, NO behavioral
// change. The existing proxy package remains the source of truth for
// request/response transformation and upstream communication.
//
// For the spike, the HTTP client is injectable so tests can simulate
// upstream errors (e.g. 429) without real network calls.
type GoogleCloudCodeProvider struct {
	// HTTPClient is the upstream HTTP client (uTLS in production,
	// mock transport in tests).
	HTTPClient *http.Client
	// AccessToken is the OAuth bearer token for the upstream.
	AccessToken string
	// ProjectID is the Google Cloud project ID.
	ProjectID string
	// Caps is the static capability set for this provider.
	Caps CapabilitySet
}

func (g *GoogleCloudCodeProvider) ID() string          { return "google-cloud-code" }
func (g *GoogleCloudCodeProvider) DisplayName() string { return "Google Cloud Code (v1internal)" }

func (g *GoogleCloudCodeProvider) Capabilities() CapabilitySet { return g.Caps }

func (g *GoogleCloudCodeProvider) AuthStrategy() AuthStrategy {
	return &oauthBearerAuth{token: g.AccessToken}
}

// oauthBearerAuth is a simple OAuth bearer auth for the spike.
// In production, this would wrap ensureFreshToken with single-flight
// refresh (per LiteLLM Vertex pattern).
type oauthBearerAuth struct {
	token string
}

func (a *oauthBearerAuth) Type() AuthType                          { return AuthOAuthBearer }
func (a *oauthBearerAuth) Apply(req *http.Request) error           { req.Header.Set("Authorization", "Bearer "+a.token); return nil }
func (a *oauthBearerAuth) Refresh(ctx context.Context) error       { return nil }
func (a *oauthBearerAuth) NeedsRefresh() bool                      { return false }

func (g *GoogleCloudCodeProvider) ResolveModel(clientModel string) (string, bool) {
	mapped := proxy.MapModel(clientModel)
	// Google Cloud Code can serve any model that MapModel resolves.
	// In production, this would also check AvailableModels.
	return mapped, mapped != ""
}

func (g *GoogleCloudCodeProvider) AvailableModels(ctx context.Context) ([]string, error) {
	return proxy.SupportedModels, nil
}

func (g *GoogleCloudCodeProvider) ChatCompletions(ctx context.Context, req *Request) (*Response, error) {
	// Delegate to existing proxy functions — no duplication.
	var upstreamBody map[string]any
	switch req.Format {
	case FormatAnthropic:
		upstreamBody = proxy.AnthropicTransformRequest(
			req.RawBody, g.ProjectID, "spike-session", 1)
	default:
		upstreamBody = proxy.TransformRequest(
			req.RawBody, g.ProjectID, "spike-session", 1)
	}

	mappedModel, _ := g.ResolveModel(req.Model)
	upstreamBody["model"] = mappedModel

	bodyBytes, err := json.Marshal(upstreamBody)
	if err != nil {
		return nil, &ProviderError{
			Provider: g.ID(), Code: ErrBadRequest,
			Message: "failed to marshal upstream body: " + err.Error(),
			Retryable: RetryNone,
		}
	}

	resp, err := proxy.SendRequest(g.HTTPClient, g.AccessToken, g.ProjectID, bodyBytes, false)
	if err != nil {
		return nil, &ProviderError{
			Provider: g.ID(), Code: ErrConnectFailure,
			Message: "upstream request failed: " + err.Error(),
			Retryable: RetryImmediately,
		}
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		bodyText, _ := io.ReadAll(resp.Body)
		return nil, ClassifyHTTPError(g.ID(), resp.StatusCode, string(bodyText))
	}

	bodyBytes2, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, &ProviderError{
			Provider: g.ID(), Code: ErrGateway,
			Message: "read upstream body: " + err.Error(),
			Retryable: RetryNone,
		}
	}

	var geminiResp map[string]any
	if err := json.Unmarshal(bodyBytes2, &geminiResp); err != nil {
		return nil, &ProviderError{
			Provider: g.ID(), Code: ErrGateway,
			Message: fmt.Sprintf("invalid upstream JSON: %v", err),
			Retryable: RetryNone,
		}
	}

	// Transform to client format using existing functions.
	var normalized map[string]any
	switch req.Format {
	case FormatAnthropic:
		normalized = proxy.AnthropicTransformResponse(geminiResp, req.Model)
	default:
		normalized = proxy.TransformResponse(geminiResp, req.Model)
	}

	return g.mapResponse(normalized, req.Model), nil
}

func (g *GoogleCloudCodeProvider) mapResponse(normalized map[string]any, model string) *Response {
	out := &Response{Model: model, RawBody: normalized}

	// Extract content from the normalized response.
	if choices, ok := normalized["choices"].([]any); ok && len(choices) > 0 {
		if choice, ok := choices[0].(map[string]any); ok {
			if msg, ok := choice["message"].(map[string]any); ok {
				if c, ok := msg["content"].(string); ok {
					out.Content = c
				}
				if tcs, ok := msg["tool_calls"].([]any); ok {
					for _, tcAny := range tcs {
						if tc, ok := tcAny.(map[string]any); ok {
							var id, name, args string
							if v, ok := tc["id"].(string); ok {
								id = v
							}
							if fn, ok := tc["function"].(map[string]any); ok {
								if v, ok := fn["name"].(string); ok {
									name = v
								}
								if v, ok := fn["arguments"].(string); ok {
									args = v
								}
							}
							out.ToolCalls = append(out.ToolCalls, ToolCall{ID: id, Name: name, Args: args})
						}
					}
				}
			}
			if fr, ok := choice["finish_reason"].(string); ok {
				out.FinishReason = FinishReason(fr)
			}
		}
	}
	// Anthropic format.
	if content, ok := normalized["content"].([]any); ok {
		for _, blockAny := range content {
			if block, ok := blockAny.(map[string]any); ok {
				if t, _ := block["type"].(string); t == "text" {
					if text, ok := block["text"].(string); ok {
						out.Content += text
					}
				}
			}
		}
	}
	if fr, ok := normalized["stop_reason"].(string); ok {
		switch fr {
		case "end_turn":
			out.FinishReason = FinishStop
		case "max_tokens":
			out.FinishReason = FinishLength
		case "tool_use":
			out.FinishReason = FinishToolCalls
		}
	}

	// Extract usage. Values may be int64 (from TransformResponse) or
	// float64 (from JSON unmarshal) depending on the path.
	if usage, ok := normalized["usage"].(map[string]any); ok {
		u := &Usage{}
		u.PromptTokens = toInt64Val(usage["prompt_tokens"])
		if u.PromptTokens == 0 {
			u.PromptTokens = toInt64Val(usage["input_tokens"])
		}
		u.CompletionTokens = toInt64Val(usage["completion_tokens"])
		if u.CompletionTokens == 0 {
			u.CompletionTokens = toInt64Val(usage["output_tokens"])
		}
		u.CachedTokens = toInt64Val(usage["cached_tokens"])
		if u.CachedTokens == 0 {
			u.CachedTokens = toInt64Val(usage["cache_read_input_tokens"])
		}
		u.ThoughtTokens = toInt64Val(usage["thought_tokens"])
		out.Usage = u
	}
	return out
}

// toInt64Val extracts an int64 from an any value that may be int64,
// float64, or json.Number.
func toInt64Val(v any) int64 {
	switch n := v.(type) {
	case int64:
		return n
	case float64:
		return int64(n)
	case int:
		return int64(n)
	}
	return 0
}

func (g *GoogleCloudCodeProvider) StreamChatCompletions(ctx context.Context, req *Request) (Stream, error) {
	// For the spike, streaming is not fully implemented — it would wrap
	// proxy.AnthropicStreamState / the OpenAI SSE streaming logic.
	// This is a stub that returns a not-implemented error. Full streaming
	// wiring is post-spike.
	return nil, &ProviderError{
		Provider: g.ID(), Code: ErrBadRequest,
		Message:   "streaming not implemented in spike",
		Retryable: RetryNone,
	}
}

func (g *GoogleCloudCodeProvider) FetchQuota(ctx context.Context) (*QuotaState, error) {
	// For the spike, quota is not wired — would call existing
	// account.ListAccounts + quota logic in production.
	return nil, nil
}

func (g *GoogleCloudCodeProvider) HealthCheck(ctx context.Context) error {
	// For the spike, health check delegates to a simple upstream probe.
	// In production, this would call fetchAvailableModels.
	return nil
}
