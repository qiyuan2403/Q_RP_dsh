// Package llm defines the model-provider seam.
//
// The interface is deliberately small: one call that returns a reply, or an
// error. Streaming is a provider detail the loop does not need — the loop's
// unit of work is a complete assistant turn, because that is the unit the
// session log records.
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
