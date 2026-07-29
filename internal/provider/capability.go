package provider

// CapabilitySet declares what a provider can do.
// Static (from adapter Config) + optionally runtime-discovered via
// AvailableModels.
type CapabilitySet struct {
	SupportsStreaming        bool
	SupportsFunctionCalling  bool
	SupportsVision           bool
	SupportsAudioInput       bool
	SupportsThinking         bool // extended reasoning / thinking
	SupportsAssistantPrefill bool
	SupportsResponseFormat   bool // json_object / response_schema
	MaxOutputTokens          int64
	ContextWindow            int64
}

// CanServe returns true if this capability set satisfies the required
// capabilities. A required field is checked only if it is set to true
// in the required set (i.e. the request actually needs that capability).
// MaxOutputTokens and ContextWindow are checked only if the required
// value is non-zero.
//
// This is used by the Router for capability-gated failover, rejecting
// the LiteLLM #31557 (context-window mismatch) and #27967 (assistant
// prefill mismatch) anti-patterns.
func (c CapabilitySet) CanServe(required CapabilitySet) bool {
	if required.SupportsStreaming && !c.SupportsStreaming {
		return false
	}
	if required.SupportsFunctionCalling && !c.SupportsFunctionCalling {
		return false
	}
	if required.SupportsVision && !c.SupportsVision {
		return false
	}
	if required.SupportsAudioInput && !c.SupportsAudioInput {
		return false
	}
	if required.SupportsThinking && !c.SupportsThinking {
		return false
	}
	if required.SupportsAssistantPrefill && !c.SupportsAssistantPrefill {
		return false
	}
	if required.SupportsResponseFormat && !c.SupportsResponseFormat {
		return false
	}
	if required.MaxOutputTokens > 0 && c.MaxOutputTokens > 0 &&
		c.MaxOutputTokens < required.MaxOutputTokens {
		return false
	}
	if required.ContextWindow > 0 && c.ContextWindow > 0 &&
		c.ContextWindow < required.ContextWindow {
		return false
	}
	return true
}
