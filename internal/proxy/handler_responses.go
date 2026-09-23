package proxy

import (
	"encoding/json"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/deungjaho/hydra/internal/account"
	"github.com/google/uuid"
)

func (s *ProxyServer) handleResponses(w http.ResponseWriter, r *http.Request) {
	startTime := time.Now()
	defer func() { s.State.metrics.observeDuration(time.Since(startTime).Seconds()) }()
	apiKey, ok := s.checkAuthFull(r)
	if !ok {
		writeJSON(w, http.StatusUnauthorized, responsesErrorBody(401, "unauthorized"))
		return
	}
	apiKeyID := keyIDPtr(apiKey)

	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxRequestBody))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, responsesErrorBody(400, "read body: "+err.Error()))
		return
	}

	var responsesReq map[string]any
	if err := json.Unmarshal(body, &responsesReq); err != nil {
		writeJSON(w, http.StatusBadRequest, responsesErrorBody(400, "invalid JSON: "+err.Error()))
		return
	}

	// Convert Responses API request → OpenAI Chat Completions format.
	// Keep the legacy conversion for sessionID extraction and provider
	// passthrough; the IR path uses the original responsesReq.
	openaiReq := responsesRequestToOpenAI(responsesReq)

	stream, _ := openaiReq["stream"].(bool)
	originalModel := strOr(openaiReq, "model", "gemini-2.5-flash")
	mappedModel := MapModel(originalModel)
	clientIP := clientIPFrom(r)

	// API key provider passthrough (DeepSeek, Zhipu, etc.).
	if providers := s.resolveAPIProviders(originalModel); len(providers) > 0 {
		s.forwardResponses(w, r, responsesReq, providers, originalModel, apiKeyID, clientIP)
		return
	}

	accounts, err := account.ListAccounts(s.State.DB)
	if err != nil {
		log.Printf("db list_accounts failed: %v", err)
		writeJSON(w, http.StatusInternalServerError, responsesErrorBody(500, "internal error"))
		return
	}

	sessionID, _ := openaiReq["user"].(string)
	schedMode, noSticky := resolveScheduling(s, apiKey, r)

	s.failoverLoop(w, accounts, failoverConfig{
		originalModel: originalModel,
		mappedModel:   mappedModel,
		sessionID:     sessionID,
		stream:        stream,
		clientIP:      clientIP,
		apiKeyID:      apiKeyID,
		schedMode:     schedMode,
		noSticky:      noSticky,
		ctx:           r.Context(),
		writeErr: func(status int, msg string) {
			writeJSON(w, status, responsesErrorBody(status, msg))
		},
		writeRawErr: func(status int, body []byte) {
			writeJSON(w, status, responsesErrorBody(status, string(body)))
		},
		buildBody: func(acc *account.Account, sessionUUID string, requestN uint64, effectiveModel string) ([]byte, error) {
			upstreamBody := irEncodeGeminiRequest(
				responsesReq, "responses", acc.ProjectID, sessionUUID, requestN)
			upstreamBody["model"] = effectiveModel
			return json.Marshal(upstreamBody)
		},
		handleSuccess: func(w http.ResponseWriter, resp *http.Response, acc *account.Account) {
			// Streaming success.
			respID := "resp_" + compactUUID()
			created := time.Now().Unix()

			stitchFn := func(st *responsesStreamState) io.ReadCloser {
				reasoning := st.LastReasoning()
				if reasoning == "" {
					return nil
				}
				// Clone the original request and append a synthetic assistant message
				// plus a synthetic user prompt asking the model to continue.
				newReq := make(map[string]any, len(responsesReq)+2)
				for k, v := range responsesReq {
					newReq[k] = v
				}

				var newInputs []any
				if origInputArr, ok := responsesReq["input"].([]any); ok {
					newInputs = append(newInputs, origInputArr...)
				} else if origInputStr, ok := responsesReq["input"].(string); ok {
					newInputs = append(newInputs, map[string]any{
						"type": "message",
						"role": "user",
						"content": []any{
							map[string]any{"type": "input_text", "text": origInputStr},
						},
					})
				}

				newInputs = append(newInputs, map[string]any{
					"type": "message",
					"role": "assistant",
					"content": []any{
						map[string]any{"type": "output_text", "text": "..."},
					},
				})
				newInputs = append(newInputs, map[string]any{
					"type": "message",
					"role": "user",
					"content": []any{
						map[string]any{"type": "input_text", "text": "Continue and execute the plan."},
					},
				})
				newReq["input"] = newInputs

				sessionUUID := strings.ReplaceAll(uuid.NewString(), "-", "")
				requestN := s.State.NextRequestN()
				effectiveModel := mappedModel
				if avail := acc.AvailableModels(); len(avail) > 0 {
					effectiveModel = ResolveModelForAccount(mappedModel, avail)
				}
				upBody := irEncodeGeminiRequest(newReq, "responses", acc.ProjectID, sessionUUID, requestN)
				upBody["model"] = effectiveModel
				bodyBytes, err := json.Marshal(upBody)
				if err != nil {
					log.Printf("stitch marshal failed: %v", err)
					return nil
				}
				accessToken, ok := s.ensureFreshToken(
					acc, mappedModel, originalModel, clientIP, apiKeyID, w)
				if !ok {
					return nil
				}
				cResp, err := SendRequest(r.Context(), s.HTTP, accessToken, acc.ProjectID, bodyBytes, true, acc.MachineID)
				if err != nil || cResp.StatusCode != http.StatusOK {
					if cResp != nil && cResp.Body != nil {
						cResp.Body.Close()
					}
					log.Printf("stitch request failed: %v", err)
					return nil
				}
				return cResp.Body
			}

			s.streamResponsesSSEIR(w, resp.Body, respID, created,
				originalModel, acc.ID, apiKeyID, clientIP, stitchFn)
			resp.Body.Close()
		},
		handleSuccessNonStream: func(w http.ResponseWriter, resp *http.Response, acc *account.Account) {
			geminiResp := parseGeminiSuccessBody(w, resp, func(status int, msg string) {
				writeJSON(w, status, responsesErrorBody(status, msg))
			})
			if geminiResp == nil {
				return
			}
			responsesResp := irDecodeGeminiResponse(
				geminiResp, "responses", originalModel)
			usage, _ := responsesResp["usage"].(map[string]any)
			promptTokens := int64Or(usage, "input_tokens", 0)
			completionTokens := int64Or(usage, "output_tokens", 0)
			cachedTokens := int64Or0(usage, "cached_tokens")
			thoughtTokens := responsesUsageThoughtTokens(usage)
			s.failoverLogSuccess(acc, originalModel,
				promptTokens, completionTokens, cachedTokens, thoughtTokens,
				clientIP, apiKeyID)
			writeJSON(w, http.StatusOK, responsesResp)
		},
	})
}
