package app

import (
	"context"
	"testing"
	"time"

	"github.com/deungjaho/hydra/internal/account"
)

// fakeAccountStore is an in-memory AccountStore for testing.
type fakeAccountStore struct {
	accounts map[int64]*account.Account
	nextID   int64
	err      error // if set, all ops return this error
}

func newFakeAccountStore() *fakeAccountStore {
	return &fakeAccountStore{accounts: make(map[int64]*account.Account), nextID: 1}
}

func (s *fakeAccountStore) ListAccounts() ([]*account.Account, error) {
	if s.err != nil {
		return nil, s.err
	}
	out := make([]*account.Account, 0, len(s.accounts))
	for _, a := range s.accounts {
		out = append(out, a)
	}
	return out, nil
}
func (s *fakeAccountStore) GetAccount(id int64) (*account.Account, error) {
	if s.err != nil {
		return nil, s.err
	}
	a, ok := s.accounts[id]
	if !ok {
		return nil, nil
	}
	return a, nil
}
func (s *fakeAccountStore) SetAccountDisabled(id int64, disabled bool) error {
	if s.err != nil {
		return s.err
	}
	a, ok := s.accounts[id]
	if !ok {
		return nil
	}
	a.OperatorDisabled = disabled
	return nil
}
func (s *fakeAccountStore) RemoveAccount(id int64) error {
	if s.err != nil {
		return s.err
	}
	delete(s.accounts, id)
	return nil
}

// fakeKeyStore is an in-memory KeyStore for testing.
type fakeKeyStore struct {
	keys   map[int64]*account.ApiKey
	nextID int64
	err    error
}

func newFakeKeyStore() *fakeKeyStore {
	return &fakeKeyStore{keys: make(map[int64]*account.ApiKey), nextID: 1}
}

func (s *fakeKeyStore) ListAPIKeys() ([]*account.ApiKey, error) {
	if s.err != nil {
		return nil, s.err
	}
	out := make([]*account.ApiKey, 0, len(s.keys))
	for _, k := range s.keys {
		out = append(out, k)
	}
	return out, nil
}
func (s *fakeKeyStore) AddAPIKey(key, label string) (int64, error) {
	if s.err != nil {
		return 0, s.err
	}
	id := s.nextID
	s.nextID++
	s.keys[id] = &account.ApiKey{ID: id, Key: key, Label: label, CreatedAt: time.Now().Unix()}
	return id, nil
}
func (s *fakeKeyStore) GetAPIKey(id int64) (*account.ApiKey, error) {
	if s.err != nil {
		return nil, s.err
	}
	k, ok := s.keys[id]
	if !ok {
		return nil, nil
	}
	return k, nil
}
func (s *fakeKeyStore) RemoveAPIKey(id int64) error {
	if s.err != nil {
		return s.err
	}
	delete(s.keys, id)
	return nil
}
func (s *fakeKeyStore) SetAPIKeyDisabled(id int64, disabled bool) error {
	if s.err != nil {
		return s.err
	}
	k, ok := s.keys[id]
	if !ok {
		return nil
	}
	k.Disabled = disabled
	return nil
}
func (s *fakeKeyStore) UpdateAPIKeyScheduling(id int64, mode string, noSticky bool) error {
	if s.err != nil {
		return s.err
	}
	k, ok := s.keys[id]
	if !ok {
		return nil
	}
	k.SchedulingMode = mode
	k.NoSticky = noSticky
	return nil
}

// fakeClock returns a fixed time.
type fakeClock struct{ t time.Time }

func (c fakeClock) Now() time.Time { return c.t }

func newTestService() *Service {
	return &Service{
		Accounts: newFakeAccountStore(),
		Keys:     newFakeKeyStore(),
		Clock:    fakeClock{t: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)},
	}
}

func TestListAccountsEmpty(t *testing.T) {
	s := newTestService()
	views, err := s.ListAccounts(context.Background(), ListAccountsOptions{})
	if err != nil {
		t.Fatalf("ListAccounts: %v", err)
	}
	if len(views) != 0 {
		t.Errorf("expected 0 accounts, got %d", len(views))
	}
}

func TestListAccountsFiltersDisabled(t *testing.T) {
	s := newTestService()
	s.Accounts.(*fakeAccountStore).accounts[1] = &account.Account{ID: 1, Email: "a@test"}
	s.Accounts.(*fakeAccountStore).accounts[2] = &account.Account{ID: 2, Email: "b@test", OperatorDisabled: true}
	s.Accounts.(*fakeAccountStore).accounts[3] = &account.Account{ID: 3, Email: "c@test", HealthDisabled: true}

	// Without IncludeDisabled → only active accounts.
	views, _ := s.ListAccounts(context.Background(), ListAccountsOptions{})
	if len(views) != 1 {
		t.Errorf("expected 1 active account, got %d", len(views))
	}

	// With IncludeDisabled → all accounts.
	views, _ = s.ListAccounts(context.Background(), ListAccountsOptions{IncludeDisabled: true})
	if len(views) != 3 {
		t.Errorf("expected 3 accounts, got %d", len(views))
	}
}

func TestGetAccountNotFound(t *testing.T) {
	s := newTestService()
	_, err := s.GetAccount(context.Background(), 999)
	if err == nil {
		t.Fatal("expected error for non-existent account")
	}
	ae, ok := err.(*AppError)
	if !ok {
		t.Fatalf("expected *AppError, got %T", err)
	}
	if ae.Code != CodeNotFound {
		t.Errorf("Code = %s, want %s", ae.Code, CodeNotFound)
	}
}

func TestDisableAccount(t *testing.T) {
	s := newTestService()
	s.Accounts.(*fakeAccountStore).accounts[1] = &account.Account{ID: 1, Email: "a@test"}
	if err := s.DisableAccount(context.Background(), 1); err != nil {
		t.Fatalf("DisableAccount: %v", err)
	}
	a, _ := s.Accounts.GetAccount(1)
	if !a.OperatorDisabled {
		t.Error("OperatorDisabled should be true")
	}
}

func TestEnableAccount(t *testing.T) {
	s := newTestService()
	s.Accounts.(*fakeAccountStore).accounts[1] = &account.Account{ID: 1, Email: "a@test", OperatorDisabled: true}
	if err := s.EnableAccount(context.Background(), 1); err != nil {
		t.Fatalf("EnableAccount: %v", err)
	}
	a, _ := s.Accounts.GetAccount(1)
	if a.OperatorDisabled {
		t.Error("OperatorDisabled should be false")
	}
}

func TestListKeysNoSecretLeak(t *testing.T) {
	s := newTestService()
	_, _ = s.Keys.AddAPIKey("hydra-secret-key-12345", "test")
	views, err := s.ListKeys(context.Background())
	if err != nil {
		t.Fatalf("ListKeys: %v", err)
	}
	if len(views) != 1 {
		t.Fatalf("expected 1 key, got %d", len(views))
	}
	// KeyPrefix must NOT contain the full key.
	if views[0].KeyPrefix == "hydra-secret-key-12345" {
		t.Error("KeyPrefix leaked full key value")
	}
	// KeyPrefix should contain prefix + ellipsis + suffix.
	if views[0].KeyPrefix == "" {
		t.Error("KeyPrefix is empty")
	}
}

func TestAddKeyReturnsFullKeyOnce(t *testing.T) {
	s := newTestService()
	result, err := s.AddKey(context.Background(), "test-label")
	if err != nil {
		t.Fatalf("AddKey: %v", err)
	}
	if result.FullKey == "" {
		t.Error("FullKey should be populated on creation")
	}
	if result.Label != "test-label" {
		t.Errorf("Label = %q, want test-label", result.Label)
	}
	// ListKeys should NOT include the full key.
	views, _ := s.ListKeys(context.Background())
	for _, v := range views {
		if v.KeyPrefix == result.FullKey {
			t.Error("ListKeys leaked full key")
		}
	}
}

func TestAddKeyDefaultLabel(t *testing.T) {
	s := newTestService()
	result, err := s.AddKey(context.Background(), "")
	if err != nil {
		t.Fatalf("AddKey: %v", err)
	}
	if result.Label != "default" {
		t.Errorf("Label = %q, want default", result.Label)
	}
}

func TestRemoveKeyNotFound(t *testing.T) {
	s := newTestService()
	err := s.RemoveKey(context.Background(), 999)
	if err == nil {
		t.Fatal("expected error for non-existent key")
	}
	ae, _ := err.(*AppError)
	if ae.Code != CodeNotFound {
		t.Errorf("Code = %s, want %s", ae.Code, CodeNotFound)
	}
}

func TestUpdateKeyScheduling(t *testing.T) {
	s := newTestService()
	id, _ := s.Keys.AddAPIKey("test-key", "test")
	if err := s.UpdateKeyScheduling(context.Background(), id, "performance", true); err != nil {
		t.Fatalf("UpdateKeyScheduling: %v", err)
	}
	k, _ := s.Keys.GetAPIKey(id)
	if k.SchedulingMode != "performance" {
		t.Errorf("SchedulingMode = %q, want performance", k.SchedulingMode)
	}
	if !k.NoSticky {
		t.Error("NoSticky should be true")
	}
}

func TestStatus(t *testing.T) {
	s := newTestService()
	s.Accounts.(*fakeAccountStore).accounts[1] = &account.Account{ID: 1, Email: "a@test"}
	s.Accounts.(*fakeAccountStore).accounts[2] = &account.Account{ID: 2, Email: "b@test", OperatorDisabled: true}
	_, _ = s.Keys.AddAPIKey("key1", "test1")
	_, _ = s.Keys.AddAPIKey("key2", "test2")

	status, err := s.Status(context.Background())
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if status.Accounts.Total != 2 {
		t.Errorf("Accounts.Total = %d, want 2", status.Accounts.Total)
	}
	if status.Accounts.Active != 1 {
		t.Errorf("Accounts.Active = %d, want 1", status.Accounts.Active)
	}
	if status.Accounts.Disabled != 1 {
		t.Errorf("Accounts.Disabled = %d, want 1", status.Accounts.Disabled)
	}
	if status.Keys.Total != 2 {
		t.Errorf("Keys.Total = %d, want 2", status.Keys.Total)
	}
}

func TestAppErrorExitCode(t *testing.T) {
	tests := []struct {
		code     ErrCode
		wantExit int
	}{
		{CodeNotFound, ExitGeneric},
		{CodeAlreadyExists, ExitGeneric},
		{CodeInvalidArgument, ExitUsage},
		{CodePermission, ExitPermission},
		{CodeUnavailable, ExitDependency},
		{CodeConfig, ExitConfig},
		{CodeInternal, ExitGeneric},
	}
	for _, tt := range tests {
		e := &AppError{Code: tt.code}
		if got := e.ExitCode(); got != tt.wantExit {
			t.Errorf("%s: ExitCode() = %d, want %d", tt.code, got, tt.wantExit)
		}
	}
}

func TestAsAppErrorWrapsGeneric(t *testing.T) {
	err := &AppError{Code: CodeNotFound}
	ae := AsAppError(err)
	if ae != err {
		t.Error("AsAppError should return the same *AppError")
	}

	ae = AsAppError(context.DeadlineExceeded)
	if ae.Code != CodeInternal {
		t.Errorf("Code = %s, want %s", ae.Code, CodeInternal)
	}
}

func TestAccountViewSchedulable(t *testing.T) {
	s := newTestService()
	s.Accounts.(*fakeAccountStore).accounts[1] = &account.Account{ID: 1, Email: "a@test"}
	s.Accounts.(*fakeAccountStore).accounts[2] = &account.Account{ID: 2, Email: "b@test", OperatorDisabled: true}
	s.Accounts.(*fakeAccountStore).accounts[3] = &account.Account{ID: 3, Email: "c@test", HealthDisabled: true}

	views, _ := s.ListAccounts(context.Background(), ListAccountsOptions{IncludeDisabled: true})
	for _, v := range views {
		switch v.ID {
		case 1:
			if !v.Schedulable {
				t.Error("account 1 should be schedulable")
			}
		case 2, 3:
			if v.Schedulable {
				t.Errorf("account %d should not be schedulable", v.ID)
			}
		}
	}
}

func TestKeyPrefixShortKey(t *testing.T) {
	// Short key should not panic and should contain ellipsis.
	p := keyPrefix("abc")
	if p == "" {
		t.Error("keyPrefix returned empty for short key")
	}
}
