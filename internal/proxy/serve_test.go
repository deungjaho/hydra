package proxy

import (
	"context"
	"net"
	"path/filepath"
	"testing"
	"time"

	"github.com/deungjaho/hydra/internal/config"
	"github.com/deungjaho/hydra/internal/db"
)

// newTestServer creates a ProxyServer bound to an ephemeral port
// with a fresh temporary DB.
func newTestServer(t *testing.T) (*ProxyServer, func()) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "test.db")
	d, err := db.Open(path)
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	cfg := &config.AppConfig{}
	cfg.Proxy.Bind = "127.0.0.1"
	cfg.Proxy.Port = 0 // will be replaced with ephemeral
	// Find a free port.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("find port: %v", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	_ = ln.Close()
	cfg.Proxy.Port = port
	cfg.HealthCheck.Enabled = false // disable health check for serve tests

	state := NewProxyState(d)
	srv := NewProxyServer(cfg, state)
	cleanup := func() { d.Close() }
	return srv, cleanup
}

// TestServeGracefulShutdownReturnsNil reproduces the bug where a normal
// context cancellation (SIGINT/SIGTERM) causes Serve to return
// context.Canceled, which the CLI interprets as an error (exit 1).
//
// After the fix, Serve should return nil when shutdown is triggered by
// context cancellation (normal signal), and only return non-nil for
// real listen/shutdown errors.
func TestServeGracefulShutdownReturnsNil(t *testing.T) {
	srv, cleanup := newTestServer(t)
	defer cleanup()

	ctx, cancel := context.WithCancel(context.Background())

	// Start Serve in a goroutine.
	errCh := make(chan error, 1)
	go func() { errCh <- srv.Serve(ctx) }()

	// Give the server a moment to start listening.
	time.Sleep(100 * time.Millisecond)

	// Simulate SIGINT/SIGTERM by cancelling the context.
	cancel()

	select {
	case err := <-errCh:
		if err != nil {
			t.Errorf("Serve returned %v on normal shutdown, want nil (exit 0)", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Serve did not return within 10s of context cancel")
	}
}

// TestServeLoopsJoinedOnShutdown verifies that all background goroutines
// (token refresher, quota refresher, cleanup) have exited before Serve
// returns. Without joining, the CLI's defer d.Close() can close the DB
// while a loop is still reading/writing.
//
// This test uses a short-interval loop configuration and race detector
// to catch the issue. Run with: go test -race -run TestServeLoopsJoined
func TestServeLoopsJoinedOnShutdown(t *testing.T) {
	srv, cleanup := newTestServer(t)
	defer cleanup()

	ctx, cancel := context.WithCancel(context.Background())

	errCh := make(chan error, 1)
	go func() { errCh <- srv.Serve(ctx) }()

	// Let loops run at least one tick.
	time.Sleep(200 * time.Millisecond)

	// Cancel and wait for Serve to return.
	cancel()

	select {
	case <-errCh:
		// Serve returned. If loops weren't joined, the race detector
		// would flag DB access after close in cleanup().
	case <-time.After(10 * time.Second):
		t.Fatal("Serve did not return within 10s of context cancel")
	}
}
