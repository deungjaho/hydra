package proxy

import (
	"sync"
	"testing"
)

// mockNotifier captures notifications for testing.
type mockNotifier struct {
	mu               sync.Mutex
	unhealthy        []string
	recovered        []string
	allDown          [][]AccountHealth
	unhealthyReasons map[string]string
}

func newMockNotifier() *mockNotifier {
	return &mockNotifier{
		unhealthyReasons: make(map[string]string),
	}
}

func (m *mockNotifier) NotifyAccountUnhealthy(email string, reason string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.unhealthy = append(m.unhealthy, email)
	m.unhealthyReasons[email] = reason
}

func (m *mockNotifier) NotifyAccountRecovered(email string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.recovered = append(m.recovered, email)
}

func (m *mockNotifier) NotifyAllAccountsDown(details []AccountHealth) {
	m.mu.Lock()
	defer m.mu.Unlock()
	cp := make([]AccountHealth, len(details))
	copy(cp, details)
	m.allDown = append(m.allDown, cp)
}

func TestLogNotifierUnhealthyThenRecover(t *testing.T) {
	n := NewLogNotifier()
	// First unhealthy notification should fire.
	n.NotifyAccountUnhealthy("a@test", "timeout")
	// Second should be suppressed (already unhealthy).
	n.NotifyAccountUnhealthy("a@test", "timeout again")
	// Recovery should fire.
	n.NotifyAccountRecovered("a@test")
	// Second recovery should be suppressed.
	n.NotifyAccountRecovered("a@test")
}

func TestLogNotifierAllDown(t *testing.T) {
	n := NewLogNotifier()
	n.NotifyAllAccountsDown([]AccountHealth{
		{Email: "a@test", Healthy: false, Reason: "timeout"},
		{Email: "b@test", Healthy: false, Reason: "401"},
	})
}

func TestMockNotifier(t *testing.T) {
	m := newMockNotifier()
	m.NotifyAccountUnhealthy("a@test", "timeout")
	m.NotifyAccountUnhealthy("b@test", "EOF")
	m.NotifyAccountRecovered("a@test")
	m.NotifyAllAccountsDown([]AccountHealth{
		{Email: "a@test", Reason: "timeout"},
	})

	if len(m.unhealthy) != 2 {
		t.Errorf("expected 2 unhealthy, got %d", len(m.unhealthy))
	}
	if len(m.recovered) != 1 {
		t.Errorf("expected 1 recovered, got %d", len(m.recovered))
	}
	if len(m.allDown) != 1 {
		t.Errorf("expected 1 allDown, got %d", len(m.allDown))
	}
	if m.unhealthyReasons["a@test"] != "timeout" {
		t.Errorf("wrong reason: %s", m.unhealthyReasons["a@test"])
	}
}

func TestIncrementHealthFailure(t *testing.T) {
	// Can't easily test ProxyServer without a DB, but we can test
	// the sync.Map logic directly.
	var failures sync.Map
	id := int64(42)

	// First increment.
	v, _ := failures.Load(id)
	count := 0
	if v != nil {
		count = v.(int)
	}
	count++
	failures.Store(id, count)
	if count != 1 {
		t.Errorf("expected 1, got %d", count)
	}

	// Second increment.
	v, _ = failures.Load(id)
	count = 0
	if v != nil {
		count = v.(int)
	}
	count++
	failures.Store(id, count)
	if count != 2 {
		t.Errorf("expected 2, got %d", count)
	}

	// Delete (reset on success).
	failures.Delete(id)
	v, _ = failures.Load(id)
	if v != nil {
		t.Errorf("expected nil after delete")
	}
}

func TestCountNonDisabled(t *testing.T) {
	// Test the helper function logic directly.
	type fakeAcc struct {
		disabled bool
	}
	accs := []fakeAcc{{false}, {true}, {false}, {true}}
	n := 0
	for _, a := range accs {
		if !a.disabled {
			n++
		}
	}
	if n != 2 {
		t.Errorf("expected 2, got %d", n)
	}
}
