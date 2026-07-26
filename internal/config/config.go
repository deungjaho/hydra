package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/BurntSushi/toml"
)

type AppConfig struct {
	Proxy           ProxyConfig           `toml:"proxy"`
	Server          ServerConfig          `toml:"server"`
	Scheduling      SchedulingConfig      `toml:"scheduling"`
	QuotaProtection QuotaProtectionConfig `toml:"quota_protection"`
}

type ProxyConfig struct {
	Port        int    `toml:"port"`
	Bind        string `toml:"bind"`
	LogRequests bool   `toml:"log_requests"`
	// UpstreamProxy is the HTTP CONNECT proxy used for outbound calls to
	// Google's cloudcode-pa endpoints (e.g. "http://127.0.0.1:7890").
	// Empty = direct connection. The proxy is NOT used for the local HTTP
	// server — only for upstream API calls.
	UpstreamProxy string `toml:"upstream_proxy"`
}

type ServerConfig struct {
	LogCapacity int `toml:"log_capacity"`
}

type SchedulingConfig struct {
	Mode SchedulingMode `toml:"mode"`
}

// SchedulingMode: cache_first / balance / performance_first.
type SchedulingMode string

const (
	SchedulingCache        SchedulingMode = "cache"
	SchedulingBalance      SchedulingMode = "balance"
	SchedulingPerformance  SchedulingMode = "performance"
)

type QuotaProtectionConfig struct {
	Enabled             bool     `toml:"enabled"`
	ThresholdPercentage int      `toml:"threshold_percentage"`
	MonitoredModels     []string `toml:"monitored_models"`
}

func defaultProxy() ProxyConfig {
	return ProxyConfig{
		Port:        18045,
		Bind:        "127.0.0.1",
		LogRequests: true,
	}
}

func defaultServer() ServerConfig {
	return ServerConfig{LogCapacity: 1000}
}

func defaultScheduling() SchedulingConfig {
	return SchedulingConfig{Mode: SchedulingBalance}
}

func defaultMonitoredModels() []string {
	return []string{
		"claude-sonnet-4-6",
		"claude-sonnet-4-6-thinking",
		"claude-opus-4-6",
		"claude-opus-4-6-thinking",
		"gemini-3-pro",
		"gemini-3-pro-preview",
		"gemini-3-pro-high",
		"gemini-3-pro-low",
		"gemini-3.1-pro-preview",
		"gemini-pro-agent",
	}
}

func defaultQuotaProtection() QuotaProtectionConfig {
	return QuotaProtectionConfig{
		Enabled:             true,
		ThresholdPercentage: 5,
		MonitoredModels:     defaultMonitoredModels(),
	}
}

func Default() AppConfig {
	return AppConfig{
		Proxy:           defaultProxy(),
		Server:          defaultServer(),
		Scheduling:      defaultScheduling(),
		QuotaProtection: defaultQuotaProtection(),
	}
}

// DefaultPtr returns a pointer to a fresh default config.
func DefaultPtr() *AppConfig {
	cfg := Default()
	return &cfg
}

// ConfigPath returns ~/.config/hydra/config.toml (platform equivalent).
func ConfigPath() string {
	base, err := os.UserConfigDir()
	if err != nil {
		base = "."
	}
	return filepath.Join(base, "hydra", "config.toml")
}

// DBPath returns the platform-equivalent data dir for the SQLite database.
// On Linux this is ~/.local/share/hydra/hydra.db; on macOS ~/Library/Application Support/hydra/hydra.db.
func DBPath() string {
	base, err := os.UserConfigDir()
	if err != nil {
		base = "."
	}
	return filepath.Join(base, "hydra", "hydra.db")
}

func Load() (*AppConfig, error) {
	path := ConfigPath()
	if _, err := os.Stat(path); os.IsNotExist(err) {
		cfg := Default()
		if err := cfg.Save(); err != nil {
			return nil, err
		}
		return &cfg, nil
	}
	var cfg AppConfig
	// Initialise with defaults so missing fields keep their default values.
	cfg = Default()
	if _, err := toml.DecodeFile(path, &cfg); err != nil {
		return nil, fmt.Errorf("config: %w", err)
	}
	applyEnvOverrides(&cfg)
	return &cfg, nil
}

// applyEnvOverrides lets environment variables override config file values.
// Supported: HYDRA_PORT, HYDRA_BIND, HYDRA_UPSTREAM_PROXY,
// HYDRA_LOG_REQUESTS, HYDRA_SCHEDULING_MODE.
func applyEnvOverrides(cfg *AppConfig) {
	if v := os.Getenv("HYDRA_PORT"); v != "" {
		if p, err := strconv.Atoi(v); err == nil && p > 0 && p < 65536 {
			cfg.Proxy.Port = p
		}
	}
	if v := os.Getenv("HYDRA_BIND"); v != "" {
		cfg.Proxy.Bind = v
	}
	if v := os.Getenv("HYDRA_UPSTREAM_PROXY"); v != "" {
		cfg.Proxy.UpstreamProxy = v
	}
	if v := os.Getenv("HYDRA_LOG_REQUESTS"); v != "" {
		cfg.Proxy.LogRequests = v == "true" || v == "1" || v == "yes"
	}
	if v := os.Getenv("HYDRA_SCHEDULING_MODE"); v != "" {
		switch strings.ToLower(v) {
		case "cache":
			cfg.Scheduling.Mode = SchedulingCache
		case "balance":
			cfg.Scheduling.Mode = SchedulingBalance
		case "performance":
			cfg.Scheduling.Mode = SchedulingPerformance
		}
	}
}

func (c *AppConfig) Save() error {
	path := ConfigPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	var buf strings.Builder
	enc := toml.NewEncoder(&buf)
	if err := enc.Encode(c); err != nil {
		return fmt.Errorf("config: %w", err)
	}
	return os.WriteFile(path, []byte(buf.String()), 0o644)
}
