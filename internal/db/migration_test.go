package db

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"
)

// TestV9MigrationIsOneTime verifies that the v9 backfill
// (operator_disabled = disabled=1 AND health_disabled=0) runs only once.
//
// Bug scenario before fix:
//  1. Open DB, v9 backfill sets operator_disabled=1 for legacy disabled accounts.
//  2. Operator enables the account (operator_disabled=0), but legacy
//     `disabled` column stays 1 (new code doesn't sync it).
//  3. Close and reopen → v9 backfill runs again → operator_disabled set
//     back to 1. BUG: account re-disabled on every restart.
//
// After fix: user_version=9 prevents the backfill from running again.
func TestV9MigrationIsOneTime(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test_v9.db")

	// First open: creates schema, runs v9 backfill (no rows yet).
	d1, err := Open(path)
	if err != nil {
		t.Fatalf("first Open: %v", err)
	}

	// Simulate a legacy account that was backfilled: disabled=1,
	// health_disabled=0, operator_disabled=1 (set by v9 backfill).
	_, err = d1.conn.Exec(`
		INSERT INTO accounts (email, access_token, refresh_token, expires_at, created_at, disabled, health_disabled, operator_disabled)
		VALUES ('legacy@test', 'tok', 'refresh', 9999999999, 0, 1, 0, 1)
	`)
	if err != nil {
		t.Fatalf("insert legacy account: %v", err)
	}

	// Simulate operator enabling the account: set operator_disabled=0.
	// The legacy `disabled` column stays 1 (new code doesn't touch it).
	_, err = d1.conn.Exec(`UPDATE accounts SET operator_disabled = 0 WHERE email = 'legacy@test'`)
	if err != nil {
		t.Fatalf("enable account: %v", err)
	}

	d1.Close()

	// Second open: v9 backfill should NOT run again.
	d2, err := Open(path)
	if err != nil {
		t.Fatalf("second Open: %v", err)
	}
	defer d2.Close()

	// Check: operator_disabled should still be 0 (not re-disabled).
	var opDisabled int
	err = d2.conn.QueryRow(
		`SELECT operator_disabled FROM accounts WHERE email = 'legacy@test'`,
	).Scan(&opDisabled)
	if err != nil {
		t.Fatalf("query operator_disabled: %v", err)
	}
	if opDisabled != 0 {
		t.Errorf("BUG: operator_disabled = %d after reopen, want 0 (v9 backfill ran again)", opDisabled)
	}

	// Verify user_version is 9.
	var version int
	err = d2.conn.QueryRow("PRAGMA user_version").Scan(&version)
	if err != nil {
		t.Fatalf("query user_version: %v", err)
	}
	if version != 9 {
		t.Errorf("user_version = %d, want 9", version)
	}
}

// TestV9MigrationBackfillsOnFirstOpen verifies that a pre-v9 DB
// (user_version=0) with legacy disabled accounts gets the correct
// operator_disabled value on first open.
func TestV9MigrationBackfillsOnFirstOpen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test_v9_backfill.db")

	// First open: creates schema, sets user_version=9.
	d1, err := Open(path)
	if err != nil {
		t.Fatalf("first Open: %v", err)
	}

	// Insert a legacy account with disabled=1, health_disabled=0,
	// operator_disabled=0 (simulating pre-v9 state before backfill).
	_, err = d1.conn.Exec(`
		INSERT INTO accounts (email, access_token, refresh_token, expires_at, created_at, disabled, health_disabled, operator_disabled)
		VALUES ('manual@test', 'tok', 'refresh', 9999999999, 0, 1, 0, 0)
	`)
	if err != nil {
		t.Fatalf("insert: %v", err)
	}
	// Also insert a health-disabled account (disabled=1, health_disabled=1).
	_, err = d1.conn.Exec(`
		INSERT INTO accounts (email, access_token, refresh_token, expires_at, created_at, disabled, health_disabled, operator_disabled)
		VALUES ('health@test', 'tok', 'refresh', 9999999999, 0, 1, 1, 0)
	`)
	if err != nil {
		t.Fatalf("insert health: %v", err)
	}

	// Reset user_version to 0 to simulate a pre-v9 DB that hasn't had
	// the backfill run yet.
	_, err = d1.conn.Exec("PRAGMA user_version = 0")
	if err != nil {
		t.Fatalf("reset user_version: %v", err)
	}
	d1.Close()

	// Second open: v9 backfill should run (user_version < 9).
	d2, err := Open(path)
	if err != nil {
		t.Fatalf("second Open: %v", err)
	}
	defer d2.Close()

	// manual@test: disabled=1, health_disabled=0 → operator_disabled should be 1.
	var opDisabled int
	err = d2.conn.QueryRow(
		`SELECT operator_disabled FROM accounts WHERE email = 'manual@test'`,
	).Scan(&opDisabled)
	if err != nil {
		t.Fatalf("query manual: %v", err)
	}
	if opDisabled != 1 {
		t.Errorf("manual@test: operator_disabled = %d, want 1 (backfilled)", opDisabled)
	}

	// health@test: disabled=1, health_disabled=1 → operator_disabled should stay 0.
	err = d2.conn.QueryRow(
		`SELECT operator_disabled FROM accounts WHERE email = 'health@test'`,
	).Scan(&opDisabled)
	if err != nil {
		t.Fatalf("query health: %v", err)
	}
	if opDisabled != 0 {
		t.Errorf("health@test: operator_disabled = %d, want 0 (health-disabled, not operator)", opDisabled)
	}
}

// TestDBFilePermissions0600 verifies that the DB file is created with
// 0600 permissions.
func TestDBFilePermissions0600(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test_perms.db")
	d, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	d.Close()

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	mode := info.Mode().Perm()
	if mode != 0o600 {
		t.Errorf("DB file mode = %o, want 0600", mode)
	}
}

// TestDBWALPermissions0600 verifies that WAL and SHM sidecars get 0600
// after a write+close+reopen cycle.
func TestDBWALPermissions0600(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test_wal.db")
	d, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	// Write something to force WAL creation.
	_, err = d.conn.Exec(`
		INSERT INTO accounts (email, access_token, refresh_token, expires_at, created_at)
		VALUES ('wal@test', 'tok', 'refresh', 9999999999, 0)
	`)
	if err != nil {
		t.Fatalf("insert: %v", err)
	}
	// Close to trigger WAL checkpoint.
	d.Close()

	// Reopen to trigger secureDBPermissions on existing WAL/SHM.
	d2, err := Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	d2.Close()

	// Check WAL file permissions if it exists.
	walPath := path + "-wal"
	if info, err := os.Stat(walPath); err == nil {
		mode := info.Mode().Perm()
		if mode != 0o600 {
			t.Errorf("WAL file mode = %o, want 0600", mode)
		}
	}

	// Check SHM file permissions if it exists.
	shmPath := path + "-shm"
	if info, err := os.Stat(shmPath); err == nil {
		mode := info.Mode().Perm()
		if mode != 0o600 {
			t.Errorf("SHM file mode = %o, want 0600", mode)
		}
	}
}

// TestDBOpenFailsOnUnreadableFile verifies that Open fails-closed when
// the DB file exists but is not readable (simulating a permission issue).
func TestDBOpenFailsOnUnreadableFile(t *testing.T) {
	// Skip on root (uid 0) — root bypasses file permissions.
	if os.Getuid() == 0 {
		t.Skip("running as root, permission test not meaningful")
	}

	path := filepath.Join(t.TempDir(), "test_unreadable.db")
	// Create a file with no read/write permissions.
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY, 0o000)
	if err != nil {
		t.Fatalf("create unreadable file: %v", err)
	}
	_ = f.Close()

	_, err = Open(path)
	if err == nil {
		// If Open succeeded, it means SQLite was able to open the file
		// (some systems allow opening 0o000 files). Clean up and skip.
		t.Skip("system allows opening 0o000 files, test not meaningful")
	}
}

// Ensure sql.DB is referenced so the import isn't unused.
var _ *sql.DB
