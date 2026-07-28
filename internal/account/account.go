package account

import (
	"encoding/json"
	"strconv"
	"strings"
	"time"

	"github.com/deungjaho/hydra/internal/db"
)

// Account is a bound Antigravity account.
//
// Disable state is a two-bit state machine:
//   - OperatorDisabled: user manually disabled via CLI/TUI (authoritative)
//   - HealthDisabled: health checker auto-disabled (auto-recoverable)
//
// An account is schedulable only when both are false.
// OperatorDisabled takes precedence: health recovery never clears it.
type Account struct {
	ID               int64
	Email            string
	AccessToken      string
	RefreshToken     string
	ProjectID        string // empty if None
	ExpiresAt        int64  // unix seconds
	QuotaRemaining   int64  // 0 if unknown (NULL)
	HasQuotaRem      bool   // true when QuotaRemaining is non-NULL
	QuotaFetchedAt   int64  // 0 if NULL
	HasQuotaFetched  bool
	QuotaJSON        string // empty if NULL
	HasQuotaJSON     bool
	QuotaSummary     string // empty if NULL
	HasQuotaSummary  bool
	ProtectedModels  []string
	LastUsedAt       int64 // 0 if NULL
	HasLastUsed      bool
	LastError        string // empty if NULL
	HasLastError     bool
	OperatorDisabled bool // user manually disabled (authoritative)
	HealthDisabled   bool // health check auto-disabled (auto-recoverable)
	CreatedAt        int64
}

// Disabled returns true if the account is not schedulable — either
// operator-disabled or health-disabled. This is the single source of
// truth for scheduling decisions.
func (a *Account) Disabled() bool {
	return a.OperatorDisabled || a.HealthDisabled
}

// QuotaModel is one entry parsed from quota_json.
type QuotaModel struct {
	Name       string `json:"name"`
	Percentage int32  `json:"percentage"`
	ResetTime  string `json:"reset_time"`
}

// QuotaModelList parses quota_json into QuotaModel entries.
func (a *Account) QuotaModels() []QuotaModel {
	if a.QuotaJSON == "" {
		return nil
	}
	var parsed struct {
		Models []QuotaModel `json:"models"`
	}
	if err := json.Unmarshal([]byte(a.QuotaJSON), &parsed); err != nil {
		return nil
	}
	out := parsed.Models[:0]
	for _, m := range parsed.Models {
		if m.Name == "" {
			continue
		}
		out = append(out, m)
	}
	return out
}

// AvailableModels is the lowercase set of models this account has in its quota.
func (a *Account) AvailableModels() map[string]struct{} {
	out := make(map[string]struct{})
	for _, q := range a.QuotaModels() {
		out[strings.ToLower(q.Name)] = struct{}{}
	}
	return out
}

// QuotaWindow is one aggregated quota window (5h or weekly).
type QuotaWindow struct {
	MaxPercentage  int32
	EarliestReset  int64
	SecsUntilReset int64
	Disabled       bool
}

// ResetIn formats secs_until_reset as "2h 15m", "3d 4h", or "now".
func (w QuotaWindow) ResetIn() string {
	s := w.SecsUntilReset
	if s <= 0 {
		return "now"
	}
	days := s / 86400
	hours := (s % 86400) / 3600
	mins := (s % 3600) / 60
	if days > 0 {
		return formatTwo(days, "d", hours, "h")
	}
	if hours > 0 {
		return formatTwo(hours, "h", mins, "m")
	}
	return formatOne(mins, "m")
}

func formatOne(v int64, unit string) string {
	return strconv.FormatInt(v, 10) + unit
}
func formatTwo(a int64, ua string, b int64, ub string) string {
	return strconv.FormatInt(a, 10) + ua + " " + strconv.FormatInt(b, 10) + ub
}

// QuotaWindows holds the four quota windows for an account.
type QuotaWindows struct {
	Gemini5h     *QuotaWindow
	GeminiWeekly *QuotaWindow
	Other5h      *QuotaWindow
	OtherWeekly  *QuotaWindow
}

// QuotaWindows parses quota_summary into the four canonical windows.
func (a *Account) QuotaWindowsParsed() QuotaWindows {
	if a.QuotaSummary == "" {
		return QuotaWindows{}
	}
	var parsed struct {
		Windows []struct {
			BucketID   string `json:"bucket_id"`
			Family     string `json:"family"`
			Window     string `json:"window"`
			Percentage int32  `json:"percentage"`
			ResetTime  string `json:"reset_time"`
			Disabled   bool   `json:"disabled"`
		} `json:"windows"`
	}
	if err := json.Unmarshal([]byte(a.QuotaSummary), &parsed); err != nil {
		return QuotaWindows{}
	}
	now := time.Now().Unix()
	var gw QuotaWindows
	for _, w := range parsed.Windows {
		if w.ResetTime == "" {
			continue
		}
		dt, err := time.Parse(time.RFC3339, w.ResetTime)
		if err != nil {
			continue
		}
		ts := dt.Unix()
		entry := QuotaWindow{
			MaxPercentage:  w.Percentage,
			EarliestReset:  ts,
			SecsUntilReset: ts - now,
			Disabled:       w.Disabled,
		}
		isGemini := w.Family == "gemini" || strings.HasPrefix(w.BucketID, "gemini")
		is5h := w.Window == "5h" || strings.HasSuffix(w.BucketID, "-5h")
		switch {
		case isGemini && is5h:
			gw.Gemini5h = &entry
		case isGemini && !is5h:
			gw.GeminiWeekly = &entry
		case !isGemini && is5h:
			gw.Other5h = &entry
		default:
			gw.OtherWeekly = &entry
		}
	}
	return gw
}

// RequestLog is one row in request_logs.
type RequestLog struct {
	ID               int64
	Ts               int64
	AccountID        int64 // 0 if NULL
	HasAccountID     bool
	Model            string // empty if NULL
	HasModel         bool
	PromptTokens     int64 // 0 if NULL
	HasPromptTokens  bool
	CompletionTokens int64
	HasCompletion    bool
	CachedTokens     int64
	HasCached        bool
	ThoughtTokens    int64
	HasThought       bool
	Status           int64
	ClientIP         string
	HasClientIP      bool
	Error            string
	HasError         bool
	CostUSD          float64
	HasCost          bool
	APIKeyID         int64
	HasAPIKeyID      bool
}

// UsageRow is an aggregated usage row.
type UsageRow struct {
	Label            string // model name or account email
	PromptTokens     int64
	CompletionTokens int64
	CachedTokens     int64
	ThoughtTokens    int64
	CostUSD          float64
	Requests         int64
}

// ApiKey is one row in api_keys.
type ApiKey struct {
	ID             int64
	Key            string
	Label          string
	Disabled       bool
	CreatedAt      int64
	SchedulingMode string // "" = 跟随全局；"cache"/"balance"/"performance" = per-key 覆盖
	NoSticky       bool   // true = 此 key 的请求跳过 sticky 绑定
}

// KeyUsage is aggregated usage per API key.
type KeyUsage struct {
	KeyID            int64
	HasKeyID         bool
	Label            string
	KeyPrefix        string
	PromptTokens     int64
	CompletionTokens int64
	CostUSD          float64
	Requests         int64
}

// KeyModelUsage is aggregated usage per API key × model.
type KeyModelUsage struct {
	KeyID            int64
	Label            string
	KeyPrefix        string
	Model            string
	PromptTokens     int64
	CompletionTokens int64
	CostUSD          float64
	Requests         int64
}

// Compile-time marker that Db is used.
var _ = db.Open
