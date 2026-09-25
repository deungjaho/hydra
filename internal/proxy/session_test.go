package proxy

import (
	"net/http/httptest"
	"testing"
)

func TestExtractSessionKey_Headers(t *testing.T) {
	req := httptest.NewRequest("POST", "/v1/chat/completions", nil)
	req.Header.Set("X-Session-ID", "custom-session-123")
	key := ExtractSessionKey(req, nil, "openai", "127.0.0.1", nil)
	if key != "custom-session-123" {
		t.Errorf("ExtractSessionKey = %q; want custom-session-123", key)
	}

	req2 := httptest.NewRequest("POST", "/v1/messages", nil)
	req2.Header.Set("X-Conversation-ID", "conv-456")
	key2 := ExtractSessionKey(req2, nil, "anthropic", "127.0.0.1", nil)
	if key2 != "conv-456" {
		t.Errorf("ExtractSessionKey = %q; want conv-456", key2)
	}
}

func TestExtractSessionKey_PayloadExplicit(t *testing.T) {
	req := httptest.NewRequest("POST", "/v1/chat/completions", nil)

	// OpenAI "user"
	bodyOpenAI := map[string]any{"user": "alice"}
	if key := ExtractSessionKey(req, bodyOpenAI, "openai", "127.0.0.1", nil); key != "alice" {
		t.Errorf("OpenAI user key = %q; want alice", key)
	}

	// Anthropic "metadata.user_id"
	bodyAnthropic := map[string]any{
		"metadata": map[string]any{"user_id": "bob"},
	}
	if key := ExtractSessionKey(req, bodyAnthropic, "anthropic", "127.0.0.1", nil); key != "bob" {
		t.Errorf("Anthropic user_id key = %q; want bob", key)
	}
}

func TestExtractSessionKey_InvariantPromptFingerprint(t *testing.T) {
	req := httptest.NewRequest("POST", "/v1/chat/completions", nil)

	// Turn 1
	turn1 := map[string]any{
		"messages": []any{
			map[string]any{"role": "user", "content": "Refactor database migration module"},
		},
	}
	key1 := ExtractSessionKey(req, turn1, "openai", "127.0.0.1", nil)
	if key1 == "" || len(key1) < 5 {
		t.Fatalf("expected valid fingerprint, got %q", key1)
	}

	// Turn 2 with additional assistant and user messages
	turn2 := map[string]any{
		"messages": []any{
			map[string]any{"role": "user", "content": "Refactor database migration module"},
			map[string]any{"role": "assistant", "content": "Here is the plan..."},
			map[string]any{"role": "user", "content": "Proceed with step 1"},
		},
	}
	key2 := ExtractSessionKey(req, turn2, "openai", "127.0.0.1", nil)

	if key1 != key2 {
		t.Errorf("multi-turn keys should match: turn1=%q turn2=%q", key1, key2)
	}

	// Turn 3 with Anthropic content parts
	turn3 := map[string]any{
		"messages": []any{
			map[string]any{
				"role": "user",
				"content": []any{
					map[string]any{"type": "text", "text": "Refactor database migration module"},
				},
			},
			map[string]any{"role": "assistant", "content": "Step 1 done"},
			map[string]any{"role": "user", "content": "Proceed with step 2"},
		},
	}
	key3 := ExtractSessionKey(req, turn3, "anthropic", "127.0.0.1", nil)
	if key1 != key3 {
		t.Errorf("content-block fingerprint should match string fingerprint: %q vs %q", key1, key3)
	}

	// Different initial prompt should generate different fingerprint
	diffTurn := map[string]any{
		"messages": []any{
			map[string]any{"role": "user", "content": "Write a snake game in Python"},
		},
	}
	keyDiff := ExtractSessionKey(req, diffTurn, "openai", "127.0.0.1", nil)
	if key1 == keyDiff {
		t.Errorf("different initial prompts should have different fingerprints: %q", key1)
	}
}

func TestStickySessions_TrajectoryContinuity(t *testing.T) {
	s := NewStickySessions()
	sessID := "fp-test123456"

	// Turn 1
	uuid1, step1 := s.NextTrajectory(sessID)
	if uuid1 == "" || step1 != 1 {
		t.Fatalf("turn 1: expected uuid and step=1, got uuid=%q step=%d", uuid1, step1)
	}

	// Turn 2
	uuid2, step2 := s.NextTrajectory(sessID)
	if uuid2 != uuid1 {
		t.Errorf("turn 2: trajectory UUID should be persistent across turns; got %q vs %q", uuid2, uuid1)
	}
	if step2 != 2 {
		t.Errorf("turn 2: step index should be 2; got %d", step2)
	}

	// Turn 3
	uuid3, step3 := s.NextTrajectory(sessID)
	if uuid3 != uuid1 || step3 != 3 {
		t.Errorf("turn 3: got uuid=%q step=%d; want uuid=%q step=3", uuid3, step3, uuid1)
	}

	// Peek should return current step without incrementing
	peekUUID, peekStep := s.PeekTrajectory(sessID)
	if peekUUID != uuid1 || peekStep != 3 {
		t.Errorf("peek: got uuid=%q step=%d; want uuid=%q step=3", peekUUID, peekStep, uuid1)
	}

	// Unbinding account does not destroy trajectory state
	s.Bind(sessID, 100)
	s.Unbind(sessID)
	if _, bound := s.Get(sessID); bound {
		t.Error("account should be unbound")
	}
	uuidAfterUnbind, stepAfterUnbind := s.NextTrajectory(sessID)
	if uuidAfterUnbind != uuid1 || stepAfterUnbind != 4 {
		t.Errorf("trajectory should survive account unbind: got uuid=%q step=%d", uuidAfterUnbind, stepAfterUnbind)
	}

	// Cleanup should evict expired trajectories
	s.Cleanup(0) // 0 maxIdle immediately evicts
	uuidAfterEvict, stepAfterEvict := s.NextTrajectory(sessID)
	if uuidAfterEvict == uuid1 {
		t.Error("trajectory UUID should be refreshed after eviction")
	}
	if stepAfterEvict != 1 {
		t.Errorf("step index should reset to 1 after eviction; got %d", stepAfterEvict)
	}
}

func TestStickySessions_EmptySessionGeneratesOneOff(t *testing.T) {
	s := NewStickySessions()

	u1, step1 := s.NextTrajectory("")
	u2, step2 := s.NextTrajectory("")

	if u1 == "" || u2 == "" {
		t.Fatal("empty session should produce non-empty UUIDs")
	}
	if u1 == u2 {
		t.Errorf("empty session calls should generate independent UUIDs: %q vs %q", u1, u2)
	}
	if step1 != 1 || step2 != 1 {
		t.Errorf("empty sessions should always have step=1; got %d and %d", step1, step2)
	}
}
