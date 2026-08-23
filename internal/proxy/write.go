package proxy

import (
	"encoding/json"
	"log"
	"net/http"
)

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	_ = enc.Encode(v)
}

func writeUnauthorized(w http.ResponseWriter) {
	writeJSON(w, http.StatusUnauthorized, map[string]any{
		"error": map[string]any{
			"code":    "UNAUTHORIZED",
			"message": "API key required",
		},
	})
}

// logErr logs an error if non-nil. Used for best-effort DB writes
// (request logs, account state updates) where the caller doesn't
// want to propagate the error but shouldn't stay completely silent.
func logErr(err error) {
	if err != nil {
		log.Printf("db write failed: %v", err)
	}
}
