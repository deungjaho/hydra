// Package proxy implements the OpenAI/Anthropic → Gemini v1internal gateway.
package proxy

import (
	"crypto/tls"
	"net/http"
	"net/url"
	"time"
)

// NewUpstreamClient returns an *http.Client whose Transport matches native agy CLI:
// standard Go crypto/tls fingerprint over HTTP/1.1 with chunked transfer encoding,
// with optional HTTP CONNECT proxy support.
//
// agy CLI is a pure Go binary compiled with Go's toolchain. It uses standard Go
// crypto/tls and HTTP/1.1 without ALPN h2 for endpoints on daily-cloudcode-pa.googleapis.com.
func NewUpstreamClient(proxyURL string) *http.Client {
	transport := &http.Transport{
		ForceAttemptHTTP2: false,
		// TLSNextProto non-nil disables HTTP/2 automatic upgrade and ALPN advertisement.
		TLSNextProto: make(map[string]func(authority string, c *tls.Conn) http.RoundTripper),
		TLSClientConfig: &tls.Config{
			// Go standard library crypto/tls defaults (identical to agy CLI binary).
		},
		MaxIdleConns:          100,
		MaxIdleConnsPerHost:   10,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   15 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
		ResponseHeaderTimeout: 60 * time.Second,
	}
	if proxyURL != "" {
		if u, err := url.Parse(proxyURL); err == nil && u.Host != "" {
			transport.Proxy = http.ProxyURL(u)
		}
	} else {
		transport.Proxy = http.ProxyFromEnvironment
	}
	return &http.Client{
		Transport: transport,
	}
}

// NewUTLSClient is an alias for NewUpstreamClient for backwards compatibility.
func NewUTLSClient(proxyURL string) *http.Client {
	return NewUpstreamClient(proxyURL)
}

// NewHTTPClient returns a standard *http.Client for non-streaming calls (OAuth,
// quota fetch). When proxyURL is set, the transport routes through the HTTP
// proxy; otherwise it respects standard proxy environment variables.
func NewHTTPClient(timeout time.Duration, proxyURL string) *http.Client {
	transport := &http.Transport{
		ForceAttemptHTTP2:     false,
		TLSNextProto:          make(map[string]func(authority string, c *tls.Conn) http.RoundTripper),
		MaxIdleConns:          10,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   15 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
		ResponseHeaderTimeout: 60 * time.Second,
	}
	if proxyURL != "" {
		u, err := url.Parse(proxyURL)
		if err == nil && u.Host != "" {
			transport.Proxy = http.ProxyURL(u)
		}
	} else {
		transport.Proxy = http.ProxyFromEnvironment
	}
	return &http.Client{
		Transport: transport,
		Timeout:   timeout,
	}
}
