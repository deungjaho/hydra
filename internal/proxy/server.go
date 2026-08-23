package proxy

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"sync"
	"time"

	"github.com/deungjaho/hydra/internal/config"
	"github.com/deungjaho/hydra/internal/provider"
	"github.com/deungjaho/hydra/internal/registry"
)

// ProxyServer is the HTTP server that exposes the OpenAI/Anthropic-compatible
// API.
type ProxyServer struct {
	Config   *config.AppConfig
	State    *ProxyState
	HTTP     *http.Client // uTLS client for Gemini upstream
	OAuth    *http.Client // standard client for OAuth/quota (no uTLS)
	Probe    *http.Client // short-timeout client for health check probes
	Registry *registry.Registry
}

// NewProxyServer builds a ProxyServer with a uTLS-backed upstream client
// and a standard HTTP client for OAuth token refresh / quota fetch.
func NewProxyServer(cfg *config.AppConfig, state *ProxyState) *ProxyServer {
	probeTimeout := time.Duration(cfg.HealthCheck.TimeoutSeconds) * time.Second
	if probeTimeout < minProbeTimeout {
		probeTimeout = defProbeTimeout
	}
	return &ProxyServer{
		Config:   cfg,
		State:    state,
		HTTP:     NewUTLSClient(upstreamTimeout, cfg.Proxy.UpstreamProxy),
		OAuth:    NewHTTPClient(oauthTimeout, cfg.Proxy.UpstreamProxy),
		Probe:    NewHTTPClient(probeTimeout, cfg.Proxy.UpstreamProxy),
		Registry: registry.New(),
	}
}

// Serve starts the HTTP listener and blocks until ctx is cancelled or
// the server fails to start. On ctx cancellation (normal SIGINT/SIGTERM),
// in-flight requests are drained (up to 30s) and all background goroutines
// are joined before returning. A normal signal-induced shutdown returns nil
// (exit 0); only real listen/shutdown errors return non-nil.
func (s *ProxyServer) Serve(ctx context.Context) error {
	addr := fmt.Sprintf("%s:%d", s.Config.Proxy.Bind, s.Config.Proxy.Port)

	// Derive a cancellable context so we can stop loops even when the
	// parent ctx is not cancelled (e.g. listen error path).
	loopCtx, cancelLoops := context.WithCancel(ctx)
	defer cancelLoops()

	// Sync TOML-configured providers into the DB, then build the
	// initial model registry from accounts + DB providers.
	if err := provider.SyncFromConfig(s.State.DB, s.Config); err != nil {
		log.Printf("provider sync from config: %v", err)
	}
	s.rebuildRegistry(nil)

	// Background loops with join tracking. All loops exit on loopCtx.Done().
	var wg sync.WaitGroup
	runLoop := func(fn func(context.Context)) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			fn(loopCtx)
		}()
	}
	runLoop(s.tokenRefresherLoop)
	runLoop(s.quotaRefresherLoop)
	runLoop(s.cleanupLoop)
	if s.Config.HealthCheck.Enabled {
		runLoop(s.healthCheckLoop)
		runLoop(s.providerHealthCheckLoop)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", s.handleHealthz)
	mux.HandleFunc("/v1/models", s.handleListModels)
	mux.HandleFunc("/v1/catalog", s.handleCodexCatalog)
	mux.HandleFunc("/v1/catalog/claude", s.handleClaudeConfig)
	mux.HandleFunc("/v1/chat/completions", s.handleChatCompletions)
	mux.HandleFunc("/responses", s.handleResponses)
	mux.HandleFunc("/v1/responses", s.handleResponses)
	mux.HandleFunc("/v1/messages", s.handleAnthropicMessages)
	mux.HandleFunc("/v1/messages/count_tokens", s.handleAnthropicCountTokens)
	mux.HandleFunc("/metrics", s.handleMetrics)

	srv := &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadHeaderTimeout: readHeaderTimeout,
		ReadTimeout:       readTimeout,
		WriteTimeout:      writeTimeout,
		MaxHeaderBytes:    maxHeaderBytes,
	}

	// Start listening in a goroutine so we can select on ctx.
	errCh := make(chan error, 1)
	go func() {
		log.Printf("hydra proxy listening on http://%s", addr)
		errCh <- srv.ListenAndServe()
	}()

	var serveErr error
	select {
	case err := <-errCh:
		// ListenAndServe returned (real error, e.g. port in use).
		serveErr = err
	case <-loopCtx.Done():
		// Normal signal-induced shutdown (ctx cancelled by signal).
		log.Printf("hydra proxy shutting down (graceful)...")
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), shutdownTimeout)
		if err := srv.Shutdown(shutdownCtx); err != nil {
			log.Printf("hydra proxy shutdown error: %v", err)
			serveErr = err
		} else {
			log.Printf("hydra proxy stopped")
		}
		shutdownCancel()
	}

	// Cancel loops and wait for all background goroutines to finish
	// before returning. This ensures no goroutine accesses the DB
	// after Serve returns and the caller closes it.
	//
	// The wait is bounded by a deadline (default 45s) so that a stuck
	// in-flight HTTP call can't block shutdown indefinitely. All
	// background HTTP calls now accept loopCtx, so cancellation
	// propagates to in-flight requests immediately.
	cancelLoops()
	waitCh := make(chan struct{})
	go func() { wg.Wait(); close(waitCh) }()
	select {
	case <-waitCh:
		// All loops exited cleanly.
	case <-time.After(loopJoinTimeout):
		log.Printf("hydra proxy: shutdown deadline exceeded, some background tasks may not have exited cleanly")
	}
	return serveErr
}
