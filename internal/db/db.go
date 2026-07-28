package db

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	_ "modernc.org/sqlite"
)

// Db wraps a SQLite connection guarded by a Mutex.
// Suitable for low-throughput CLI/TUI workloads.
type Db struct {
	conn *sql.DB
	mu   sync.Mutex
}

func Open(path string) (*Db, error) {
	if parent := filepath.Dir(path); parent != "" {
		if err := os.MkdirAll(parent, 0o755); err != nil {
			return nil, err
		}
	}
	conn, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	if _, err := conn.Exec("PRAGMA journal_mode=WAL; PRAGMA foreign_keys=ON;"); err != nil {
		return nil, err
	}
	d := &Db{conn: conn}
	if err := d.migrate(); err != nil {
		return nil, err
	}
	// Enforce restrictive permissions — the DB stores OAuth tokens,
	// refresh tokens, and API keys in plaintext.
	_ = os.Chmod(path, 0o600)
	return d, nil
}

func (d *Db) Close() error { return d.conn.Close() }

// WithConn runs f while holding the global mutex.
func (d *Db) WithConn(f func(*sql.DB) error) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	return f(d.conn)
}

func (d *Db) migrate() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	_, err := d.conn.Exec(`
        CREATE TABLE IF NOT EXISTS accounts (
            id              INTEGER PRIMARY KEY AUTOINCREMENT,
            email           TEXT NOT NULL UNIQUE,
            access_token    TEXT NOT NULL,
            refresh_token   TEXT NOT NULL,
            project_id      TEXT,
            expires_at      INTEGER,
            quota_remaining INTEGER,
            last_used_at    INTEGER,
            last_error      TEXT,
            disabled        INTEGER NOT NULL DEFAULT 0,
            created_at      INTEGER NOT NULL
        );
        CREATE TABLE IF NOT EXISTS request_logs (
            id          INTEGER PRIMARY KEY AUTOINCREMENT,
            ts          INTEGER NOT NULL,
            account_id  INTEGER,
            model       TEXT,
            prompt_tokens   INTEGER,
            completion_tokens INTEGER,
            status      INTEGER,
            client_ip   TEXT,
            error       TEXT
        );
        CREATE TABLE IF NOT EXISTS api_keys (
            id          INTEGER PRIMARY KEY AUTOINCREMENT,
            key         TEXT NOT NULL UNIQUE,
            label       TEXT NOT NULL DEFAULT '',
            disabled    INTEGER NOT NULL DEFAULT 0,
            created_at  INTEGER NOT NULL
        );
    `)
	if err != nil {
		return err
	}
	// v2: quota blob + protection + cost.
	if err := ensureColumn(d.conn, "accounts", "quota_fetched_at", "INTEGER"); err != nil {
		return err
	}
	if err := ensureColumn(d.conn, "accounts", "quota_json", "TEXT"); err != nil {
		return err
	}
	if err := ensureColumn(d.conn, "accounts", "protected_models", "TEXT NOT NULL DEFAULT '[]'"); err != nil {
		return err
	}
	if err := ensureColumn(d.conn, "request_logs", "cost_usd", "REAL"); err != nil {
		return err
	}
	// v3: cache + thinking token tracking.
	if err := ensureColumn(d.conn, "request_logs", "cached_tokens", "INTEGER"); err != nil {
		return err
	}
	if err := ensureColumn(d.conn, "request_logs", "thought_tokens", "INTEGER"); err != nil {
		return err
	}
	// v4: quota summary blob.
	if err := ensureColumn(d.conn, "accounts", "quota_summary", "TEXT"); err != nil {
		return err
	}
	// v5: per-key usage tracking.
	if err := ensureColumn(d.conn, "request_logs", "api_key_id", "INTEGER"); err != nil {
		return err
	}
	// v6: indexes for common query patterns.
	for _, idx := range []string{
		"CREATE INDEX IF NOT EXISTS idx_request_logs_ts ON request_logs(ts)",
		"CREATE INDEX IF NOT EXISTS idx_request_logs_api_key_id ON request_logs(api_key_id)",
		"CREATE INDEX IF NOT EXISTS idx_request_logs_account_id ON request_logs(account_id)",
		"CREATE INDEX IF NOT EXISTS idx_request_logs_model ON request_logs(model)",
	} {
		if _, err := d.conn.Exec(idx); err != nil {
			return err
		}
	}
	// v7: per-API-key scheduling override.
	if err := ensureColumn(d.conn, "api_keys", "scheduling_mode", "TEXT NOT NULL DEFAULT ''"); err != nil {
		return err
	}
	if err := ensureColumn(d.conn, "api_keys", "no_sticky", "INTEGER NOT NULL DEFAULT 0"); err != nil {
		return err
	}
	// v8: distinguish health-check auto-disable from manual disable.
	if err := ensureColumn(d.conn, "accounts", "health_disabled", "INTEGER NOT NULL DEFAULT 0"); err != nil {
		return err
	}
	return nil
}

func ensureColumn(conn *sql.DB, table, col, decl string) error {
	var n int
	row := conn.QueryRow(
		"SELECT COUNT(*) FROM pragma_table_info(?) WHERE name=?",
		table, col,
	)
	if err := row.Scan(&n); err != nil {
		return err
	}
	if n == 0 {
		_, err := conn.Exec(fmt.Sprintf("ALTER TABLE %s ADD COLUMN %s %s", table, col, decl))
		return err
	}
	return nil
}
