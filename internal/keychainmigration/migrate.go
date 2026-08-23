// Package keychainmigration provides the migration logic to move
// plaintext secrets from the SQLite database to the OS keychain.
//
// This is a separate package from internal/keychain to avoid import
// cycles: the keychain package is imported by account and provider
// (for secret resolution), while this package imports all three.
package keychainmigration

import (
	"fmt"
	"log"

	"github.com/deungjaho/hydra/internal/account"
	"github.com/deungjaho/hydra/internal/db"
	"github.com/deungjaho/hydra/internal/keychain"
	"github.com/deungjaho/hydra/internal/provider"
)

// MigrateResult tracks the outcome of a keychain migration.
type MigrateResult struct {
	AccountsMigrated  int
	ProvidersMigrated int
	APIKeysMigrated   int
	Skipped           int // secrets that were already migrated or keychain unavailable
	Errors            []string
}

// Migrate moves all plaintext secrets from the DB to the OS keychain.
// After migration, the DB columns contain the sentinel value
// "keychain:" and the real secrets are stored in the keychain.
//
// If the keychain is unavailable, migration is skipped for that
// secret and the plaintext value remains in the DB.
func Migrate(d *db.Db) (*MigrateResult, error) {
	result := &MigrateResult{}

	// --- Migrate account tokens ---
	accounts, err := account.ListAccounts(d)
	if err != nil {
		return nil, fmt.Errorf("list accounts: %w", err)
	}
	for _, a := range accounts {
		// Access token
		if !keychain.IsKeychainValue(a.AccessToken) && a.AccessToken != "" {
			if err := keychain.Set(keychain.AccountAccessTokenKey(a.ID), a.AccessToken); err != nil {
				result.Skipped++
				result.Errors = append(result.Errors,
					fmt.Sprintf("account %d access token: %v", a.ID, err))
			} else {
				if err := account.UpdateAccessTokenOnly(d, a.ID, keychain.KeychainSentinel); err != nil {
					result.Errors = append(result.Errors,
						fmt.Sprintf("account %d access token DB update: %v", a.ID, err))
				} else {
					result.AccountsMigrated++
				}
			}
		}
		// Refresh token
		if !keychain.IsKeychainValue(a.RefreshToken) && a.RefreshToken != "" {
			if err := keychain.Set(keychain.AccountRefreshTokenKey(a.ID), a.RefreshToken); err != nil {
				result.Skipped++
				result.Errors = append(result.Errors,
					fmt.Sprintf("account %d refresh token: %v", a.ID, err))
			} else {
				if err := account.UpdateRefreshTokenOnly(d, a.ID, keychain.KeychainSentinel); err != nil {
					result.Errors = append(result.Errors,
						fmt.Sprintf("account %d refresh token DB update: %v", a.ID, err))
				}
			}
		}
	}

	// --- Migrate provider API keys ---
	providers, err := provider.ListProviders(d)
	if err != nil {
		return nil, fmt.Errorf("list providers: %w", err)
	}
	for _, p := range providers {
		if !keychain.IsKeychainValue(p.APIKey) && p.APIKey != "" {
			if err := keychain.Set(keychain.ProviderAPIKeyKey(p.ID), p.APIKey); err != nil {
				result.Skipped++
				result.Errors = append(result.Errors,
					fmt.Sprintf("provider %d api key: %v", p.ID, err))
			} else {
				if err := provider.UpdateProviderAPIKeyOnly(d, p.ID, keychain.KeychainSentinel); err != nil {
					result.Errors = append(result.Errors,
						fmt.Sprintf("provider %d api key DB update: %v", p.ID, err))
				} else {
					result.ProvidersMigrated++
				}
			}
		}
	}

	// --- Migrate client API keys ---
	apiKeys, err := account.ListAPIKeys(d)
	if err != nil {
		return nil, fmt.Errorf("list api keys: %w", err)
	}
	for _, k := range apiKeys {
		if !keychain.IsKeychainValue(k.Key) && k.Key != "" {
			if err := keychain.Set(keychain.ClientAPIKeyKey(k.ID), k.Key); err != nil {
				result.Skipped++
				result.Errors = append(result.Errors,
					fmt.Sprintf("apikey %d: %v", k.ID, err))
			} else {
				if err := account.UpdateAPIKeyOnly(d, k.ID, keychain.KeychainSentinel); err != nil {
					result.Errors = append(result.Errors,
						fmt.Sprintf("apikey %d DB update: %v", k.ID, err))
				} else {
					result.APIKeysMigrated++
				}
			}
		}
	}

	log.Printf("keychain migration: %d accounts, %d providers, %d api keys migrated, %d skipped",
		result.AccountsMigrated, result.ProvidersMigrated, result.APIKeysMigrated, result.Skipped)

	return result, nil
}
