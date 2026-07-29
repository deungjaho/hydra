package provider

import (
	"context"
	"io"
)

// Stream is a normalized streaming interface. Providers translate their
// native SSE format (OpenAI [DONE], Anthropic message_stop, Gemini close)
// into this unified chunk sequence.
type Stream interface {
	// Next returns the next chunk, or io.EOF when the stream is complete.
	// All providers MUST emit exactly one final chunk with FinishReason != ""
	// before returning io.EOF (normalizes the 3 different termination styles).
	Next(ctx context.Context) (*StreamChunk, error)
	// Close releases the upstream connection.
	Close() error
}

// StreamChunk is the normalized streaming chunk.
type StreamChunk struct {
	// Exactly one of DeltaContent / DeltaToolCall / DeltaThinking is
	// non-empty per chunk (mirrors Anthropic's content_block_delta discipline).
	DeltaContent  string
	DeltaToolCall *ToolCall
	DeltaThinking string
	// Usage is present on the final chunk (cumulative, like Gemini/Anthropic).
	Usage *Usage
	// FinishReason is non-empty ONLY on the final chunk.
	FinishReason FinishReason
}

// FinishReason is the normalized reason generation stopped.
type FinishReason string

const (
	FinishStop          FinishReason = "stop"
	FinishLength        FinishReason = "length"
	FinishToolCalls     FinishReason = "tool_calls"
	FinishContentFilter FinishReason = "content_filter"
)

// EchoStream is a trivial Stream implementation for testing. It emits
// one chunk with the given content and finish reason, then returns io.EOF.
type EchoStream struct {
	chunks []*StreamChunk
	idx    int
}

func NewEchoStream(content string) *EchoStream {
	return &EchoStream{
		chunks: []*StreamChunk{
			{DeltaContent: content},
			{FinishReason: FinishStop, Usage: &Usage{PromptTokens: 1, CompletionTokens: 1}},
		},
	}
}

func (s *EchoStream) Next(ctx context.Context) (*StreamChunk, error) {
	if s.idx >= len(s.chunks) {
		return nil, io.EOF
	}
	ch := s.chunks[s.idx]
	s.idx++
	return ch, nil
}

func (s *EchoStream) Close() error { return nil }
