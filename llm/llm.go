// Package llm defines the model-provider contract.
//
// Providers may expose incremental output through StreamProvider while keeping
// Provider for callers that need one completed response.
package llm

import (
	"context"
	"encoding/json"
)

// Role is a message's author.
type Role string

const (
	// RoleSystem is the system instruction.
	RoleSystem Role = "system"
	// RoleUser is the human's message.
	RoleUser Role = "user"
	// RoleAssistant is the model's message.
	RoleAssistant Role = "assistant"
	// RoleTool is a tool result.
	RoleTool Role = "tool"
)

// ToolCall is one tool invocation the model requested.
type ToolCall struct {
	// ID keys the call, so its result can be matched to it.
	ID string `json:"id"`
	// Name is the tool's registered name.
	Name string `json:"name"`
	// Arguments is the raw JSON argument object.
	Arguments string `json:"arguments"`
}

// Message is one model-visible message.
type Message struct {
	Role Role `json:"role"`
	// Text is the message body. An assistant message that only requests tools
	// may carry an empty body, which is why this is not omitted on the wire.
	Text string `json:"text,omitempty"`
	// ToolCalls are the calls an assistant message requested.
	ToolCalls []ToolCall `json:"toolCalls,omitempty"`
	// ToolCallID keys a tool result to the call it answers.
	ToolCallID string `json:"toolCallId,omitempty"`
}

// ToolSpec describes one available tool to the model.
type ToolSpec struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	// Parameters is a JSON Schema object.
	Parameters json.RawMessage `json:"parameters"`
}

// Request is one model call.
type Request struct {
	Model    string
	System   string
	Messages []Message
	Tools    []ToolSpec
}

// Response is one model reply.
type Response struct {
	// Text is the assistant's prose.
	Text string
	// ToolCalls are the calls the model requested. An empty slice means the
	// model is done and the turn can complete.
	ToolCalls []ToolCall
	// Usage reports token accounting, when the provider supplies it.
	Usage Usage
}

// Usage is token accounting for one call.
type Usage struct {
	PromptTokens     int
	CompletionTokens int
	TotalTokens      int
}

// Provider performs model calls.
type Provider interface {
	Complete(ctx context.Context, req Request) (Response, error)
}

// StreamChunk is one provider-neutral incremental response fragment.
type StreamChunk struct {
	// TextDelta appends visible assistant text.
	TextDelta string
	// ToolCallDelta identifies one tool-call fragment. ArgumentsDelta is
	// concatenated in order; ID and Name may be supplied only on its first part.
	ToolCallDelta *ToolCallDelta
	// Usage is emitted by providers that report final token accounting.
	Usage *Usage
}

// ToolCallDelta is a partial function call emitted by a streaming provider.
type ToolCallDelta struct {
	Index          int
	ID             string
	Name           string
	ArgumentsDelta string
}

// StreamProvider performs a model call and reports ordered response fragments.
// The callback runs synchronously on the provider's calling goroutine.
type StreamProvider interface {
	Stream(ctx context.Context, req Request, yield func(StreamChunk) error) error
}
