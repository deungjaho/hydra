package account

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// Three fallback hosts — the desktop app tries them in order.
var quotaHosts = []string{
	"https://daily-cloudcode-pa.sandbox.googleapis.com",
	"https://daily-cloudcode-pa.googleapis.com",
	"https://cloudcode-pa.googleapis.com",
}

const quotaUserAgent = "Antigravity/4.3.0 (Macintosh; Intel Mac OS X 10_15_7) Chrome/132.0.6834.160 Electron/39.2.3"

// FetchedQuota is the result of a successful quota fetch.
type FetchedQuota struct {
	JSONBlob         string
	MaxPercentage    int64
	HasMaxPercentage bool
	ModelPercentages map[string]int32 // keyed by lowercase model name
	SummaryBlob      string
}

// QuotaWindowInfo is one quota window from the summary endpoint.
type QuotaWindowInfo struct {
	BucketID   string `json:"bucket_id"`
	Family     string `json:"family"`
	Window     string `json:"window"`
	Percentage int32  `json:"percentage"`
	ResetTime  string `json:"reset_time"`
	Disabled   bool   `json:"disabled"`
}

// FetchQuota fetches both the model list and the 4-window summary.
func FetchQuota(client *http.Client, accessToken, projectID string) (*FetchedQuota, error) {
	return FetchQuotaCtx(context.Background(), client, accessToken, projectID)
}

// FetchQuotaCtx is the context-aware variant.
func FetchQuotaCtx(ctx context.Context, client *http.Client, accessToken, projectID string) (*FetchedQuota, error) {
	payload := []byte("{}")
	if projectID != "" {
		payload, _ = json.Marshal(map[string]string{"project": projectID})
	}

	modelsBody, summaryBody, err := fetchBoth(ctx, client, accessToken, payload)
	if err != nil {
		return nil, err
	}

	modelPercentages := parseModelPercentages(modelsBody)
	jsonBlob, maxPercentage, hasMax := buildModelsBlob(modelsBody)
	summaryBlob, _ := parseSummary(summaryBody)

	return &FetchedQuota{
		JSONBlob:         jsonBlob,
		MaxPercentage:    maxPercentage,
		HasMaxPercentage: hasMax,
		ModelPercentages: modelPercentages,
		SummaryBlob:      summaryBlob,
	}, nil
}

func fetchBoth(ctx context.Context, client *http.Client, accessToken string, payload []byte) (string, string, error) {
	var lastErr error
	for _, host := range quotaHosts {
		if ctx.Err() != nil {
			return "", "", ctx.Err()
		}
		modelsURL := host + "/v1internal:fetchAvailableModels"
		summaryURL := host + "/v1internal:retrieveUserQuotaSummary"
		modelsResp, err := postJSON(ctx, client, modelsURL, accessToken, payload)
		if err != nil {
			lastErr = err
			continue
		}
		summaryResp, err := postJSON(ctx, client, summaryURL, accessToken, payload)
		if err != nil {
			lastErr = err
			continue
		}
		if modelsResp.status >= 200 && modelsResp.status < 300 &&
			summaryResp.status >= 200 && summaryResp.status < 300 {
			return modelsResp.body, summaryResp.body, nil
		}
		lastErr = fmt.Errorf("upstream: quota fetch %s returned %d/%d: %s | %s",
			host, modelsResp.status, summaryResp.status, modelsResp.body, summaryResp.body)
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("upstream: all quota endpoints failed")
	}
	return "", "", lastErr
}

type rawResp struct {
	status int
	body   string
}

func postJSON(ctx context.Context, client *http.Client, urlStr, accessToken string, payload []byte) (*rawResp, error) {
	req, err := http.NewRequestWithContext(ctx, "POST", urlStr, bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("User-Agent", quotaUserAgent)
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return &rawResp{status: resp.StatusCode, body: string(body)}, nil
}

func parseModelPercentages(body string) map[string]int32 {
	var parsed struct {
		Models map[string]json.RawMessage `json:"models"`
	}
	if err := json.Unmarshal([]byte(body), &parsed); err != nil {
		return map[string]int32{}
	}
	out := make(map[string]int32, len(parsed.Models))
	for name, info := range parsed.Models {
		var qi struct {
			ResetTime         string  `json:"resetTime"`
			RemainingFraction float64 `json:"remainingFraction"`
			HasFraction       bool    `json:"-"`
		}
		_ = json.Unmarshal(info, &qi)
		// Detect presence of remainingFraction.
		var probe map[string]json.RawMessage
		_ = json.Unmarshal(info, &probe)
		_, hasFraction := probe["remainingFraction"]
		qi.HasFraction = hasFraction
		hasReset := qi.ResetTime != ""

		var frac float64
		switch {
		case qi.HasFraction:
			frac = qi.RemainingFraction
		case hasReset:
			frac = 0.0
		default:
			continue
		}
		pct := int32(frac*100.0 + 0.5)
		if pct < 0 {
			pct = 0
		}
		if pct > 100 {
			pct = 100
		}
		out[strings.ToLower(name)] = pct
	}
	return out
}

func buildModelsBlob(body string) (string, int64, bool) {
	var parsed struct {
		Models map[string]json.RawMessage `json:"models"`
	}
	if err := json.Unmarshal([]byte(body), &parsed); err != nil {
		b, _ := json.Marshal(map[string][]any{"models": {}})
		return string(b), 0, false
	}
	type outModel struct {
		Name       string `json:"name"`
		Percentage int32  `json:"percentage"`
		ResetTime  string `json:"reset_time"`
	}
	out := make([]outModel, 0, len(parsed.Models))
	var maxPct int32
	hasMax := false
	for name, info := range parsed.Models {
		var qi struct {
			ResetTime         string  `json:"resetTime"`
			RemainingFraction float64 `json:"remainingFraction"`
		}
		_ = json.Unmarshal(info, &qi)
		var probe map[string]json.RawMessage
		_ = json.Unmarshal(info, &probe)
		_, hasFraction := probe["remainingFraction"]
		hasReset := qi.ResetTime != ""

		var pct int32
		switch {
		case hasFraction:
			frac := qi.RemainingFraction
			pct = int32(frac*100.0 + 0.5)
			if pct < 0 {
				pct = 0
			}
			if pct > 100 {
				pct = 100
			}
		case hasReset:
			pct = 0
		default:
			// Model has no quota info — still include it with 100%.
			pct = 100
		}
		out = append(out, outModel{Name: name, Percentage: pct, ResetTime: qi.ResetTime})
		if !hasMax || pct > maxPct {
			maxPct = pct
		}
		hasMax = true
	}
	blob := map[string]any{"models": out}
	b, _ := json.Marshal(blob)
	if b == nil {
		b = []byte(`{"models":[]}`)
	}
	return string(b), int64(maxPct), hasMax
}

func parseSummary(body string) (string, []QuotaWindowInfo) {
	var parsed struct {
		Groups []struct {
			Buckets []struct {
				BucketID          string  `json:"bucketId"`
				Window            string  `json:"window"`
				ResetTime         string  `json:"resetTime"`
				RemainingFraction float64 `json:"remainingFraction"`
				Disabled          bool    `json:"disabled"`
			} `json:"buckets"`
		} `json:"groups"`
	}
	if err := json.Unmarshal([]byte(body), &parsed); err != nil {
		b, _ := json.Marshal(map[string][]any{"windows": {}})
		return string(b), nil
	}
	var windows []QuotaWindowInfo
	for _, g := range parsed.Groups {
		for _, b := range g.Buckets {
			family := "third_party"
			if strings.HasPrefix(b.BucketID, "gemini") {
				family = "gemini"
			}
			pct := int32(b.RemainingFraction*100.0 + 0.5)
			if pct < 0 {
				pct = 0
			}
			if pct > 100 {
				pct = 100
			}
			windows = append(windows, QuotaWindowInfo{
				BucketID:   b.BucketID,
				Family:     family,
				Window:     b.Window,
				Percentage: pct,
				ResetTime:  b.ResetTime,
				Disabled:   b.Disabled,
			})
		}
	}
	blob := map[string]any{"windows": windows}
	b, _ := json.Marshal(blob)
	if b == nil {
		b = []byte(`{"windows":[]}`)
	}
	return string(b), windows
}

// ProbeAccount does a lightweight connectivity check by calling
// fetchAvailableModels only. This consumes no quota and verifies:
//   - access token is valid (not expired)
//   - upstream endpoints are reachable
//   - project ID is correct
//
// Returns nil if healthy, or an error describing the failure.
func ProbeAccount(client *http.Client, accessToken, projectID string) error {
	return ProbeAccountCtx(context.Background(), client, accessToken, projectID)
}

// ProbeAccountCtx is the context-aware variant.
func ProbeAccountCtx(ctx context.Context, client *http.Client, accessToken, projectID string) error {
	payload := []byte("{}")
	if projectID != "" {
		payload, _ = json.Marshal(map[string]string{"project": projectID})
	}
	var lastErr error
	for _, host := range quotaHosts {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		modelsURL := host + "/v1internal:fetchAvailableModels"
		resp, err := postJSON(ctx, client, modelsURL, accessToken, payload)
		if err != nil {
			lastErr = err
			continue
		}
		if resp.status >= 200 && resp.status < 300 {
			return nil
		}
		lastErr = fmt.Errorf("probe %s: HTTP %d: %s",
			host, resp.status, truncate(resp.body, 200))
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("probe: all quota endpoints unreachable")
	}
	return lastErr
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

// ComputeProtectedModels decides which models should be marked as protected.
// A model is protected when its remaining percentage falls below threshold.
// Models already protected but now above the threshold are removed (recovered).
//
// If monitored is non-empty, only those models are checked. If monitored is
// empty, all models in modelPercentages are checked.
func ComputeProtectedModels(
	current []string,
	modelPercentages map[string]int32,
	monitored []string,
	threshold int32,
) []string {
	out := append([]string{}, current...)
	check := monitored
	if len(check) == 0 {
		check = make([]string, 0, len(modelPercentages))
		for m := range modelPercentages {
			check = append(check, m)
		}
	}
	for _, stdID := range check {
		pct, ok := modelPercentages[stdID]
		if !ok {
			pct = 100
		}
		isProtected := false
		for _, m := range out {
			if m == stdID {
				isProtected = true
				break
			}
		}
		if pct < threshold && !isProtected {
			out = append(out, stdID)
		} else if pct >= threshold && isProtected {
			filtered := out[:0]
			for _, m := range out {
				if m != stdID {
					filtered = append(filtered, m)
				}
			}
			out = filtered
		}
	}
	return out
}
