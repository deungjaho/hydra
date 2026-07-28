package proxy

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/deungjaho/hydra/internal/account"
	"github.com/deungjaho/hydra/internal/config"
	"github.com/deungjaho/hydra/internal/db"
)

// newAuthTestServer creates a ProxyServer with a fresh DB and optional
// API keys for auth testing.
func newAuthTestServer(t *testing.T, keys ...string) *ProxyServer {
	t.Helper()
	path := filepath.Join(t.TempDir(), "test.db")
	d, err := db.Open(path)
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { d.Close() })
	for i, k := range keys {
		if _, err := account.AddAPIKey(d, k, "test-key-"+string(rune('a'+i))); err != nil {
			t.Fatalf("AddAPIKey: %v", err)
		}
	}
	cfg := &config.AppConfig{}
	state := NewProxyState(d)
	return NewProxyServer(cfg, state)
}

func TestMetricsRequiresAuth(t *testing.T) {
	srv := newAuthTestServer(t, "secret-key-123")
	req := httptest.NewRequest("GET", "/metrics", nil)
	rr := httptest.NewRecorder()
	srv.handleMetrics(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Errorf("no key: got %d, want %d", rr.Code, http.StatusUnauthorized)
	}
}

func TestMetricsWithValidKey(t *testing.T) {
	srv := newAuthTestServer(t, "secret-key-123")
	req := httptest.NewRequest("GET", "/metrics", nil)
	req.Header.Set("Authorization", "Bearer secret-key-123")
	rr := httptest.NewRecorder()
	srv.handleMetrics(rr, req)
	if rr.Code != http.StatusOK {
		t.Errorf("valid key: got %d, want %d", rr.Code, http.StatusOK)
	}
}

func TestMetricsWithWrongKey(t *testing.T) {
	srv := newAuthTestServer(t, "secret-key-123")
	req := httptest.NewRequest("GET", "/metrics", nil)
	req.Header.Set("Authorization", "Bearer wrong-key")
	rr := httptest.NewRecorder()
	srv.handleMetrics(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Errorf("wrong key: got %d, want %d", rr.Code, http.StatusUnauthorized)
	}
}

func TestMetricsOpenAccessWhenNoKeys(t *testing.T) {
	// First-run scenario: no API keys configured → open access.
	srv := newAuthTestServer(t) // no keys
	req := httptest.NewRequest("GET", "/metrics", nil)
	rr := httptest.NewRecorder()
	srv.handleMetrics(rr, req)
	if rr.Code != http.StatusOK {
		t.Errorf("no keys configured: got %d, want %d (open access)", rr.Code, http.StatusOK)
	}
}

func TestListModelsRequiresAuth(t *testing.T) {
	srv := newAuthTestServer(t, "secret-key-123")
	req := httptest.NewRequest("GET", "/v1/models", nil)
	rr := httptest.NewRecorder()
	srv.handleListModels(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Errorf("no key: got %d, want %d", rr.Code, http.StatusUnauthorized)
	}
}

func TestListModelsWithValidKey(t *testing.T) {
	srv := newAuthTestServer(t, "secret-key-123")
	req := httptest.NewRequest("GET", "/v1/models", nil)
	req.Header.Set("Authorization", "Bearer secret-key-123")
	rr := httptest.NewRecorder()
	srv.handleListModels(rr, req)
	if rr.Code != http.StatusOK {
		t.Errorf("valid key: got %d, want %d", rr.Code, http.StatusOK)
	}
}

func TestAuthWithXAPIKeyHeader(t *testing.T) {
	srv := newAuthTestServer(t, "secret-key-123")
	req := httptest.NewRequest("GET", "/v1/models", nil)
	req.Header.Set("X-API-Key", "secret-key-123")
	rr := httptest.NewRecorder()
	srv.handleListModels(rr, req)
	if rr.Code != http.StatusOK {
		t.Errorf("X-API-Key header: got %d, want %d", rr.Code, http.StatusOK)
	}
}

func TestAuthWithXGoogAPIKeyHeader(t *testing.T) {
	srv := newAuthTestServer(t, "secret-key-123")
	req := httptest.NewRequest("GET", "/v1/models", nil)
	req.Header.Set("X-Goog-Api-Key", "secret-key-123")
	rr := httptest.NewRecorder()
	srv.handleListModels(rr, req)
	if rr.Code != http.StatusOK {
		t.Errorf("X-Goog-Api-Key header: got %d, want %d", rr.Code, http.StatusOK)
	}
}
