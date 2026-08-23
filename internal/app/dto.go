package app

// DTO types expose stable product semantics. They do not directly
// serialize SQLite rows or leak internal fields (tokens, raw error
// text, legacy columns). Secret fields are never included by default.

// AccountView is the stable representation of an account for CLI/TUI.
type AccountView struct {
	ID               int64    `json:"id"`
	Email            string   `json:"email"`
	ProjectID        string   `json:"project_id,omitempty"`
	OperatorDisabled bool     `json:"operator_disabled"`
	HealthDisabled   bool     `json:"health_disabled"`
	Schedulable      bool     `json:"schedulable"`
	QuotaMaxPct      int32    `json:"quota_max_pct,omitempty"`
	HasQuota         bool     `json:"has_quota"`
	ProtectedModels  []string `json:"protected_models,omitempty"`
	LastError        string   `json:"last_error,omitempty"`
	CreatedAt        string   `json:"created_at"` // RFC3339 UTC
}

// KeyView is the stable representation of an API key. The full key
// value is NEVER included here — only a prefix/fingerprint.
type KeyView struct {
	ID             int64  `json:"id"`
	Label          string `json:"label"`
	KeyPrefix      string `json:"key_prefix"`
	Disabled       bool   `json:"disabled"`
	SchedulingMode string `json:"scheduling_mode,omitempty"`
	NoSticky       bool   `json:"no_sticky"`
	CreatedAt      string `json:"created_at"` // RFC3339 UTC
}

// KeyCreatedView includes the full key value once, at creation time.
// Callers must warn the user that this is the only chance to save it.
type KeyCreatedView struct {
	KeyView
	FullKey string `json:"full_key"` // only populated on creation/rotate
}

// StatusView is the high-level service status.
type StatusView struct {
	Version    string       `json:"version"`
	ConfigPath string       `json:"config_path,omitempty"`
	DBPath     string       `json:"db_path,omitempty"`
	Accounts   StatusCounts `json:"accounts"`
	Keys       StatusCounts `json:"keys"`
}

type StatusCounts struct {
	Total    int `json:"total"`
	Active   int `json:"active"`
	Disabled int `json:"disabled"`
}

// DoctorView is the diagnostic output of `hydra doctor`.
type DoctorView struct {
	Checks []DoctorCheck `json:"checks"`
	OK     bool          `json:"ok"`
}

type DoctorCheck struct {
	Name   string `json:"name"`
	Status string `json:"status"` // "ok", "warn", "fail"
	Detail string `json:"detail,omitempty"`
}

// VersionView is the output of `hydra version`.
type VersionView struct {
	Version string `json:"version"`
	Commit  string `json:"commit,omitempty"`
}
