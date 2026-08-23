package proxy

import (
	"testing"

	"github.com/deungjaho/hydra/internal/config"
	"github.com/deungjaho/hydra/internal/registry"
)

func newTestProxyServer(cfg *config.AppConfig) *ProxyServer {
	s := &ProxyServer{Config: cfg, Registry: registry.New()}
	s.Registry.RebuildFromConfig(cfg)
	return s
}

func TestResolveAPIProvider(t *testing.T) {
	s := newTestProxyServer(&config.AppConfig{
		APIProviders: []config.APIProviderConfig{
			{Name: "deepseek", BaseURL: "https://api.deepseek.com/v1", APIKey: "sk-ds"},
			{Name: "zhipu", BaseURL: "https://open.bigmodel.cn/api/paas/v4", APIKey: "sk-zp"},
		},
		Models: []config.ModelEntry{
			{ID: "deepseek-chat", Provider: "deepseek"},
			{ID: "deepseek-reasoner", Provider: "deepseek"},
			{ID: "glm-4", Provider: "zhipu"},
		},
	})

	tests := []struct {
		model    string
		wantName string
		wantNil  bool
	}{
		{"deepseek-chat", "deepseek", false},
		{"deepseek-reasoner", "deepseek", false},
		{"glm-4", "zhipu", false},
		{"DEEPSEEK-CHAT", "deepseek", false}, // case-insensitive
		{"gemini-2.5-flash", "", true},       // falls through to antigravity
		{"unknown-model", "", true},
	}

	for _, tt := range tests {
		t.Run(tt.model, func(t *testing.T) {
			p := s.resolveAPIProvider(tt.model)
			if tt.wantNil {
				if p != nil {
					t.Fatalf("expected nil, got %s", p.Name)
				}
				return
			}
			if p == nil {
				t.Fatalf("expected provider %s, got nil", tt.wantName)
			}
			if p.Name != tt.wantName {
				t.Fatalf("expected %s, got %s", tt.wantName, p.Name)
			}
			if p.APIKey == "" {
				t.Fatal("APIKey is empty")
			}
		})
	}
}

func TestRegisteredModelIDs(t *testing.T) {
	s := newTestProxyServer(&config.AppConfig{
		APIProviders: []config.APIProviderConfig{
			{Name: "deepseek", BaseURL: "https://api.deepseek.com", APIKey: "sk-ds"},
			{Name: "zhipu", BaseURL: "https://open.bigmodel.cn", APIKey: "sk-zp"},
		},
		Models: []config.ModelEntry{
			{ID: "deepseek-chat", Provider: "deepseek"},
			{ID: "glm-4", Provider: "zhipu"},
		},
	})
	ids := s.registeredModelIDs()
	if len(ids) != 2 {
		t.Fatalf("expected 2 IDs, got %d", len(ids))
	}
}

func TestAnthropicRequestToOpenAIChat(t *testing.T) {
	req := map[string]any{
		"model":      "deepseek-chat",
		"max_tokens": float64(1024),
		"system":     "You are helpful.",
		"messages": []any{
			map[string]any{
				"role":    "user",
				"content": "hello",
			},
		},
		"stream": true,
	}

	out := anthropicRequestToOpenAIChat(req)

	if out["model"] != "deepseek-chat" {
		t.Fatalf("model: expected deepseek-chat, got %v", out["model"])
	}
	if out["max_tokens"] != float64(1024) {
		t.Fatalf("max_tokens mismatch: %v", out["max_tokens"])
	}
	if out["stream"] != true {
		t.Fatal("stream should be true")
	}

	msgs, ok := out["messages"].([]any)
	if !ok {
		t.Fatal("messages not a slice")
	}
	if len(msgs) != 2 {
		t.Fatalf("expected 2 messages (system+user), got %d", len(msgs))
	}

	// First message should be system.
	sysMsg, ok := msgs[0].(map[string]any)
	if !ok {
		t.Fatal("first message not a map")
	}
	if sysMsg["role"] != "system" {
		t.Fatalf("expected system role, got %v", sysMsg["role"])
	}
	if sysMsg["content"] != "You are helpful." {
		t.Fatalf("system content mismatch: %v", sysMsg["content"])
	}
}

func TestOpenAIChatToAnthropic(t *testing.T) {
	chatResp := map[string]any{
		"id": "chatcmpl-123",
		"choices": []any{
			map[string]any{
				"message": map[string]any{
					"role":    "assistant",
					"content": "Hello!",
				},
				"finish_reason": "stop",
			},
		},
		"usage": map[string]any{
			"prompt_tokens":     float64(10),
			"completion_tokens": float64(5),
		},
	}

	out := openAIChatToAnthropic(chatResp, "deepseek-chat")

	if out["type"] != "message" {
		t.Fatalf("type: expected message, got %v", out["type"])
	}
	if out["model"] != "deepseek-chat" {
		t.Fatalf("model mismatch: %v", out["model"])
	}
	if out["stop_reason"] != "end_turn" {
		t.Fatalf("stop_reason: expected end_turn, got %v", out["stop_reason"])
	}

	content, ok := out["content"].([]any)
	if !ok || len(content) != 1 {
		t.Fatalf("expected 1 content block, got %v", out["content"])
	}
	block, _ := content[0].(map[string]any)
	if block["type"] != "text" {
		t.Fatalf("expected text block, got %v", block["type"])
	}
	if block["text"] != "Hello!" {
		t.Fatalf("text mismatch: %v", block["text"])
	}

	usage, _ := out["usage"].(map[string]any)
	if usage["input_tokens"] != int64(10) {
		t.Fatalf("input_tokens: expected 10, got %v", usage["input_tokens"])
	}
	if usage["output_tokens"] != int64(5) {
		t.Fatalf("output_tokens: expected 5, got %v", usage["output_tokens"])
	}
}

func TestOpenAIChatToResponses(t *testing.T) {
	chatResp := map[string]any{
		"id": "chatcmpl-456",
		"choices": []any{
			map[string]any{
				"message": map[string]any{
					"role":    "assistant",
					"content": "Hi there!",
				},
				"finish_reason": "stop",
			},
		},
		"usage": map[string]any{
			"prompt_tokens":     float64(8),
			"completion_tokens": float64(3),
		},
	}

	out := openAIChatToResponses(chatResp, "glm-4")

	if out["object"] != "response" {
		t.Fatalf("object: expected response, got %v", out["object"])
	}
	if out["model"] != "glm-4" {
		t.Fatalf("model mismatch: %v", out["model"])
	}
	if out["status"] != "completed" {
		t.Fatalf("status: expected completed, got %v", out["status"])
	}

	output, ok := out["output"].([]any)
	if !ok || len(output) != 1 {
		t.Fatalf("expected 1 output item, got %v", out["output"])
	}
	item, _ := output[0].(map[string]any)
	if item["type"] != "message" {
		t.Fatalf("expected message type, got %v", item["type"])
	}
}
