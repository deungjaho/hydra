package provider

import (
	"net/http"
)

// ProxyDeps provides the proxy-level functions that GoogleCloudCodeProvider
// needs. This interface breaks the circular dependency between the proxy
// and provider packages: the proxy package implements this interface and
// injects it into the provider, so the provider package does not need to
// import proxy.
type ProxyDeps interface {
	// TransformRequest transforms an OpenAI ChatCompletions request body
	// to the upstream Gemini format.
	TransformRequest(openaiReq map[string]any, projectID, sessionUUID string, requestN uint64) map[string]any

	// AnthropicTransformRequest transforms an Anthropic Messages request
	// body to the upstream Gemini format.
	AnthropicTransformRequest(anthropicReq map[string]any, projectID, sessionUUID string, requestN uint64) map[string]any

	// TransformResponse transforms a Gemini response to the OpenAI
	// ChatCompletions format.
	TransformResponse(geminiResp map[string]any, model string) map[string]any

	// AnthropicTransformResponse transforms a Gemini response to the
	// Anthropic Messages format.
	AnthropicTransformResponse(geminiResp map[string]any, model string) map[string]any

	// SendRequest sends a request to the upstream and returns the HTTP
	// response.
	SendRequest(client *http.Client, token, projectID string, body []byte, stream bool) (*http.Response, error)

	// MapModel maps a client-requested model name to the upstream model ID.
	MapModel(model string) string

	// SupportedModels returns the list of models supported by the upstream.
	SupportedModels() []string
}

// mockDeps is a test-only implementation of ProxyDeps that returns
// canned values without any real transformation.
type mockDeps struct {
	modelMap map[string]string
}

func (m *mockDeps) TransformRequest(req map[string]any, _, _ string, _ uint64) map[string]any {
	return req
}
func (m *mockDeps) AnthropicTransformRequest(req map[string]any, _, _ string, _ uint64) map[string]any {
	return req
}
func (m *mockDeps) TransformResponse(resp map[string]any, _ string) map[string]any {
	return resp
}
func (m *mockDeps) AnthropicTransformResponse(resp map[string]any, _ string) map[string]any {
	return resp
}
func (m *mockDeps) SendRequest(_ *http.Client, _, _ string, _ []byte, _ bool) (*http.Response, error) {
	return nil, nil
}
func (m *mockDeps) MapModel(model string) string {
	if mapped, ok := m.modelMap[model]; ok {
		return mapped
	}
	return ""
}
func (m *mockDeps) SupportedModels() []string {
	return []string{"gemini-2.5-flash", "gemini-2.5-pro"}
}
