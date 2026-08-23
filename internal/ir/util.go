package ir

import (
	"strings"
	"time"

	"github.com/google/uuid"
)

// compactUUID returns a UUID with hyphens removed, matching the format
// used by the production proxy for generated response IDs.
func compactUUID() string {
	return strings.ReplaceAll(uuid.NewString(), "-", "")
}

// nowUnix returns the current Unix timestamp in seconds.
func nowUnix() int64 {
	return time.Now().Unix()
}
