package keychain

import (
	"errors"
	"fmt"
	"log"

	"github.com/zalando/go-keyring"
)

// Service is the keychain service name used for all Hydra secrets.
const Service = "hydra"

// KeychainSentinel is stored in the DB in place of the real secret
// when the secret has been migrated to the keychain. On read, if the
// DB value equals this sentinel, the real value is fetched from the
// keychain.
const KeychainSentinel = "keychain:"

// ErrKeychainUnavailable is returned when the OS keychain is not
// accessible (e.g., headless Linux without D-Bus/Secret Service).
var ErrKeychainUnavailable = errors.New("keychain: OS keychain not available")

// Set stores a secret in the OS keychain. If the keychain is
// unavailable, it returns ErrKeychainUnavailable and the caller should
// fall back to storing the secret in the DB.
func Set(key, secret string) error {
	if err := keyring.Set(Service, key, secret); err != nil {
		log.Printf("keychain: failed to set %s: %v", key, err)
		return ErrKeychainUnavailable
	}
	return nil
}

// Get retrieves a secret from the OS keychain. If the keychain is
// unavailable or the secret is not found, it returns
// ErrKeychainUnavailable and the caller should fall back to the DB
// value.
func Get(key string) (string, error) {
	secret, err := keyring.Get(Service, key)
	if err != nil {
		if errors.Is(err, keyring.ErrNotFound) {
			return "", fmt.Errorf("keychain: %s not found: %w", key, err)
		}
		log.Printf("keychain: failed to get %s: %v", key, err)
		return "", ErrKeychainUnavailable
	}
	return secret, nil
}

// Delete removes a secret from the OS keychain. It is not an error if
// the secret does not exist.
func Delete(key string) error {
	if err := keyring.Delete(Service, key); err != nil {
		if errors.Is(err, keyring.ErrNotFound) {
			return nil
		}
		return err
	}
	return nil
}

// IsKeychainValue returns true if the DB value is the sentinel,
// indicating the real secret is in the keychain.
func IsKeychainValue(dbValue string) bool {
	return dbValue == KeychainSentinel
}

// ResolveSecret fetches the secret from the keychain if the DB value
// is the sentinel. Otherwise, it returns the DB value directly (for
// backward compatibility with secrets not yet migrated).
func ResolveSecret(dbValue, keychainKey string) string {
	if !IsKeychainValue(dbValue) {
		return dbValue
	}
	secret, err := Get(keychainKey)
	if err != nil {
		// Fall back to the DB value (which is the sentinel, but
		// better than returning empty). In practice, this means
		// the keychain is unavailable and the secret was already
		// migrated — the caller should handle this gracefully.
		log.Printf("keychain: cannot resolve %s, falling back to DB: %v", keychainKey, err)
		return ""
	}
	return secret
}

// --- Key naming helpers ---

// AccountAccessTokenKey returns the keychain key for an account's
// access token.
func AccountAccessTokenKey(accountID int64) string {
	return fmt.Sprintf("account-%d-access-token", accountID)
}

// AccountRefreshTokenKey returns the keychain key for an account's
// refresh token.
func AccountRefreshTokenKey(accountID int64) string {
	return fmt.Sprintf("account-%d-refresh-token", accountID)
}

// ProviderAPIKeyKey returns the keychain key for a provider's API key.
func ProviderAPIKeyKey(providerID int64) string {
	return fmt.Sprintf("provider-%d-api-key", providerID)
}

// ClientAPIKeyKey returns the keychain key for a client API key.
func ClientAPIKeyKey(apiKeyID int64) string {
	return fmt.Sprintf("apikey-%d-key", apiKeyID)
}
