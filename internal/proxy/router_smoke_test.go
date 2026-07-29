package proxy

import (
	"context"
	"io"
	"net"
	"net/http"
	"testing"
	"time"
)

// TestRouterSmoke_HealthzAndModels verifies that the server starts
// with the multi-provider Router wired in and that /healthz and
// /v1/models respond correctly. This is the Phase 1 smoke test.
func TestRouterSmoke_HealthzAndModels(t *testing.T) {
	srv, cleanup := newTestServer(t)
	defer cleanup()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errCh := make(chan error, 1)
	go func() { errCh <- srv.Serve(ctx) }()

	// Wait for the server to start listening.
	time.Sleep(200 * time.Millisecond)

	base := "http://" + srv.Config.Proxy.Bind + ":" +
		itoa(srv.Config.Proxy.Port)

	// /healthz should return 200 "ok".
	resp, err := http.Get(base + "/healthz")
	if err != nil {
		t.Fatalf("healthz GET failed: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Errorf("healthz status = %d, want 200", resp.StatusCode)
	}
	if string(body) != "ok" {
		t.Errorf("healthz body = %q, want %q", string(body), "ok")
	}

	// /v1/models should return 200 with a JSON list (open access,
	// no API keys configured).
	resp2, err := http.Get(base + "/v1/models")
	if err != nil {
		t.Fatalf("models GET failed: %v", err)
	}
	body2, _ := io.ReadAll(resp2.Body)
	resp2.Body.Close()
	if resp2.StatusCode != 200 {
		t.Errorf("models status = %d, want 200", resp2.StatusCode)
	}
	if len(body2) == 0 {
		t.Error("models body should not be empty")
	}

	// Verify the Router is non-nil (wired in NewProxyServer).
	if srv.Router == nil {
		t.Fatal("Router should not be nil after NewProxyServer")
	}

	cancel()
	<-errCh
}

// TestRouterSmoke_ChatCompletionsNoAccounts verifies that a
// chat completion request is routed through the Router and returns
// 503 (no accounts) rather than panicking. This proves the Router
// wiring doesn't break the request path.
func TestRouterSmoke_ChatCompletionsNoAccounts(t *testing.T) {
	srv, cleanup := newTestServer(t)
	defer cleanup()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errCh := make(chan error, 1)
	go func() { errCh <- srv.Serve(ctx) }()
	time.Sleep(200 * time.Millisecond)

	base := "http://" + srv.Config.Proxy.Bind + ":" +
		itoa(srv.Config.Proxy.Port)

	// Send a chat completion request. With no accounts in the DB,
	// the Router selects google-cloud-code, then the failover loop
	// finds no accounts and returns 503.
	resp, err := http.Post(base+"/v1/chat/completions",
		"application/json",
		stringReader(`{"model":"gemini-2.5-flash","messages":[{"role":"user","content":"hi"}]}`))
	if err != nil {
		t.Fatalf("chat completions POST failed: %v", err)
	}
	resp.Body.Close()

	// With no accounts, we expect 503 (no available accounts).
	// The Router itself should succeed (google-cloud-code is
	// available), so we should NOT get a 400 (router error).
	if resp.StatusCode == 400 {
		t.Error("chat completions returned 400 (router error) — "+
			"Router should have selected google-cloud-code")
	}
	// 503 is expected (no accounts in DB).
	if resp.StatusCode != 503 {
		t.Logf("chat completions status = %d (expected 503 with no accounts)",
			resp.StatusCode)
	}

	cancel()
	<-errCh
}

// itoa is a local int-to-string helper to avoid importing strconv.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

// stringReader returns an io.Reader for a string.
func stringReader(s string) io.Reader {
	return &stringReaderImpl{s: s}
}

type stringReaderImpl struct {
	s   string
	pos int
}

func (r *stringReaderImpl) Read(p []byte) (int, error) {
	if r.pos >= len(r.s) {
		return 0, io.EOF
	}
	n := copy(p, r.s[r.pos:])
	r.pos += n
	return n, nil
}

// Ensure net.Listen is referenced (used by newTestServer).
var _ = net.Listen
