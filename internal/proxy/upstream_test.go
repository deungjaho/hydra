package proxy

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/deungjaho/hydra/internal/version"
)

func TestUpstreamURL(t *testing.T) {
	nonStream := UpstreamURL(false)
	if nonStream != "https://daily-cloudcode-pa.googleapis.com/v1internal:generateContent" {
		t.Errorf("UpstreamURL(false) = %s", nonStream)
	}

	stream := UpstreamURL(true)
	if stream != "https://daily-cloudcode-pa.googleapis.com/v1internal:streamGenerateContent?alt=sse" {
		t.Errorf("UpstreamURL(true) = %s", stream)
	}
}

func TestSendRequest_Headers(t *testing.T) {
	var capturedReq *http.Request
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedReq = r
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{}`))
	}))
	defer ts.Close()

	client := ts.Client()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Send through doSend directly against test server
	resp, err := doSend(ctx, client, ts.URL, "test-token-123", []byte(`{"hello":"world"}`))
	if err != nil {
		t.Fatalf("doSend error: %v", err)
	}
	defer resp.Body.Close()
	_, _ = io.ReadAll(resp.Body)

	if capturedReq == nil {
		t.Fatal("expected request to be received")
	}

	// 1. Authorization
	if capturedReq.Header.Get("Authorization") != "Bearer test-token-123" {
		t.Errorf("Authorization = %s, want Bearer test-token-123", capturedReq.Header.Get("Authorization"))
	}

	// 2. User-Agent
	if capturedReq.Header.Get("User-Agent") != version.UserAgent() {
		t.Errorf("User-Agent = %s, want %s", capturedReq.Header.Get("User-Agent"), version.UserAgent())
	}

	// 3. Content-Type
	if capturedReq.Header.Get("Content-Type") != "application/json" {
		t.Errorf("Content-Type = %s, want application/json", capturedReq.Header.Get("Content-Type"))
	}

	// 4. Accept-Encoding
	if !strings.Contains(capturedReq.Header.Get("Accept-Encoding"), "gzip") {
		t.Errorf("Accept-Encoding = %s, want gzip", capturedReq.Header.Get("Accept-Encoding"))
	}

	// 5. Must NOT contain x-goog-user-project
	if capturedReq.Header.Get("x-goog-user-project") != "" {
		t.Errorf("x-goog-user-project header must NOT be sent, got %s", capturedReq.Header.Get("x-goog-user-project"))
	}

	// 6. Must NOT contain legacy machine ID or session ID headers
	if capturedReq.Header.Get("x-machine-id") != "" {
		t.Errorf("x-machine-id header must NOT be sent, got %s", capturedReq.Header.Get("x-machine-id"))
	}
}

func TestNewUpstreamClient_Configuration(t *testing.T) {
	client := NewUpstreamClient("")
	if client == nil {
		t.Fatal("NewUpstreamClient returned nil")
	}
	tr, ok := client.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("expected *http.Transport, got %T", client.Transport)
	}
	if tr.ForceAttemptHTTP2 {
		t.Error("ForceAttemptHTTP2 should be false to match agy CLI")
	}
	if tr.TLSNextProto == nil {
		t.Error("TLSNextProto must be non-nil to disable HTTP/2 ALPN")
	}
	if tr.MaxIdleConns < 100 {
		t.Errorf("MaxIdleConns = %d, want >= 100", tr.MaxIdleConns)
	}
}
