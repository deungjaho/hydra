package proxy

import (
	"testing"
	"time"
)

func TestRateLimitTracker_CleanupRemovesExpired(t *testing.T) {
	r := NewRateLimitTracker()
	r.SetCooldown(1, "gemini-3-pro", 1) // 1 second cooldown
	r.SetCooldown(2, "claude-sonnet-4-6", 300)

	// Wait for the first cooldown to expire.
	time.Sleep(1100 * time.Millisecond)

	r.Cleanup()

	// Expired entry should be gone.
	if r.IsLimited(1, "gemini-3-pro") {
		t.Error("cooldown for account 1 should have been cleaned up")
	}
	// Unexpired entry should remain.
	if !r.IsLimited(2, "claude-sonnet-4-6") {
		t.Error("cooldown for account 2 should still be active")
	}
}

func TestRateLimitTracker_IsLimitedDeletesExpired(t *testing.T) {
	r := NewRateLimitTracker()
	r.SetCooldown(1, "gemini-3-pro", 1)
	time.Sleep(1100 * time.Millisecond)

	// IsLimited should return false AND delete the expired entry.
	if r.IsLimited(1, "gemini-3-pro") {
		t.Error("expired cooldown should return false")
	}
	// Second call should still return false (entry was deleted).
	if r.IsLimited(1, "gemini-3-pro") {
		t.Error("deleted cooldown should return false")
	}
}

func TestStickySessions_CleanupRemovesIdle(t *testing.T) {
	s := NewStickySessions()
	s.Bind("session-1", 42)
	s.Bind("session-2", 99)

	// Access session-1 to update its lastAccess.
	time.Sleep(50 * time.Millisecond)
	_, _ = s.Get("session-1")

	// Cleanup with 30ms maxIdle — session-2 should be evicted, session-1 kept.
	s.Cleanup(30 * time.Millisecond)

	if id, ok := s.Get("session-1"); !ok || id != 42 {
		t.Errorf("session-1 should still be bound, got id=%d ok=%v", id, ok)
	}
	if _, ok := s.Get("session-2"); ok {
		t.Error("session-2 should have been cleaned up (idle)")
	}
}

func TestStickySessions_BindAndGet(t *testing.T) {
	s := NewStickySessions()
	s.Bind("abc", 7)
	id, ok := s.Get("abc")
	if !ok || id != 7 {
		t.Errorf("Get(abc) = %d, %v; want 7, true", id, ok)
	}
	if _, ok := s.Get("nonexistent"); ok {
		t.Error("Get(nonexistent) should return false")
	}
}

func TestStickySessions_Unbind(t *testing.T) {
	s := NewStickySessions()
	s.Bind("abc", 7)
	s.Unbind("abc")
	if _, ok := s.Get("abc"); ok {
		t.Error("Get after Unbind should return false")
	}
}
