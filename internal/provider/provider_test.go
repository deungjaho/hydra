package provider

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// --- Compile-time interface satisfaction checks ---

// These compile-time checks prove that both providers satisfy the
// Provider interface. If either provider stops satisfying the interface,
// the build fails.
var _ Provider = (*GoogleCloudCodeProvider)(nil)
var _ Provider = (*EchoProvider)(nil)

// --- testDeps: a test-only ProxyDeps implementation ---

type testDeps struct {
	modelMap  map[string]string
	models    []string
	// transformResp is the response returned by TransformResponse.
	transformResp map[string]any
}

func (d *testDeps) TransformRequest(req map[string]any, _, _ string, _ uint64) map[string]any {
	return req
}
func (d *testDeps) AnthropicTransformRequest(req map[string]any, _, _ string, _ uint64) map[string]any {
	return req
}
func (d *testDeps) TransformResponse(_ map[string]any, _ string) map[string]any {
	if d.transformResp != nil {
		return d.transformResp
	}
	return map[string]any{
		"choices": []any{
			map[string]any{
				"message":      map[string]any{"content": "Hello from Gemini"},
				"finish_reason": "stop",
			},
		},
		"usage": map[string]any{
			"prompt_tokens":     int64(5),
			"completion_tokens": int64(3),
		},
	}
}
func (d *testDeps) AnthropicTransformResponse(_ map[string]any, _ string) map[string]any {
	if d.transformResp != nil {
		return d.transformResp
	}
	return map[string]any{
		"content": []any{
			map[string]any{"type": "text", "text": "Hello from Gemini"},
		},
		"stop_reason": "end_turn",
		"usage": map[string]any{
			"input_tokens":  int64(5),
			"output_tokens": int64(3),
		},
	}
}
func (d *testDeps) SendRequest(client *http.Client, _, _ string, body []byte, _ bool) (*http.Response, error) {
	// Use the client's transport (mockTransport in tests) to get a canned response.
	req, _ := http.NewRequest("POST", "http://mock", strings.NewReader(string(body)))
	return client.Do(req)
}
func (d *testDeps) MapModel(model string) string {
	if mapped, ok := d.modelMap[model]; ok {
		return mapped
	}
	return ""
}
func (d *testDeps) SupportedModels() []string {
	if d.models != nil {
		return d.models
	}
	return []string{"gemini-2.5-flash", "gemini-2.5-pro"}
}

// defaultTestDeps returns a testDeps with standard model mappings.
func defaultTestDeps() *testDeps {
	return &testDeps{
		modelMap: map[string]string{
			"gpt-4":            "gemini-2.5-flash",
			"gemini-2.5-flash": "gemini-2.5-flash",
			"claude-sonnet-4-6": "gemini-2.5-flash",
		},
	}
}

// --- Test 1: GoogleCloudCodeProvider satisfies interface at runtime ---

func TestGoogleCloudCodeProviderSatisfiesInterface(t *testing.T) {
	g := &GoogleCloudCodeProvider{
		Deps:        defaultTestDeps(),
		HTTPClient:  &http.Client{},
		AccessToken: "test-token",
		ProjectID:   "test-project",
		Caps: CapabilitySet{
			SupportsStreaming:       true,
			SupportsFunctionCalling: true,
			SupportsVision:          true,
			SupportsThinking:        true,
			MaxOutputTokens:         65536,
			ContextWindow:           1000000,
		},
	}

	if g.ID() != "google-cloud-code" {
		t.Errorf("ID() = %q, want %q", g.ID(), "google-cloud-code")
	}
	if g.DisplayName() == "" {
		t.Error("DisplayName() should not be empty")
	}
	caps := g.Capabilities()
	if !caps.SupportsStreaming {
		t.Error("Capabilities().SupportsStreaming should be true")
	}
	if g.AuthStrategy().Type() != AuthOAuthBearer {
		t.Errorf("AuthStrategy().Type() = %q, want %q", g.AuthStrategy().Type(), AuthOAuthBearer)
	}
	models, err := g.AvailableModels(context.Background())
	if err != nil {
		t.Fatalf("AvailableModels error: %v", err)
	}
	if len(models) == 0 {
		t.Error("AvailableModels should return non-empty list")
	}
	mapped, ok := g.ResolveModel("gpt-4")
	if !ok {
		t.Error("ResolveModel(gpt-4) should succeed")
	}
	if mapped != "gemini-2.5-flash" {
		t.Errorf("ResolveModel(gpt-4) = %q, want %q", mapped, "gemini-2.5-flash")
	}
}

// --- Test 2: EchoProvider satisfies interface at runtime ---

func TestEchoProviderSatisfiesInterface(t *testing.T) {
	e := &EchoProvider{
		Caps: CapabilitySet{
			SupportsVision:    true,
			SupportsThinking:  true,
			MaxOutputTokens:   4096,
			ContextWindow:     8192,
		},
	}

	if e.ID() != "echo" {
		t.Errorf("ID() = %q, want %q", e.ID(), "echo")
	}
	if e.AuthStrategy().Type() != AuthNone {
		t.Errorf("AuthStrategy().Type() = %q, want %q", e.AuthStrategy().Type(), AuthNone)
	}
	models, err := e.AvailableModels(context.Background())
	if err != nil {
		t.Fatalf("AvailableModels error: %v", err)
	}
	if len(models) != 1 || models[0] != "echo-model" {
		t.Errorf("AvailableModels = %v, want [echo-model]", models)
	}
	mapped, ok := e.ResolveModel("any-model")
	if !ok {
		t.Error("ResolveModel(any-model) should succeed")
	}
	if mapped != "any-model" {
		t.Errorf("ResolveModel(any-model) = %q, want %q", mapped, "any-model")
	}
	// Test ChatCompletions returns canned response.
	resp, err := e.ChatCompletions(context.Background(), &Request{
		Model: "test-model",
		RawBody: map[string]any{"model": "test-model", "messages": []any{}},
		Format: FormatOpenAI,
	})
	if err != nil {
		t.Fatalf("ChatCompletions error: %v", err)
	}
	if resp.Content != "echo" {
		t.Errorf("Content = %q, want %q", resp.Content, "echo")
	}
	if resp.FinishReason != FinishStop {
		t.Errorf("FinishReason = %q, want %q", resp.FinishReason, FinishStop)
	}
}

// --- Test 3: Router failover on simulated 429 ---

func TestRouterFailoverOn429(t *testing.T) {
	mockUpstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":{"code":429,"message":"quota exceeded"}}`))
	}))
	defer mockUpstream.Close()

	mockClient := &http.Client{
		Transport: &mockTransport{status: 429, body: `{"error":{"code":429,"message":"quota exceeded"}}`},
	}

	google := &GoogleCloudCodeProvider{
		Deps:        defaultTestDeps(),
		HTTPClient:  mockClient,
		AccessToken: "test-token",
		ProjectID:   "test-project",
		Caps: CapabilitySet{
			SupportsStreaming:       true,
			SupportsFunctionCalling: true,
			MaxOutputTokens:         65536,
			ContextWindow:           1000000,
		},
	}

	echo := &EchoProvider{
		Caps: CapabilitySet{
			MaxOutputTokens: 4096,
			ContextWindow:   8192,
		},
	}

	router := NewRouter(google, echo)

	req := &Request{
		Model:   "gemini-2.5-flash",
		Format:  FormatOpenAI,
		RawBody: map[string]any{"model": "gemini-2.5-flash", "messages": []any{map[string]any{"role": "user", "content": "hi"}}},
	}

	// Execute should failover from GoogleCloudCode (429) to Echo.
	resp, err := router.Execute(context.Background(), req)
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if resp.Content != "echo" {
		t.Errorf("Content = %q, want %q (should have failovered to Echo)", resp.Content, "echo")
	}
}

// --- Test 4: Router returns structured error when no compatible provider ---

func TestRouterNoCompatibleProvider(t *testing.T) {
	google := &GoogleCloudCodeProvider{
		Deps:        defaultTestDeps(),
		HTTPClient:  &http.Client{},
		AccessToken: "test-token",
		ProjectID:   "test-project",
		Caps: CapabilitySet{
			SupportsVision: false,
		},
	}

	echo := &EchoProvider{
		Caps: CapabilitySet{
			SupportsVision: false,
		},
	}

	router := NewRouter(google, echo)

	req := &Request{
		Model: "some-vision-model",
		RequiredCapabilities: CapabilitySet{
			SupportsVision: true,
		},
		Format:  FormatOpenAI,
		RawBody: map[string]any{"model": "some-vision-model", "messages": []any{}},
	}

	_, err := router.Route(context.Background(), req)
	if err == nil {
		t.Fatal("expected error when no provider has required capability")
	}

	pe, ok := err.(*ProviderError)
	if !ok {
		t.Fatalf("expected *ProviderError, got %T: %v", err, err)
	}
	if pe.Code != ErrBadRequest {
		t.Errorf("Code = %q, want %q", pe.Code, ErrBadRequest)
	}
	if pe.Retryable != RetryNone {
		t.Errorf("Retryable = %v, want RetryNone", pe.Retryable)
	}
}

// --- Test 5: Router does not failover on non-retryable error (401) ---

func TestRouterNoFailoverOn401(t *testing.T) {
	mockClient := &http.Client{
		Transport: &mockTransport{status: 401, body: `{"error":{"message":"unauthorized"}}`},
	}

	google := &GoogleCloudCodeProvider{
		Deps:        defaultTestDeps(),
		HTTPClient:  mockClient,
		AccessToken: "expired-token",
		ProjectID:   "test-project",
		Caps:        CapabilitySet{SupportsStreaming: true, MaxOutputTokens: 65536, ContextWindow: 1000000},
	}

	echo := &EchoProvider{
		Caps: CapabilitySet{MaxOutputTokens: 4096, ContextWindow: 8192},
	}

	router := NewRouter(google, echo)

	req := &Request{
		Model:   "gemini-2.5-flash",
		Format:  FormatOpenAI,
		RawBody: map[string]any{"model": "gemini-2.5-flash", "messages": []any{map[string]any{"role": "user", "content": "hi"}}},
	}

	_, err := router.Execute(context.Background(), req)
	if err == nil {
		t.Fatal("expected error on 401")
	}

	pe, ok := err.(*ProviderError)
	if !ok {
		t.Fatalf("expected *ProviderError, got %T: %v", err, err)
	}
	if pe.Code != ErrAuthentication {
		t.Errorf("Code = %q, want %q", pe.Code, ErrAuthentication)
	}
	if pe.Retryable != RetryNone {
		t.Errorf("Retryable = %v, want RetryNone (401 should not failover)", pe.Retryable)
	}
}

// --- Test 6: CapabilitySet.CanServe ---

func TestCapabilitySetCanServe(t *testing.T) {
	caps := CapabilitySet{
		SupportsStreaming:       true,
		SupportsFunctionCalling: true,
		SupportsVision:          false,
		MaxOutputTokens:         4096,
		ContextWindow:           8192,
	}

	if !caps.CanServe(CapabilitySet{}) {
		t.Error("CanServe with no requirements should be true")
	}
	if !caps.CanServe(CapabilitySet{SupportsStreaming: true}) {
		t.Error("CanServe with streaming requirement should be true")
	}
	if caps.CanServe(CapabilitySet{SupportsVision: true}) {
		t.Error("CanServe with vision requirement should be false")
	}
	if caps.CanServe(CapabilitySet{MaxOutputTokens: 8192}) {
		t.Error("CanServe with MaxOutputTokens > our limit should be false")
	}
	if !caps.CanServe(CapabilitySet{MaxOutputTokens: 2048}) {
		t.Error("CanServe with MaxOutputTokens <= our limit should be true")
	}
}

// --- Test 7: ClassifyHTTPError ---

func TestClassifyHTTPError(t *testing.T) {
	tests := []struct {
		status     int
		wantCode   ErrorCode
		wantRetry  RetryDecision
	}{
		{400, ErrBadRequest, RetryNone},
		{401, ErrAuthentication, RetryNone},
		{403, ErrPermission, RetryNone},
		{404, ErrNotFound, RetryImmediately},
		{429, ErrRateLimit, RetryAfterBackoff},
		{500, ErrServerError, RetryAfterBackoff},
		{502, ErrGateway, RetryImmediately},
		{503, ErrGateway, RetryImmediately},
		{504, ErrGateway, RetryImmediately},
		{529, ErrOverloaded, RetryAfterBackoff},
	}
	for _, tt := range tests {
		pe := ClassifyHTTPError("test", tt.status, "body")
		if pe.Code != tt.wantCode {
			t.Errorf("status %d: Code = %q, want %q", tt.status, pe.Code, tt.wantCode)
		}
		if pe.Retryable != tt.wantRetry {
			t.Errorf("status %d: Retryable = %v, want %v", tt.status, pe.Retryable, tt.wantRetry)
		}
	}
}

// --- mockTransport returns a canned HTTP response for testing ---

type mockTransport struct {
	status int
	body   string
}

func (m *mockTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.Body != nil {
		_, _ = io.Copy(io.Discard, req.Body)
		req.Body.Close()
	}
	return &http.Response{
		StatusCode: m.status,
		Body:       io.NopCloser(strings.NewReader(m.body)),
		Header:     make(http.Header),
	}, nil
}

var _ http.RoundTripper = (*mockTransport)(nil)

// --- Test 8: GoogleCloudCodeProvider ChatCompletions with mock 200 ---

func TestGoogleCloudCodeProviderChatCompletionsSuccess(t *testing.T) {
	// The mock deps' TransformResponse returns an OpenAI-format response.
	// The mock transport returns a 200 with any body (the deps ignore it
	// and return the canned response).
	mockClient := &http.Client{
		Transport: &mockTransport{status: 200, body: `{"response":{}}`},
	}

	g := &GoogleCloudCodeProvider{
		Deps:        defaultTestDeps(),
		HTTPClient:  mockClient,
		AccessToken: "test-token",
		ProjectID:   "test-project",
		Caps:        CapabilitySet{SupportsStreaming: true, MaxOutputTokens: 65536, ContextWindow: 1000000},
	}

	resp, err := g.ChatCompletions(context.Background(), &Request{
		Model:   "gemini-2.5-flash",
		Format:  FormatOpenAI,
		RawBody: map[string]any{"model": "gemini-2.5-flash", "messages": []any{map[string]any{"role": "user", "content": "hi"}}},
	})
	if err != nil {
		t.Fatalf("ChatCompletions error: %v", err)
	}
	if !strings.Contains(resp.Content, "Hello from Gemini") {
		t.Errorf("Content = %q, want it to contain 'Hello from Gemini'", resp.Content)
	}
	if resp.FinishReason != FinishStop {
		t.Errorf("FinishReason = %q, want %q", resp.FinishReason, FinishStop)
	}
	if resp.Usage == nil {
		t.Fatal("Usage should not be nil")
	}
	if resp.Usage.PromptTokens != 5 {
		t.Errorf("PromptTokens = %d, want 5", resp.Usage.PromptTokens)
	}
}

// --- Test 9: EchoProvider streaming ---

func TestEchoProviderStreaming(t *testing.T) {
	e := &EchoProvider{Caps: CapabilitySet{}}
	stream, err := e.StreamChatCompletions(context.Background(), &Request{
		Model: "test", Format: FormatOpenAI, RawBody: map[string]any{},
	})
	if err != nil {
		t.Fatalf("StreamChatCompletions error: %v", err)
	}

	var chunks []*StreamChunk
	for {
		ch, err := stream.Next(context.Background())
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("Next error: %v", err)
		}
		chunks = append(chunks, ch)
	}

	if len(chunks) != 2 {
		t.Fatalf("expected 2 chunks, got %d", len(chunks))
	}
	if chunks[0].DeltaContent != "echo" {
		t.Errorf("chunk 0 DeltaContent = %q, want %q", chunks[0].DeltaContent, "echo")
	}
	if chunks[1].FinishReason != FinishStop {
		t.Errorf("chunk 1 FinishReason = %q, want %q", chunks[1].FinishReason, FinishStop)
	}
}

// --- Test 10: GoogleCloudCodeProvider with ModelMap (no Deps) for lightweight selection ---

func TestGoogleCloudCodeProviderModelMapSelection(t *testing.T) {
	g := &GoogleCloudCodeProvider{
		ModelMap: map[string]string{
			"gpt-4":            "gemini-2.5-flash",
			"gemini-2.5-flash": "gemini-2.5-flash",
		},
		Caps: CapabilitySet{SupportsStreaming: true, MaxOutputTokens: 65536, ContextWindow: 1000000},
	}

	// ResolveModel works without Deps (uses ModelMap).
	mapped, ok := g.ResolveModel("gpt-4")
	if !ok {
		t.Error("ResolveModel(gpt-4) should succeed via ModelMap")
	}
	if mapped != "gemini-2.5-flash" {
		t.Errorf("ResolveModel(gpt-4) = %q, want %q", mapped, "gemini-2.5-flash")
	}

	// Unknown model returns false.
	_, ok = g.ResolveModel("unknown-model")
	if ok {
		t.Error("ResolveModel(unknown-model) should fail")
	}

	// ChatCompletions without Deps returns error.
	_, err := g.ChatCompletions(context.Background(), &Request{
		Model: "gpt-4", Format: FormatOpenAI, RawBody: map[string]any{},
	})
	if err == nil {
		t.Fatal("ChatCompletions without Deps should return error")
	}
	pe, ok := err.(*ProviderError)
	if !ok {
		t.Fatalf("expected *ProviderError, got %T", err)
	}
	if pe.Code != ErrBadRequest {
		t.Errorf("Code = %q, want %q", pe.Code, ErrBadRequest)
	}
}

// Suppress unused import warning for httptest (used in TestRouterFailoverOn429).
var _ = httptest.NewServer
var _ = json.Marshal
