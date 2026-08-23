package proxy

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/deungjaho/hydra/internal/account"
)

// healthCheckLoop runs a periodic connectivity probe against every
// non-disabled account. It calls fetchAvailableModels (lightweight, no
// quota usage) to verify token validity + upstream reachability.
//
// On failure:
//   - increments the account's consecutive-failure counter
//   - fires a Notifier notification
//   - after FailureThreshold consecutive failures, auto-disables the
//     account (protects against repeatedly hitting dead accounts)
//
// On success:
//   - resets the failure counter
//   - if the account was previously unhealthy, fires a recovery notification
func (s *ProxyServer) healthCheckLoop(ctx context.Context) {
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

	// Run once immediately at startup, then on every tick.
	s.runHealthCheck(ctx, threshold)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.runHealthCheck(ctx, threshold)
		}
	}
}

func (s *ProxyServer) runHealthCheck(ctx context.Context, threshold int) {
	accounts, err := account.ListAccounts(s.State.DB)
	if err != nil {
		log.Printf("health check: list accounts failed: %v", err)
		return
	}

	var allDown []AccountHealth
	for _, acc := range accounts {
		if ctx.Err() != nil {
			return
		}
		// Skip operator-disabled accounts (user intent).
		// Probe health-disabled accounts so they can auto-recover.
		if acc.OperatorDisabled {
			continue
		}
		healthy, reason := s.probeAccount(ctx, acc)
		ah := AccountHealth{
			Email:    acc.Email,
			Healthy:  healthy,
			Reason:   reason,
			Disabled: acc.Disabled(),
		}

		if healthy {
			s.State.healthFailures.Delete(acc.ID)
			// If the account was health-disabled, auto-recover it.
			if acc.HealthDisabled {
				if err := account.MarkHealthRecovered(s.State.DB, acc.ID); err != nil {
					log.Printf("health check: recover %s failed: %v", acc.Email, err)
				} else {
					log.Printf("health check: %s RECOVERED (auto-re-enabled)", acc.Email)
				}
			}
			s.State.Notifier.NotifyAccountRecovered(acc.Email)
			log.Printf("health check: %s OK", acc.Email)
			continue
		}

		// Unhealthy — increment failure counter.
		count := s.incrementHealthFailure(acc.ID)
		s.State.Notifier.NotifyAccountUnhealthy(acc.Email, reason)

		if count >= threshold && !acc.HealthDisabled {
			log.Printf("health check: %s failed %d times, auto-disabling",
				acc.Email, count)
			logErr(account.MarkHealthDisabled(s.State.DB, acc.ID,
				"health check: "+reason))
			s.State.healthFailures.Delete(acc.ID)
			s.State.RateLimiter.Clear(acc.ID)
		}
		allDown = append(allDown, ah)
	}

	// If every serviceable account (non-operator-disabled) is down,
	// fire the all-down alert. health-disabled accounts are included
	// in the "serviceable" set because they are candidates for recovery.
	// Only operator-disabled accounts are excluded (user intent).
	serviceable := countServiceable(accounts)
	if len(allDown) > 0 && len(allDown) == serviceable {
		s.State.Notifier.NotifyAllAccountsDown(allDown)
	}
}

// probeAccount does the actual connectivity test. It first ensures
// the token is fresh, then calls ProbeAccount (fetchAvailableModels).
// Uses the short-timeout Probe client so a stuck account doesn't block
// the health check goroutine for 60 seconds.
func (s *ProxyServer) probeAccount(ctx context.Context, acc *account.Account) (bool, string) {
	accessToken := acc.AccessToken
	if account.NeedsRefresh(acc.ExpiresAt) {
		tok, expiresAt, err := account.RefreshTokenCtx(ctx, s.Probe, acc.RefreshToken)
		if err != nil {
			if ctx.Err() != nil {
				return false, "shutdown in progress"
			}
			return false, fmt.Sprintf("token refresh: %v", err)
		}
		if err := account.UpdateTokens(s.State.DB, acc.ID, tok, expiresAt, ""); err != nil {
			log.Printf("health check: persist token for %s: %v", acc.Email, err)
		}
		accessToken = tok
	}

	if err := account.ProbeAccountCtx(ctx, s.Probe, accessToken, acc.ProjectID); err != nil {
		if ctx.Err() != nil {
			return false, "shutdown in progress"
		}
		return false, err.Error()
	}
	return true, ""
}

func (s *ProxyServer) incrementHealthFailure(id int64) int {
	v, _ := s.State.healthFailures.Load(id)
	count := 0
	if v != nil {
		count = v.(int)
	}
	count++
	s.State.healthFailures.Store(id, count)
	return count
}

// countServiceable returns the number of accounts that are not
// operator-disabled. This includes active accounts and health-disabled
// accounts (which are candidates for recovery). Used by the health
// checker to determine if all serviceable accounts are down.
func countServiceable(accounts []*account.Account) int {
	n := 0
	for _, a := range accounts {
		if !a.OperatorDisabled {
			n++
		}
	}
	return n
}

// countNonDisabled is retained for compatibility — it counts accounts
// that are fully enabled (not operator-disabled and not health-disabled).
func countNonDisabled(accounts []*account.Account) int {
	n := 0
	for _, a := range accounts {
		if !a.Disabled() {
			n++
		}
	}
	return n
}
