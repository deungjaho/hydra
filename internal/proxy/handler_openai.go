package proxy

import (
	"encoding/json"
	"io"
	"log"
	"net/http"
	"time"

	"github.com/deungjaho/hydra/internal/account"
	"github.com/deungjaho/hydra/internal/ir"
)

func (s *ProxyServer) handleChatCompletions(w http.ResponseWriter, r *http.Request) {
	startTime := time.Now()
	defer func() { s.State.metrics.observeDuration(time.Since(startTime).Seconds()) }()
	apiKey, ok := s.checkAuthFull(r)
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	apiKeyID := keyIDPtr(apiKey)

	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxRequestBody)) // 32MB
	if err != nil {
		http.Error(w, "read body: "+err.Error(), http.StatusBadRequest)
		return
	}

	var openaiReq map[string]any
	if err := json.Unmarshal(body, &openaiReq); err != nil {
		http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}

	stream, _ := openaiReq["stream"].(bool)
	originalModel := strOr(openaiReq, "model", "gemini-2.5-flash")
	mappedModel := MapModel(originalModel)
	clientIP := clientIPFrom(r)

	// API key provider passthrough (DeepSeek, Zhipu, etc.).
	if providers := s.resolveAPIProviders(originalModel); len(providers) > 0 {
		s.forwardChatCompletions(w, r, body, providers, originalModel, apiKeyID, clientIP)
		return
	}

	accounts, err := account.ListAccounts(s.State.DB)
	if err != nil {
		log.Printf("db list_accounts failed: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	sessionID := ExtractSessionKey(r, openaiReq, "openai", clientIP, apiKeyID)
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
			http.Error(w, msg, status)
		},
		writeRawErr: func(status int, body []byte) {
			w.WriteHeader(status)
			_, _ = w.Write(body)
		},
		buildBody: func(acc *account.Account, sessionUUID string, requestN uint64, effectiveModel string) ([]byte, error) {
			upstreamBody := irEncodeGeminiRequest(
				openaiReq, "openai", acc.ProjectID, sessionUUID, requestN)
			upstreamBody["model"] = effectiveModel
			return json.Marshal(upstreamBody)
		},
		handleSuccess: func(w http.ResponseWriter, resp *http.Response, acc *account.Account) {
			// Streaming success.
			chatID := "chatcmpl-" + compactUUID()
			created := time.Now().Unix()

			stitchFn := func(gstate *ir.GeminiStreamState) io.ReadCloser {
				thought := gstate.LastThinking()
				if thought == "" {
					return nil
				}
				newReq := make(map[string]any, len(openaiReq)+2)
				for k, v := range openaiReq {
					newReq[k] = v
				}
				origMsgs, _ := openaiReq["messages"].([]any)
				newMsgs := make([]any, 0, len(origMsgs)+2)
				newMsgs = append(newMsgs, origMsgs...)
				newMsgs = append(newMsgs, map[string]any{
					"role":    "assistant",
					"content": "...",
				})
				newMsgs = append(newMsgs, map[string]any{
					"role":    "user",
					"content": "Continue and execute the plan.",
				})
				newReq["messages"] = newMsgs

				sessionUUID, requestN := s.State.Sticky.NextTrajectory(sessionID)
				effectiveModel := mappedModel
				if avail := acc.AvailableModels(); len(avail) > 0 {
					effectiveModel = ResolveModelForAccount(mappedModel, avail)
				}
				upBody := irEncodeGeminiRequest(newReq, "openai", acc.ProjectID, sessionUUID, requestN)
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

			s.streamOpenAISSEIR(w, resp.Body, chatID, created,
				originalModel, acc.ID, apiKeyID, clientIP, stitchFn)
			resp.Body.Close()
		},
		handleSuccessNonStream: func(w http.ResponseWriter, resp *http.Response, acc *account.Account) {
			geminiResp := parseGeminiSuccessBody(w, resp, func(status int, msg string) {
				http.Error(w, msg, status)
			})
			if geminiResp == nil {
				return
			}
			openaiResp := irDecodeGeminiResponse(geminiResp, "openai", originalModel)
			usage, _ := openaiResp["usage"].(map[string]any)
			promptTokens := int64Or(usage, "prompt_tokens", 0)
			completionTokens := int64Or(usage, "completion_tokens", 0)
			cachedTokens := int64Or(usage, "cached_tokens", 0)
			thoughtTokens := int64Or(usage, "thought_tokens", 0)
			s.failoverLogSuccess(acc, originalModel,
				promptTokens, completionTokens, cachedTokens, thoughtTokens,
				clientIP, apiKeyID)
			writeJSON(w, http.StatusOK, openaiResp)
		},
	})
}
