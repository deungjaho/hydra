package proxy

import (
	"math/rand"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/deungjaho/hydra/internal/account"
	"github.com/deungjaho/hydra/internal/config"
	"github.com/deungjaho/hydra/internal/db"
)

// ConcurrencyTracker enforces a per-account in-flight request limit.
// When an account is at capacity, SelectAccount skips it (unless all
// accounts are at capacity, in which case the least-loaded one is
// returned). Acquire/Release must be called by the failover loop around
// each upstream request.
type ConcurrencyTracker struct {
	mu       sync.Mutex
	inFlight map[int64]int
	limit    int
}

// NewConcurrencyTracker creates a tracker with the given per-account
// concurrent request limit. A limit <= 0 disables concurrency control.
func NewConcurrencyTracker(limit int) *ConcurrencyTracker {
	return &ConcurrencyTracker{
		inFlight: make(map[int64]int),
		limit:    limit,
	}
}

// IsAtCapacity returns true if the account has reached its concurrency
// limit.
func (c *ConcurrencyTracker) IsAtCapacity(accountID int64) bool {
	if c.limit <= 0 {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.inFlight[accountID] >= c.limit
}

// InFlight returns the current in-flight count for an account.
func (c *ConcurrencyTracker) InFlight(accountID int64) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.inFlight[accountID]
}

// Acquire increments the in-flight counter. Call before sending the
// upstream request.
func (c *ConcurrencyTracker) Acquire(accountID int64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.inFlight[accountID]++
}

// Release decrements the in-flight counter. Call after the upstream
// request completes (success or failure).
func (c *ConcurrencyTracker) Release(accountID int64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.inFlight[accountID] > 0 {
		c.inFlight[accountID]--
		if c.inFlight[accountID] == 0 {
			delete(c.inFlight, accountID)
		}
	}
}

// RateLimitTracker tracks per-account / per-model cooldowns in memory.
type RateLimitTracker struct {
	mu        sync.Mutex
	cooldowns map[string]time.Time
}

func NewRateLimitTracker() *RateLimitTracker {
	return &RateLimitTracker{cooldowns: make(map[string]time.Time)}
}

func (r *RateLimitTracker) IsLimited(accountID int64, model string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	t, ok := r.cooldowns[r.key(accountID, model)]
	if !ok {
		return false
	}
	if time.Now().Before(t) {
		return true
	}
	// Expired — delete to prevent unbounded growth.
	delete(r.cooldowns, r.key(accountID, model))
	return false
}

func (r *RateLimitTracker) SetCooldown(accountID int64, model string, secs int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.cooldowns[r.key(accountID, model)] = time.Now().Add(time.Duration(secs) * time.Second)
}

func (r *RateLimitTracker) Clear(accountID int64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	prefix := itoaInt64(accountID) + ":"
	bare := itoaInt64(accountID)
	for k := range r.cooldowns {
		if strings.HasPrefix(k, prefix) || k == bare {
			delete(r.cooldowns, k)
		}
	}
}

// Cleanup removes all expired cooldown entries. Called periodically
// to prevent unbounded map growth from one-off model cooldowns.
func (r *RateLimitTracker) Cleanup() {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := time.Now()
	for k, t := range r.cooldowns {
		if !now.Before(t) {
			delete(r.cooldowns, k)
		}
	}
}

func (r *RateLimitTracker) key(accountID int64, model string) string {
	if model == "" {
		return itoaInt64(accountID)
	}
	return itoaInt64(accountID) + ":" + model
}

// StickySessions maps session id → account id.
// Tracks last access time so stale bindings can be evicted.
type StickySessions struct {
	mu         sync.Mutex
	bindings   map[string]int64
	lastAccess map[string]time.Time
}

func NewStickySessions() *StickySessions {
	return &StickySessions{
		bindings:   make(map[string]int64),
		lastAccess: make(map[string]time.Time),
	}
}

func (s *StickySessions) Get(sessionID string) (int64, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	id, ok := s.bindings[sessionID]
	if ok {
		s.lastAccess[sessionID] = time.Now()
	}
	return id, ok
}

func (s *StickySessions) Bind(sessionID string, accountID int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.bindings[sessionID] = accountID
	s.lastAccess[sessionID] = time.Now()
}

func (s *StickySessions) Unbind(sessionID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.bindings, sessionID)
	delete(s.lastAccess, sessionID)
}

// Cleanup removes bindings not accessed within maxIdle. Called
// periodically to prevent unbounded growth from ended sessions.
func (s *StickySessions) Cleanup(maxIdle time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	cutoff := time.Now().Add(-maxIdle)
	for sid, t := range s.lastAccess {
		if t.Before(cutoff) {
			delete(s.bindings, sid)
			delete(s.lastAccess, sid)
		}
	}
}

// SelectAccount picks the best available account for a request.
//
// mappedModel is the orbit-mapped model id (post-rewrite).
// sessionID is the conversation session id (used for sticky binding).
// protectedFilter — when true, accounts whose protected_models contains
// mappedModel are excluded.
// stickyEnabled — when false, skip sticky binding entirely (global config).
// noSticky — when true, skip sticky binding for this specific request
// (per-request opt-out via X-Hydra-No-Sticky header).
// concurrency — when non-nil and limit > 0, accounts at capacity are
// deprioritized (moved to the end of the candidate list).
// quotaWarnThreshold — when > 0, accounts whose quota_remaining is below
// this percentage are deprioritized in the sort order.
func SelectAccount(
	accounts []*account.Account,
	limiter *RateLimitTracker,
	sticky *StickySessions,
	mode config.SchedulingMode,
	mappedModel string,
	sessionID string,
	protectedFilter bool,
	stickyEnabled bool,
	noSticky bool,
	concurrency *ConcurrencyTracker,
	quotaWarnThreshold int32,
) *account.Account {
	modelLC := strings.ToLower(mappedModel)
	var candidates []*account.Account
	for _, a := range accounts {
		if a.Disabled() {
			continue
		}
		if limiter.IsLimited(a.ID, mappedModel) {
			continue
		}
		if protectedFilter {
			protected := false
			for _, m := range a.ProtectedModels {
				if strings.ToLower(m) == modelLC {
					protected = true
					break
				}
			}
			if protected {
				continue
			}
		}
		candidates = append(candidates, a)
	}
	if len(candidates) == 0 {
		// All accounts are either disabled, rate-limited, or protected
		// for this model. Fall back to the highest-quota non-disabled
		// account so the request at least gets tried upstream.
		fallback := selectFallback(accounts, limiter, mappedModel)
		return fallback
	}

	// Filter out concurrency-saturated accounts. If all are saturated,
	// keep them all (we'd rather queue than 503).
	if concurrency != nil && concurrency.limit > 0 {
		var free []*account.Account
		for _, a := range candidates {
			if !concurrency.IsAtCapacity(a.ID) {
				free = append(free, a)
			}
		}
		if len(free) > 0 {
			candidates = free
		}
	}

	// Sticky session (Cache + Balance), unless globally disabled or
	// per-request opt-out.
	stickyActive := stickyEnabled && !noSticky &&
		sessionID != "" && mode != config.SchedulingPerformance
	if stickyActive {
		if boundID, ok := sticky.Get(sessionID); ok {
			for _, a := range candidates {
				if a.ID == boundID {
					// Even sticky-bound account must not be at capacity.
					if concurrency != nil && concurrency.limit > 0 &&
						concurrency.IsAtCapacity(a.ID) {
						sticky.Unbind(sessionID)
						break
					}
					return a
				}
			}
			// Bound account unavailable — unbind.
			sticky.Unbind(sessionID)
		}
	}

	// Performance → P2C.
	if mode == config.SchedulingPerformance {
		return p2cSelect(candidates, concurrency, quotaWarnThreshold)
	}

	// Balance / Cache without binding: sort by quota warning tier,
	// then quota_remaining desc, then LRU.
	sort.SliceStable(candidates, func(i, j int) bool {
		wi := quotaWarnTier(candidates[i], quotaWarnThreshold)
		wj := quotaWarnTier(candidates[j], quotaWarnThreshold)
		if wi != wj {
			return wi < wj // lower tier (higher quota) first
		}
		// Within the same tier, prefer less-loaded accounts.
		if concurrency != nil && concurrency.limit > 0 {
			ci := concurrency.InFlight(candidates[i].ID)
			cj := concurrency.InFlight(candidates[j].ID)
			if ci != cj {
				return ci < cj
			}
		}
		qa := quotaRemainingOrZero(candidates[i])
		qb := quotaRemainingOrZero(candidates[j])
		if qa != qb {
			return qa > qb
		}
		return lastUsedOrZero(candidates[i]) < lastUsedOrZero(candidates[j])
	})
	return candidates[0]
}

func quotaRemainingOrZero(a *account.Account) int64 {
	if a.QuotaRemaining == nil {
		return 0
	}
	return *a.QuotaRemaining
}
func lastUsedOrZero(a *account.Account) int64 {
	if a.LastUsedAt == nil {
		return 0
	}
	return *a.LastUsedAt
}

// quotaWarnTier returns 0 for healthy accounts (quota >= threshold),
// 1 for accounts in the warning zone (quota < threshold but not
// protected). This gives a two-tier sort: healthy accounts are always
// preferred over warning-zone accounts.
func quotaWarnTier(a *account.Account, threshold int32) int {
	if threshold <= 0 {
		return 0
	}
	q := quotaRemainingOrZero(a)
	if q < int64(threshold) {
		return 1
	}
	return 0
}

// selectFallback picks the best non-disabled account when all candidates
// were filtered out by protection/cooldown. Prefers accounts not on cooldown.
func selectFallback(accounts []*account.Account, limiter *RateLimitTracker, mappedModel string) *account.Account {
	var best *account.Account
	var bestQuota int64
	for _, a := range accounts {
		if a.Disabled() {
			continue
		}
		// Skip rate-limited accounts if possible.
		if limiter.IsLimited(a.ID, mappedModel) {
			continue
		}
		q := quotaRemainingOrZero(a)
		if best == nil || q > bestQuota {
			best = a
			bestQuota = q
		}
	}
	// Last resort: ignore cooldowns.
	if best == nil {
		for _, a := range accounts {
			if a.Disabled() {
				continue
			}
			q := quotaRemainingOrZero(a)
			if best == nil || q > bestQuota {
				best = a
				bestQuota = q
			}
		}
	}
	return best
}

func p2cSelect(candidates []*account.Account, concurrency *ConcurrencyTracker, quotaWarnThreshold int32) *account.Account {
	if len(candidates) == 1 {
		return candidates[0]
	}
	rng := rand.New(rand.NewSource(time.Now().UnixNano()))
	a := candidates[rng.Intn(len(candidates))]
	b := candidates[rng.Intn(len(candidates))]
	// Prefer the account in the better quota warning tier.
	ti := quotaWarnTier(a, quotaWarnThreshold)
	tj := quotaWarnTier(b, quotaWarnThreshold)
	if ti < tj {
		return a
	}
	if tj < ti {
		return b
	}
	// Prefer the less-loaded account.
	if concurrency != nil && concurrency.limit > 0 {
		ci := concurrency.InFlight(a.ID)
		cj := concurrency.InFlight(b.ID)
		if ci < cj {
			return a
		}
		if cj < ci {
			return b
		}
	}
	qa := quotaRemainingOrZero(a)
	qb := quotaRemainingOrZero(b)
	if qb > qa {
		return b
	}
	if qa > qb {
		return a
	}
	la := lastUsedOrZero(a)
	lb := lastUsedOrZero(b)
	if la <= lb {
		return a
	}
	return b
}

// ProxyState is the shared proxy state: account pool + rate limiter + sticky + counter.
type ProxyState struct {
	DB                     *db.Db
	RateLimiter            *RateLimitTracker
	Sticky                 *StickySessions
	Concurrency            *ConcurrencyTracker
	requestCounter         muCounter
	startedAt              time.Time
	metrics                metricsCollector
	Notifier               Notifier
	healthFailures         sync.Map // accountID (int64) → consecutive failure count (int)
	providerHealthFailures sync.Map // providerID (int64) → consecutive failure count (int)
}

// metricsCollector tracks in-memory request metrics for Prometheus.
type metricsCollector struct {
	mu      sync.Mutex
	buckets []float64 // histogram bucket upper bounds
	counts  []uint64  // count per bucket
	sum     float64
	total   uint64
}

func newMetricsCollector() metricsCollector {
	return metricsCollector{
		buckets: []float64{0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30, 60},
		counts:  make([]uint64, 9),
	}
}

func (m *metricsCollector) observeDuration(seconds float64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sum += seconds
	m.total++
	for i, b := range m.buckets {
		if seconds <= b {
			m.counts[i]++
			break
		}
	}
}

func (m *metricsCollector) snapshot() (buckets []float64, counts []uint64, sum float64, total uint64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	buckets = append([]float64{}, m.buckets...)
	counts = append([]uint64{}, m.counts...)
	sum = m.sum
	total = m.total
	return
}

type muCounter struct {
	mu sync.Mutex
	n  uint64
}

func (c *muCounter) Next() uint64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.n++
	return c.n
}

func NewProxyState(d *db.Db, concurrencyLimit int) *ProxyState {
	return &ProxyState{
		DB:          d,
		RateLimiter: NewRateLimitTracker(),
		Sticky:      NewStickySessions(),
		Concurrency: NewConcurrencyTracker(concurrencyLimit),
		startedAt:   time.Now(),
		metrics:     newMetricsCollector(),
		Notifier:    NewLogNotifier(),
	}
}

func (s *ProxyState) NextRequestN() uint64 { return s.requestCounter.Next() }

// itoaInt64 wraps strconv.FormatInt for use in cooldown keys.
func itoaInt64(v int64) string {
	return strconv.FormatInt(v, 10)
}
