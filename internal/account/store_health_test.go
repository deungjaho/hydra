package account

import (
	"path/filepath"
	"testing"

	"github.com/deungjaho/hydra/internal/db"
)

// testDB creates a fresh temporary DB for each test.
func testDB(t *testing.T) *db.Db {
	t.Helper()
	path := filepath.Join(t.TempDir(), "test.db")
	d, err := db.Open(path)
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { d.Close() })
	return d
}

// addTestAccount inserts a minimal account and returns its id.
func addTestAccount(t *testing.T, d *db.Db, email string) int64 {
	t.Helper()
	id, err := AddAccount(d, email, "tok", "refresh", "", 9999999999)
	if err != nil {
		t.Fatalf("AddAccount: %v", err)
	}
	return id
}

// TestOperatorDisableNotOverriddenByHealthRecovery reproduces the bug
// where a manually disabled account (that was previously health-disabled)
// gets re-enabled by the health checker's MarkHealthRecovered.
//
// Steps:
//  1. Health check disables the account (health_disabled=1, disabled=1).
//  2. Operator manually disables the account.
//  3. Health check probes the account, it's healthy → MarkHealthRecovered.
//  4. BUG: account is now disabled=0 despite operator's manual disable.
//
// After the fix, step 3 should NOT clear disabled when operator_disabled=1.
func TestOperatorDisableNotOverriddenByHealthRecovery(t *testing.T) {
	d := testDB(t)
	id := addTestAccount(t, d, "op@test")

	// Step 1: health check auto-disables.
	if err := MarkHealthDisabled(d, id, "health check: timeout"); err != nil {
		t.Fatalf("MarkHealthDisabled: %v", err)
	}
	acc, _ := GetAccount(d, id)
	if !acc.Disabled() || !acc.HealthDisabled {
		t.Fatalf("after health disable: Disabled=%v HealthDisabled=%v", acc.Disabled(), acc.HealthDisabled)
	}

	// Step 2: operator manually disables.
	if err := SetAccountDisabled(d, id, true); err != nil {
		t.Fatalf("SetAccountDisabled: %v", err)
	}

	// Step 3: health check probes and finds it healthy → recover.
	if err := MarkHealthRecovered(d, id); err != nil {
		t.Fatalf("MarkHealthRecovered: %v", err)
	}

	// Step 4: account should still be disabled because operator disabled it.
	acc, _ = GetAccount(d, id)
	if !acc.Disabled() {
		t.Errorf("BUG: operator-disabled account was re-enabled by health recovery; Disabled=%v", acc.Disabled())
	}
	if !acc.OperatorDisabled {
		t.Errorf("OperatorDisabled should still be true after health recovery")
	}
}
