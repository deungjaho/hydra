package app

import (
	"context"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/deungjaho/hydra/internal/account"
	"github.com/deungjaho/hydra/internal/config"
	"github.com/deungjaho/hydra/internal/db"
)

// AccountStore is the consumer-side interface for account operations.
// Defined here so the service can be tested with a fake store.
type AccountStore interface {
	ListAccounts() ([]*account.Account, error)
	GetAccount(id int64) (*account.Account, error)
	SetAccountDisabled(id int64, disabled bool) error
	RemoveAccount(id int64) error
}

// KeyStore is the consumer-side interface for API key operations.
type KeyStore interface {
	ListAPIKeys() ([]*account.ApiKey, error)
	AddAPIKey(key, label string) (int64, error)
	GetAPIKey(id int64) (*account.ApiKey, error)
	RemoveAPIKey(id int64) error
	SetAPIKeyDisabled(id int64, disabled bool) error
	UpdateAPIKeyScheduling(id int64, mode string, noSticky bool) error
}

// Clock abstracts time for testing.
type Clock interface {
	Now() time.Time
}

type realClock struct{}

func (realClock) Now() time.Time { return time.Now() }

// Service is the application layer that CLI and TUI call into.
// It depends on store interfaces, not concrete DB types, and never
// prints to stdout/stderr or reads the terminal.
type Service struct {
	Accounts   AccountStore
	Keys       KeyStore
	Clock      Clock
	Config     *config.AppConfig
	ConfigPath string
	DBPath     string
	VersionStr string
	Commit     string
}

// NewService creates a Service backed by the given DB.
func NewService(d *db.Db, cfg *config.AppConfig, opts ...ServiceOption) *Service {
	s := &Service{
		Accounts: &dbAccountStore{d: d},
		Keys:     &dbKeyStore{d: d},
		Clock:    realClock{},
		Config:   cfg,
	}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

// ServiceOption configures a Service.
type ServiceOption func(*Service)

func WithClock(c Clock) ServiceOption       { return func(s *Service) { s.Clock = c } }
func WithConfigPath(p string) ServiceOption { return func(s *Service) { s.ConfigPath = p } }
func WithDBPath(p string) ServiceOption     { return func(s *Service) { s.DBPath = p } }
func WithVersion(v string) ServiceOption    { return func(s *Service) { s.VersionStr = v } }
func WithCommit(c string) ServiceOption     { return func(s *Service) { s.Commit = c } }

// --- DB-backed store implementations ---

type dbAccountStore struct{ d *db.Db }

func (s *dbAccountStore) ListAccounts() ([]*account.Account, error) {
	return account.ListAccounts(s.d)
}
func (s *dbAccountStore) GetAccount(id int64) (*account.Account, error) {
	return account.GetAccount(s.d, id)
}
func (s *dbAccountStore) SetAccountDisabled(id int64, disabled bool) error {
	return account.SetAccountDisabled(s.d, id, disabled)
}
func (s *dbAccountStore) RemoveAccount(id int64) error {
	return account.RemoveAccount(s.d, id)
}

type dbKeyStore struct{ d *db.Db }

func (s *dbKeyStore) ListAPIKeys() ([]*account.ApiKey, error) {
	return account.ListAPIKeys(s.d)
}
func (s *dbKeyStore) AddAPIKey(key, label string) (int64, error) {
	return account.AddAPIKey(s.d, key, label)
}
func (s *dbKeyStore) GetAPIKey(id int64) (*account.ApiKey, error) {
	return account.GetAPIKey(s.d, id)
}
func (s *dbKeyStore) RemoveAPIKey(id int64) error {
	return account.RemoveAPIKey(s.d, id)
}
func (s *dbKeyStore) SetAPIKeyDisabled(id int64, disabled bool) error {
	return account.SetAPIKeyDisabled(s.d, id, disabled)
}
func (s *dbKeyStore) UpdateAPIKeyScheduling(id int64, mode string, noSticky bool) error {
	return account.UpdateAPIKeyScheduling(s.d, id, mode, noSticky)
}

// --- Use cases ---

// ListAccountsOptions controls filtering for ListAccounts.
type ListAccountsOptions struct {
	IncludeDisabled bool // include operator/health-disabled accounts
}

// ListAccounts returns a list of AccountView DTOs.
func (s *Service) ListAccounts(ctx context.Context, opts ListAccountsOptions) ([]AccountView, error) {
	accounts, err := s.Accounts.ListAccounts()
	if err != nil {
		return nil, NewError(CodeInternal, "failed to list accounts", WithCause(err))
	}
	out := make([]AccountView, 0, len(accounts))
	for _, a := range accounts {
		if !opts.IncludeDisabled && a.Disabled() {
			continue
		}
		out = append(out, s.accountToView(a))
	}
	return out, nil
}

// GetAccount returns a single AccountView by ID.
func (s *Service) GetAccount(ctx context.Context, id int64) (AccountView, error) {
	a, err := s.Accounts.GetAccount(id)
	if err != nil {
		return AccountView{}, NewError(CodeInternal, "failed to get account", WithCause(err))
	}
	if a == nil {
		return AccountView{}, NotFound("account", id)
	}
	return s.accountToView(a), nil
}

// DisableAccount sets operator_disabled=true for the given account.
func (s *Service) DisableAccount(ctx context.Context, id int64) error {
	a, err := s.Accounts.GetAccount(id)
	if err != nil {
		return NewError(CodeInternal, "failed to get account", WithCause(err))
	}
	if a == nil {
		return NotFound("account", id)
	}
	if err := s.Accounts.SetAccountDisabled(id, true); err != nil {
		return NewError(CodeInternal, "failed to disable account", WithCause(err))
	}
	return nil
}

// EnableAccount sets operator_disabled=false for the given account.
func (s *Service) EnableAccount(ctx context.Context, id int64) error {
	a, err := s.Accounts.GetAccount(id)
	if err != nil {
		return NewError(CodeInternal, "failed to get account", WithCause(err))
	}
	if a == nil {
		return NotFound("account", id)
	}
	if err := s.Accounts.SetAccountDisabled(id, false); err != nil {
		return NewError(CodeInternal, "failed to enable account", WithCause(err))
	}
	return nil
}

// RemoveAccount deletes an account by ID.
func (s *Service) RemoveAccount(ctx context.Context, id int64) error {
	a, err := s.Accounts.GetAccount(id)
	if err != nil {
		return NewError(CodeInternal, "failed to get account", WithCause(err))
	}
	if a == nil {
		return NotFound("account", id)
	}
	if err := s.Accounts.RemoveAccount(id); err != nil {
		return NewError(CodeInternal, "failed to remove account", WithCause(err))
	}
	return nil
}

// ListKeys returns a list of KeyView DTOs. Full key values are never
// included — only prefixes.
func (s *Service) ListKeys(ctx context.Context) ([]KeyView, error) {
	keys, err := s.Keys.ListAPIKeys()
	if err != nil {
		return nil, NewError(CodeInternal, "failed to list keys", WithCause(err))
	}
	out := make([]KeyView, 0, len(keys))
	for _, k := range keys {
		out = append(out, s.keyToView(k))
	}
	return out, nil
}

// AddKey creates a new API key and returns a KeyCreatedView containing
// the full key value once. The caller is responsible for warning the
// user that this is the only chance to save the key.
func (s *Service) AddKey(ctx context.Context, label string) (KeyCreatedView, error) {
	if label == "" {
		label = "default"
	}
	fullKey := generateAPIKey()
	id, err := s.Keys.AddAPIKey(fullKey, label)
	if err != nil {
		return KeyCreatedView{}, NewError(CodeInternal, "failed to add key", WithCause(err))
	}
	k, err := s.Keys.GetAPIKey(id)
	if err != nil || k == nil {
		return KeyCreatedView{}, NewError(CodeInternal, "failed to read back key", WithCause(err))
	}
	return KeyCreatedView{
		KeyView: s.keyToView(k),
		FullKey: fullKey,
	}, nil
}

// RemoveKey deletes an API key by ID.
func (s *Service) RemoveKey(ctx context.Context, id int64) error {
	k, err := s.Keys.GetAPIKey(id)
	if err != nil {
		return NewError(CodeInternal, "failed to get key", WithCause(err))
	}
	if k == nil {
		return NotFound("key", id)
	}
	if err := s.Keys.RemoveAPIKey(id); err != nil {
		return NewError(CodeInternal, "failed to remove key", WithCause(err))
	}
	return nil
}

// DisableKey sets disabled=true for the given key.
func (s *Service) DisableKey(ctx context.Context, id int64) error {
	k, err := s.Keys.GetAPIKey(id)
	if err != nil {
		return NewError(CodeInternal, "failed to get key", WithCause(err))
	}
	if k == nil {
		return NotFound("key", id)
	}
	if err := s.Keys.SetAPIKeyDisabled(id, true); err != nil {
		return NewError(CodeInternal, "failed to disable key", WithCause(err))
	}
	return nil
}

// EnableKey sets disabled=false for the given key.
func (s *Service) EnableKey(ctx context.Context, id int64) error {
	k, err := s.Keys.GetAPIKey(id)
	if err != nil {
		return NewError(CodeInternal, "failed to get key", WithCause(err))
	}
	if k == nil {
		return NotFound("key", id)
	}
	if err := s.Keys.SetAPIKeyDisabled(id, false); err != nil {
		return NewError(CodeInternal, "failed to enable key", WithCause(err))
	}
	return nil
}

// UpdateKeyScheduling sets per-key scheduling override.
func (s *Service) UpdateKeyScheduling(ctx context.Context, id int64, mode string, noSticky bool) error {
	k, err := s.Keys.GetAPIKey(id)
	if err != nil {
		return NewError(CodeInternal, "failed to get key", WithCause(err))
	}
	if k == nil {
		return NotFound("key", id)
	}
	if err := s.Keys.UpdateAPIKeyScheduling(id, mode, noSticky); err != nil {
		return NewError(CodeInternal, "failed to update key scheduling", WithCause(err))
	}
	return nil
}

// Status returns the high-level service status.
func (s *Service) Status(ctx context.Context) (StatusView, error) {
	accounts, err := s.Accounts.ListAccounts()
	if err != nil {
		return StatusView{}, NewError(CodeInternal, "failed to list accounts", WithCause(err))
	}
	keys, err := s.Keys.ListAPIKeys()
	if err != nil {
		return StatusView{}, NewError(CodeInternal, "failed to list keys", WithCause(err))
	}
	var accts, keysC StatusCounts
	accts.Total = len(accounts)
	for _, a := range accounts {
		if a.Disabled() {
			accts.Disabled++
		} else {
			accts.Active++
		}
	}
	keysC.Total = len(keys)
	for _, k := range keys {
		if k.Disabled {
			keysC.Disabled++
		} else {
			keysC.Active++
		}
	}
	return StatusView{
		Version:    s.VersionStr,
		ConfigPath: s.ConfigPath,
		DBPath:     s.DBPath,
		Accounts:   accts,
		Keys:       keysC,
	}, nil
}

// Version returns version info.
func (s *Service) Version(ctx context.Context) VersionView {
	return VersionView{
		Version: s.VersionStr,
		Commit:  s.Commit,
	}
}

// --- helpers ---

// AccountToView converts an account.Account to an AccountView DTO.
// Exported so CLI commands that already have *account.Account can
// build DTOs without going through the service method.
func (s *Service) AccountToView(a *account.Account) AccountView {
	return s.accountToView(a)
}

func (s *Service) accountToView(a *account.Account) AccountView {
	v := AccountView{
		ID:               a.ID,
		Email:            a.Email,
		ProjectID:        a.ProjectID,
		OperatorDisabled: a.OperatorDisabled,
		HealthDisabled:   a.HealthDisabled,
		Schedulable:      !a.Disabled(),
		ProtectedModels:  a.ProtectedModels,
		LastError:        a.LastError,
		CreatedAt:        time.Unix(a.CreatedAt, 0).UTC().Format(time.RFC3339),
	}
	if a.HasQuotaRem {
		v.QuotaMaxPct = int32(a.QuotaRemaining)
		v.HasQuota = true
	}
	return v
}

// KeyToView converts an account.ApiKey to a KeyView DTO.
// Exported so CLI commands that already have *account.ApiKey can
// build DTOs without going through the service method.
func (s *Service) KeyToView(k *account.ApiKey) KeyView {
	return s.keyToView(k)
}

func (s *Service) keyToView(k *account.ApiKey) KeyView {
	return KeyView{
		ID:             k.ID,
		Label:          k.Label,
		KeyPrefix:      keyPrefix(k.Key),
		Disabled:       k.Disabled,
		SchedulingMode: k.SchedulingMode,
		NoSticky:       k.NoSticky,
		CreatedAt:      time.Unix(k.CreatedAt, 0).UTC().Format(time.RFC3339),
	}
}

// keyPrefix returns a non-reversible prefix for display, e.g. "hydra-…ab3f".
func keyPrefix(key string) string {
	if len(key) <= 8 {
		return key[:len(key)/2] + "…"
	}
	return key[:6] + "…" + key[len(key)-4:]
}

// generateAPIKey creates a new random API key string.
func generateAPIKey() string {
	return "hydra-" + strings.ReplaceAll(uuid.NewString(), "-", "")
}
