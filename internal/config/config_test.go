package config

import (
	"os"
	"testing"
)

func TestDefault(t *testing.T) {
	cfg := Default()
	if cfg.Proxy.Port != 18045 {
		t.Errorf("Proxy.Port = %d, want 18045", cfg.Proxy.Port)
	}
	if cfg.Proxy.Bind != "127.0.0.1" {
		t.Errorf("Proxy.Bind = %q, want 127.0.0.1", cfg.Proxy.Bind)
	}
	if !cfg.Proxy.LogRequests {
		t.Error("Proxy.LogRequests should be true")
	}
	if cfg.Server.LogCapacity != 1000 {
		t.Errorf("Server.LogCapacity = %d, want 1000",
			cfg.Server.LogCapacity)
	}
	if cfg.Scheduling.Mode != SchedulingBalance {
		t.Errorf("Scheduling.Mode = %v, want balance",
			cfg.Scheduling.Mode)
	}
	if !cfg.QuotaProtection.Enabled {
		t.Error("QuotaProtection.Enabled should be true")
	}
	if cfg.QuotaProtection.ThresholdPercentage != 5 {
		t.Errorf("ThresholdPercentage = %d, want 5",
			cfg.QuotaProtection.ThresholdPercentage)
	}
	if len(cfg.QuotaProtection.MonitoredModels) == 0 {
		t.Error("MonitoredModels should not be empty")
	}
	if !cfg.HealthCheck.Enabled {
		t.Error("HealthCheck.Enabled should be true")
	}
	if cfg.HealthCheck.IntervalSeconds != 120 {
		t.Errorf("HealthCheck.IntervalSeconds = %d, want 120",
			cfg.HealthCheck.IntervalSeconds)
	}
	if cfg.HealthCheck.FailureThreshold != 3 {
		t.Errorf("HealthCheck.FailureThreshold = %d, want 3",
			cfg.HealthCheck.FailureThreshold)
	}
}

func TestDefaultPtr(t *testing.T) {
	ptr := DefaultPtr()
	if ptr == nil {
		t.Fatal("DefaultPtr returned nil")
	}
	if ptr.Proxy.Port != 18045 {
		t.Errorf("Port = %d, want 18045", ptr.Proxy.Port)
	}
}

func TestDefaultPtr_IndependentInstances(t *testing.T) {
	a := DefaultPtr()
	b := DefaultPtr()
	a.Proxy.Port = 9999
	if b.Proxy.Port == 9999 {
		t.Error("instances should be independent")
	}
}

func TestApplyEnvOverrides_Port(t *testing.T) {
	os.Setenv("HYDRA_PORT", "8080")
	defer os.Unsetenv("HYDRA_PORT")
	cfg := Default()
	applyEnvOverrides(&cfg)
	if cfg.Proxy.Port != 8080 {
		t.Errorf("Port = %d, want 8080", cfg.Proxy.Port)
	}
}

func TestApplyEnvOverrides_InvalidPort(t *testing.T) {
	os.Setenv("HYDRA_PORT", "not-a-number")
	defer os.Unsetenv("HYDRA_PORT")
	cfg := Default()
	applyEnvOverrides(&cfg)
	if cfg.Proxy.Port != 18045 {
		t.Errorf("Port = %d, want 18045 (unchanged)", cfg.Proxy.Port)
	}
}

func TestApplyEnvOverrides_PortOutOfRange(t *testing.T) {
	os.Setenv("HYDRA_PORT", "99999")
	defer os.Unsetenv("HYDRA_PORT")
	cfg := Default()
	applyEnvOverrides(&cfg)
	if cfg.Proxy.Port != 18045 {
		t.Errorf("Port = %d, want 18045 (out of range ignored)",
			cfg.Proxy.Port)
	}
}

func TestApplyEnvOverrides_Bind(t *testing.T) {
	os.Setenv("HYDRA_BIND", "0.0.0.0")
	defer os.Unsetenv("HYDRA_BIND")
	cfg := Default()
	applyEnvOverrides(&cfg)
	if cfg.Proxy.Bind != "0.0.0.0" {
		t.Errorf("Bind = %q, want 0.0.0.0", cfg.Proxy.Bind)
	}
}

func TestApplyEnvOverrides_UpstreamProxy(t *testing.T) {
	os.Setenv("HYDRA_UPSTREAM_PROXY", "http://127.0.0.1:7890")
	defer os.Unsetenv("HYDRA_UPSTREAM_PROXY")
	cfg := Default()
	applyEnvOverrides(&cfg)
	if cfg.Proxy.UpstreamProxy != "http://127.0.0.1:7890" {
		t.Errorf("UpstreamProxy = %q", cfg.Proxy.UpstreamProxy)
	}
}

func TestApplyEnvOverrides_LogRequests(t *testing.T) {
	tests := []struct {
		val  string
		want bool
	}{
		{"true", true},
		{"1", true},
		{"yes", true},
		{"false", false},
		{"0", false},
		{"no", false},
		{"", true}, // empty = no override, keeps default
	}
	for _, tt := range tests {
		if tt.val == "" {
			os.Unsetenv("HYDRA_LOG_REQUESTS")
		} else {
			os.Setenv("HYDRA_LOG_REQUESTS", tt.val)
		}
		cfg := Default()
		applyEnvOverrides(&cfg)
		if cfg.Proxy.LogRequests != tt.want {
			t.Errorf("HYDRA_LOG_REQUESTS=%q → LogRequests = %v, want %v",
				tt.val, cfg.Proxy.LogRequests, tt.want)
		}
	}
	os.Unsetenv("HYDRA_LOG_REQUESTS")
}

func TestApplyEnvOverrides_SchedulingMode(t *testing.T) {
	tests := []struct {
		val  string
		want SchedulingMode
	}{
		{"cache", SchedulingCache},
		{"balance", SchedulingBalance},
		{"performance", SchedulingPerformance},
		{"CACHE", SchedulingCache},     // case insensitive
		{"Balance", SchedulingBalance}, // case insensitive
		{"invalid", SchedulingBalance}, // invalid keeps default
	}
	for _, tt := range tests {
		os.Setenv("HYDRA_SCHEDULING_MODE", tt.val)
		cfg := Default()
		applyEnvOverrides(&cfg)
		if cfg.Scheduling.Mode != tt.want {
			t.Errorf("HYDRA_SCHEDULING_MODE=%q → Mode = %v, want %v",
				tt.val, cfg.Scheduling.Mode, tt.want)
		}
	}
	os.Unsetenv("HYDRA_SCHEDULING_MODE")
}

func TestApplyEnvOverrides_NoEnvKeepsDefaults(t *testing.T) {
	os.Unsetenv("HYDRA_PORT")
	os.Unsetenv("HYDRA_BIND")
	os.Unsetenv("HYDRA_UPSTREAM_PROXY")
	os.Unsetenv("HYDRA_LOG_REQUESTS")
	os.Unsetenv("HYDRA_SCHEDULING_MODE")
	cfg := Default()
	applyEnvOverrides(&cfg)
	if cfg.Proxy.Port != 18045 {
		t.Errorf("Port = %d, want 18045", cfg.Proxy.Port)
	}
	if cfg.Proxy.Bind != "127.0.0.1" {
		t.Errorf("Bind = %q, want 127.0.0.1", cfg.Proxy.Bind)
	}
	if cfg.Scheduling.Mode != SchedulingBalance {
		t.Errorf("Mode = %v, want balance", cfg.Scheduling.Mode)
	}
}

func TestDefaultProviders(t *testing.T) {
	providers := DefaultProviders()
	if len(providers) != 1 {
		t.Fatalf("DefaultProviders returned %d, want 1", len(providers))
	}
	if providers[0].ID != "google-cloud-code" {
		t.Errorf("ID = %q, want google-cloud-code", providers[0].ID)
	}
	if providers[0].Type != "google-cloud-code" {
		t.Errorf("Type = %q, want google-cloud-code", providers[0].Type)
	}
	if !providers[0].Enabled {
		t.Error("Enabled should be true")
	}
}

func TestDefault_HasProviders(t *testing.T) {
	cfg := Default()
	if len(cfg.Providers) != 1 {
		t.Fatalf("Default().Providers = %d, want 1", len(cfg.Providers))
	}
	if cfg.Providers[0].ID != "google-cloud-code" {
		t.Errorf("Providers[0].ID = %q, want google-cloud-code", cfg.Providers[0].ID)
	}
}
