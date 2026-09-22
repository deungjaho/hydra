package proxy

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/deungjaho/hydra/internal/version"
	"github.com/google/uuid"
)

// Antigravity v1internal upstream base (sandbox used by the desktop app).
const v1InternalBase = "https://daily-cloudcode-pa.googleapis.com/v1internal"

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
// ctx is the inbound client request context: if the client disconnects or
// times out, the upstream request is cancelled and its h2 stream slot is
// freed instead of starving the shared connection.
//
// machineID is the per-account machine identifier (persisted in DB). Pass
// an empty string to generate a random one (for backwards compatibility).
//
// Implements the 403 SERVICE_DISABLED fallback: when the request includes the
// x-goog-user-project header and the upstream returns 403, retry the exact
// same request without that header. Without the header, Google infers the
// project from the OAuth token's scope and bypasses the project-level
// API-enabled check.
//
// The caller is responsible for closing resp.Body.
func SendRequest(ctx context.Context, client *http.Client, accessToken, projectID string, body []byte, stream bool, machineID string) (*http.Response, error) {
	urlStr := UpstreamURL(stream)
	if machineID == "" {
		machineID = newMachineID()
	}
	sessionID := newSessionID()

	resp, err := doSend(ctx, client, urlStr, accessToken, projectID, body, machineID, sessionID, true)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode == 403 {
		// Drain and close the first response before retrying.
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
		resp2, err := doSend(ctx, client, urlStr, accessToken, projectID, body, machineID, sessionID, false)
		if err != nil {
			return nil, err
		}
		return resp2, nil
	}
	return resp, nil
}

func doSend(
	ctx context.Context,
	client *http.Client,
	urlStr, accessToken, projectID string,
	body []byte,
	machineID, sessionID string,
	includeProjectHeader bool,
) (*http.Response, error) {
	reqCtx, cancel := context.WithCancel(ctx)
	req, err := http.NewRequestWithContext(reqCtx, "POST", urlStr, bytes.NewReader(body))
	if err != nil {
		cancel()
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("User-Agent", version.UserAgent())
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-client-name", "antigravity")
	req.Header.Set("x-client-version", version.ClientVersion())
	req.Header.Set("x-machine-id", machineID)
	req.Header.Set("x-vscode-sessionid", sessionID)

	if includeProjectHeader && projectID != "" && projectID != "test-project" && projectID != "project-id" {
		req.Header.Set("x-goog-user-project", projectID)
	}

	// Bound the wait for response headers only. The whole-body timeout on
	// http.Client would also kill healthy long-running SSE streams; a hung
	// upstream that never sends headers is the actual failure mode.
	type result struct {
		resp *http.Response
		err  error
	}
	ch := make(chan result, 1)
	go func() {
		resp, err := client.Do(req)
		ch <- result{resp, err}
	}()
	timer := time.NewTimer(upstreamHeaderTimeout)
	defer timer.Stop()
	select {
	case res := <-ch:
		if res.err != nil {
			cancel()
			return nil, res.err
		}
		res.resp.Body = &cancelOnClose{ReadCloser: res.resp.Body, cancel: cancel}
		return res.resp, nil
	case <-timer.C:
		cancel()
		<-ch
		return nil, fmt.Errorf("upstream response headers timeout (%s)", upstreamHeaderTimeout)
	}
}

// cancelOnClose closes the body and cancels the request context so the
// upstream stream slot is released even if the caller leaves early.
type cancelOnClose struct {
	io.ReadCloser
	cancel context.CancelFunc
}

func (b *cancelOnClose) Close() error {
	err := b.ReadCloser.Close()
	b.cancel()
	return err
}

// newMachineID generates a random uppercase UUID for the x-machine-id header.
// In normal operation, the caller passes a per-account machine ID persisted
// in the DB; this function is the fallback when no account ID is available.
func newMachineID() string {
	return strings.ToUpper(uuid.NewString())
}

// newSessionID generates a fresh UUID for each request, mimicking the real
// Antigravity desktop app which creates a new VS Code session ID per request.
func newSessionID() string {
	return uuid.NewString()
}
