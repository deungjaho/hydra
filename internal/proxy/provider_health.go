package proxy

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"time"

	"github.com/deungjaho/hydra/internal/provider"
)

// providerHealthCheckLoop runs a periodic connectivity probe against every
// non-operator-disabled API key provider. It sends a GET /v1/models request
// to verify the provider is reachable and the API key is valid.
//
// On failure:
//   - increments the provider's consecutive-failure counter
//   - fires a Notifier notification
//   - after FailureThreshold consecutive failures, auto-disables the
//     provider (health_disabled=1)
//
// On success:
//   - resets the failure counter
//   - if the provider was previously health-disabled, auto-recovers it
func (s *ProxyServer) providerHealthCheckLoop(ctx context.Context) {
	interval := time.Duration(s.Config.HealthCheck.IntervalSeconds) * time.Second
	if interval < 10*time.Second {
		interval = 10 * time.Second
	}
	threshold := s.Config.HealthCheck.FailureThreshold
	if threshold < 1 {
		threshold = 3
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	s.runProviderHealthCheck(ctx, threshold)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.runProviderHealthCheck(ctx, threshold)
		}
	}
}

func (s *ProxyServer) runProviderHealthCheck(ctx context.Context, threshold int) {
	providers, err := provider.ListProviders(s.State.DB)
	if err != nil {
		log.Printf("provider health check: list providers failed: %v", err)
		return
	}

	for _, p := range providers {
		if ctx.Err() != nil {
			return
		}
		// Skip operator-disabled providers (user intent).
		// Probe health-disabled providers so they can auto-recover.
		if p.OperatorDisabled {
			continue
		}
		healthy, reason := s.probeProvider(ctx, p)
		if healthy {
			s.State.providerHealthFailures.Delete(p.ID)
			if p.HealthDisabled {
				if err := provider.MarkProviderHealthRecovered(s.State.DB, p.ID); err != nil {
					log.Printf("provider health check: recover %s failed: %v", p.Name, err)
				} else {
					log.Printf("provider health check: %s RECOVERED (auto-re-enabled)", p.Name)
				}
			}
			s.State.Notifier.NotifyProviderRecovered(p.Name)
			log.Printf("provider health check: %s OK", p.Name)
			continue
		}

		count := s.incrementProviderHealthFailure(p.ID)
		s.State.Notifier.NotifyProviderUnhealthy(p.Name, reason)

		if count >= threshold && !p.HealthDisabled {
			log.Printf("provider health check: %s failed %d times, auto-disabling",
				p.Name, count)
			logErr(provider.MarkProviderHealthDisabled(s.State.DB, p.ID,
				"health check: "+reason))
			s.State.providerHealthFailures.Delete(p.ID)
		}
	}
}

// probeProvider does a GET /v1/models to verify connectivity + API key validity.
func (s *ProxyServer) probeProvider(ctx context.Context, p *provider.Provider) (bool, string) {
	url := p.BaseURL + "/v1/models"
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return false, fmt.Sprintf("build request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+p.APIKey)

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return false, "shutdown in progress"
		}
		return false, err.Error()
	}
	defer resp.Body.Close()

	// 401/403 means the key is invalid — definitely unhealthy.
	// 5xx means the provider is having issues — unhealthy.
	// 2xx or 404 (endpoint not supported but provider is reachable) — healthy.
	if resp.StatusCode == 401 || resp.StatusCode == 403 {
		return false, fmt.Sprintf("HTTP %d (invalid API key)", resp.StatusCode)
	}
	if resp.StatusCode >= 500 {
		return false, fmt.Sprintf("HTTP %d (server error)", resp.StatusCode)
	}
	return true, ""
}

func (s *ProxyServer) incrementProviderHealthFailure(id int64) int {
	v, _ := s.State.providerHealthFailures.Load(id)
	count := 0
	if v != nil {
		count = v.(int)
	}
	count++
	s.State.providerHealthFailures.Store(id, count)
	return count
}
