package proxy

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"strings"
)

// ExtractSessionKey identifies the conversation session from incoming request
// headers, explicit payload fields, or falls back to an invariant first-turn
// prompt fingerprint.
func ExtractSessionKey(r *http.Request, rawReq map[string]any, protocol string, clientIP string, apiKeyID *int64) string {
	// 1. Explicit request headers take precedence
	if r != nil {
		if sid := strings.TrimSpace(r.Header.Get("X-Session-ID")); sid != "" {
			return sid
		}
		if sid := strings.TrimSpace(r.Header.Get("X-Conversation-ID")); sid != "" {
			return sid
		}
	}

	// 2. Explicit payload fields
	if rawReq != nil {
		if u, ok := rawReq["user"].(string); ok && strings.TrimSpace(u) != "" {
			return strings.TrimSpace(u)
		}
		if u, ok := rawReq["session_id"].(string); ok && strings.TrimSpace(u) != "" {
			return strings.TrimSpace(u)
		}
		if u, ok := rawReq["conversation_id"].(string); ok && strings.TrimSpace(u) != "" {
			return strings.TrimSpace(u)
		}
		if md, ok := rawReq["metadata"].(map[string]any); ok {
			for _, k := range []string{"user_id", "session_id", "conversation_id"} {
				if v, ok := md[k].(string); ok && strings.TrimSpace(v) != "" {
					return strings.TrimSpace(v)
				}
			}
		}
	}

	// 3. Fallback: Invariant first user message fingerprint
	firstUser := extractFirstUserMessage(rawReq)
	if firstUser == "" {
		return ""
	}

	h := sha256.New()
	if clientIP != "" {
		h.Write([]byte(clientIP))
	}
	if apiKeyID != nil {
		fmt.Fprintf(h, ":%d", *apiKeyID)
	}
	h.Write([]byte(":" + firstUser))
	return "fp-" + hex.EncodeToString(h.Sum(nil)[:12])
}

// extractFirstUserMessage extracts the text content of the first user message.
func extractFirstUserMessage(rawReq map[string]any) string {
	if rawReq == nil {
		return ""
	}
	msgs, ok := rawReq["messages"].([]any)
	if !ok || len(msgs) == 0 {
		return ""
	}

	for _, m := range msgs {
		msg, ok := m.(map[string]any)
		if !ok {
			continue
		}
		role, _ := msg["role"].(string)
		if role != "user" {
			continue
		}

		// Found first user turn. Extract text.
		content := msg["content"]
		switch c := content.(type) {
		case string:
			return strings.TrimSpace(c)
		case []any:
			var sb strings.Builder
			for _, part := range c {
				pMap, ok := part.(map[string]any)
				if !ok {
					continue
				}
				if txt, ok := pMap["text"].(string); ok {
					sb.WriteString(strings.TrimSpace(txt))
					sb.WriteString(" ")
				}
			}
			return strings.TrimSpace(sb.String())
		}
	}
	return ""
}
