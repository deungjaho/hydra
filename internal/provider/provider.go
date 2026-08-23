// Package provider manages API key providers and their model mappings
// in the database.
//
// A Provider is an OpenAI-compatible upstream that authenticates with a
// static API key (DeepSeek, Zhipu, Moonshot, xAI, etc.). Unlike AGY
// accounts (which use OAuth and have quota management), providers are
// simple: they have a base URL, an API key, and a set of model IDs.
//
// Providers and their model mappings are stored in the database, enabling
// runtime management via CLI and TUI without restarting the proxy.
package provider

import (
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/deungjaho/hydra/internal/config"
	"github.com/deungjaho/hydra/internal/db"
	"github.com/deungjaho/hydra/internal/keychain"
)

// Provider is one API key provider entry.
type Provider struct {
	ID               int64
	Name             string
	BaseURL          string
	APIKey           string
	OperatorDisabled bool
	HealthDisabled   bool
	LastError        string
	LastCheckedAt    *int64
	CreatedAt        int64
}

// Disabled reports whether the provider is schedulable.
func (p *Provider) Disabled() bool {
	return p.OperatorDisabled || p.HealthDisabled
}

// ModelMapping is one model → provider association.
type ModelMapping struct {
	ID              int64
	ModelID         string
	ProviderID      int64
	DisplayName     string
	ContextWindow   int
	MaxOutputTokens int64
	Priority        int
	Disabled        bool
}

// AddProvider inserts a new provider. Returns the provider id.
func AddProvider(d *db.Db, name, baseURL, apiKey string) (int64, error) {
	var id int64
	err := d.WithConn(func(conn *sql.DB) error {
		_, err := conn.Exec(
			`INSERT INTO providers (name, base_url, api_key, created_at)
             VALUES (?, ?, ?, ?)`,
			name, baseURL, apiKey, time.Now().Unix(),
		)
		if err != nil {
			return err
		}
		return conn.QueryRow("SELECT last_insert_rowid()").Scan(&id)
	})
	return id, err
}

// ListProviders returns all providers ordered by id.
func ListProviders(d *db.Db) ([]*Provider, error) {
	var out []*Provider
	err := d.WithConn(func(conn *sql.DB) error {
		rows, err := conn.Query(`SELECT id, name, base_url, api_key,
			operator_disabled, health_disabled, last_error, last_checked_at, created_at
			FROM providers ORDER BY id`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			p, err := scanProvider(rows)
			if err != nil {
				return err
			}
			out = append(out, p)
		}
		return rows.Err()
	})
	return out, err
}

// GetProvider returns a single provider by id.
func GetProvider(d *db.Db, id int64) (*Provider, error) {
	var p *Provider
	err := d.WithConn(func(conn *sql.DB) error {
		row := conn.QueryRow(`SELECT id, name, base_url, api_key,
			operator_disabled, health_disabled, last_error, last_checked_at, created_at
			FROM providers WHERE id = ?`, id)
		var err error
		p, err = scanProvider(row)
		return err
	})
	return p, err
}

// GetProviderByName returns a single provider by name.
func GetProviderByName(d *db.Db, name string) (*Provider, error) {
	var p *Provider
	err := d.WithConn(func(conn *sql.DB) error {
		row := conn.QueryRow(`SELECT id, name, base_url, api_key,
			operator_disabled, health_disabled, last_error, last_checked_at, created_at
			FROM providers WHERE name = ?`, name)
		var err error
		p, err = scanProvider(row)
		return err
	})
	return p, err
}

// RemoveProvider deletes a provider and its model mappings (cascade).
func RemoveProvider(d *db.Db, id int64) error {
	return d.WithConn(func(conn *sql.DB) error {
		_, err := conn.Exec(`DELETE FROM providers WHERE id = ?`, id)
		return err
	})
}

// UpdateProvider updates a provider's base URL and/or API key.
// Pass empty strings to leave a field unchanged.
func UpdateProvider(d *db.Db, id int64, baseURL, apiKey string) error {
	return d.WithConn(func(conn *sql.DB) error {
		if baseURL != "" && apiKey != "" {
			_, err := conn.Exec(`UPDATE providers SET base_url = ?, api_key = ? WHERE id = ?`,
				baseURL, apiKey, id)
			return err
		}
		if baseURL != "" {
			_, err := conn.Exec(`UPDATE providers SET base_url = ? WHERE id = ?`, baseURL, id)
			return err
		}
		if apiKey != "" {
			_, err := conn.Exec(`UPDATE providers SET api_key = ? WHERE id = ?`, apiKey, id)
			return err
		}
		return nil
	})
}

// UpdateProviderAPIKeyOnly sets the api_key column for a provider.
// Used by keychain migration to replace the plaintext key with the
// sentinel value.
func UpdateProviderAPIKeyOnly(d *db.Db, id int64, value string) error {
	return d.WithConn(func(conn *sql.DB) error {
		_, err := conn.Exec(`UPDATE providers SET api_key = ? WHERE id = ?`, value, id)
		return err
	})
}

// SetProviderDisabled sets the operator-disabled flag.
func SetProviderDisabled(d *db.Db, id int64, disabled bool) error {
	return d.WithConn(func(conn *sql.DB) error {
		v := 0
		if disabled {
			v = 1
		}
		_, err := conn.Exec(`UPDATE providers SET operator_disabled = ? WHERE id = ?`, v, id)
		return err
	})
}

// MarkProviderHealthDisabled marks a provider as health-disabled.
func MarkProviderHealthDisabled(d *db.Db, id int64, reason string) error {
	return d.WithConn(func(conn *sql.DB) error {
		_, err := conn.Exec(`UPDATE providers SET health_disabled = 1, last_error = ? WHERE id = ?`,
			reason, id)
		return err
	})
}

// MarkProviderHealthRecovered clears the health-disabled flag.
func MarkProviderHealthRecovered(d *db.Db, id int64) error {
	return d.WithConn(func(conn *sql.DB) error {
		_, err := conn.Exec(`UPDATE providers SET health_disabled = 0, last_error = NULL WHERE id = ?`, id)
		return err
	})
}

// UpdateProviderHealthCheck records the last health check result.
func UpdateProviderHealthCheck(d *db.Db, id int64, healthy bool, reason string) error {
	return d.WithConn(func(conn *sql.DB) error {
		now := time.Now().Unix()
		if healthy {
			_, err := conn.Exec(`UPDATE providers SET last_checked_at = ?, health_disabled = 0, last_error = NULL WHERE id = ?`,
				now, id)
			return err
		}
		_, err := conn.Exec(`UPDATE providers SET last_checked_at = ?, last_error = ? WHERE id = ?`,
			now, reason, id)
		return err
	})
}

// --- Model mappings ---

// AddModelMapping adds a model → provider mapping. priority controls
// failover order (lower = higher priority, 0 = default).
func AddModelMapping(d *db.Db, modelID string, providerID int64, displayName string, contextWindow int, maxOutputTokens int64, priority int) (int64, error) {
	var id int64
	err := d.WithConn(func(conn *sql.DB) error {
		_, err := conn.Exec(
			`INSERT INTO provider_models (model_id, provider_id, display_name, context_window, max_output_tokens, priority)
             VALUES (?, ?, ?, ?, ?, ?)`,
			modelID, providerID, displayName, contextWindow, maxOutputTokens, priority,
		)
		if err != nil {
			return err
		}
		return conn.QueryRow("SELECT last_insert_rowid()").Scan(&id)
	})
	return id, err
}

// ListModelMappings returns all model → provider mappings, ordered by
// model_id then priority (ascending).
func ListModelMappings(d *db.Db) ([]*ModelMapping, error) {
	var out []*ModelMapping
	err := d.WithConn(func(conn *sql.DB) error {
		rows, err := conn.Query(`SELECT pm.id, pm.model_id, pm.provider_id,
			pm.display_name, pm.context_window, pm.max_output_tokens, pm.priority, pm.disabled
			FROM provider_models pm ORDER BY pm.model_id, pm.priority`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var m ModelMapping
			var displayName sql.NullString
			var disabled int64
			if err := rows.Scan(&m.ID, &m.ModelID, &m.ProviderID,
				&displayName, &m.ContextWindow, &m.MaxOutputTokens, &m.Priority, &disabled); err != nil {
				return err
			}
			m.DisplayName = displayName.String
			m.Disabled = disabled != 0
			out = append(out, &m)
		}
		return rows.Err()
	})
	return out, err
}

// RemoveModelMapping removes a model mapping by model ID.
func RemoveModelMapping(d *db.Db, modelID string) error {
	return d.WithConn(func(conn *sql.DB) error {
		_, err := conn.Exec(`DELETE FROM provider_models WHERE model_id = ?`, modelID)
		return err
	})
}

// SetModelMappingDisabled sets the disabled flag for a model mapping by model ID.
func SetModelMappingDisabled(d *db.Db, modelID string, disabled bool) error {
	return d.WithConn(func(conn *sql.DB) error {
		v := 0
		if disabled {
			v = 1
		}
		_, err := conn.Exec(`UPDATE provider_models SET disabled = ? WHERE model_id = ?`, v, modelID)
		return err
	})
}

// ListDisabledModelIDs returns model_ids of all disabled provider model mappings.
func ListDisabledModelIDs(d *db.Db) ([]string, error) {
	var out []string
	err := d.WithConn(func(conn *sql.DB) error {
		rows, err := conn.Query(`SELECT DISTINCT model_id FROM provider_models WHERE disabled = 1`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				return err
			}
			out = append(out, id)
		}
		return rows.Err()
	})
	return out, err
}

// SyncFromConfig imports TOML-configured providers and model mappings
// into the database. Providers that already exist (matched by name)
// are updated if base_url or api_key differs. Model mappings that
// already exist (matched by model_id + provider_id) are left as-is.
//
// This runs at startup so that legacy config.toml entries are
// automatically migrated to the DB, and the registry only needs
// to read from the DB.
func SyncFromConfig(d *db.Db, cfg *config.AppConfig) error {
	// Build provider name → ID map from DB.
	existingProviders, err := ListProviders(d)
	if err != nil {
		return fmt.Errorf("list providers: %w", err)
	}
	providerIDByName := make(map[string]int64)
	for _, p := range existingProviders {
		providerIDByName[strings.ToLower(p.Name)] = p.ID
	}

	// Sync each TOML provider.
	for i := range cfg.APIProviders {
		p := &cfg.APIProviders[i]
		name := strings.ToLower(p.Name)
		baseURL := strings.TrimRight(p.BaseURL, "/")
		existingID, exists := providerIDByName[name]
		if !exists {
			id, err := AddProvider(d, p.Name, baseURL, p.APIKey)
			if err != nil {
				return fmt.Errorf("add provider %s: %w", p.Name, err)
			}
			providerIDByName[name] = id
		} else {
			// Update base_url / api_key if they differ.
			existing, err := GetProvider(d, existingID)
			if err != nil {
				return fmt.Errorf("get provider %s: %w", p.Name, err)
			}
			if existing.BaseURL != baseURL || existing.APIKey != p.APIKey {
				if err := UpdateProvider(d, existingID, baseURL, p.APIKey); err != nil {
					return fmt.Errorf("update provider %s: %w", p.Name, err)
				}
			}
		}
	}

	// Sync each TOML model mapping.
	// Build existing mapping set from DB (model_id + provider_id).
	existingMappings, err := ListModelMappings(d)
	if err != nil {
		return fmt.Errorf("list model mappings: %w", err)
	}
	existingSet := make(map[string]bool)
	for _, m := range existingMappings {
		key := fmt.Sprintf("%s|%d", strings.ToLower(m.ModelID), m.ProviderID)
		existingSet[key] = true
	}

	for _, m := range cfg.Models {
		provID, ok := providerIDByName[strings.ToLower(m.Provider)]
		if !ok {
			continue
		}
		key := fmt.Sprintf("%s|%d", strings.ToLower(m.ID), provID)
		if existingSet[key] {
			continue
		}
		if _, err := AddModelMapping(d, m.ID, provID, m.DisplayName,
			m.ContextWindow, m.MaxOutputTokens, 0); err != nil {
			return fmt.Errorf("add model mapping %s: %w", m.ID, err)
		}
	}

	return nil
}

// --- Helpers ---

type rowScanner interface {
	Scan(dest ...any) error
}

func scanProvider(row rowScanner) (*Provider, error) {
	var p Provider
	var lastError sql.NullString
	var lastCheckedAt sql.NullInt64
	var operatorDisabled, healthDisabled int64
	err := row.Scan(
		&p.ID, &p.Name, &p.BaseURL, &p.APIKey,
		&operatorDisabled, &healthDisabled, &lastError, &lastCheckedAt, &p.CreatedAt,
	)
	if err != nil {
		return nil, err
	}
	p.OperatorDisabled = operatorDisabled != 0
	p.HealthDisabled = healthDisabled != 0
	p.LastError = lastError.String
	if lastCheckedAt.Valid {
		p.LastCheckedAt = &lastCheckedAt.Int64
	}
	// Resolve secret from keychain if it was migrated.
	p.APIKey = keychain.ResolveSecret(p.APIKey, keychain.ProviderAPIKeyKey(p.ID))
	return &p, nil
}

// MaskAPIKey returns a masked version of the API key for display.
// Shows only the first 6 and last 4 characters.
func MaskAPIKey(key string) string {
	if len(key) <= 10 {
		return strings.Repeat("*", len(key))
	}
	return key[:6] + "..." + key[len(key)-4:]
}

// ErrNotFound is returned when a provider is not found.
var ErrNotFound = fmt.Errorf("provider not found")
