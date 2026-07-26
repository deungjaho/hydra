package proxy

import (
	"bytes"
	"io"
	"net/http"
	"strings"
	"sync"

	"github.com/google/uuid"
)

// Antigravity v1internal upstream base (sandbox used by the desktop app).
const v1InternalBase = "https://daily-cloudcode-pa.sandbox.googleapis.com/v1internal"

// UserAgent must match the Antigravity desktop app format exactly — the
// upstream validates client identity via UA + headers. A mismatched UA causes 403.
const (
	upstreamUserAgent    = "Antigravity/4.3.0 (Macintosh; Intel Mac OS X 10_15_7) Chrome/132.0.6834.160 Electron/39.2.3"
	upstreamClientVersion = "4.3.0"
)

// UpstreamURL builds the upstream URL for a generateContent call.
// v1internal uses `{base}:{method}` format (model is passed in the body).
func UpstreamURL(stream bool) string {
	method := "generateContent"
	if stream {
		method = "streamGenerateContent"
		return v1InternalBase + ":" + method + "?alt=sse"
	}
	return v1InternalBase + ":" + method
}

// SendRequest sends a request to the upstream and returns the raw response.
//
// Implements the 403 SERVICE_DISABLED fallback: when the request includes the
// x-goog-user-project header and the upstream returns 403, retry the exact
// same request without that header. Without the header, Google infers the
// project from the OAuth token's scope and bypasses the project-level
// API-enabled check.
//
// The caller is responsible for closing resp.Body.
func SendRequest(client *http.Client, accessToken, projectID string, body []byte, stream bool) (*http.Response, error) {
	urlStr := UpstreamURL(stream)
	machineID := machineID()
	sessionID := sessionID()

	resp, err := doSend(client, urlStr, accessToken, projectID, body, machineID, sessionID, true)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode == 403 {
		// Drain and close the first response before retrying.
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
		resp2, err := doSend(client, urlStr, accessToken, projectID, body, machineID, sessionID, false)
		if err != nil {
			return nil, err
		}
		return resp2, nil
	}
	return resp, nil
}

func doSend(
	client *http.Client,
	urlStr, accessToken, projectID string,
	body []byte,
	machineID, sessionID string,
	includeProjectHeader bool,
) (*http.Response, error) {
	req, err := http.NewRequest("POST", urlStr, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("User-Agent", upstreamUserAgent)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-client-name", "antigravity")
	req.Header.Set("x-client-version", upstreamClientVersion)
	req.Header.Set("x-machine-id", machineID)
	req.Header.Set("x-vscode-sessionid", sessionID)
	req.Header.Set("anthropic-beta", "claude-code-20250219")

	if includeProjectHeader && projectID != "" && projectID != "test-project" && projectID != "project-id" {
		req.Header.Set("x-goog-user-project", projectID)
	}
	return client.Do(req)
}

// Lazily-generated stable machine/session identifiers.
var (
	machineIDOnce  sync.Once
	machineIDValue string
	sessionIDOnce  sync.Once
	sessionIDValue string
)

func machineID() string {
	machineIDOnce.Do(func() {
		machineIDValue = strings.ToUpper(uuid.NewString())
	})
	return machineIDValue
}

func sessionID() string {
	sessionIDOnce.Do(func() {
		sessionIDValue = uuid.NewString()
	})
	return sessionIDValue
}
