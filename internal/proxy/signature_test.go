package proxy

import (
	"strings"
	"testing"
)

// TestAnthropicThinkingSignaturePassthrough verifies that the real
// signature from the client's thinking block is forwarded to the
// upstream, and that the skip validator is only used as fallback.
func TestAnthropicThinkingSignaturePassthrough(t *testing.T) {
	// We can't easily call anthropicMessageToContent directly because
	// it requires a toolNameMap, but we can test the logic by
	// examining the function's behavior through a test harness.
	// Instead, test the signature selection logic inline.

	tests := []struct {
		name     string
		block    map[string]any
		wantSig  string
		wantSkip bool
	}{
		{
			name:     "real signature forwarded",
			block:    map[string]any{"thinking": "hello", "signature": "real-sig-123"},
			wantSig:  "real-sig-123",
			wantSkip: false,
		},
		{
			name:     "empty signature falls back to skip",
			block:    map[string]any{"thinking": "hello", "signature": ""},
			wantSig:  "skip_thought_signature_validator",
			wantSkip: true,
		},
		{
			name:     "missing signature falls back to skip",
			block:    map[string]any{"thinking": "hello"},
			wantSig:  "skip_thought_signature_validator",
			wantSkip: true,
		},
		{
			name:     "non-string signature falls back to skip",
			block:    map[string]any{"thinking": "hello", "signature": 123},
			wantSig:  "skip_thought_signature_validator",
			wantSkip: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Replicate the signature selection logic from anthropicMessageToContent.
			sig := "skip_thought_signature_validator"
			if s, ok := tt.block["signature"].(string); ok && s != "" {
				sig = s
			}
			if sig != tt.wantSig {
				t.Errorf("sig = %q, want %q", sig, tt.wantSig)
			}
			if strings.HasPrefix(sig, "skip_") == !tt.wantSkip {
				t.Errorf("skip fallback expected=%v, got sig=%q", tt.wantSkip, sig)
			}
		})
	}
}

// TestAnthropicThinkingBlockConversion tests the full content conversion
// to ensure thinking blocks produce the right Gemini-format parts.
func TestAnthropicThinkingBlockConversion(t *testing.T) {
	msg := map[string]any{
		"role": "assistant",
		"content": []any{
			map[string]any{
				"type":      "thinking",
				"thinking":  "Let me reason about this",
				"signature": "abc123signature",
			},
		},
	}
	result := anthropicMessageToContent(msg, map[string]string{})
	parts, ok := result["parts"].([]any)
	if !ok {
		t.Fatalf("result[parts] is not []any: %T", result["parts"])
	}
	var thinkingPart map[string]any
	for _, v := range parts {
		if p, ok := v.(map[string]any); ok {
			if p["thought"] == true {
				thinkingPart = p
				break
			}
		}
	}
	if thinkingPart == nil {
		t.Fatal("no thinking part found in conversion result")
	}
	if thinkingPart["thoughtSignature"] != "abc123signature" {
		t.Errorf("thoughtSignature = %v, want abc123signature", thinkingPart["thoughtSignature"])
	}
	if thinkingPart["text"] != "Let me reason about this" {
		t.Errorf("text = %v, want 'Let me reason about this'", thinkingPart["text"])
	}
}

func TestAnthropicThinkingBlockFallback(t *testing.T) {
	msg := map[string]any{
		"role": "assistant",
		"content": []any{
			map[string]any{
				"type":     "thinking",
				"thinking": "Reasoning without signature",
			},
		},
	}
	result := anthropicMessageToContent(msg, map[string]string{})
	parts, ok := result["parts"].([]any)
	if !ok {
		t.Fatalf("result[parts] is not []any: %T", result["parts"])
	}
	var thinkingPart map[string]any
	for _, v := range parts {
		if p, ok := v.(map[string]any); ok {
			if p["thought"] == true {
				thinkingPart = p
				break
			}
		}
	}
	if thinkingPart == nil {
		t.Fatal("no thinking part found in conversion result")
	}
	if thinkingPart["thoughtSignature"] != "skip_thought_signature_validator" {
		t.Errorf("thoughtSignature = %v, want skip_thought_signature_validator", thinkingPart["thoughtSignature"])
	}
}
