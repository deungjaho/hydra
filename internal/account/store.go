package account

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/deungjaho/hydra/internal/db"
	"github.com/deungjaho/hydra/internal/keychain"
	"github.com/google/uuid"
)

const accountColumns = `id, email, access_token, refresh_token, project_id, expires_at,
                    quota_remaining, quota_fetched_at, quota_json, quota_summary, protected_models,
                    last_used_at, last_error, operator_disabled, health_disabled, created_at, machine_id`

func scanAccount(row interface {
	Scan(dest ...any) error
}) (*Account, error) {
	var a Account
	var projectID sql.NullString
	var quotaRemaining sql.NullInt64
	var quotaFetchedAt sql.NullInt64
	var quotaJSON sql.NullString
	var quotaSummary sql.NullString
	var protectedRaw sql.NullString
	var lastUsedAt sql.NullInt64
	var lastError sql.NullString
	var operatorDisabled int64
	var healthDisabled int64
	var machineID sql.NullString
	err := row.Scan(
		&a.ID, &a.Email, &a.AccessToken, &a.RefreshToken, &projectID, &a.ExpiresAt,
		&quotaRemaining, &quotaFetchedAt, &quotaJSON, &quotaSummary, &protectedRaw,
		&lastUsedAt, &lastError, &operatorDisabled, &healthDisabled, &a.CreatedAt, &machineID,
	)
	if err != nil {
		return nil, err
	}
	a.ProjectID = projectID.String
	// Resolve secrets from keychain if they were migrated.
	a.AccessToken = keychain.ResolveSecret(a.AccessToken, keychain.AccountAccessTokenKey(a.ID))
	a.RefreshToken = keychain.ResolveSecret(a.RefreshToken, keychain.AccountRefreshTokenKey(a.ID))
	if quotaRemaining.Valid {
		a.QuotaRemaining = &quotaRemaining.Int64
	}
	if quotaFetchedAt.Valid {
		a.QuotaFetchedAt = &quotaFetchedAt.Int64
	}
	a.QuotaJSON = quotaJSON.String
	a.QuotaSummary = quotaSummary.String
	if protectedRaw.Valid {
		_ = json.Unmarshal([]byte(protectedRaw.String), &a.ProtectedModels)
	}
	if lastUsedAt.Valid {
		a.LastUsedAt = &lastUsedAt.Int64
	}
	a.LastError = lastError.String
	a.OperatorDisabled = operatorDisabled != 0
	a.HealthDisabled = healthDisabled != 0
	a.MachineID = machineID.String
	return &a, nil
}

// AddAccount inserts a new account, or updates tokens if the email already
// exists (upsert). Returns the account id.
func AddAccount(d *db.Db, email, accessToken, refreshToken, projectID string, expiresAt int64) (int64, error) {
	var id int64
	err := d.WithConn(func(conn *sql.DB) error {
		// Try to find an existing account with this email first.
		row := conn.QueryRow(`SELECT id FROM accounts WHERE email = ?`, email)
		var existingID int64
		if err := row.Scan(&existingID); err == nil {
			// Account exists → update tokens. Re-auth clears health_disabled
			// (token was the problem) but preserves operator_disabled (user intent).
			id = existingID
			if projectID == "" {
				_, err := conn.Exec(
					`UPDATE accounts SET access_token = ?, refresh_token = ?, `+
						`expires_at = ?, last_error = NULL, `+
						`health_disabled = 0 WHERE id = ?`,
					accessToken, refreshToken, expiresAt, existingID,
				)
				return err
			}
			_, err := conn.Exec(
				`UPDATE accounts SET access_token = ?, refresh_token = ?, `+
					`project_id = COALESCE(?, project_id), expires_at = ?, `+
					`last_error = NULL, health_disabled = 0 WHERE id = ?`,
				accessToken, refreshToken, projectID, expiresAt, existingID,
			)
			return err
		}
		// Not found → insert new. Generate a per-account machine ID.
		machineID := strings.ToUpper(uuid.NewString())
		if projectID == "" {
			_, err := conn.Exec(
				`INSERT INTO accounts (email, access_token, refresh_token, project_id, expires_at, created_at, machine_id)
                 VALUES (?, ?, ?, NULL, ?, ?, ?)`,
				email, accessToken, refreshToken, expiresAt, time.Now().Unix(), machineID,
			)
			if err != nil {
				return err
			}
		} else {
			_, err := conn.Exec(
				`INSERT INTO accounts (email, access_token, refresh_token, project_id, expires_at, created_at, machine_id)
                 VALUES (?, ?, ?, ?, ?, ?, ?)`,
				email, accessToken, refreshToken, projectID, expiresAt, time.Now().Unix(), machineID,
			)
			if err != nil {
				return err
			}
		}
		return conn.QueryRow("SELECT last_insert_rowid()").Scan(&id)
	})
	return id, err
}

// ListAccounts returns all accounts ordered by id.
func ListAccounts(d *db.Db) ([]*Account, error) {
	var out []*Account
	err := d.WithConn(func(conn *sql.DB) error {
		rows, err := conn.Query(`SELECT ` + accountColumns + ` FROM accounts ORDER BY id`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			a, err := scanAccount(rows)
			if err != nil {
				return err
			}
			out = append(out, a)
		}
		return rows.Err()
	})
	return out, err
}

// GetAccount returns one account by id.
func GetAccount(d *db.Db, id int64) (*Account, error) {
	var a *Account
	err := d.WithConn(func(conn *sql.DB) error {
		row := conn.QueryRow(`SELECT `+accountColumns+` FROM accounts WHERE id = ?`, id)
		var err error
		a, err = scanAccount(row)
		return err
	})
	return a, err
}

// UpdateTokens persists a freshly refreshed access token.
func UpdateTokens(d *db.Db, id int64, accessToken string, expiresAt int64, projectID string) error {
	return d.WithConn(func(conn *sql.DB) error {
		if projectID == "" {
			_, err := conn.Exec(
				`UPDATE accounts SET access_token = ?, expires_at = ?, last_error = NULL WHERE id = ?`,
				accessToken, expiresAt, id,
			)
			return err
		}
		_, err := conn.Exec(
			`UPDATE accounts SET access_token = ?, expires_at = ?, `+
				`project_id = COALESCE(?, project_id), `+
				`last_error = NULL WHERE id = ?`,
			accessToken, expiresAt, projectID, id,
		)
		return err
	})
}

// UpdateAccessTokenOnly sets the access_token column to the given value
// without touching other fields. Used by keychain migration to replace
// the plaintext token with the sentinel value.
func UpdateAccessTokenOnly(d *db.Db, id int64, value string) error {
	return d.WithConn(func(conn *sql.DB) error {
		_, err := conn.Exec(`UPDATE accounts SET access_token = ? WHERE id = ?`, value, id)
		return err
	})
}

// UpdateRefreshTokenOnly sets the refresh_token column to the given
// value without touching other fields. Used by keychain migration.
func UpdateRefreshTokenOnly(d *db.Db, id int64, value string) error {
	return d.WithConn(func(conn *sql.DB) error {
		_, err := conn.Exec(`UPDATE accounts SET refresh_token = ? WHERE id = ?`, value, id)
		return err
	})
}

// UpdateAPIKeyOnly sets the key column for a client API key. Used by
// keychain migration to replace the plaintext key with the sentinel.
func UpdateAPIKeyOnly(d *db.Db, id int64, value string) error {
	return d.WithConn(func(conn *sql.DB) error {
		_, err := conn.Exec(`UPDATE api_keys SET key = ? WHERE id = ?`, value, id)
		return err
	})
}

// UpdateQuota persists freshly fetched quota data.
func UpdateQuota(
	d *db.Db, id int64,
	quotaJSON, quotaSummary string,
	maxPercentage int64, hasMax bool,
	protected []string,
) error {
	protectedJSON, _ := json.Marshal(protected)
	if protectedJSON == nil {
		protectedJSON = []byte("[]")
	}
	return d.WithConn(func(conn *sql.DB) error {
		if hasMax {
			_, err := conn.Exec(
				`UPDATE accounts
                 SET quota_json = ?, quota_summary = ?, quota_remaining = ?, quota_fetched_at = ?, protected_models = ?
                 WHERE id = ?`,
				quotaJSON, quotaSummary, maxPercentage, time.Now().Unix(), string(protectedJSON), id,
			)
			return err
		}
		_, err := conn.Exec(
			`UPDATE accounts
             SET quota_json = ?, quota_summary = ?, quota_remaining = NULL, quota_fetched_at = ?, protected_models = ?
             WHERE id = ?`,
			quotaJSON, quotaSummary, time.Now().Unix(), string(protectedJSON), id,
		)
		return err
	})
}

// MarkUsed updates last_used_at and optionally quota_remaining.
func MarkUsed(d *db.Db, id int64, quota int64, hasQuota bool) error {
	return d.WithConn(func(conn *sql.DB) error {
		if hasQuota {
			_, err := conn.Exec(
				`UPDATE accounts SET last_used_at = ?, quota_remaining = COALESCE(?, quota_remaining) WHERE id = ?`,
				time.Now().Unix(), quota, id,
			)
			return err
		}
		_, err := conn.Exec(
			`UPDATE accounts SET last_used_at = ? WHERE id = ?`,
			time.Now().Unix(), id,
		)
		return err
	})
}

// MarkError records an error on the account. The disable parameter is
// retained for API compatibility but should be false — health-check
// disabling now uses MarkHealthDisabled. When disable=true, it sets
// health_disabled (not operator_disabled) so the account can auto-recover.
func MarkError(d *db.Db, id int64, errMsg string, disable bool) error {
	return d.WithConn(func(conn *sql.DB) error {
		if disable {
			_, err := conn.Exec(
				`UPDATE accounts SET last_error = ?, health_disabled = 1 WHERE id = ?`,
				errMsg, id,
			)
			return err
		}
		_, err := conn.Exec(
			`UPDATE accounts SET last_error = ? WHERE id = ?`,
			errMsg, id,
		)
		return err
	})
}

// RemoveAccount deletes an account by id.
func RemoveAccount(d *db.Db, id int64) error {
	return d.WithConn(func(conn *sql.DB) error {
		_, err := conn.Exec(`DELETE FROM accounts WHERE id = ?`, id)
		return err
	})
}

// SetAccountDisabled toggles operator_disabled (manual user action).
// Does not touch health_disabled — a manually disabled account stays
// disabled regardless of health check results. Health recovery never
// clears operator_disabled.
func SetAccountDisabled(d *db.Db, id int64, disabled bool) error {
	return d.WithConn(func(conn *sql.DB) error {
		v := 0
		if disabled {
			v = 1
		}
		_, err := conn.Exec(`UPDATE accounts SET operator_disabled = ? WHERE id = ?`, v, id)
		return err
	})
}

// MarkHealthDisabled sets health_disabled=1. Used by the health checker
// when an account exceeds the failure threshold. The account remains
// probeable so it can auto-recover. Does not touch operator_disabled.
func MarkHealthDisabled(d *db.Db, id int64, reason string) error {
	return d.WithConn(func(conn *sql.DB) error {
		_, err := conn.Exec(
			`UPDATE accounts SET last_error = ?, health_disabled = 1 WHERE id = ?`,
			reason, id,
		)
		return err
	})
}

// MarkHealthRecovered clears health_disabled and last_error. Used by
// the health checker when a previously health-disabled account passes
// its probe again. Does NOT clear operator_disabled — if the operator
// manually disabled the account, it stays disabled.
func MarkHealthRecovered(d *db.Db, id int64) error {
	return d.WithConn(func(conn *sql.DB) error {
		_, err := conn.Exec(
			`UPDATE accounts SET health_disabled = 0, last_error = NULL WHERE id = ?`,
			id,
		)
		return err
	})
}

// LogRequest inserts a row into request_logs.
func LogRequest(d *db.Db, p LogRequestParams) error {
	return d.WithConn(func(conn *sql.DB) error {
		_, err := conn.Exec(
			`INSERT INTO request_logs
             (ts, account_id, model, prompt_tokens, completion_tokens, cached_tokens, thought_tokens,
              status, client_ip, error, api_key_id)
             VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			time.Now().Unix(),
			p.AccountID,
			p.Model,
			p.PromptTokens,
			p.CompletionTokens,
			p.CachedTokens,
			p.ThoughtTokens,
			p.Status,
			p.ClientIP,
			p.Error,
			p.APIKeyID,
		)
		return err
	})
}

type LogRequestParams struct {
	AccountID        *int64
	Model            *string
	PromptTokens     *int64
	CompletionTokens *int64
	CachedTokens     *int64
	ThoughtTokens    *int64
	Status           int64
	ClientIP         *string
	Error            *string
	APIKeyID         *int64
}

// RecentLogs returns the most recent log entries.
func RecentLogs(d *db.Db, limit int) ([]*RequestLog, error) {
	var out []*RequestLog
	err := d.WithConn(func(conn *sql.DB) error {
		rows, err := conn.Query(
			`SELECT id, ts, account_id, model, prompt_tokens, completion_tokens, cached_tokens, thought_tokens,
                    status, client_ip, error, api_key_id
             FROM request_logs ORDER BY ts DESC LIMIT ?`,
			limit,
		)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var l RequestLog
			var accountID sql.NullInt64
			var model sql.NullString
			var prompt sql.NullInt64
			var completion sql.NullInt64
			var cached sql.NullInt64
			var thought sql.NullInt64
			var clientIP sql.NullString
			var errMsg sql.NullString
			var apiKeyID sql.NullInt64
			if err := rows.Scan(
				&l.ID, &l.Ts, &accountID, &model, &prompt, &completion, &cached, &thought,
				&l.Status, &clientIP, &errMsg, &apiKeyID,
			); err != nil {
				return err
			}
			if accountID.Valid {
				l.AccountID = &accountID.Int64
			}
			if model.Valid {
				l.Model = &model.String
			}
			if prompt.Valid {
				l.PromptTokens = &prompt.Int64
			}
			if completion.Valid {
				l.CompletionTokens = &completion.Int64
			}
			if cached.Valid {
				l.CachedTokens = &cached.Int64
			}
			if thought.Valid {
				l.ThoughtTokens = &thought.Int64
			}
			if clientIP.Valid {
				l.ClientIP = &clientIP.String
			}
			if errMsg.Valid {
				l.Error = &errMsg.String
			}
			if apiKeyID.Valid {
				l.APIKeyID = &apiKeyID.Int64
			}
			out = append(out, &l)
		}
		return rows.Err()
	})
	return out, err
}

// UsageByModel aggregates usage grouped by model.
func UsageByModel(d *db.Db, sinceTs int64) ([]UsageRow, error) {
	return queryUsage(d, sinceTs, `
        SELECT model,
                SUM(prompt_tokens), SUM(completion_tokens), SUM(cached_tokens), SUM(thought_tokens),
                COUNT(*)
         FROM request_logs
         WHERE ts >= ? AND status = 200 AND model IS NOT NULL
         GROUP BY model
         ORDER BY SUM(prompt_tokens) + SUM(completion_tokens) DESC`)
}

// UsageByAccount aggregates usage grouped by account email.
func UsageByAccount(d *db.Db, sinceTs int64) ([]UsageRow, error) {
	return queryUsage(d, sinceTs, `
        SELECT a.email,
                SUM(r.prompt_tokens), SUM(r.completion_tokens), SUM(r.cached_tokens), SUM(r.thought_tokens),
                COUNT(*)
         FROM request_logs r
         LEFT JOIN accounts a ON a.id = r.account_id
         WHERE r.ts >= ? AND r.status = 200
         GROUP BY r.account_id
         ORDER BY SUM(r.prompt_tokens) + SUM(r.completion_tokens) DESC`)
}

func queryUsage(d *db.Db, sinceTs int64, query string) ([]UsageRow, error) {
	var out []UsageRow
	err := d.WithConn(func(conn *sql.DB) error {
		rows, err := conn.Query(query, sinceTs)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var r UsageRow
			var label sql.NullString
			var prompt, completion, cached, thought, requests sql.NullInt64
			if err := rows.Scan(&label, &prompt, &completion, &cached, &thought, &requests); err != nil {
				return err
			}
			r.Label = label.String
			r.PromptTokens = prompt.Int64
			r.CompletionTokens = completion.Int64
			r.CachedTokens = cached.Int64
			r.ThoughtTokens = thought.Int64
			r.Requests = requests.Int64
			out = append(out, r)
		}
		return rows.Err()
	})
	return out, err
}

// AddAPIKey inserts a new API key.
func AddAPIKey(d *db.Db, key, label string) (int64, error) {
	var id int64
	err := d.WithConn(func(conn *sql.DB) error {
		_, err := conn.Exec(
			`INSERT INTO api_keys (key, label, created_at) VALUES (?, ?, ?)`,
			key, label, time.Now().Unix(),
		)
		if err != nil {
			return err
		}
		return conn.QueryRow("SELECT last_insert_rowid()").Scan(&id)
	})
	return id, err
}

// ListAPIKeys returns all API keys. Secrets are resolved from the
// keychain if they were migrated.
func ListAPIKeys(d *db.Db) ([]*ApiKey, error) {
	var out []*ApiKey
	err := d.WithConn(func(conn *sql.DB) error {
		rows, err := conn.Query(`SELECT id, key, label, disabled, created_at, scheduling_mode, no_sticky FROM api_keys ORDER BY id`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var k ApiKey
			var disabled int64
			var noSticky int64
			if err := rows.Scan(&k.ID, &k.Key, &k.Label, &disabled, &k.CreatedAt, &k.SchedulingMode, &noSticky); err != nil {
				return err
			}
			k.Disabled = disabled != 0
			k.NoSticky = noSticky != 0
			// Resolve secret from keychain if migrated.
			k.Key = keychain.ResolveSecret(k.Key, keychain.ClientAPIKeyKey(k.ID))
			out = append(out, &k)
		}
		return rows.Err()
	})
	return out, err
}

// FindAPIKey returns a non-disabled key by token string, or nil if not found.
// If keys have been migrated to the keychain, the DB stores the sentinel
// value instead of the real key — in that case, all keys are loaded and
// resolved from the keychain for matching.
func FindAPIKey(d *db.Db, key string) (*ApiKey, error) {
	// First, try a direct DB lookup (fast path for non-migrated keys).
	var k *ApiKey
	err := d.WithConn(func(conn *sql.DB) error {
		row := conn.QueryRow(
			`SELECT id, key, label, disabled, created_at, scheduling_mode, no_sticky FROM api_keys WHERE key = ? AND disabled = 0`,
			key,
		)
		var ak ApiKey
		var disabled int64
		var noSticky int64
		if err := row.Scan(&ak.ID, &ak.Key, &ak.Label, &disabled, &ak.CreatedAt, &ak.SchedulingMode, &noSticky); err != nil {
			if err == sql.ErrNoRows {
				return nil
			}
			return err
		}
		ak.Disabled = disabled != 0
		ak.NoSticky = noSticky != 0
		k = &ak
		return nil
	})
	if err != nil {
		return nil, err
	}
	if k != nil {
		return k, nil
	}

	// Fast path missed — the key may have been migrated to the keychain.
	// Load all keys and resolve from keychain for matching.
	all, err := ListAPIKeys(d)
	if err != nil {
		return nil, err
	}
	for _, ak := range all {
		if ak.Disabled {
			continue
		}
		// ResolveSecret returns the DB value if it's not the sentinel,
		// or fetches from keychain if it is.
		resolved := keychain.ResolveSecret(ak.Key, keychain.ClientAPIKeyKey(ak.ID))
		if resolved == key {
			return ak, nil
		}
	}
	return nil, nil
}

// RemoveAPIKey deletes a key by id.
func RemoveAPIKey(d *db.Db, id int64) error {
	return d.WithConn(func(conn *sql.DB) error {
		_, err := conn.Exec(`DELETE FROM api_keys WHERE id = ?`, id)
		return err
	})
}

// RotateAPIKey replaces the key string for an existing key id.
// The old key string immediately becomes invalid.
func RotateAPIKey(d *db.Db, id int64, newKey string) error {
	return d.WithConn(func(conn *sql.DB) error {
		res, err := conn.Exec(`UPDATE api_keys SET key = ? WHERE id = ?`, newKey, id)
		if err != nil {
			return err
		}
		n, _ := res.RowsAffected()
		if n == 0 {
			return fmt.Errorf("API key #%d not found", id)
		}
		return nil
	})
}

// GetAPIKey returns a single key by id.
func GetAPIKey(d *db.Db, id int64) (*ApiKey, error) {
	var k ApiKey
	var disabled int64
	var noSticky int64
	err := d.WithConn(func(conn *sql.DB) error {
		return conn.QueryRow(
			`SELECT id, key, label, disabled, created_at, scheduling_mode, no_sticky FROM api_keys WHERE id = ?`,
			id,
		).Scan(&k.ID, &k.Key, &k.Label, &disabled, &k.CreatedAt, &k.SchedulingMode, &noSticky)
	})
	if err != nil {
		return nil, err
	}
	k.Disabled = disabled != 0
	k.NoSticky = noSticky != 0
	return &k, nil
}

// SetAPIKeyDisabled toggles a key's disabled flag.
func SetAPIKeyDisabled(d *db.Db, id int64, disabled bool) error {
	return d.WithConn(func(conn *sql.DB) error {
		v := 0
		if disabled {
			v = 1
		}
		_, err := conn.Exec(`UPDATE api_keys SET disabled = ? WHERE id = ?`, v, id)
		return err
	})
}

// UpdateAPIKeyScheduling sets per-key scheduling_mode and no_sticky.
// schedulingMode = "" means follow global config.
func UpdateAPIKeyScheduling(d *db.Db, id int64, schedulingMode string, noSticky bool) error {
	return d.WithConn(func(conn *sql.DB) error {
		ns := 0
		if noSticky {
			ns = 1
		}
		res, err := conn.Exec(
			`UPDATE api_keys SET scheduling_mode = ?, no_sticky = ? WHERE id = ?`,
			schedulingMode, ns, id,
		)
		if err != nil {
			return err
		}
		n, _ := res.RowsAffected()
		if n == 0 {
			return fmt.Errorf("API key #%d not found", id)
		}
		return nil
	})
}

// UsageByKey aggregates usage per API key.
func UsageByKey(d *db.Db, sinceTs int64) ([]KeyUsage, error) {
	var out []KeyUsage
	err := d.WithConn(func(conn *sql.DB) error {
		rows, err := conn.Query(`
            SELECT k.id, k.label, k.key,
                    SUM(r.prompt_tokens), SUM(r.completion_tokens), COUNT(*)
             FROM request_logs r
             LEFT JOIN api_keys k ON k.id = r.api_key_id
             WHERE r.ts >= ? AND r.status = 200
             GROUP BY k.id, k.label, k.key
             ORDER BY SUM(r.prompt_tokens) + SUM(r.completion_tokens) DESC`, sinceTs)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var u KeyUsage
			var keyID sql.NullInt64
			var label, key sql.NullString
			var prompt, completion, requests sql.NullInt64
			if err := rows.Scan(&keyID, &label, &key, &prompt, &completion, &requests); err != nil {
				return err
			}
			if keyID.Valid {
				u.KeyID = &keyID.Int64
			}
			if label.Valid {
				u.Label = label.String
			} else {
				u.Label = "(no key)"
			}
			if key.Valid {
				k := key.String
				if len(k) >= 8 {
					pre := k[:8]
					suf := k[len(k)-4:]
					u.KeyPrefix = pre + "…" + suf
				} else {
					u.KeyPrefix = k
				}
			}
			u.PromptTokens = prompt.Int64
			u.CompletionTokens = completion.Int64
			u.Requests = requests.Int64
			out = append(out, u)
		}
		return rows.Err()
	})
	return out, err
}

// UsageByKeyModel aggregates usage per API key × model.
func UsageByKeyModel(d *db.Db, sinceTs int64) ([]KeyModelUsage, error) {
	var out []KeyModelUsage
	err := d.WithConn(func(conn *sql.DB) error {
		rows, err := conn.Query(`
            SELECT k.id, k.label, k.key, r.model,
                    SUM(r.prompt_tokens), SUM(r.completion_tokens), COUNT(*)
             FROM request_logs r
             LEFT JOIN api_keys k ON k.id = r.api_key_id
             WHERE r.ts >= ? AND r.status = 200
             GROUP BY k.id, k.label, k.key, r.model
             ORDER BY SUM(r.prompt_tokens) + SUM(r.completion_tokens) DESC`, sinceTs)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var u KeyModelUsage
			var keyID sql.NullInt64
			var label, key, model sql.NullString
			var prompt, completion, requests sql.NullInt64
			if err := rows.Scan(&keyID, &label, &key, &model, &prompt, &completion, &requests); err != nil {
				return err
			}
			u.KeyID = keyID.Int64
			if label.Valid {
				u.Label = label.String
			} else {
				u.Label = "(no key)"
			}
			if key.Valid {
				k := key.String
				if len(k) >= 8 {
					u.KeyPrefix = k[:8] + "…" + k[len(k)-4:]
				} else {
					u.KeyPrefix = k
				}
			}
			if model.Valid {
				u.Model = model.String
			}
			u.PromptTokens = prompt.Int64
			u.CompletionTokens = completion.Int64
			u.Requests = requests.Int64
			out = append(out, u)
		}
		return rows.Err()
	})
	return out, err
}
