package provider

import "context"

// EchoProvider is a trivial provider for spike testing. It returns a
// canned response with no network calls, proving the Provider interface
// is provider-agnostic (not Google-specific).
type EchoProvider struct {
	Caps CapabilitySet
}

func (e *EchoProvider) ID() string          { return "echo" }
func (e *EchoProvider) DisplayName() string { return "Echo (test)" }
func (e *EchoProvider) Capabilities() CapabilitySet { return e.Caps }
func (e *EchoProvider) AuthStrategy() AuthStrategy   { return NoAuth{} }

func (e *EchoProvider) ResolveModel(clientModel string) (string, bool) {
	return clientModel, true // echo serves any model
}

func (e *EchoProvider) AvailableModels(ctx context.Context) ([]string, error) {
	return []string{"echo-model"}, nil
}

func (e *EchoProvider) ChatCompletions(ctx context.Context, req *Request) (*Response, error) {
	return &Response{
		Model:        req.Model,
		Content:      "echo",
		FinishReason: FinishStop,
		Usage:        &Usage{PromptTokens: 1, CompletionTokens: 1},
	}, nil
}

func (e *EchoProvider) StreamChatCompletions(ctx context.Context, req *Request) (Stream, error) {
	return NewEchoStream("echo"), nil
}

func (e *EchoProvider) FetchQuota(ctx context.Context) (*QuotaState, error) { return nil, nil }
func (e *EchoProvider) HealthCheck(ctx context.Context) error               { return nil }
