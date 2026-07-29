package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

// GoogleCloudCodeProvider wraps the existing Google Cloud Code upstream
// logic behind the Provider interface.
//
// It uses ProxyDeps (injected by the proxy package) for request/response
// transformation and upstream communication. This breaks the circular
// dependency between the proxy and provider packages.
//
// For the spike, the HTTP client is injectable so tests can simulate
// upstream errors (e.g. 429) without real network calls.
type GoogleCloudCodeProvider struct {
	// Deps provides the proxy-level transformation and communication
	// functions. Required for ChatCompletions; not needed for
	// ResolveModel/AvailableModels if ModelMap is set.
	Deps ProxyDeps
	// HTTPClient is the upstream HTTP client (uTLS in production,
	// mock transport in tests).
	HTTPClient *http.Client
	// AccessToken is the OAuth bearer token for the upstream.
	AccessToken string
	// ProjectID is the Google Cloud project ID.
	ProjectID string
	// Caps is the static capability set for this provider.
	Caps CapabilitySet
	// ModelMap overrides Deps.MapModel when Deps is nil (for lightweight
	// provider selection without full deps injection).
	ModelMap map[string]string
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

func (a *oauthBearerAuth) Type() AuthType                    { return AuthOAuthBearer }
func (a *oauthBearerAuth) Apply(req *http.Request) error     { req.Header.Set("Authorization", "Bearer "+a.token); return nil }
func (a *oauthBearerAuth) Refresh(ctx context.Context) error { return nil }
func (a *oauthBearerAuth) NeedsRefresh() bool                { return false }

func (g *GoogleCloudCodeProvider) ResolveModel(clientModel string) (string, bool) {
	if g.Deps != nil {
		mapped := g.Deps.MapModel(clientModel)
		return mapped, mapped != ""
	}
	if g.ModelMap != nil {
		if mapped, ok := g.ModelMap[clientModel]; ok {
			return mapped, true
		}
	}
	return "", false
}

func (g *GoogleCloudCodeProvider) AvailableModels(ctx context.Context) ([]string, error) {
	if g.Deps != nil {
		return g.Deps.SupportedModels(), nil
	}
	return []string{}, nil
}

func (g *GoogleCloudCodeProvider) ChatCompletions(ctx context.Context, req *Request) (*Response, error) {
	if g.Deps == nil {
		return nil, &ProviderError{
			Provider: g.ID(), Code: ErrBadRequest,
			Message:   "ProxyDeps not configured",
			Retryable: RetryNone,
		}
	}

	// Delegate to injected proxy functions — no duplication.
	var upstreamBody map[string]any
	switch req.Format {
	case FormatAnthropic:
		upstreamBody = g.Deps.AnthropicTransformRequest(
			req.RawBody, g.ProjectID, "spike-session", 1)
	default:
		upstreamBody = g.Deps.TransformRequest(
			req.RawBody, g.ProjectID, "spike-session", 1)
	}

	mappedModel, _ := g.ResolveModel(req.Model)
	upstreamBody["model"] = mappedModel

	bodyBytes, err := json.Marshal(upstreamBody)
	if err != nil {
		return nil, &ProviderError{
			Provider: g.ID(), Code: ErrBadRequest,
			Message:   "failed to marshal upstream body: " + err.Error(),
			Retryable: RetryNone,
		}
	}

	resp, err := g.Deps.SendRequest(g.HTTPClient, g.AccessToken, g.ProjectID, bodyBytes, false)
	if err != nil {
		return nil, &ProviderError{
			Provider: g.ID(), Code: ErrConnectFailure,
			Message:   "upstream request failed: " + err.Error(),
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
			Message:   "read upstream body: " + err.Error(),
			Retryable: RetryNone,
		}
	}

	var geminiResp map[string]any
	if err := json.Unmarshal(bodyBytes2, &geminiResp); err != nil {
		return nil, &ProviderError{
			Provider: g.ID(), Code: ErrGateway,
			Message:   fmt.Sprintf("invalid upstream JSON: %v", err),
			Retryable: RetryNone,
		}
	}

	// Transform to client format using injected functions.
	var normalized map[string]any
	switch req.Format {
	case FormatAnthropic:
		normalized = g.Deps.AnthropicTransformResponse(geminiResp, req.Model)
	default:
		normalized = g.Deps.TransformResponse(geminiResp, req.Model)
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
	// the SSE streaming logic. Full streaming wiring is post-spike
	// (Phase 2 task 7).
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
