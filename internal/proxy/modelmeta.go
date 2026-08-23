package proxy

import (
	"sync"

	"github.com/deungjaho/hydra/internal/account"
)

// modelMetaRegistry stores AGY's per-model metadata (thinkingBudget,
// maxOutputTokens, supportsThinking, etc.) dynamically from
// fetchAvailableModels. This replaces hardcoded values in catalog.go
// and mapper.go.
var (
	metaMu  sync.RWMutex
	metaReg map[string]account.ModelMeta
)

// UpdateModelMetas replaces the global model metadata registry.
// Called after each successful fetchAvailableModels.
func UpdateModelMetas(metas map[string]account.ModelMeta) {
	metaMu.Lock()
	defer metaMu.Unlock()
	metaReg = metas
}

// LookupModelMetaDynamic returns AGY-provided metadata for a model,
// or nil if not available (caller should fall back to hardcoded defaults).
func LookupModelMetaDynamic(model string) *account.ModelMeta {
	metaMu.RLock()
	defer metaMu.RUnlock()
	if metaReg == nil {
		return nil
	}
	if m, ok := metaReg[lower(model)]; ok {
		return &m
	}
	return nil
}

func lower(s string) string {
	out := make([]byte, len(s))
	for i := range s {
		c := s[i]
		if c >= 'A' && c <= 'Z' {
			c += 32
		}
		out[i] = c
	}
	return string(out)
}
