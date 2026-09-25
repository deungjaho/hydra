package proxy

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/deungjaho/hydra/internal/version"
)

// Antigravity v1internal upstream base (daily sandbox used by agy CLI).
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
// times out, the upstream request is cancelled and resources are released.
//
// machineID and projectID are accepted for backwards compatibility with
// existing caller signatures. The project is passed in the JSON envelope body
// matching native agy CLI behavior; no x-goog-user-project header is sent.
//
// The caller is responsible for closing resp.Body.
func SendRequest(ctx context.Context, client *http.Client, accessToken, projectID string, body []byte, stream bool, machineID string) (*http.Response, error) {
	urlStr := UpstreamURL(stream)
	return doSend(ctx, client, urlStr, accessToken, body)
}

func doSend(
	ctx context.Context,
	client *http.Client,
	urlStr, accessToken string,
	body []byte,
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
// upstream connection is released even if the caller leaves early.
type cancelOnClose struct {
	io.ReadCloser
	cancel context.CancelFunc
}

func (b *cancelOnClose) Close() error {
	err := b.ReadCloser.Close()
	b.cancel()
	return err
}
