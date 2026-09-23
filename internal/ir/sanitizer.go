package ir

import "strings"

// SanitizeSystemPrompt cleans competitor-agent attribution headers, SDK signatures,
// and model references that trigger Google Cloud Code / Antigravity WAF blocks (429 RESOURCE_EXHAUSTED).
func SanitizeSystemPrompt(s string) string {
	if s == "" {
		return ""
	}

	// 1. Strip OpenAI Codex preamble signature:
	// Google WAF blocks "You are Codex, a coding agent based on GPT-5"
	// Completely remove the sentence to leave zero trace.
	if strings.Contains(s, "You are Codex, a coding agent based on GPT-5") {
		s = strings.ReplaceAll(s, "You are Codex, a coding agent based on GPT-5. ", "")
		s = strings.ReplaceAll(s, "You are Codex, a coding agent based on GPT-5.\n", "")
		s = strings.ReplaceAll(s, "You are Codex, a coding agent based on GPT-5.", "")
		s = strings.ReplaceAll(s, "You are Codex, a coding agent based on GPT-5", "")
	}
	if strings.Contains(s, "You are Codex, a coding agent based on Gemini") {
		s = strings.ReplaceAll(s, "You are Codex, a coding agent based on Gemini. ", "")
		s = strings.ReplaceAll(s, "You are Codex, a coding agent based on Gemini.\n", "")
		s = strings.ReplaceAll(s, "You are Codex, a coding agent based on Gemini.", "")
		s = strings.ReplaceAll(s, "You are Codex, a coding agent based on Gemini", "")
	}

	// 2. Strip Anthropic attribution headers and SDK boilerplate:
	// Google WAF blocks "x-anthropic-billing-header:" and "Claude Agent SDK"
	if isAnthropicSignature(s) {
		lines := strings.Split(s, "\n")
		var kept []string
		for _, line := range lines {
			if !isAnthropicSignature(line) {
				kept = append(kept, line)
			}
		}
		s = strings.Join(kept, "\n")
	}

	return s
}

func isAnthropicSignature(s string) bool {
	return strings.Contains(s, "x-anthropic-billing-header:") ||
		strings.Contains(s, "Anthropic's Claude Agent SDK") ||
		strings.Contains(s, "Claude Agent SDK")
}
