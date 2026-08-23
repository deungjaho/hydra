package app

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/deungjaho/hydra/internal/account"
	"github.com/deungjaho/hydra/internal/db"
)

// newTempDB creates a fresh empty DB in a temp directory for testing.
func newTempDB(t *testing.T) (*db.Db, func()) {
	t.Helper()
	dir := t.TempDir()
	d, err := db.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	return d, func() { d.Close() }
}

// TestDBAccountStoreGetAccountNotFound verifies that the real DB-backed
// adapter returns (nil, nil) — not (nil, sql.ErrNoRows) — when an account
// is not found. This is the contract the service layer relies on to
// produce NOT_FOUND errors.
func TestDBAccountStoreGetAccountNotFound(t *testing.T) {
	d, cleanup := newTempDB(t)
	defer cleanup()
	store := &dbAccountStore{d: d}
	a, err := store.GetAccount(999)
	if err != nil {
		t.Errorf("expected nil error for not-found, got %v", err)
	}
	if a != nil {
		t.Errorf("expected nil account, got %+v", a)
	}
}

// TestDBKeyStoreGetAPIKeyNotFound verifies the same contract for keys.
func TestDBKeyStoreGetAPIKeyNotFound(t *testing.T) {
	d, cleanup := newTempDB(t)
	defer cleanup()
	store := &dbKeyStore{d: d}
	k, err := store.GetAPIKey(999)
	if err != nil {
		t.Errorf("expected nil error for not-found, got %v", err)
	}
	if k != nil {
		t.Errorf("expected nil key, got %+v", k)
	}
}

// TestServiceGetAccountNotFoundRealDB verifies the full path through the
// service layer with a real DB produces a NOT_FOUND AppError, not INTERNAL.
func TestServiceGetAccountNotFoundRealDB(t *testing.T) {
	d, cleanup := newTempDB(t)
	defer cleanup()
	svc := NewService(d, nil)
	_, err := svc.GetAccount(context.Background(), 999)
	if err == nil {
		t.Fatal("expected error for non-existent account")
	}
	ae, ok := err.(*AppError)
	if !ok {
		t.Fatalf("expected *AppError, got %T: %v", err, err)
	}
	if ae.Code != CodeNotFound {
		t.Errorf("Code = %s, want %s (real DB should produce NOT_FOUND, not INTERNAL)", ae.Code, CodeNotFound)
	}
}

// TestServiceGetKeyNotFoundRealDB verifies the same for keys.
func TestServiceGetKeyNotFoundRealDB(t *testing.T) {
	d, cleanup := newTempDB(t)
	defer cleanup()
	svc := NewService(d, nil)
	err := svc.RemoveKey(context.Background(), 999)
	if err == nil {
		t.Fatal("expected error for non-existent key")
	}
	ae, ok := err.(*AppError)
	if !ok {
		t.Fatalf("expected *AppError, got %T: %v", err, err)
	}
	if ae.Code != CodeNotFound {
		t.Errorf("Code = %s, want %s", ae.Code, CodeNotFound)
	}
}

// TestServiceDisableAccountRealDB verifies a full write path through the
// real DB-backed service.
func TestServiceDisableAccountRealDB(t *testing.T) {
	d, cleanup := newTempDB(t)
	defer cleanup()
	id, err := account.AddAccount(d, "test@example.com", "tok", "ref", "", 0)
	if err != nil {
		t.Fatalf("AddAccount: %v", err)
	}
	svc := NewService(d, nil)
	if err := svc.DisableAccount(context.Background(), id); err != nil {
		t.Fatalf("DisableAccount: %v", err)
	}
	a, err := account.GetAccount(d, id)
	if err != nil {
		t.Fatalf("GetAccount: %v", err)
	}
	if !a.OperatorDisabled {
		t.Error("OperatorDisabled should be true after DisableAccount")
	}
}

// TestServiceAddKeyRealDB verifies that AddKey through the service creates
// a key in the real DB and returns the full key once.
func TestServiceAddKeyRealDB(t *testing.T) {
	d, cleanup := newTempDB(t)
	defer cleanup()
	svc := NewService(d, nil)
	result, err := svc.AddKey(context.Background(), "test-label")
	if err != nil {
		t.Fatalf("AddKey: %v", err)
	}
	if result.FullKey == "" {
		t.Error("FullKey should be populated")
	}
	// Verify the key exists in the DB.
	k, err := account.GetAPIKey(d, result.ID)
	if err != nil {
		t.Fatalf("GetAPIKey: %v", err)
	}
	if k.Label != "test-label" {
		t.Errorf("Label = %q, want test-label", k.Label)
	}
}

// TestServiceStatusRealDB verifies Status with a real DB.
func TestServiceStatusRealDB(t *testing.T) {
	d, cleanup := newTempDB(t)
	defer cleanup()
	_, _ = account.AddAccount(d, "a@test", "t", "r", "", 0)
	_, _ = account.AddAPIKey(d, "key1", "label1")
	svc := NewService(d, nil, WithVersion("test"))
	status, err := svc.Status(context.Background())
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if status.Accounts.Total != 1 {
		t.Errorf("Accounts.Total = %d, want 1", status.Accounts.Total)
	}
	if status.Keys.Total != 1 {
		t.Errorf("Keys.Total = %d, want 1", status.Keys.Total)
	}
	if status.Version != "test" {
		t.Errorf("Version = %q, want test", status.Version)
	}
}

// TestServiceListAccountsRealDB verifies ListAccounts with a real DB
// including the disabled filter.
func TestServiceListAccountsRealDB(t *testing.T) {
	d, cleanup := newTempDB(t)
	defer cleanup()
	id1, _ := account.AddAccount(d, "a@test", "t", "r", "", 0)
	id2, _ := account.AddAccount(d, "b@test", "t", "r", "", 0)
	_ = account.SetAccountDisabled(d, id2, true)

	svc := NewService(d, nil)
	// Without IncludeDisabled → only active.
	views, _ := svc.ListAccounts(context.Background(), ListAccountsOptions{})
	if len(views) != 1 {
		t.Errorf("expected 1 active, got %d", len(views))
	}
	if views[0].ID != id1 {
		t.Errorf("expected account %d, got %d", id1, views[0].ID)
	}
	// With IncludeDisabled → all.
	views, _ = svc.ListAccounts(context.Background(), ListAccountsOptions{IncludeDisabled: true})
	if len(views) != 2 {
		t.Errorf("expected 2 total, got %d", len(views))
	}
}

// TestSqlErrNoRowsIsNotLeaked ensures that sql.ErrNoRows from the store
// layer does not leak into the service error message.
func TestSqlErrNoRowsIsNotLeaked(t *testing.T) {
	d, cleanup := newTempDB(t)
	defer cleanup()
	svc := NewService(d, nil)
	_, err := svc.GetAccount(context.Background(), 999)
	if err == nil {
		t.Fatal("expected error")
	}
	// The error message should NOT contain "sql: no rows" — it should
	// be a clean NOT_FOUND message.
	ae := err.(*AppError)
	if ae.Code != CodeNotFound {
		t.Errorf("Code = %s, want %s", ae.Code, CodeNotFound)
	}
	// Check that the cause is not sql.ErrNoRows (it should be nil).
	if ae.Cause == sql.ErrNoRows {
		t.Error("Cause should not be sql.ErrNoRows — adapter should have converted it")
	}
}
