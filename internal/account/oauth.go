package account

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

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
	defaultOAuthClientID     = "OAUTH_CLIENT_ID"
	defaultOAuthClientSecret = "OAUTH_CLIENT_SECRET"

	oauthAuthURL            = "https://accounts.google.com/o/oauth2/v2/auth"
	oauthTokenURL           = "https://oauth2.googleapis.com/token"
	oauthUserInfoURL        = "https://www.googleapis.com/oauth2/v2/userinfo"
	loadCodeAssistURL       = "https://daily-cloudcode-pa.sandbox.googleapis.com/v1internal:loadCodeAssist"
	oauthUserAgent          = "vscode/1.X.X (Antigravity/4.4.7)"
	oauthRedirectURI        = "http://localhost"
	oauthRefreshSkew  int64 = 900
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
	req.Header.Set("User-Agent", oauthUserAgent)
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
	req.Header.Set("User-Agent", oauthUserAgent)
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
	req.Header.Set("User-Agent", oauthUserAgent)
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
	form := url.Values{
		"client_id":     {oauthClientID()},
		"client_secret": {oauthClientSecret()},
		"refresh_token": {refreshToken},
		"grant_type":    {"refresh_token"},
	}
	req, err := http.NewRequest("POST", oauthTokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return "", 0, err
	}
	req.Header.Set("User-Agent", oauthUserAgent)
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
