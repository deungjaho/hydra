// Package version provides the single source of truth for the Antigravity
// client version, User-Agent string, and TLS fingerprint identity.
//
// All upstream-facing code (proxy, account OAuth, quota fetch) must use these
// values to ensure a consistent client identity across every endpoint.
// A mismatched UA or version causes 403 SERVICE_DISABLED from Google.
package version

import (
	"fmt"
	"os"
)

// antigravityVersion is the default Antigravity desktop client version.
// Override with HYDRA_UPSTREAM_CLIENT_VERSION env var.
const antigravityVersion = "4.4.7"

// chromeVersion and electronVersion mirror the Chromium/Electron versions
// reported by the real Antigravity desktop app's User-Agent string.
const (
	chromeVersion   = "132.0.6834.160"
	electronVersion = "39.2.3"
)

// ClientVersion returns the Antigravity client version string (e.g. "4.4.7").
// Honours HYDRA_UPSTREAM_CLIENT_VERSION if set.
func ClientVersion() string {
	if v := os.Getenv("HYDRA_UPSTREAM_CLIENT_VERSION"); v != "" {
		return v
	}
	return antigravityVersion
}

// UserAgent returns the full User-Agent string matching the Antigravity
// desktop app format. The upstream validates client identity via UA.
func UserAgent() string {
	return fmt.Sprintf("Antigravity/%s (Macintosh; Intel Mac OS X 10_15_7) Chrome/%s Electron/%s",
		ClientVersion(), chromeVersion, electronVersion)
}

// OAuthUserAgent returns the User-Agent used for OAuth token exchange and
// related account-management calls (loadCodeAssist, userinfo).
func OAuthUserAgent() string {
	return fmt.Sprintf("vscode/1.X.X (Antigravity/%s)", ClientVersion())
}
