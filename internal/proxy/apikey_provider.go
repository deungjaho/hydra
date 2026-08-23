package proxy

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/deungjaho/hydra/internal/account"
	"github.com/deungjaho/hydra/internal/config"
	"github.com/deungjaho/hydra/internal/provider"
	"github.com/deungjaho/hydra/internal/registry"
)

// apiProviderEntry is a resolved provider+key ready for forwarding.
type apiProviderEntry struct {
	Name    string
	BaseURL string
	APIKey  string
}

// resolveAPIProviders looks up the model in the registry and returns all
// associated providers in priority order. Returns nil if the model should
// fall through to the Antigravity path.
func (s *ProxyServer) resolveAPIProviders(model string) []*apiProviderEntry {
	if s.Registry == nil {
		return nil
	}
	provs := s.Registry.ProvidersFor(model)
	if len(provs) == 0 {
		return nil
	}
	out := make([]*apiProviderEntry, 0, len(provs))
	for _, p := range provs {
		out = append(out, &apiProviderEntry{
			Name:    p.Name,
			BaseURL: p.BaseURL,
			APIKey:  p.APIKey,
		})
	}
	return out
}

// resolveAPIProvider returns the first (highest-priority) provider.
// Kept for backward compatibility with code that doesn't need failover.
func (s *ProxyServer) resolveAPIProvider(model string) *apiProviderEntry {
	provs := s.resolveAPIProviders(model)
	if len(provs) == 0 {
		return nil
	}
	return provs[0]
}

// registeredModelIDs returns all provider-sourced model IDs from the registry.
func (s *ProxyServer) registeredModelIDs() []string {
	if s.Registry == nil {
		return nil
	}
	return s.Registry.ProviderModels()
}

// registeredModelMeta returns display metadata for a registered model.
func (s *ProxyServer) registeredModelMeta(model string) (display string, ctx int, maxOut int64, ok bool) {
	if s.Registry == nil {
		return
	}
	e := s.Registry.Lookup(model)
	if e == nil || e.Source != registry.SourceProvider {
		return
	}
	display = e.DisplayName
	ctx = e.ContextWindow
	maxOut = e.MaxOutputTokens
	ok = true
	return
}

// apiKeyForwardClient is a shared HTTP client for API key provider calls.
var apiKeyForwardClient = &http.Client{
	Timeout: 300 * time.Second,
}

// markProviderUnhealthyOnRequestFailure tracks consecutive request failures
// for a provider and auto-disables it after the threshold is reached.
// This complements the background health check loop — if real requests fail
// repeatedly, the provider is disabled immediately without waiting for the
// next health check cycle.
func (s *ProxyServer) markProviderUnhealthyOnRequestFailure(providerName string, statusCode int) {
	// Only track server errors and rate limits, not client errors (4xx).
	if statusCode < 500 && statusCode != 429 {
		return
	}
	// Look up the provider by name to get its ID.
	providers, err := provider.ListProviders(s.State.DB)
	if err != nil {
		return
	}
	for _, p := range providers {
		if p.Name != providerName {
			continue
		}
		count := s.incrementProviderHealthFailure(p.ID)
		threshold := 3
		if s.Config.HealthCheck.FailureThreshold > 0 {
			threshold = s.Config.HealthCheck.FailureThreshold
		}
		if count >= threshold && !p.HealthDisabled {
			log.Printf("provider %s: %d consecutive request failures, auto-disabling",
				p.Name, count)
			logErr(provider.MarkProviderHealthDisabled(s.State.DB, p.ID,
				fmt.Sprintf("request failed with HTTP %d", statusCode)))
			s.State.providerHealthFailures.Delete(p.ID)
			s.State.Notifier.NotifyProviderUnhealthy(p.Name,
				fmt.Sprintf("HTTP %d", statusCode))
		}
		return
	}
}

// resetProviderHealthFailures clears the failure counter for a provider
// after a successful request.
func (s *ProxyServer) resetProviderHealthFailures(providerName string) {
	providers, err := provider.ListProviders(s.State.DB)
	if err != nil {
		return
	}
	for _, p := range providers {
		if p.Name != providerName {
			continue
		}
		s.State.providerHealthFailures.Delete(p.ID)
		return
	}
}

// injectThinkingParams ensures DeepSeek-style thinking mode is enabled.
// DeepSeek needs `thinking: {type: "enabled"}` + `reasoning_effort` to
// activate thinking mode. If the client already set reasoning_effort
// (from Codex/Claude), pass it through. Otherwise default to "high".
func injectThinkingParams(openaiReq map[string]any) {
	if _, ok := openaiReq["thinking"]; !ok {
		openaiReq["thinking"] = map[string]any{"type": "enabled"}
	}
	if _, ok := openaiReq["reasoning_effort"]; !ok {
		// Check if reasoning.effort was passed (some clients nest it).
		if reasoning, ok := openaiReq["reasoning"].(map[string]any); ok {
			if effort, ok := reasoning["effort"].(string); ok && effort != "" && effort != "none" {
				openaiReq["reasoning_effort"] = effort
				return
			}
		}
		openaiReq["reasoning_effort"] = "high"
	}
}

// normalizeRoles converts roles that some providers don't accept.
// DeepSeek rejects "developer" (OpenAI's newer system role name) —
// map it to "system". Also strips empty content messages.
func normalizeRoles(openaiReq map[string]any) {
	msgs, ok := openaiReq["messages"].([]any)
	if !ok {
		return
	}
	for _, mAny := range msgs {
		m, ok := mAny.(map[string]any)
		if !ok {
			continue
		}
		if role, _ := m["role"].(string); role == "developer" {
			m["role"] = "system"
		}
	}
}

// normalizeTools filters out tool types the provider doesn't accept.
// DeepSeek only accepts type:"function". Codex sends type:"custom"
// (MCP tools, shell tools, etc.) — drop those since the provider
// can't execute them anyway.
func normalizeTools(openaiReq map[string]any) {
	tools, ok := openaiReq["tools"].([]any)
	if !ok {
		return
	}
	filtered := make([]any, 0, len(tools))
	for _, tAny := range tools {
		t, ok := tAny.(map[string]any)
		if !ok {
			continue
		}
		ttype, _ := t["type"].(string)
		if ttype == "function" {
			filtered = append(filtered, t)
		}
		// Drop "custom", "web_search", "file_search", etc.
	}
	if len(filtered) == 0 {
		delete(openaiReq, "tools")
		delete(openaiReq, "tool_choice")
	} else {
		openaiReq["tools"] = filtered
	}
}

// forwardChatCompletions transparently forwards an OpenAI Chat Completions
// request to one or more API key providers. If the first provider returns
// 429 or 5xx, the request is retried with the next provider (multi-key
// failover). Both request and response are already OpenAI format — no
// transformation needed.
func (s *ProxyServer) forwardChatCompletions(w http.ResponseWriter, r *http.Request,
	body []byte, providers []*apiProviderEntry, model string, apiKeyID *int64, clientIP string) {

	// Parse body to inject thinking params, then re-serialize.
	var openaiReq map[string]any
	if err := json.Unmarshal(body, &openaiReq); err != nil {
		http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}
	normalizeRoles(openaiReq)
	normalizeTools(openaiReq)
	injectThinkingParams(openaiReq)
	body, _ = json.Marshal(openaiReq)

	for i, prov := range providers {
		url := prov.BaseURL + "/chat/completions"
		req, err := http.NewRequestWithContext(r.Context(), "POST", url, bytes.NewReader(body))
		if err != nil {
			http.Error(w, "internal error: "+err.Error(), http.StatusInternalServerError)
			return
		}
		req.Header.Set("Authorization", "Bearer "+prov.APIKey)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", r.Header.Get("Accept"))

		log.Printf("apikey provider %s: forwarding to %s model=%s", prov.Name, url, model)
		resp, err := apiKeyForwardClient.Do(req)
		if err != nil {
			log.Printf("apikey provider %s: request failed: %v", prov.Name, err)
			s.markProviderUnhealthyOnRequestFailure(prov.Name, 502)
			logErr(account.LogRequest(s.State.DB, account.LogRequestParams{
				Model:    &model,
				Status:   502,
				ClientIP: pstrIf(clientIP != "", clientIP),
				Error:    pstr(err.Error()),
				APIKeyID: apiKeyID,
			}))
			// Try next provider if available.
			if i+1 < len(providers) {
				log.Printf("apikey provider failover: %s failed, trying next", prov.Name)
				continue
			}
			http.Error(w, "upstream request failed", http.StatusBadGateway)
			return
		}

		// Check if we should failover to the next provider.
		if (resp.StatusCode == 429 || resp.StatusCode >= 500) && i+1 < len(providers) {
			respBody, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			log.Printf("apikey provider failover: %s got %d, trying next",
				prov.Name, resp.StatusCode)
			s.markProviderUnhealthyOnRequestFailure(prov.Name, resp.StatusCode)
			logErr(account.LogRequest(s.State.DB, account.LogRequestParams{
				Model:    &model,
				Status:   int64(resp.StatusCode),
				ClientIP: pstrIf(clientIP != "", clientIP),
				Error:    pstr(string(respBody)),
				APIKeyID: apiKeyID,
			}))
			continue
		}

		defer resp.Body.Close()

		// Log the provider request result.
		logErr(account.LogRequest(s.State.DB, account.LogRequestParams{
			Model:    &model,
			Status:   int64(resp.StatusCode),
			ClientIP: pstrIf(clientIP != "", clientIP),
			APIKeyID: apiKeyID,
		}))

		// Track provider health based on real request results.
		if resp.StatusCode >= 500 || resp.StatusCode == 429 {
			s.markProviderUnhealthyOnRequestFailure(prov.Name, resp.StatusCode)
		} else if resp.StatusCode < 400 {
			s.resetProviderHealthFailures(prov.Name)
		}

		copyHeaders(w.Header(), resp.Header)
		w.WriteHeader(resp.StatusCode)

		// SSE streaming: flush each line.
		if isSSEResponse(resp) {
			flushCopy(w, resp.Body)
		} else {
			_, _ = io.Copy(w, resp.Body)
		}
		return
	}

	// All providers exhausted.
	http.Error(w, "all providers exhausted (rate limited or unavailable)",
		http.StatusServiceUnavailable)
}

// forwardResponses converts a Responses API request to Chat Completions,
// forwards to one or more API key providers (with multi-key failover),
// and converts the response back to Responses API format.
func (s *ProxyServer) forwardResponses(w http.ResponseWriter, r *http.Request,
	responsesReq map[string]any, providers []*apiProviderEntry, model string, apiKeyID *int64, clientIP string) {

	openaiReq := responsesRequestToOpenAI(responsesReq)
	openaiReq["model"] = model
	normalizeRoles(openaiReq)
	normalizeTools(openaiReq)
	injectThinkingParams(openaiReq)

	stream, _ := openaiReq["stream"].(bool)

	body, err := json.Marshal(openaiReq)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, responsesErrorBody(500, "marshal error"))
		return
	}

	for i, prov := range providers {
		url := prov.BaseURL + "/chat/completions"
		req, err := http.NewRequestWithContext(r.Context(), "POST", url, bytes.NewReader(body))
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, responsesErrorBody(500, "internal error"))
			return
		}
		req.Header.Set("Authorization", "Bearer "+prov.APIKey)
		req.Header.Set("Content-Type", "application/json")

		resp, err := apiKeyForwardClient.Do(req)
		if err != nil {
			log.Printf("apikey provider %s: responses request failed: %v", prov.Name, err)
			s.markProviderUnhealthyOnRequestFailure(prov.Name, 502)
			logErr(account.LogRequest(s.State.DB, account.LogRequestParams{
				Model:    &model,
				Status:   502,
				ClientIP: pstrIf(clientIP != "", clientIP),
				Error:    pstr(err.Error()),
				APIKeyID: apiKeyID,
			}))
			if i+1 < len(providers) {
				log.Printf("apikey provider failover: %s failed, trying next", prov.Name)
				continue
			}
			writeJSON(w, http.StatusBadGateway, responsesErrorBody(502, "upstream request failed"))
			return
		}

		// Check if we should failover.
		if (resp.StatusCode == 429 || resp.StatusCode >= 500) && i+1 < len(providers) {
			respBody, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			log.Printf("apikey provider failover: %s got %d, trying next",
				prov.Name, resp.StatusCode)
			s.markProviderUnhealthyOnRequestFailure(prov.Name, resp.StatusCode)
			logErr(account.LogRequest(s.State.DB, account.LogRequestParams{
				Model:    &model,
				Status:   int64(resp.StatusCode),
				ClientIP: pstrIf(clientIP != "", clientIP),
				Error:    pstr(string(respBody)),
				APIKeyID: apiKeyID,
			}))
			continue
		}

		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			respBody, _ := io.ReadAll(resp.Body)
			if resp.StatusCode >= 500 || resp.StatusCode == 429 {
				s.markProviderUnhealthyOnRequestFailure(prov.Name, resp.StatusCode)
			}
			logErr(account.LogRequest(s.State.DB, account.LogRequestParams{
				Model:    &model,
				Status:   int64(resp.StatusCode),
				ClientIP: pstrIf(clientIP != "", clientIP),
				APIKeyID: apiKeyID,
			}))
			writeJSON(w, resp.StatusCode, responsesErrorBody(resp.StatusCode, string(respBody)))
			return
		}

		// Log successful provider request and reset failure counter.
		s.resetProviderHealthFailures(prov.Name)
		logErr(account.LogRequest(s.State.DB, account.LogRequestParams{
			Model:    &model,
			Status:   200,
			ClientIP: pstrIf(clientIP != "", clientIP),
			APIKeyID: apiKeyID,
		}))

		if stream {
			w.Header().Set("Content-Type", "text/event-stream")
			w.Header().Set("Cache-Control", "no-cache")
			w.WriteHeader(http.StatusOK)
			convertOpenAIStreamToResponses(w, resp.Body, model)
		} else {
			var chatResp map[string]any
			if err := json.NewDecoder(resp.Body).Decode(&chatResp); err != nil {
				writeJSON(w, http.StatusBadGateway, responsesErrorBody(502, "decode upstream response: "+err.Error()))
				return
			}
			writeJSON(w, http.StatusOK, openAIChatToResponses(chatResp, model))
		}
		return
	}

	// All providers exhausted.
	writeJSON(w, http.StatusServiceUnavailable,
		responsesErrorBody(503, "all providers exhausted (rate limited or unavailable)"))
}

// forwardAnthropicMessages converts an Anthropic Messages API request to
// Chat Completions, forwards to one or more API key providers (with
// multi-key failover), and converts the response back to Anthropic format.
func (s *ProxyServer) forwardAnthropicMessages(w http.ResponseWriter, r *http.Request,
	anthropicReq map[string]any, providers []*apiProviderEntry, model string, apiKeyID *int64, clientIP string) {

	openaiReq := anthropicRequestToOpenAIChat(anthropicReq)
	openaiReq["model"] = model
	normalizeRoles(openaiReq)
	normalizeTools(openaiReq)
	injectThinkingParams(openaiReq)

	stream, _ := openaiReq["stream"].(bool)

	body, err := json.Marshal(openaiReq)
	if err != nil {
		http.Error(w, "internal error: "+err.Error(), http.StatusInternalServerError)
		return
	}

	for i, prov := range providers {
		url := prov.BaseURL + "/chat/completions"
		req, err := http.NewRequestWithContext(r.Context(), "POST", url, bytes.NewReader(body))
		if err != nil {
			http.Error(w, "internal error: "+err.Error(), http.StatusInternalServerError)
			return
		}
		req.Header.Set("Authorization", "Bearer "+prov.APIKey)
		req.Header.Set("Content-Type", "application/json")

		resp, err := apiKeyForwardClient.Do(req)
		if err != nil {
			log.Printf("apikey provider %s: anthropic request failed: %v", prov.Name, err)
			s.markProviderUnhealthyOnRequestFailure(prov.Name, 502)
			logErr(account.LogRequest(s.State.DB, account.LogRequestParams{
				Model:    &model,
				Status:   502,
				ClientIP: pstrIf(clientIP != "", clientIP),
				Error:    pstr(err.Error()),
				APIKeyID: apiKeyID,
			}))
			if i+1 < len(providers) {
				log.Printf("apikey provider failover: %s failed, trying next", prov.Name)
				continue
			}
			http.Error(w, "upstream request failed", http.StatusBadGateway)
			return
		}

		// Check if we should failover.
		if (resp.StatusCode == 429 || resp.StatusCode >= 500) && i+1 < len(providers) {
			respBody, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			log.Printf("apikey provider failover: %s got %d, trying next",
				prov.Name, resp.StatusCode)
			s.markProviderUnhealthyOnRequestFailure(prov.Name, resp.StatusCode)
			logErr(account.LogRequest(s.State.DB, account.LogRequestParams{
				Model:    &model,
				Status:   int64(resp.StatusCode),
				ClientIP: pstrIf(clientIP != "", clientIP),
				Error:    pstr(string(respBody)),
				APIKeyID: apiKeyID,
			}))
			continue
		}

		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			respBody, _ := io.ReadAll(resp.Body)
			if resp.StatusCode >= 500 || resp.StatusCode == 429 {
				s.markProviderUnhealthyOnRequestFailure(prov.Name, resp.StatusCode)
			}
			logErr(account.LogRequest(s.State.DB, account.LogRequestParams{
				Model:    &model,
				Status:   int64(resp.StatusCode),
				ClientIP: pstrIf(clientIP != "", clientIP),
				APIKeyID: apiKeyID,
			}))
			http.Error(w, string(respBody), resp.StatusCode)
			return
		}

		// Log successful provider request and reset failure counter.
		s.resetProviderHealthFailures(prov.Name)
		logErr(account.LogRequest(s.State.DB, account.LogRequestParams{
			Model:    &model,
			Status:   200,
			ClientIP: pstrIf(clientIP != "", clientIP),
			APIKeyID: apiKeyID,
		}))

		if stream {
			w.Header().Set("Content-Type", "text/event-stream")
			w.Header().Set("Cache-Control", "no-cache")
			w.WriteHeader(http.StatusOK)
			convertOpenAIStreamToAnthropic(w, resp.Body, model)
		} else {
			var chatResp map[string]any
			if err := json.NewDecoder(resp.Body).Decode(&chatResp); err != nil {
				http.Error(w, "decode upstream response: "+err.Error(), http.StatusBadGateway)
				return
			}
			writeJSON(w, http.StatusOK, openAIChatToAnthropic(chatResp, model))
		}
		return
	}

	// All providers exhausted.
	http.Error(w, "all providers exhausted (rate limited or unavailable)",
		http.StatusServiceUnavailable)
}

func isSSEResponse(resp *http.Response) bool {
	ct := resp.Header.Get("Content-Type")
	return strings.Contains(ct, "text/event-stream")
}

// flushCopy copies SSE data from src to dst, flushing after each line.
func flushCopy(dst http.ResponseWriter, src io.Reader) {
	flusher, _ := dst.(http.Flusher)
	scanner := bufio.NewScanner(src)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		_, _ = fmt.Fprintf(dst, "%s\n", line)
		if flusher != nil {
			flusher.Flush()
		}
	}
}

func copyHeaders(dst, src http.Header) {
	for k, vs := range src {
		if strings.EqualFold(k, "Transfer-Encoding") || strings.EqualFold(k, "Content-Length") {
			continue
		}
		for _, v := range vs {
			dst.Add(k, v)
		}
	}
}

// anthropicRequestToOpenAIChat converts an Anthropic Messages API request
// to OpenAI Chat Completions format.
func anthropicRequestToOpenAIChat(req map[string]any) map[string]any {
	out := make(map[string]any, 12)

	out["model"] = strOr(req, "model", "")
	if v, ok := req["stream"]; ok {
		out["stream"] = v
	}
	if v, ok := req["temperature"]; ok {
		out["temperature"] = v
	}
	if v, ok := req["top_p"]; ok {
		out["top_p"] = v
	}
	if v, ok := req["max_tokens"]; ok {
		out["max_tokens"] = v
	}
	if v, ok := req["stop_sequences"]; ok {
		out["stop"] = v
	}
	if v, ok := req["user_id"]; ok {
		out["user"] = v
	}

	// system → system message
	var messages []any
	if sys, ok := req["system"].(string); ok && sys != "" {
		messages = append(messages, map[string]any{
			"role":    "system",
			"content": sys,
		})
	} else if sysArr, ok := req["system"].([]any); ok {
		var sysParts []string
		for _, p := range sysArr {
			if pm, ok := p.(map[string]any); ok {
				if t, _ := pm["type"].(string); t == "text" {
					if text, ok := pm["text"].(string); ok {
						sysParts = append(sysParts, text)
					}
				}
			}
		}
		if len(sysParts) > 0 {
			messages = append(messages, map[string]any{
				"role":    "system",
				"content": strings.Join(sysParts, "\n"),
			})
		}
	}

	// messages
	if msgs, ok := req["messages"].([]any); ok {
		for _, mAny := range msgs {
			m, ok := mAny.(map[string]any)
			if !ok {
				continue
			}
			role, _ := m["role"].(string)
			content := anthropicContentToOpenAI(m["content"])
			messages = append(messages, map[string]any{
				"role":    role,
				"content": content,
			})
		}
	}
	out["messages"] = messages

	// tools
	if tools, ok := req["tools"].([]any); ok {
		out["tools"] = anthropicToolsToOpenAI(tools)
	}
	if v, ok := req["tool_choice"]; ok {
		out["tool_choice"] = anthropicToolChoiceToOpenAI(v)
	}

	return out
}

func anthropicContentToOpenAI(content any) any {
	switch v := content.(type) {
	case string:
		return v
	case []any:
		var parts []any
		for _, part := range v {
			pm, ok := part.(map[string]any)
			if !ok {
				continue
			}
			ptype, _ := pm["type"].(string)
			switch ptype {
			case "text":
				text, _ := pm["text"].(string)
				parts = append(parts, map[string]any{
					"type": "text",
					"text": text,
				})
			case "image":
				src, _ := pm["source"].(map[string]any)
				if src != nil {
					srcType, _ := src["type"].(string)
					if srcType == "base64" {
						mediaType, _ := src["media_type"].(string)
						data, _ := src["data"].(string)
						parts = append(parts, map[string]any{
							"type": "image_url",
							"image_url": map[string]any{
								"url": fmt.Sprintf("data:%s;base64,%s", mediaType, data),
							},
						})
					}
				}
			case "tool_use":
				// Handled at message level via tool_calls
			case "tool_result":
				// Handled at message level via tool role
			}
		}
		if len(parts) > 0 {
			return parts
		}
		return ""
	default:
		return ""
	}
}

func anthropicToolsToOpenAI(tools []any) []any {
	out := make([]any, 0, len(tools))
	for _, tool := range tools {
		tm, ok := tool.(map[string]any)
		if !ok {
			continue
		}
		name, _ := tm["name"].(string)
		desc, _ := tm["description"].(string)
		params, _ := tm["input_schema"].(map[string]any)
		out = append(out, map[string]any{
			"type": "function",
			"function": map[string]any{
				"name":        name,
				"description": desc,
				"parameters":  params,
			},
		})
	}
	return out
}

func anthropicToolChoiceToOpenAI(choice any) any {
	m, ok := choice.(map[string]any)
	if !ok {
		return choice
	}
	ct, _ := m["type"].(string)
	switch ct {
	case "auto":
		return "auto"
	case "any":
		return "required"
	case "tool":
		name, _ := m["name"].(string)
		return map[string]any{
			"type":     "function",
			"function": map[string]any{"name": name},
		}
	default:
		return "auto"
	}
}

// openAIChatToAnthropic converts an OpenAI Chat Completions response to
// Anthropic Messages API format.
func openAIChatToAnthropic(resp map[string]any, model string) map[string]any {
	content := []any{}
	stopReason := "end_turn"

	if choices, ok := resp["choices"].([]any); ok && len(choices) > 0 {
		choice, _ := choices[0].(map[string]any)
		if choice != nil {
			if fr, ok := choice["finish_reason"].(string); ok {
				stopReason = openAIToAnthropicStopReason(fr)
			}
			if msg, ok := choice["message"].(map[string]any); ok {
				// Reasoning content → thinking block (DeepSeek thinking mode).
				if reasoning, ok := msg["reasoning_content"].(string); ok && reasoning != "" {
					content = append(content, map[string]any{
						"type":     "thinking",
						"thinking": reasoning,
					})
				}
				// Text content
				if text, ok := msg["content"].(string); ok && text != "" {
					content = append(content, map[string]any{
						"type": "text",
						"text": text,
					})
				}
				// Tool calls
				if toolCalls, ok := msg["tool_calls"].([]any); ok {
					for _, tcAny := range toolCalls {
						tc, ok := tcAny.(map[string]any)
						if !ok {
							continue
						}
						id, _ := tc["id"].(string)
						fn, _ := tc["function"].(map[string]any)
						if fn == nil {
							continue
						}
						name, _ := fn["name"].(string)
						args, _ := fn["arguments"].(string)
						var input any
						if err := json.Unmarshal([]byte(args), &input); err != nil {
							input = map[string]any{}
						}
						content = append(content, map[string]any{
							"type":  "tool_use",
							"id":    id,
							"name":  name,
							"input": input,
						})
					}
				}
			}
		}
	}

	if len(content) == 0 {
		content = []any{map[string]any{"type": "text", "text": ""}}
	}

	usage := map[string]any{
		"input_tokens":  0,
		"output_tokens": 0,
	}
	if u, ok := resp["usage"].(map[string]any); ok {
		if pt, ok := u["prompt_tokens"].(float64); ok {
			usage["input_tokens"] = int64(pt)
		}
		if ct, ok := u["completion_tokens"].(float64); ok {
			usage["output_tokens"] = int64(ct)
		}
	}

	return map[string]any{
		"id":            "msg_" + compactUUID(),
		"type":          "message",
		"role":          "assistant",
		"content":       content,
		"model":         model,
		"stop_reason":   stopReason,
		"stop_sequence": nil,
		"usage":         usage,
	}
}

func openAIToAnthropicStopReason(fr string) string {
	switch fr {
	case "stop":
		return "end_turn"
	case "length":
		return "max_tokens"
	case "tool_calls", "function_call":
		return "tool_use"
	case "content_filter":
		return "end_turn"
	default:
		return "end_turn"
	}
}

// openAIChatToResponses converts an OpenAI Chat Completions response to
// Responses API format.
func openAIChatToResponses(resp map[string]any, model string) map[string]any {
	var output []any
	finishReason := "stop"

	if choices, ok := resp["choices"].([]any); ok && len(choices) > 0 {
		choice, _ := choices[0].(map[string]any)
		if choice != nil {
			if fr, ok := choice["finish_reason"].(string); ok {
				finishReason = fr
			}
			if msg, ok := choice["message"].(map[string]any); ok {
				// Reasoning content → reasoning output item (DeepSeek thinking mode).
				if reasoning, ok := msg["reasoning_content"].(string); ok && reasoning != "" {
					output = append(output, map[string]any{
						"id":      "rs_" + compactUUID(),
						"type":    "reasoning",
						"content": []any{},
						"summary": []any{
							map[string]any{
								"type": "summary_text",
								"text": reasoning,
							},
						},
					})
				}
				// Text content → message output item
				if text, ok := msg["content"].(string); ok && text != "" {
					output = append(output, map[string]any{
						"id":     "msg_" + compactUUID(),
						"type":   "message",
						"status": "completed",
						"role":   "assistant",
						"content": []any{
							map[string]any{
								"type":        "output_text",
								"text":        text,
								"annotations": []any{},
							},
						},
					})
				}
				// Tool calls → function_call output items
				if toolCalls, ok := msg["tool_calls"].([]any); ok {
					for _, tcAny := range toolCalls {
						tc, ok := tcAny.(map[string]any)
						if !ok {
							continue
						}
						id, _ := tc["id"].(string)
						fn, _ := tc["function"].(map[string]any)
						if fn == nil {
							continue
						}
						name, _ := fn["name"].(string)
						args, _ := fn["arguments"].(string)
						output = append(output, map[string]any{
							"id":        id,
							"type":      "function_call",
							"call_id":   id,
							"name":      name,
							"arguments": args,
							"status":    "completed",
						})
					}
				}
			}
		}
	}

	if output == nil {
		output = []any{}
	}

	usage := map[string]any{
		"input_tokens":          0,
		"output_tokens":         0,
		"total_tokens":          0,
		"input_tokens_details":  map[string]any{"cached_tokens": 0},
		"output_tokens_details": map[string]any{"reasoning_tokens": 0},
	}
	if u, ok := resp["usage"].(map[string]any); ok {
		if pt, ok := u["prompt_tokens"].(float64); ok {
			usage["input_tokens"] = int64(pt)
		}
		if ct, ok := u["completion_tokens"].(float64); ok {
			usage["output_tokens"] = int64(ct)
		}
		usage["total_tokens"] = usage["input_tokens"].(int64) + usage["output_tokens"].(int64)
	}

	status := "completed"
	if strings.EqualFold(finishReason, "length") {
		status = "incomplete"
	}

	return map[string]any{
		"id":                   "resp_" + compactUUID(),
		"object":               "response",
		"created_at":           time.Now().Unix(),
		"model":                model,
		"status":               status,
		"error":                nil,
		"incomplete_details":   nil,
		"instructions":         nil,
		"output":               output,
		"usage":                usage,
		"parallel_tool_calls":  true,
		"temperature":          1.0,
		"top_p":                1.0,
		"max_output_tokens":    nil,
		"previous_response_id": nil,
		"reasoning":            nil,
		"text":                 nil,
		"truncation":           "disabled",
		"metadata":             map[string]any{},
		"store":                false,
		"service_tier":         "default",
	}
}

// convertOpenAIStreamToResponses reads an OpenAI SSE stream and writes
// Responses API SSE events.
func convertOpenAIStreamToResponses(w http.ResponseWriter, body io.Reader, model string) {
	flusher, _ := w.(http.Flusher)
	st := newResponsesStreamState("resp_"+compactUUID(), model, time.Now().Unix())

	// Send initial events.
	writeSSE := func(s string) {
		_, _ = io.WriteString(w, s)
		if flusher != nil {
			flusher.Flush()
		}
	}

	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		data := strings.TrimPrefix(line, "data: ")
		if data == "[DONE]" {
			// Finalize: close any open reasoning item, then message item, then completed.
			if st.reasoningAdded {
				writeSSE(st.reasoningSummaryTextDoneEvent())
				writeSSE(st.reasoningSummaryPartDoneEvent())
				writeSSE(st.reasoningItemDoneEvent())
			}
			if st.messageAdded {
				writeSSE(st.outputTextDoneEvent())
				writeSSE(st.contentPartDoneEvent())
				writeSSE(st.outputItemDoneEvent())
			}
			writeSSE(st.completedEvent())
			continue
		}

		var chunk map[string]any
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			continue
		}

		// Extract usage from final chunk.
		if u, ok := chunk["usage"].(map[string]any); ok {
			if pt, ok := u["prompt_tokens"].(float64); ok {
				st.totalPrompt = int64(pt)
			}
			if ct, ok := u["completion_tokens"].(float64); ok {
				st.totalCompletion = int64(ct)
			}
		}

		// Extract finish reason.
		if choices, ok := chunk["choices"].([]any); ok && len(choices) > 0 {
			choice, _ := choices[0].(map[string]any)
			if choice != nil {
				if fr, ok := choice["finish_reason"].(string); ok && fr != "" {
					st.finishReason = fr
				}
				if delta, ok := choice["delta"].(map[string]any); ok {
					// Reasoning delta (DeepSeek thinking mode).
					if reasoning, ok := delta["reasoning_content"].(string); ok && reasoning != "" {
						events := st.startReasoning()
						for _, e := range events {
							writeSSE(e)
						}
						writeSSE(st.reasoningDeltaEvent(reasoning))
						st.accumulatedReason += reasoning
					}
					// Text delta.
					if text, ok := delta["content"].(string); ok && text != "" {
						events := st.startMessage()
						for _, e := range events {
							writeSSE(e)
						}
						writeSSE(st.textDeltaEvent(text))
						st.accumulatedText += text
					}
					// Tool call deltas.
					if toolCalls, ok := delta["tool_calls"].([]any); ok {
						for _, tcAny := range toolCalls {
							tc, ok := tcAny.(map[string]any)
							if !ok {
								continue
							}
							idx, _ := tc["index"].(float64)
							fn, _ := tc["function"].(map[string]any)
							if fn == nil {
								continue
							}
							name, _ := fn["name"].(string)
							argsDelta, _ := fn["arguments"].(string)
							callID, _ := tc["id"].(string)
							if callID == "" {
								callID = fmt.Sprintf("fc_%d", int(idx))
							}
							if name != "" {
								st.outputIndex++
								writeSSE(st.functionCallItemAddedEvent(callID, name))
							}
							if argsDelta != "" {
								writeSSE(st.functionCallArgsDeltaEvent(callID, name, argsDelta))
							}
						}
					}
				}
			}
		}
	}
}

// convertOpenAIStreamToAnthropic reads an OpenAI SSE stream and writes
// Anthropic Messages API SSE events. Handles DeepSeek's reasoning_content
// deltas by emitting thinking content blocks before text blocks.
func convertOpenAIStreamToAnthropic(w http.ResponseWriter, body io.Reader, model string) {
	flusher, _ := w.(http.Flusher)
	msgID := "msg_" + compactUUID()

	writeSSE := func(eventType string, data map[string]any) {
		data["type"] = eventType
		b, _ := json.Marshal(data)
		_, _ = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", eventType, b)
		if flusher != nil {
			flusher.Flush()
		}
	}

	// message_start
	writeSSE("message_start", map[string]any{
		"message": map[string]any{
			"id":            msgID,
			"type":          "message",
			"role":          "assistant",
			"content":       []any{},
			"model":         model,
			"stop_reason":   nil,
			"stop_sequence": nil,
			"usage": map[string]any{
				"input_tokens":  0,
				"output_tokens": 0,
			},
		},
	})

	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	finishReason := ""
	var inputTokens, outputTokens int64

	// Block tracking: thinking block at index 0, text block at index 1.
	// Tool use blocks get indices 2, 3, ... as they appear.
	blockIndex := -1 // -1 = no block started yet
	thinkingStarted := false
	textStarted := false

	// Tool call tracking: maps OpenAI tool_calls index → Anthropic
	// content block index. OpenAI streams tool calls with a stable
	// "index" field; we need to map each to a unique Anthropic block
	// index.
	toolCallBlocks := make(map[int]int) // openai index → anthropic block index
	nextBlockIndex := 2                 // next available block index for tools

	startThinkingBlock := func() {
		blockIndex = 0
		thinkingStarted = true
		writeSSE("content_block_start", map[string]any{
			"index":         0,
			"content_block": map[string]any{"type": "thinking", "thinking": ""},
		})
	}
	startTextBlock := func() {
		if thinkingStarted {
			writeSSE("content_block_stop", map[string]any{"index": 0})
			thinkingStarted = false
		}
		blockIndex = 1
		textStarted = true
		writeSSE("content_block_start", map[string]any{
			"index":         1,
			"content_block": map[string]any{"type": "text", "text": ""},
		})
	}
	closeTextBlock := func() {
		if textStarted {
			writeSSE("content_block_stop", map[string]any{"index": 1})
			textStarted = false
		}
	}

	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		data := strings.TrimPrefix(line, "data: ")
		if data == "[DONE]" {
			break
		}

		var chunk map[string]any
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			continue
		}

		if u, ok := chunk["usage"].(map[string]any); ok {
			if pt, ok := u["prompt_tokens"].(float64); ok {
				inputTokens = int64(pt)
			}
			if ct, ok := u["completion_tokens"].(float64); ok {
				outputTokens = int64(ct)
			}
		}

		if choices, ok := chunk["choices"].([]any); ok && len(choices) > 0 {
			choice, _ := choices[0].(map[string]any)
			if choice == nil {
				continue
			}
			if fr, ok := choice["finish_reason"].(string); ok && fr != "" {
				finishReason = fr
			}
			if delta, ok := choice["delta"].(map[string]any); ok {
				// Reasoning delta → thinking block.
				if reasoning, ok := delta["reasoning_content"].(string); ok && reasoning != "" {
					if !thinkingStarted {
						startThinkingBlock()
					}
					writeSSE("content_block_delta", map[string]any{
						"index": blockIndex,
						"delta": map[string]any{"type": "thinking_delta", "thinking": reasoning},
					})
				}
				// Text delta → text block.
				if text, ok := delta["content"].(string); ok && text != "" {
					if !textStarted {
						startTextBlock()
					}
					writeSSE("content_block_delta", map[string]any{
						"index": blockIndex,
						"delta": map[string]any{"type": "text_delta", "text": text},
					})
				}
				// Tool calls delta -> tool_use content blocks.
				if toolCalls, ok := delta["tool_calls"].([]any); ok {
					for _, tcAny := range toolCalls {
						tc, _ := tcAny.(map[string]any)
						if tc == nil {
							continue
						}
						tcIdx := int(float64Or(tc, "index", 0))
						fn, _ := tc["function"].(map[string]any)
						if fn == nil {
							continue
						}
						// New tool call -> start a new content block.
						if _, exists := toolCallBlocks[tcIdx]; !exists {
							closeTextBlock()
							anthropicIdx := nextBlockIndex
							nextBlockIndex++
							toolCallBlocks[tcIdx] = anthropicIdx
							toolID, _ := tc["id"].(string)
							if toolID == "" {
								toolID = "toolu_" + compactUUID()
							}
							toolName, _ := fn["name"].(string)
							writeSSE("content_block_start", map[string]any{
								"index": anthropicIdx,
								"content_block": map[string]any{
									"type":  "tool_use",
									"id":    toolID,
									"name":  toolName,
									"input": map[string]any{},
								},
							})
						}
						// Arguments delta -> input_json_delta.
						if argsDelta, ok := fn["arguments"].(string); ok && argsDelta != "" {
							writeSSE("content_block_delta", map[string]any{
								"index": toolCallBlocks[tcIdx],
								"delta": map[string]any{
									"type":         "input_json_delta",
									"partial_json": argsDelta,
								},
							})
						}
					}
				}
			}
		}
	}

	// Close any open blocks.
	if thinkingStarted {
		writeSSE("content_block_stop", map[string]any{"index": 0})
	}
	if textStarted {
		writeSSE("content_block_stop", map[string]any{"index": 1})
	}
	// Close any open tool_use blocks.
	for _, idx := range toolCallBlocks {
		writeSSE("content_block_stop", map[string]any{"index": idx})
	}
	// If nothing was emitted (empty response), emit an empty text block.
	if !thinkingStarted && !textStarted && len(toolCallBlocks) == 0 {
		writeSSE("content_block_start", map[string]any{
			"index":         0,
			"content_block": map[string]any{"type": "text", "text": ""},
		})
		writeSSE("content_block_stop", map[string]any{"index": 0})
	}

	// message_delta with stop reason
	stopReason := openAIToAnthropicStopReason(finishReason)
	writeSSE("message_delta", map[string]any{
		"delta": map[string]any{"stop_reason": stopReason, "stop_sequence": nil},
		"usage": map[string]any{"input_tokens": inputTokens, "output_tokens": outputTokens},
	})

	// message_stop
	writeSSE("message_stop", map[string]any{})
}

// Ensure config import is used.
var _ = config.SchedulingBalance
