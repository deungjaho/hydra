package account

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/deungjaho/hydra/internal/version"
	"github.com/google/uuid"
)

// OAuth credentials for Antigravity's Google OAuth client.
//
// These are the same client ID and secret embedded in the Antigravity
// desktop application. OAuth desktop clients use a "public" client type
// where the secret is not truly confidential — it ships in the client
// binary and cannot be kept private. Hydra reuses these credentials to
// obtain OAuth tokens that work with the same upstream API.
//
// To use your own Google OAuth client, set the environment variables:
//
//	HYDRA_OAUTH_CLIENT_ID
//	HYDRA_OAUTH_CLIENT_SECRET
//
// If Google ever revokes the default client ID, users can self-serve
// by creating their own OAuth desktop client in Google Cloud Console
// and pointing Hydra at it via these env vars.
const (
	defaultOAuthClientID     = "1071006060591-tmhssin2h21lcre235vtolojh4g403ep.apps.googleusercontent.com"
	defaultOAuthClientSecret = "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"

	oauthAuthURL             = "https://accounts.google.com/o/oauth2/v2/auth"
	oauthTokenURL            = "https://oauth2.googleapis.com/token"
	oauthDeviceCodeURL       = "https://oauth2.googleapis.com/device/code"
	oauthUserInfoURL         = "https://www.googleapis.com/oauth2/v2/userinfo"
	loadCodeAssistURL        = "https://daily-cloudcode-pa.sandbox.googleapis.com/v1internal:loadCodeAssist"
	oauthRedirectURI         = "http://localhost"
	oauthRefreshSkew   int64 = 900
)

// oauthClientID returns the OAuth client ID, preferring the
// HYDRA_OAUTH_CLIENT_ID env var over the built-in default.
func oauthClientID() string {
	if v := os.Getenv("HYDRA_OAUTH_CLIENT_ID"); v != "" {
		return v
	}
	return defaultOAuthClientID
}

// oauthClientSecret returns the OAuth client secret, preferring the
// HYDRA_OAUTH_CLIENT_SECRET env var over the built-in default.
func oauthClientSecret() string {
	if v := os.Getenv("HYDRA_OAUTH_CLIENT_SECRET"); v != "" {
		return v
	}
	return defaultOAuthClientSecret
}

const oauthScopes = "openid " +
	"https://www.googleapis.com/auth/cloud-platform " +
	"https://www.googleapis.com/auth/userinfo.email " +
	"https://www.googleapis.com/auth/userinfo.profile " +
	"https://www.googleapis.com/auth/cclog " +
	"https://www.googleapis.com/auth/experimentsandconfigs"

// AuthResult is the outcome of a successful OAuth flow.
type AuthResult struct {
	Email        string
	AccessToken  string
	RefreshToken string
	ProjectID    string // empty if None
	ExpiresAt    int64
}

type tokenResponse struct {
	AccessToken  string `json:"access_token"`
	ExpiresIn    int64  `json:"expires_in"`
	RefreshToken string `json:"refresh_token,omitempty"`
	IDToken      string `json:"id_token,omitempty"`
}

type userInfo struct {
	Email string `json:"email"`
	Name  string `json:"name,omitempty"`
}

type loadCodeAssistResponse struct {
	CloudaicompanionProject string `json:"cloudaicompanionProject"`
}

// RunOAuthFlow runs the interactive OAuth flow in the terminal.
func RunOAuthFlow(client *http.Client) (*AuthResult, error) {
	state := strings.ReplaceAll(uuid.NewString(), "-", "")

	q := url.Values{}
	q.Set("client_id", oauthClientID())
	q.Set("redirect_uri", oauthRedirectURI)
	q.Set("response_type", "code")
	q.Set("scope", oauthScopes)
	q.Set("access_type", "offline")
	q.Set("prompt", "consent")
	q.Set("state", state)
	authURL := oauthAuthURL + "?" + q.Encode()

	fmt.Println()
	fmt.Println("=== Antigravity Account Authorization ===")
	fmt.Println()
	fmt.Println("1. Open this URL in any browser (local or remote):")
	fmt.Println()
	fmt.Println("   " + authURL)
	fmt.Println()
	fmt.Println("2. Sign in with your Google account and approve the permissions.")
	fmt.Println()
	fmt.Printf("3. After approval the browser will redirect to a URL like:\n"+
		"   http://localhost/?code=4/0Axx...&scope=...&state=%s\n", state)
	fmt.Println("   The page won't load — that's expected. Copy the ENTIRE URL from")
	fmt.Println("   the browser address bar and paste it below.")
	fmt.Println()

	callbackURL, err := prompt("Paste the full redirected URL here: ")
	if err != nil {
		return nil, err
	}
	code, err := extractCode(callbackURL, state)
	if err != nil {
		return nil, err
	}

	tokenResp, err := exchangeCode(client, code)
	if err != nil {
		return nil, err
	}
	user, err := getUserInfo(client, tokenResp.AccessToken)
	if err != nil {
		return nil, err
	}
	projectID, _ := FetchProjectID(client, tokenResp.AccessToken)

	if tokenResp.RefreshToken == "" {
		return nil, fmt.Errorf("oauth: Google did not return a refresh_token. " +
			"Revoke access at https://myaccount.google.com/permissions and try again.")
	}

	return &AuthResult{
		Email:        user.Email,
		AccessToken:  tokenResp.AccessToken,
		RefreshToken: tokenResp.RefreshToken,
		ProjectID:    projectID,
		ExpiresAt:    time.Now().Unix() + tokenResp.ExpiresIn,
	}, nil
}

func exchangeCode(client *http.Client, code string) (*tokenResponse, error) {
	form := url.Values{
		"client_id":     {oauthClientID()},
		"client_secret": {oauthClientSecret()},
		"code":          {code},
		"redirect_uri":  {oauthRedirectURI},
		"grant_type":    {"authorization_code"},
	}
	req, err := http.NewRequest("POST", oauthTokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", version.OAuthUserAgent())
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("oauth: token exchange failed (%d): %s", resp.StatusCode, string(body))
	}
	var t tokenResponse
	if err := json.Unmarshal(body, &t); err != nil {
		return nil, fmt.Errorf("oauth: invalid token response: %w | body: %s", err, string(body))
	}
	return &t, nil
}

func getUserInfo(client *http.Client, accessToken string) (*userInfo, error) {
	req, err := http.NewRequest("GET", oauthUserInfoURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", version.OAuthUserAgent())
	req.Header.Set("Authorization", "Bearer "+accessToken)
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("oauth: userinfo failed (%d): %s", resp.StatusCode, string(body))
	}
	var u userInfo
	if err := json.Unmarshal(body, &u); err != nil {
		return nil, fmt.Errorf("oauth: invalid userinfo: %w | body: %s", err, string(body))
	}
	return &u, nil
}

// FetchProjectID calls loadCodeAssist to get the user's cloudaicompanion project.
func FetchProjectID(client *http.Client, accessToken string) (string, error) {
	body := strings.NewReader(`{"metadata":{"ideType":"ANTIGRAVITY"}}`)
	req, err := http.NewRequest("POST", loadCodeAssistURL, body)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", version.OAuthUserAgent())
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+accessToken)
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("oauth: loadCodeAssist failed (%d): %s", resp.StatusCode, string(respBody))
	}
	var r loadCodeAssistResponse
	if err := json.Unmarshal(respBody, &r); err != nil {
		return "", fmt.Errorf("oauth: invalid loadCodeAssist: %w | body: %s", err, string(respBody))
	}
	return r.CloudaicompanionProject, nil
}

// RefreshToken refreshes an access_token using a refresh_token.
// Returns (new_access_token, new_expires_at).
func RefreshToken(client *http.Client, refreshToken string) (string, int64, error) {
	return RefreshTokenCtx(context.Background(), client, refreshToken)
}

// RefreshTokenCtx is the context-aware variant. When ctx is cancelled,
// the in-flight HTTP request is aborted immediately.
func RefreshTokenCtx(ctx context.Context, client *http.Client, refreshToken string) (string, int64, error) {
	form := url.Values{
		"client_id":     {oauthClientID()},
		"client_secret": {oauthClientSecret()},
		"refresh_token": {refreshToken},
		"grant_type":    {"refresh_token"},
	}
	req, err := http.NewRequestWithContext(ctx, "POST", oauthTokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return "", 0, err
	}
	req.Header.Set("User-Agent", version.OAuthUserAgent())
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := client.Do(req)
	if err != nil {
		return "", 0, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", 0, fmt.Errorf("oauth: refresh failed (%d): %s", resp.StatusCode, string(body))
	}
	var t tokenResponse
	if err := json.Unmarshal(body, &t); err != nil {
		return "", 0, fmt.Errorf("oauth: invalid refresh response: %w | body: %s", err, string(body))
	}
	return t.AccessToken, time.Now().Unix() + t.ExpiresIn, nil
}

// NeedsRefresh returns true if the token is within REFRESH_SKEW of expiry (or already expired).
func NeedsRefresh(expiresAt int64) bool {
	return expiresAt <= time.Now().Unix()+oauthRefreshSkew
}

func extractCode(callbackURL, expectedState string) (string, error) {
	query := callbackURL
	if i := strings.Index(callbackURL, "?"); i >= 0 {
		query = callbackURL[i+1:]
	}
	var code, state string
	for _, pair := range strings.Split(query, "&") {
		k, v, _ := strings.Cut(pair, "=")
		if k == "code" {
			code = strings.TrimSpace(v)
		} else if k == "state" {
			state = strings.TrimSpace(v)
		}
	}
	if code == "" {
		return "", fmt.Errorf("oauth: no `code` parameter found in the pasted URL")
	}
	if state != "" && state != expectedState {
		return "", fmt.Errorf("oauth: state mismatch (expected %s, got %s). "+
			"This may be a stale paste — restart the command.",
			expectedState, state)
	}
	return code, nil
}

func prompt(msg string) (string, error) {
	fmt.Print(msg)
	if err := os.Stdout.Sync(); err != nil {
		// non-fatal
		_ = err
	}
	reader := bufio.NewReader(os.Stdin)
	line, err := reader.ReadString('\n')
	if err != nil && err != io.EOF {
		return "", err
	}
	return strings.TrimSpace(line), nil
}

// DeviceCodeResult is the initial response from the device code endpoint.
type DeviceCodeResult struct {
	DeviceCode      string `json:"device_code"`
	UserCode        string `json:"user_code"`
	VerificationURL string `json:"verification_url"`
	ExpiresIn       int64  `json:"expires_in"`
	Interval        int64  `json:"interval"`
}

// RequestDeviceCode starts the OAuth Device Flow by requesting a device
// code from Google. The caller should display the user_code and
// verification_url to the user, then poll PollDeviceToken until a token
// is issued or the device code expires.
func RequestDeviceCode(client *http.Client) (*DeviceCodeResult, error) {
	form := url.Values{
		"client_id": {oauthClientID()},
		"scope":     {oauthScopes},
	}
	req, err := http.NewRequest("POST", oauthDeviceCodeURL, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", version.OAuthUserAgent())
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("oauth: device code request failed (%d): %s", resp.StatusCode, string(body))
	}
	var r DeviceCodeResult
	if err := json.Unmarshal(body, &r); err != nil {
		return nil, fmt.Errorf("oauth: invalid device code response: %w | body: %s", err, string(body))
	}
	if r.VerificationURL == "" {
		r.VerificationURL = "https://www.google.com/device"
	}
	if r.Interval == 0 {
		r.Interval = 5
	}
	return &r, nil
}

// PollDeviceToken polls the token endpoint for a device flow token.
// Returns:
//   - (*AuthResult, nil) on success
//   - (nil, ErrDevicePending) if the user hasn't completed authorization yet
//   - (nil, ErrDeviceExpired) if the device code expired
//   - (nil, other error) on failure
func PollDeviceToken(client *http.Client, deviceCode string) (*AuthResult, error) {
	form := url.Values{
		"client_id":     {oauthClientID()},
		"client_secret": {oauthClientSecret()},
		"device_code":   {deviceCode},
		"grant_type":    {"urn:ietf:params:oauth:grant-type:device_code"},
	}
	req, err := http.NewRequest("POST", oauthTokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", version.OAuthUserAgent())
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		var t tokenResponse
		if err := json.Unmarshal(body, &t); err != nil {
			return nil, fmt.Errorf("oauth: invalid token response: %w | body: %s", err, string(body))
		}
		if t.RefreshToken == "" {
			return nil, fmt.Errorf("oauth: Google did not return a refresh_token. " +
				"Revoke access at https://myaccount.google.com/permissions and try again.")
		}
		user, err := getUserInfo(client, t.AccessToken)
		if err != nil {
			return nil, err
		}
		projectID, _ := FetchProjectID(client, t.AccessToken)
		return &AuthResult{
			Email:        user.Email,
			AccessToken:  t.AccessToken,
			RefreshToken: t.RefreshToken,
			ProjectID:    projectID,
			ExpiresAt:    time.Now().Unix() + t.ExpiresIn,
		}, nil
	}

	// Parse error response.
	var errResp struct {
		Error string `json:"error"`
	}
	_ = json.Unmarshal(body, &errResp)
	switch errResp.Error {
	case "authorization_pending":
		return nil, ErrDevicePending
	case "expired_token":
		return nil, ErrDeviceExpired
	case "slow_down":
		return nil, ErrDeviceSlowDown
	default:
		return nil, fmt.Errorf("oauth: device poll failed (%d): %s", resp.StatusCode, string(body))
	}
}

// Device flow sentinel errors.
var (
	ErrDevicePending  = fmt.Errorf("oauth: authorization pending")
	ErrDeviceExpired  = fmt.Errorf("oauth: device code expired")
	ErrDeviceSlowDown = fmt.Errorf("oauth: polling too fast, slow down")
)

// RunDeviceFlow runs the complete OAuth Device Flow interactively in the
// terminal. This is an alternative to RunOAuthFlow that doesn't require
// the user to copy-paste a redirect URL — instead, Google displays a
// user code and the user visits a verification URL.
//
// This is particularly useful for SSH/headless environments where
// starting a local HTTP server to receive the redirect is impractical.
func RunDeviceFlow(client *http.Client) (*AuthResult, error) {
	dc, err := RequestDeviceCode(client)
	if err != nil {
		return nil, err
	}

	fmt.Println()
	fmt.Println("=== Antigravity Account Authorization (Device Flow) ===")
	fmt.Println()
	fmt.Printf("1. Go to: %s\n", dc.VerificationURL)
	fmt.Printf("2. Enter code: %s\n", dc.UserCode)
	fmt.Println("3. Sign in with your Google account and approve the permissions.")
	fmt.Println()
	fmt.Println("Waiting for authorization...")

	interval := time.Duration(dc.Interval) * time.Second
	deadline := time.Now().Add(time.Duration(dc.ExpiresIn) * time.Second)

	for time.Now().Before(deadline) {
		time.Sleep(interval)
		result, err := PollDeviceToken(client, dc.DeviceCode)
		if err == nil {
			fmt.Printf("Authorized as %s\n", result.Email)
			return result, nil
		}
		if err == ErrDevicePending {
			continue
		}
		if err == ErrDeviceSlowDown {
			interval += 5 * time.Second
			continue
		}
		return nil, err
	}
	return nil, fmt.Errorf("oauth: device code expired before authorization completed")
}
