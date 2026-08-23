package account

import (
	"os"
	"testing"
	"time"
)

func TestExtractCode_Success(t *testing.T) {
	url := "http://localhost/?code=4/0Axx123&scope=x&state=abc"
	code, err := extractCode(url, "abc")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if code != "4/0Axx123" {
		t.Errorf("code = %q, want 4/0Axx123", code)
	}
}

func TestExtractCode_NoQuery(t *testing.T) {
	// URL without ? — entire string is treated as query.
	code, err := extractCode("code=abc123&state=xyz", "xyz")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if code != "abc123" {
		t.Errorf("code = %q, want abc123", code)
	}
}

func TestExtractCode_MissingCode(t *testing.T) {
	_, err := extractCode("http://localhost/?state=abc", "abc")
	if err == nil {
		t.Error("expected error for missing code")
	}
}

func TestExtractCode_EmptyURL(t *testing.T) {
	_, err := extractCode("", "")
	if err == nil {
		t.Error("expected error for empty URL")
	}
}

func TestExtractCode_StateMismatch(t *testing.T) {
	_, err := extractCode(
		"http://localhost/?code=abc&state=wrong", "expected")
	if err == nil {
		t.Error("expected error for state mismatch")
	}
}

func TestExtractCode_NoStateInURL(t *testing.T) {
	// If URL has no state param, it's accepted (state is optional in URL).
	code, err := extractCode("http://localhost/?code=abc", "expected")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if code != "abc" {
		t.Errorf("code = %q, want abc", code)
	}
}

func TestExtractCode_TrimWhitespace(t *testing.T) {
	url := "http://localhost/?code=  abc123  &state=xyz"
	code, err := extractCode(url, "xyz")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if code != "abc123" {
		t.Errorf("code = %q, want abc123 (trimmed)", code)
	}
}

func TestNeedsRefresh_Expired(t *testing.T) {
	// Token expired 1 hour ago.
	expired := time.Now().Unix() - 3600
	if !NeedsRefresh(expired) {
		t.Error("expired token should need refresh")
	}
}

func TestNeedsRefresh_ExpiringSoon(t *testing.T) {
	// Token expires in 100 seconds (within 900s skew).
	soon := time.Now().Unix() + 100
	if !NeedsRefresh(soon) {
		t.Error("token expiring within skew should need refresh")
	}
}

func TestNeedsRefresh_NotExpired(t *testing.T) {
	// Token expires in 1 hour (well beyond 900s skew).
	future := time.Now().Unix() + 3600
	if NeedsRefresh(future) {
		t.Error("valid token should not need refresh")
	}
}

func TestNeedsRefresh_FarFuture(t *testing.T) {
	far := time.Now().Unix() + 86400
	if NeedsRefresh(far) {
		t.Error("token far in future should not need refresh")
	}
}

func TestOauthClientID_Default(t *testing.T) {
	os.Unsetenv("HYDRA_OAUTH_CLIENT_ID")
	got := oauthClientID()
	if got != defaultOAuthClientID {
		t.Errorf("default client ID = %q, want %q", got, defaultOAuthClientID)
	}
}

func TestOauthClientID_EnvOverride(t *testing.T) {
	os.Setenv("HYDRA_OAUTH_CLIENT_ID", "custom-client-id")
	defer os.Unsetenv("HYDRA_OAUTH_CLIENT_ID")
	got := oauthClientID()
	if got != "custom-client-id" {
		t.Errorf("client ID = %q, want custom-client-id", got)
	}
}

func TestOauthClientSecret_Default(t *testing.T) {
	os.Unsetenv("HYDRA_OAUTH_CLIENT_SECRET")
	got := oauthClientSecret()
	if got != defaultOAuthClientSecret {
		t.Errorf("default secret = %q, want default", got)
	}
}

func TestOauthClientSecret_EnvOverride(t *testing.T) {
	os.Setenv("HYDRA_OAUTH_CLIENT_SECRET", "custom-secret")
	defer os.Unsetenv("HYDRA_OAUTH_CLIENT_SECRET")
	got := oauthClientSecret()
	if got != "custom-secret" {
		t.Errorf("secret = %q, want custom-secret", got)
	}
}

func TestOauthClientID_EmptyEnvFallsBack(t *testing.T) {
	os.Setenv("HYDRA_OAUTH_CLIENT_ID", "")
	defer os.Unsetenv("HYDRA_OAUTH_CLIENT_ID")
	got := oauthClientID()
	if got != defaultOAuthClientID {
		t.Errorf("empty env should fall back to default, got %q", got)
	}
}
