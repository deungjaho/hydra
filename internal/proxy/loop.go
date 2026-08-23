package proxy

import (
	"context"
	"log"
	"time"

	"github.com/deungjaho/hydra/internal/account"
)

func (s *ProxyServer) quotaRefresherLoop(ctx context.Context) {
	// Run once on boot, then every 5 minutes.
	_ = RefreshAllQuotasCtx(ctx, s)
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := RefreshAllQuotasCtx(ctx, s); err != nil {
				log.Printf("background quota refresh failed: %v", err)
			}
		}
	}
}

// tokenRefresherLoop proactively refreshes access tokens that are within
// oauthRefreshSkew (15 min) of expiry, so requests don't hit a stale token.
func (s *ProxyServer) tokenRefresherLoop(ctx context.Context) {
	ticker := time.NewTicker(1 * time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.refreshExpiringTokens(ctx)
		}
	}
}

func (s *ProxyServer) refreshExpiringTokens(ctx context.Context) {
	accounts, err := account.ListAccounts(s.State.DB)
	if err != nil {
		return
	}
	for _, acc := range accounts {
		if ctx.Err() != nil {
			return
		}
		if acc.Disabled() || !account.NeedsRefresh(acc.ExpiresAt) {
			continue
		}
		tok, expiresAt, err := account.RefreshTokenCtx(ctx, s.OAuth, acc.RefreshToken)
		if err != nil {
			if ctx.Err() != nil {
				return // shutdown in progress
			}
			log.Printf("proactive token refresh failed for %s: %v", acc.Email, err)
			continue
		}
		if err := account.UpdateTokens(s.State.DB, acc.ID, tok, expiresAt, ""); err != nil {
			log.Printf("persist proactively refreshed token failed for %s: %v", acc.Email, err)
			continue
		}
		log.Printf("proactively refreshed token for %s (expires in %dm)",
			acc.Email, (expiresAt-time.Now().Unix())/60)
	}
}

// cleanupLoop periodically evicts expired cooldown entries and stale
// sticky session bindings to prevent unbounded in-memory map growth.
func (s *ProxyServer) cleanupLoop(ctx context.Context) {
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.State.RateLimiter.Cleanup()
			s.State.Sticky.Cleanup(stickyCleanupPeriod)
		}
	}
}

// RefreshAllQuotas refreshes quota for every bound account.
func RefreshAllQuotas(s *ProxyServer) error {
	return RefreshAllQuotasCtx(context.Background(), s)
}

// RefreshAllQuotasCtx is the context-aware variant. It checks ctx.Err()
// between accounts and passes ctx to all external HTTP calls, so
// shutdown can abort in-flight quota refreshes immediately.
func RefreshAllQuotasCtx(ctx context.Context, s *ProxyServer) error {
	accounts, err := account.ListAccounts(s.State.DB)
	if err != nil {
		return err
	}
	if len(accounts) == 0 {
		return nil
	}
	threshold := int32(s.Config.QuotaProtection.ThresholdPercentage)
	monitored := s.Config.QuotaProtection.MonitoredModels
	protectionEnabled := s.Config.QuotaProtection.Enabled

	for _, acc := range accounts {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if acc.Disabled() {
			continue
		}
		accessToken := acc.AccessToken
		if account.NeedsRefresh(acc.ExpiresAt) {
			tok, expiresAt, err := account.RefreshTokenCtx(ctx, s.OAuth, acc.RefreshToken)
			if err != nil {
				if ctx.Err() != nil {
					return ctx.Err() // shutdown in progress
				}
				log.Printf("refresh token failed for %s: %v", acc.Email, err)
				continue
			}
			if err := account.UpdateTokens(s.State.DB, acc.ID, tok, expiresAt, ""); err != nil {
				log.Printf("persist refreshed token failed for %s: %v", acc.Email, err)
			}
			accessToken = tok
		}
		fetched, err := account.FetchQuotaCtx(ctx, s.OAuth, accessToken, acc.ProjectID)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err() // shutdown in progress
			}
			log.Printf("fetch quota failed for %s: %v", acc.Email, err)
			continue
		}
		// Update global model metadata registry with AGY's latest data.
		if len(fetched.ModelMetas) > 0 {
			UpdateModelMetas(fetched.ModelMetas)
		}
		// Rebuild the unified model registry from all accounts + config.
		s.rebuildRegistry(fetched.ModelMetas)
		newProtected := acc.ProtectedModels
		if protectionEnabled {
			newProtected = account.ComputeProtectedModels(
				acc.ProtectedModels,
				fetched.ModelPercentages,
				monitored,
				threshold,
			)
		}
		if err := account.UpdateQuota(
			s.State.DB, acc.ID,
			fetched.JSONBlob, fetched.SummaryBlob,
			fetched.MaxPercentage, fetched.HasMaxPercentage,
			newProtected,
		); err != nil {
			log.Printf("persist quota failed for %s: %v", acc.Email, err)
			continue
		}
		log.Printf("refreshed quota for %s (max=%d%%, protected=%v)", acc.Email, fetched.MaxPercentage, newProtected)
	}
	return nil
}

// rebuildRegistry refreshes the unified model registry from all accounts
// and DB-stored providers. Called after each quota refresh and at startup.
func (s *ProxyServer) rebuildRegistry(metas map[string]account.ModelMeta) {
	if s.Registry == nil {
		return
	}
	accounts, _ := account.ListAccounts(s.State.DB)
	s.Registry.RebuildFromAGY(accounts, metas)
	if err := s.Registry.RebuildFromDB(s.State.DB); err != nil {
		log.Printf("registry: rebuild from DB failed: %v", err)
	}
}
