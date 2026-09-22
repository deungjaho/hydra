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

// antigravityVersion is the default Antigravity CLI client version.
// Override with HYDRA_UPSTREAM_CLIENT_VERSION env var.
const antigravityVersion = "1.2.8"

// ClientVersion returns the Antigravity client version string (e.g. "1.2.8").
// Honours HYDRA_UPSTREAM_CLIENT_VERSION if set.
func ClientVersion() string {
	if v := os.Getenv("HYDRA_UPSTREAM_CLIENT_VERSION"); v != "" {
		return v
	}
	return antigravityVersion
}

// UserAgent returns the full User-Agent string matching native agy CLI format.
// The upstream validates client identity via UA.
func UserAgent() string {
	return fmt.Sprintf("antigravity/cli/%s (aidev_client; os_type=darwin; arch=arm64; cl=985603393; auth_method=consumer)",
		ClientVersion())
}

// OAuthUserAgent returns the User-Agent used for OAuth token exchange and
// related account-management calls (loadCodeAssist, userinfo).
func OAuthUserAgent() string {
	return fmt.Sprintf("antigravity/cli/%s (aidev_client; os_type=darwin; arch=arm64; cl=985603393; auth_method=consumer)", ClientVersion())
}
