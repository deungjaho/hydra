package keychain

import (
	"testing"
)

func TestIsKeychainValue(t *testing.T) {
	tests := []struct {
		value string
		want  bool
	}{
		{KeychainSentinel, true},
		{"keychain:", true},
		{"sk-real-key", false},
		{"", false},
		{"ya29.real-token", false},
	}
	for _, tt := range tests {
		got := IsKeychainValue(tt.value)
		if got != tt.want {
			t.Errorf("IsKeychainValue(%q) = %v, want %v", tt.value, got, tt.want)
		}
	}
}

func TestResolveSecret_PlaintextFallback(t *testing.T) {
	// When the DB value is not the sentinel, it should be returned
	// directly without touching the keychain.
	got := ResolveSecret("sk-real-key", "some-keychain-key")
	if got != "sk-real-key" {
		t.Errorf("ResolveSecret plaintext = %q, want %q", got, "sk-real-key")
	}
}

func TestResolveSecret_EmptyValue(t *testing.T) {
	// Empty DB value should return empty without keychain access.
	got := ResolveSecret("", "some-keychain-key")
	if got != "" {
		t.Errorf("ResolveSecret empty = %q, want empty", got)
	}
}

func TestKeyNaming(t *testing.T) {
	if got := AccountAccessTokenKey(42); got != "account-42-access-token" {
		t.Errorf("AccountAccessTokenKey = %q", got)
	}
	if got := AccountRefreshTokenKey(42); got != "account-42-refresh-token" {
		t.Errorf("AccountRefreshTokenKey = %q", got)
	}
	if got := ProviderAPIKeyKey(7); got != "provider-7-api-key" {
		t.Errorf("ProviderAPIKeyKey = %q", got)
	}
	if got := ClientAPIKeyKey(99); got != "apikey-99-key" {
		t.Errorf("ClientAPIKeyKey = %q", got)
	}
}
