package proxy

import (
	"net/http"

	"github.com/deungjaho/hydra/internal/provider"
)

// proxyDeps implements provider.ProxyDeps by wrapping the proxy
// package's existing transformation and communication functions.
// This breaks the circular dependency: the proxy package can now
// import the provider package (for Router, Provider, etc.) while
// the provider package does not import proxy.
type proxyDeps struct{}

// Compile-time check that proxyDeps implements provider.ProxyDeps.
var _ provider.ProxyDeps = proxyDeps{}

func (proxyDeps) TransformRequest(openaiReq map[string]any, projectID, sessionUUID string, requestN uint64) map[string]any {
	return TransformRequest(openaiReq, projectID, sessionUUID, requestN)
}

func (proxyDeps) AnthropicTransformRequest(anthropicReq map[string]any, projectID, sessionUUID string, requestN uint64) map[string]any {
	return AnthropicTransformRequest(anthropicReq, projectID, sessionUUID, requestN)
}

func (proxyDeps) TransformResponse(geminiResp map[string]any, model string) map[string]any {
	return TransformResponse(geminiResp, model)
}

func (proxyDeps) AnthropicTransformResponse(geminiResp map[string]any, model string) map[string]any {
	return AnthropicTransformResponse(geminiResp, model)
}

func (proxyDeps) SendRequest(client *http.Client, token, projectID string, body []byte, stream bool) (*http.Response, error) {
	return SendRequest(client, token, projectID, body, stream)
}

func (proxyDeps) MapModel(model string) string {
	return MapModel(model)
}

func (proxyDeps) SupportedModels() []string {
	return SupportedModels
}

// buildProviders constructs the provider list from config. For Phase 1,
// only "google-cloud-code" is supported; other types are ignored
// (they'll be implemented in Phase 2).
func buildProviders(cfg []configProviderSpec) []provider.Provider {
	var providers []provider.Provider
	for _, pc := range cfg {
		if !pc.Enabled {
			continue
		}
		switch pc.Type {
		case "google-cloud-code":
			providers = append(providers, &provider.GoogleCloudCodeProvider{
				Deps: proxyDeps{},
				Caps: provider.CapabilitySet{
					SupportsStreaming:       true,
					SupportsFunctionCalling: true,
					SupportsVision:          true,
					SupportsThinking:        true,
					MaxOutputTokens:         65536,
					ContextWindow:           1000000,
				},
			})
		default:
			// Unknown provider types are silently skipped for now.
			// Phase 2 will add real OpenAI/Anthropic adapters.
		}
	}
	if len(providers) == 0 {
		// Backward compatibility: default to a single google-cloud-code provider.
		providers = append(providers, &provider.GoogleCloudCodeProvider{
			Deps: proxyDeps{},
			Caps: provider.CapabilitySet{
				SupportsStreaming:       true,
				SupportsFunctionCalling: true,
				SupportsVision:          true,
				SupportsThinking:        true,
				MaxOutputTokens:         65536,
				ContextWindow:           1000000,
			},
		})
	}
	return providers
}

// configProviderSpec is a lightweight struct for passing provider config
// to buildProviders. It mirrors config.ProviderConfig without importing
// the config package here (it's already imported by server.go).
type configProviderSpec struct {
	ID      string
	Type    string
	Enabled bool
}
