// Package ir defines the intermediate representation (IR) for protocol
// translation in hydra.
//
// The IR is the common format that all inbound protocols (OpenAI Chat
// Completions, OpenAI Responses, Anthropic Messages) decode to and all
// outbound protocols (Gemini v1internal, OpenAI-compatible providers)
// encode from. This reduces pairwise translation from N×N to N×2.
//
// Design principles:
//   - The IR must be able to losslessly represent all semantics that any
//     supported protocol can express.
//   - Protocol-specific extensions that have no IR equivalent are preserved
//     in the Extra map rather than silently dropped.
//   - Special tool kinds (local_shell, apply_patch, mcp, computer, etc.)
//     are explicitly modelled via ToolKind, not treated as ordinary
//     function tools.
//   - Streaming is handled via IRStreamEvent, a union of all possible
//     incremental events. Each protocol's stream encoder is responsible
//     for synthesizing protocol-specific lifecycle events from the IR
//     event stream.
package ir

// Request is the protocol-neutral representation of an inference request.
type Request struct {
	Model          string
	System         string // system/developer prompt
	Messages       []Message
	Tools          []Tool
	ToolChoice     any // nil = auto, "none", "required", or specific tool
	Stream         bool
	MaxTokens      int64 // 0 = unset
	Temperature    *float64
	TopP           *float64
	TopK           *int64
	StopSequences  []string
	Reasoning      *Reasoning      // nil = no thinking
	ResponseFormat *ResponseFormat // nil = no structured output
	// Extra preserves protocol-specific fields that have no direct IR
	// equivalent (e.g. OpenAI parallel_tool_calls, Anthropic metadata).
	Extra map[string]any
}

// Message is a single conversational turn.
type Message struct {
	Role    string // "system", "user", "assistant", "tool"
	Content []Content
}

// Content is a typed piece of message content. Only one field is set
// per instance, determined by Type.
type Content struct {
	Type       ContentType
	Text       string
	Image      *Image
	Audio      *Audio
	ToolCall   *ToolCall
	ToolResult *ToolResult
	Thinking   *Thinking
	WebSearch  *WebSearchResult
}

// ContentType enumerates all content variants.
type ContentType int

const (
	ContentText ContentType = iota
	ContentThinking
	ContentImage
	ContentAudio
	ContentToolCall
	ContentToolResult
	ContentWebSearch
)

// Image is an inline or URL-referenced image.
type Image struct {
	MimeType string
	Data     string // base64-encoded data (empty if URL-only)
	URL      string // optional URL
}

// Audio is an inline audio clip.
type Audio struct {
	MimeType string
	Data     string // base64-encoded data
}

// ToolCall is a tool invocation produced by the assistant.
type ToolCall struct {
	ID   string
	Name string
	Args map[string]any // structured arguments
	// RawArgs preserves the original argument string for protocols
	// that pass arguments as JSON strings (OpenAI tool_calls).
	RawArgs string
	// Signature preserves thinking signatures from protocols that
	// use them (Anthropic thoughtSignature, AGY thoughtSignature).
	Signature string
	// Index is the positional index of this tool call within a single
	// response turn, used by streaming protocols (OpenAI tool_calls)
	// to associate delta chunks belonging to the same call. -1 = unset.
	Index int
}

// ToolResult is the response from a tool invocation.
type ToolResult struct {
	ID      string // matches the ToolCall ID
	Name    string
	Content string
	IsError bool
	// Signature preserves thought signatures attached to tool results
	// in some protocols (AGY functionResponse.thoughtSignature).
	Signature string
}

// Thinking represents extended reasoning / chain-of-thought content.
type Thinking struct {
	Text      string
	Signature string
	Redacted  bool
}

// WebSearchResult represents the result of a server-side web search
// (e.g. Gemini google_search grounding or Anthropic web_search_20250305).
type WebSearchResult struct {
	Query   string // the search query (if known)
	Sources []WebSearchSource
}

// WebSearchSource is a single search result entry.
type WebSearchSource struct {
	URI     string
	Title   string
	Snippet string
}

// Reasoning controls extended thinking behavior.
type Reasoning struct {
	Effort       string // "low", "medium", "high", or ""
	BudgetTokens int64  // 0 = unset
}

// ResponseFormat requests structured output.
type ResponseFormat struct {
	Type   string // "json_object" or "json_schema"
	Schema map[string]any
}

// Tool is a tool definition available to the model.
type Tool struct {
	Name        string
	Description string
	Schema      map[string]any // JSON Schema for parameters
	Kind        ToolKind
}

// ToolKind classifies tools beyond ordinary functions.
type ToolKind int

const (
	ToolFunction ToolKind = iota
	ToolLocalShell
	ToolShell
	ToolApplyPatch
	ToolMCP
	ToolComputer
	ToolWebSearch
	ToolFileSearch
	ToolCodeInterpreter
)

// Response is the protocol-neutral representation of an inference response.
type Response struct {
	ID           string
	Model        string
	Content      []Content // assistant output (text, thinking, tool calls)
	FinishReason string    // "stop", "length", "tool_calls", "content_filter"
	Usage        Usage
	Created      int64 // unix timestamp; 0 = auto-generate
}

// Usage is token consumption metadata.
type Usage struct {
	Prompt     int64
	Completion int64
	Cached     int64
	Thought    int64
}

// StreamEvent is one incremental event in a streaming response.
type StreamEvent struct {
	Type         StreamEventType
	Delta        string           // text or thinking delta
	ToolCall     *ToolCall        // for tool_call events
	Usage        *Usage           // for usage events (usually final chunk)
	FinishReason string           // for done events
	WebSearch    *WebSearchResult // for web_search result events
}

// StreamEventType enumerates all streaming event variants.
type StreamEventType int

const (
	StreamTextDelta StreamEventType = iota
	StreamThinkingDelta
	StreamToolCallDelta
	StreamToolCallDone
	StreamUsage
	StreamDone
	StreamWebSearch
)
