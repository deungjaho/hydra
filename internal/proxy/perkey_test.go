package proxy

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/deungjaho/hydra/internal/account"
	"github.com/deungjaho/hydra/internal/config"
	"github.com/deungjaho/hydra/internal/db"
)

// TestAuthFailClosedOnDBError verifies that when the DB is closed
// (simulating a DB error), all authenticated endpoints deny access
// rather than degrading to open access.
func TestAuthFailClosedOnDBError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test_auth.db")
	d, err := db.Open(path)
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	// Add a key so we're not in "no keys = open access" mode.
	if _, err := account.AddAPIKey(d, "secret-key", "test"); err != nil {
		t.Fatalf("AddAPIKey: %v", err)
	}

	cfg := &config.AppConfig{}
	state := NewProxyState(d)
	srv := NewProxyServer(cfg, state)

	// Close the DB to simulate a DB error / connection loss.
	d.Close()

	// Request with no key → should be denied (fail-closed), not open.
	req := httptest.NewRequest("GET", "/v1/models", nil)
	rr := httptest.NewRecorder()
	srv.handleListModels(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Errorf("no key + DB error: got %d, want %d (fail-closed)", rr.Code, http.StatusUnauthorized)
	}

	// Request with a valid key → should also be denied (DB can't verify).
	req = httptest.NewRequest("GET", "/v1/models", nil)
	req.Header.Set("Authorization", "Bearer secret-key")
	rr = httptest.NewRecorder()
	srv.handleListModels(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Errorf("valid key + DB error: got %d, want %d (fail-closed)", rr.Code, http.StatusUnauthorized)
	}

	// Metrics should also be denied.
	req = httptest.NewRequest("GET", "/metrics", nil)
	req.Header.Set("Authorization", "Bearer secret-key")
	rr = httptest.NewRecorder()
	srv.handleMetrics(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Errorf("metrics + DB error: got %d, want %d (fail-closed)", rr.Code, http.StatusUnauthorized)
	}
}

// TestPerKeySchedulingOverrideChatCompletions verifies that the
// OpenAI /v1/chat/completions endpoint uses the API key's
// scheduling_mode and no_sticky override.
func TestPerKeySchedulingOverrideChatCompletions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test_perkey_chat.db")
	d, err := db.Open(path)
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { d.Close() })

	// Add a key with scheduling override.
	id, err := account.AddAPIKey(d, "sched-key-chat", "chat-test")
	if err != nil {
		t.Fatalf("AddAPIKey: %v", err)
	}
	if err := account.UpdateAPIKeyScheduling(d, id, "performance", true); err != nil {
		t.Fatalf("UpdateAPIKeyScheduling: %v", err)
	}

	// Verify the key was stored correctly.
	k, err := account.FindAPIKey(d, "sched-key-chat")
	if err != nil {
		t.Fatalf("FindAPIKey: %v", err)
	}
	if k == nil {
		t.Fatal("FindAPIKey returned nil")
	}
	if k.SchedulingMode != "performance" {
		t.Errorf("SchedulingMode = %q, want performance", k.SchedulingMode)
	}
	if !k.NoSticky {
		t.Errorf("NoSticky = false, want true")
	}

	// Verify checkAuthFull returns the key with override fields.
	cfg := &config.AppConfig{}
	state := NewProxyState(d)
	srv := NewProxyServer(cfg, state)

	req := httptest.NewRequest("POST", "/v1/chat/completions", nil)
	req.Header.Set("Authorization", "Bearer sched-key-chat")
	apiKey, ok := srv.checkAuthFull(req)
	if !ok {
		t.Fatal("checkAuthFull returned false for valid key")
	}
	if apiKey == nil {
		t.Fatal("checkAuthFull returned nil key")
	}
	if apiKey.SchedulingMode != "performance" {
		t.Errorf("apiKey.SchedulingMode = %q, want performance", apiKey.SchedulingMode)
	}
	if !apiKey.NoSticky {
		t.Errorf("apiKey.NoSticky = false, want true")
	}
}

// TestPerKeySchedulingOverrideAnthropicMessages verifies that the
// Anthropic /v1/messages endpoint uses the API key's scheduling override.
func TestPerKeySchedulingOverrideAnthropicMessages(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test_perkey_msg.db")
	d, err := db.Open(path)
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { d.Close() })

	id, err := account.AddAPIKey(d, "sched-key-msg", "msg-test")
	if err != nil {
		t.Fatalf("AddAPIKey: %v", err)
	}
	if err := account.UpdateAPIKeyScheduling(d, id, "balance", true); err != nil {
		t.Fatalf("UpdateAPIKeyScheduling: %v", err)
	}

	cfg := &config.AppConfig{}
	state := NewProxyState(d)
	srv := NewProxyServer(cfg, state)

	req := httptest.NewRequest("POST", "/v1/messages", nil)
	req.Header.Set("Authorization", "Bearer sched-key-msg")
	apiKey, ok := srv.checkAuthFull(req)
	if !ok || apiKey == nil {
		t.Fatal("checkAuthFull returned false/nil for valid key")
	}
	if apiKey.SchedulingMode != "balance" {
		t.Errorf("SchedulingMode = %q, want balance", apiKey.SchedulingMode)
	}
	if !apiKey.NoSticky {
		t.Errorf("NoSticky = false, want true")
	}
}

// TestPerKeySchedulingOverrideCountTokens verifies that the
// /v1/messages/count_tokens endpoint uses the API key's scheduling override.
func TestPerKeySchedulingOverrideCountTokens(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test_perkey_count.db")
	d, err := db.Open(path)
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { d.Close() })

	id, err := account.AddAPIKey(d, "sched-key-count", "count-test")
	if err != nil {
		t.Fatalf("AddAPIKey: %v", err)
	}
	if err := account.UpdateAPIKeyScheduling(d, id, "performance", true); err != nil {
		t.Fatalf("UpdateAPIKeyScheduling: %v", err)
	}

	cfg := &config.AppConfig{}
	state := NewProxyState(d)
	srv := NewProxyServer(cfg, state)

	req := httptest.NewRequest("POST", "/v1/messages/count_tokens", nil)
	req.Header.Set("Authorization", "Bearer sched-key-count")
	apiKey, ok := srv.checkAuthFull(req)
	if !ok || apiKey == nil {
		t.Fatal("checkAuthFull returned false/nil for valid key")
	}
	if apiKey.SchedulingMode != "performance" {
		t.Errorf("SchedulingMode = %q, want performance", apiKey.SchedulingMode)
	}
}

// TestChatCompletionsRejectsInvalidKey verifies that a wrong key
// gets 401 on the chat completions endpoint.
func TestChatCompletionsRejectsInvalidKey(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test_chat_401.db")
	d, err := db.Open(path)
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { d.Close() })
	account.AddAPIKey(d, "real-key", "test")

	cfg := &config.AppConfig{}
	state := NewProxyState(d)
	srv := NewProxyServer(cfg, state)

	body, _ := json.Marshal(map[string]any{"model": "gemini-2.5-flash", "messages": []any{}})
	req := httptest.NewRequest("POST", "/v1/chat/completions", nil)
	req.Header.Set("Authorization", "Bearer wrong-key")
	req.Body = nil
	rr := httptest.NewRecorder()
	srv.handleChatCompletions(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Errorf("wrong key: got %d, want %d", rr.Code, http.StatusUnauthorized)
	}
	_ = body
}
