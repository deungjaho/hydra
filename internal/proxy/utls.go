// Package proxy implements the OpenAI/Anthropic → Gemini v1internal gateway.
package proxy

import (
	"bufio"
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	utls "github.com/refraction-networking/utls"
	"golang.org/x/net/http2"
)

// NewUTLSClient returns an *http.Client whose Transport emulates Chrome 120
// via uTLS, with full HTTP/2 support over the uTLS connection.
//
// Required: Google's cloudcode-pa endpoints reject clients without a matching
// TLS fingerprint with 403 SERVICE_DISABLED. They also negotiate HTTP/2 via
// ALPN, so we must support h2 over the uTLS connection.
//
// If proxyURL is non-empty (e.g. "http://127.0.0.1:7890"), upstream TLS
// connections tunnel through the proxy via HTTP CONNECT, then perform the
// uTLS handshake over the tunnelled raw stream.
func NewUTLSClient(timeout time.Duration, proxyURL string) *http.Client {
	return &http.Client{
		Transport: newUTLSTransport(proxyURL),
		Timeout:   timeout,
	}
}

// utlsDialer manages uTLS connections with optional HTTP CONNECT proxy.
type utlsDialer struct {
	proxyURL string
}

func (d *utlsDialer) dialTLS(ctx context.Context, addr string) (net.Conn, error) {
	host, _, _ := net.SplitHostPort(addr)

	rawConn, err := dialUpstream(ctx, addr, d.proxyURL)
	if err != nil {
		return nil, err
	}

	config := &utls.Config{
		ServerName: host,
		NextProtos: []string{"h2", "http/1.1"},
	}
	uconn := utls.UClient(rawConn, config, utls.HelloChrome_120)
	if err := uconn.HandshakeContext(ctx); err != nil {
		_ = rawConn.Close()
		return nil, fmt.Errorf("uTLS handshake: %w", err)
	}
	return uconn, nil
}

// newUTLSTransport creates a custom http.RoundTripper that:
//   - For HTTP/2 (h2) connections: uses http2.Transport with uTLS dialing
//   - Falls back to HTTP/1.1 if the server doesn't support h2
type utlsTransport struct {
	h2transport *http2.Transport
	h1transport *http.Transport
	proxyURL    string
}

func newUTLSTransport(proxyURL string) *utlsTransport {
	dialer := &utlsDialer{proxyURL: proxyURL}

	t := &utlsTransport{
		proxyURL: proxyURL,
		h2transport: &http2.Transport{
			AllowHTTP: false,
			DialTLSContext: func(ctx context.Context, network, addr string, _ *tls.Config) (net.Conn, error) {
				return dialer.dialTLS(ctx, addr)
			},
		},
	}

	// HTTP/1.1 fallback transport (for non-h2 servers)
	t.h1transport = &http.Transport{
		DialTLSContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			return dialer.dialTLS(ctx, addr)
		},
		ForceAttemptHTTP2:     false,
		MaxIdleConns:          100,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   15 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
		ResponseHeaderTimeout: 60 * time.Second,
	}

	return t
}

// RoundTrip implements http.RoundTripper. It tries HTTP/2 first (since Google's
// cloudcode-pa endpoints negotiate h2), and falls back to HTTP/1.1 if the
// connection doesn't support h2.
func (t *utlsTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	// http2.Transport requires the request to use the canonical URL scheme.
	// It will dial via DialTLSContext, get a uTLS conn, check ALPN, and
	// use h2 if negotiated.
	resp, err := t.h2transport.RoundTrip(req)
	if err == nil {
		return resp, nil
	}

	// If h2 fails (e.g. server only supports HTTP/1.1), fall back.
	// http2.Transport returns an error like "http2: server sent GOAWAY"
	// or connection errors. We retry with HTTP/1.1.
	// Note: this creates a new connection, but that's acceptable.
	return t.h1transport.RoundTrip(req)
}

// dialUpstream establishes a raw TCP stream to addr, optionally tunnelling
// through an HTTP CONNECT proxy.
func dialUpstream(ctx context.Context, addr, proxyURL string) (net.Conn, error) {
	dialer := &net.Dialer{
		Timeout:   30 * time.Second,
		KeepAlive: 30 * time.Second,
	}

	if proxyURL == "" {
		return dialer.DialContext(ctx, "tcp", addr)
	}

	u, err := url.Parse(proxyURL)
	if err != nil {
		return nil, fmt.Errorf("parse proxy URL: %w", err)
	}
	proxyAddr := u.Host
	if proxyAddr == "" {
		proxyAddr = proxyURL
	}
	if !strings.Contains(proxyAddr, ":") {
		proxyAddr += ":8080"
	}

	conn, err := dialer.DialContext(ctx, "tcp", proxyAddr)
	if err != nil {
		return nil, fmt.Errorf("dial proxy %s: %w", proxyAddr, err)
	}

	connectReq := &http.Request{
		Method: "CONNECT",
		URL:    &url.URL{Opaque: addr},
		Host:   addr,
		Header: make(http.Header),
	}
	if err := connectReq.Write(conn); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("write CONNECT: %w", err)
	}

	br := bufio.NewReader(conn)
	resp, err := http.ReadResponse(br, connectReq)
	if err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("read CONNECT response: %w", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		_ = conn.Close()
		return nil, fmt.Errorf("proxy CONNECT %s failed: %s", addr, resp.Status)
	}

	if br.Buffered() > 0 {
		return &bufferedConn{r: br, Conn: conn}, nil
	}
	return conn, nil
}

type bufferedConn struct {
	r *bufio.Reader
	net.Conn
}

func (c *bufferedConn) Read(b []byte) (int, error) {
	return c.r.Read(b)
}

// NewHTTPClient returns a standard *http.Client for non-uTLS calls (OAuth,
// quota fetch). When proxyURL is set, the transport routes through the HTTP
// proxy.
func NewHTTPClient(timeout time.Duration, proxyURL string) *http.Client {
	transport := &http.Transport{
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
	}
	return &http.Client{
		Transport: transport,
		Timeout:   timeout,
	}
}
