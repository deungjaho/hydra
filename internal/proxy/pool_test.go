package proxy

import (
	"testing"

	"github.com/deungjaho/hydra/internal/account"
)

func TestConcurrencyTracker_Basic(t *testing.T) {
	ct := NewConcurrencyTracker(2)
	if ct.IsAtCapacity(1) {
		t.Error("account 1 should not be at capacity initially")
	}
	ct.Acquire(1)
	ct.Acquire(1)
	if !ct.IsAtCapacity(1) {
		t.Error("account 1 should be at capacity after 2 acquires")
	}
	ct.Release(1)
	if ct.IsAtCapacity(1) {
		t.Error("account 1 should not be at capacity after 1 release")
	}
	ct.Release(1)
	if ct.InFlight(1) != 0 {
		t.Errorf("in-flight = %d, want 0", ct.InFlight(1))
	}
}

func TestConcurrencyTracker_Unlimited(t *testing.T) {
	ct := NewConcurrencyTracker(0) // 0 = unlimited
	for i := 0; i < 100; i++ {
		ct.Acquire(1)
	}
	if ct.IsAtCapacity(1) {
		t.Error("unlimited tracker should never be at capacity")
	}
}

func TestConcurrencyTracker_ReleaseUnderflow(t *testing.T) {
	ct := NewConcurrencyTracker(1)
	ct.Release(1) // should not panic or go negative
	if ct.InFlight(1) != 0 {
		t.Errorf("in-flight = %d, want 0", ct.InFlight(1))
	}
}

func TestQuotaWarnTier(t *testing.T) {
	q100 := int64(100)
	q10 := int64(10)
	q0 := int64(0)

	tests := []struct {
		name      string
		account   *account.Account
		threshold int32
		want      int
	}{
		{"healthy", &account.Account{QuotaRemaining: &q100}, 20, 0},
		{"warning", &account.Account{QuotaRemaining: &q10}, 20, 1},
		{"zero quota", &account.Account{QuotaRemaining: &q0}, 20, 1},
		{"nil quota", &account.Account{QuotaRemaining: nil}, 20, 1},
		{"disabled threshold", &account.Account{QuotaRemaining: &q10}, 0, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := quotaWarnTier(tt.account, tt.threshold)
			if got != tt.want {
				t.Errorf("quotaWarnTier = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestSelectAccount_ConcurrencyFilter(t *testing.T) {
	q50 := int64(50)
	q80 := int64(80)
	acc1 := &account.Account{ID: 1, QuotaRemaining: &q50}
	acc2 := &account.Account{ID: 2, QuotaRemaining: &q80}
	accounts := []*account.Account{acc1, acc2}

	limiter := NewRateLimitTracker()
	sticky := NewStickySessions()
	ct := NewConcurrencyTracker(1)

	// Acc1 at capacity, acc2 free → should pick acc2.
	ct.Acquire(1)
	got := SelectAccount(accounts, limiter, sticky, "balance", "model", "",
		false, true, true, ct, 0)
	if got.ID != 2 {
		t.Errorf("SelectAccount = %d, want 2 (acc1 at capacity)", got.ID)
	}

	// Both at capacity → should still return one (fallback).
	ct.Acquire(2)
	got = SelectAccount(accounts, limiter, sticky, "balance", "model", "",
		false, true, true, ct, 0)
	if got == nil {
		t.Error("SelectAccount returned nil when all at capacity")
	}
}

func TestSelectAccount_QuotaWarnDeprioritization(t *testing.T) {
	q80 := int64(80)
	q10 := int64(10)
	acc1 := &account.Account{ID: 1, QuotaRemaining: &q10} // warning zone
	acc2 := &account.Account{ID: 2, QuotaRemaining: &q80} // healthy
	accounts := []*account.Account{acc1, acc2}

	limiter := NewRateLimitTracker()
	sticky := NewStickySessions()
	ct := NewConcurrencyTracker(0)

	// With quotaWarnThreshold=20, acc1 (quota=10) should be deprioritized.
	got := SelectAccount(accounts, limiter, sticky, "balance", "model", "",
		false, true, true, ct, 20)
	if got.ID != 2 {
		t.Errorf("SelectAccount = %d, want 2 (acc1 in warning zone)", got.ID)
	}

	// Without quota warning, acc1 has lower quota but is still a candidate.
	// The sort should prefer acc2 (higher quota) anyway.
	got = SelectAccount(accounts, limiter, sticky, "balance", "model", "",
		false, true, true, ct, 0)
	if got.ID != 2 {
		t.Errorf("SelectAccount = %d, want 2 (higher quota)", got.ID)
	}
}
