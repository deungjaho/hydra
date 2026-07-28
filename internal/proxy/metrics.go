package proxy

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/deungjaho/hydra/internal/account"
)

// handleMetrics exposes Prometheus-format metrics at /metrics.
//
// Metrics:
//
//	hydra_requests_24h{key,label,model,status}        gauge (24h sliding window)
//	hydra_prompt_tokens_24h{key,label,model}          gauge
//	hydra_completion_tokens_24h{key,label,model}      gauge
//	hydra_cost_usd_24h{key,label,model}               gauge
//	hydra_request_duration_seconds{model}             histogram (cumulative since start)
//	hydra_account_quota_remaining{account,model}      gauge (0-100)
//	hydra_accounts_total{status}                      gauge
//	hydra_api_keys_total{status}                      gauge
//	hydra_uptime_seconds                              gauge
func (s *ProxyServer) handleMetrics(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.checkAuth(r); !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	var b strings.Builder

	// --- Uptime ---
	b.WriteString(fmt.Sprintf("# HELP hydra_uptime_seconds Time since proxy started.\n"))
	b.WriteString(fmt.Sprintf("# TYPE hydra_uptime_seconds gauge\n"))
	b.WriteString(fmt.Sprintf("hydra_uptime_seconds %d\n", int64(time.Since(s.State.startedAt).Seconds())))

	// --- Accounts ---
	accounts, _ := account.ListAccounts(s.State.DB)
	active, disabled := 0, 0
	for _, a := range accounts {
		if a.Disabled() {
			disabled++
		} else {
			active++
		}
	}
	b.WriteString("\n# HELP hydra_accounts_total Number of accounts by status.\n")
	b.WriteString("# TYPE hydra_accounts_total gauge\n")
	b.WriteString(fmt.Sprintf("hydra_accounts_total{status=\"active\"} %d\n", active))
	b.WriteString(fmt.Sprintf("hydra_accounts_total{status=\"disabled\"} %d\n", disabled))

	// --- API keys ---
	keys, _ := account.ListAPIKeys(s.State.DB)
	keyActive, keyDisabled := 0, 0
	for _, k := range keys {
		if k.Disabled {
			keyDisabled++
		} else {
			keyActive++
		}
	}
	b.WriteString("\n# HELP hydra_api_keys_total Number of API keys by status.\n")
	b.WriteString("# TYPE hydra_api_keys_total gauge\n")
	b.WriteString(fmt.Sprintf("hydra_api_keys_total{status=\"active\"} %d\n", keyActive))
	b.WriteString(fmt.Sprintf("hydra_api_keys_total{status=\"disabled\"} %d\n", keyDisabled))

	// --- Per-key/model usage (last 24h, sliding window) ---
	// These are gauges, not counters — the 24h window means values
	// can decrease as old requests fall out of the window.
	since := time.Now().Unix() - 86400
	usage, _ := account.UsageByKeyModel(s.State.DB, since)
	b.WriteString("\n# HELP hydra_requests_24h Requests in the last 24h by key/model/status.\n")
	b.WriteString("# TYPE hydra_requests_24h gauge\n")
	for _, u := range usage {
		lbl := promLabel("key", u.KeyPrefix, "label", u.Label, "model", u.Model, "status", "200")
		b.WriteString(fmt.Sprintf("hydra_requests_24h{%s} %d\n", lbl, u.Requests))
	}
	b.WriteString("\n# HELP hydra_prompt_tokens_24h Prompt tokens in the last 24h by key/model.\n")
	b.WriteString("# TYPE hydra_prompt_tokens_24h gauge\n")
	for _, u := range usage {
		lbl := promLabel("key", u.KeyPrefix, "label", u.Label, "model", u.Model)
		b.WriteString(fmt.Sprintf("hydra_prompt_tokens_24h{%s} %d\n", lbl, u.PromptTokens))
	}
	b.WriteString("\n# HELP hydra_completion_tokens_24h Completion tokens in the last 24h by key/model.\n")
	b.WriteString("# TYPE hydra_completion_tokens_24h gauge\n")
	for _, u := range usage {
		lbl := promLabel("key", u.KeyPrefix, "label", u.Label, "model", u.Model)
		b.WriteString(fmt.Sprintf("hydra_completion_tokens_24h{%s} %d\n", lbl, u.CompletionTokens))
	}
	b.WriteString("\n# HELP hydra_cost_usd_24h Estimated cost in USD in the last 24h by key/model.\n")
	b.WriteString("# TYPE hydra_cost_usd_24h gauge\n")
	for _, u := range usage {
		lbl := promLabel("key", u.KeyPrefix, "label", u.Label, "model", u.Model)
		b.WriteString(fmt.Sprintf("hydra_cost_usd_24h{%s} %.6f\n", lbl, u.CostUSD))
	}

	// --- Per-account/model quota ---
	b.WriteString("\n# HELP hydra_account_quota_remaining Remaining quota percentage per account/model.\n")
	b.WriteString("# TYPE hydra_account_quota_remaining gauge\n")
	for _, a := range accounts {
		for _, q := range a.QuotaModels() {
			b.WriteString(fmt.Sprintf("hydra_account_quota_remaining{account=%q,model=%q} %d\n",
				a.Email, q.Name, q.Percentage))
		}
	}

	// --- Request duration histogram ---
	buckets, counts, sum, total := s.State.metrics.snapshot()
	b.WriteString("\n# HELP hydra_request_duration_seconds Request latency distribution.\n")
	b.WriteString("# TYPE hydra_request_duration_seconds histogram\n")
	for i, bound := range buckets {
		b.WriteString(fmt.Sprintf("hydra_request_duration_seconds_bucket{le=\"%g\"} %d\n", bound, counts[i]))
	}
	b.WriteString(fmt.Sprintf("hydra_request_duration_seconds_bucket{le=\"+Inf\"} %d\n", total))
	b.WriteString(fmt.Sprintf("hydra_request_duration_seconds_sum %.6f\n", sum))
	b.WriteString(fmt.Sprintf("hydra_request_duration_seconds_count %d\n", total))

	_, _ = w.Write([]byte(b.String()))
}

// promLabel builds a Prometheus label string: k1="v1",k2="v2"
func promLabel(kv ...string) string {
	var parts []string
	for i := 0; i+1 < len(kv); i += 2 {
		parts = append(parts, fmt.Sprintf("%s=%q", kv[i], kv[i+1]))
	}
	return strings.Join(parts, ",")
}
