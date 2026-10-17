// Package agent runs the turn/step loop.
//
// The loop's shape is fixed by the official harness:
//
//	A TURN is one user request. Within it, STEPS repeat until the model stops
//	asking for tools. A step is one model call plus the tool calls it requests.
//
// Every transition is written to the session log before its effect is reported,
// so a crash mid-turn leaves a log that says exactly how far the turn got.
package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/qiyuan2403/Q_RP_dsh/llm"
	"github.com/qiyuan2403/Q_RP_dsh/session"
	"github.com/qiyuan2403/Q_RP_dsh/tools"
)

// Errors the loop reports.
var (
	// ErrBusy reports a second turn started while one is running.
	ErrBusy = errors.New("agent: a turn is already running")
	// ErrNoModel reports a turn started without a configured model.
	ErrNoModel = errors.New("agent: no model configured")
)

// Config describes one agent.
type Config struct {
	// Provider performs model calls.
	Provider llm.Provider
	// Registry holds the tools the model may call.
	Registry *tools.Registry
	// SystemPrompt is the composed system instruction.
	SystemPrompt string
	// Model is the model id sent to the provider.
	Model string
	// MaxSteps bounds the steps in one turn.
	//
	// A model that keeps requesting tools forever would otherwise burn tokens
	// without bound; the cap turns that into a reported stop reason.
	MaxSteps int
}

// Agent is one conversation.
type Agent struct {
	cfg Config
	log *session.Log

	mu      sync.Mutex
	running bool
	// history is the model-visible message list, rebuilt from the log.
	history []llm.Message
}

// New builds an agent over an open session log.
func New(cfg Config, log *session.Log) *Agent {
	if cfg.MaxSteps <= 0 {
		cfg.MaxSteps = 32
	}
	return &Agent{cfg: cfg, log: log}
}

// StopReason explains why a turn ended.
type StopReason string

const (
	// StopCompleted: the model produced a final answer with no tool calls.
	StopCompleted StopReason = "completed"
	// StopMaxSteps: the step cap was reached.
	StopMaxSteps StopReason = "max-steps"
	// StopCancelled: the caller's context was cancelled.
	StopCancelled StopReason = "cancelled"
	// StopModelError: the provider failed.
	StopModelError StopReason = "model-error"
)

// TurnResult is the outcome of one turn.
type TurnResult struct {
	// Reason explains why the turn ended.
	Reason StopReason
	// Text is the final assistant text, when the turn completed.
	Text string
	// Steps is how many model calls the turn made.
	Steps int
	// Err carries the underlying failure for the error stop reasons.
	Err error
}

// RunTurn appends the user message and drives the loop to completion.
//
// The turn bracket is written to the log FIRST (`turn/start`), so a crash
// during the turn leaves a record that the turn began. The matching
// `turn/end` carries the stop reason.
func (a *Agent) RunTurn(ctx context.Context, userText string) (TurnResult, error) {
	a.mu.Lock()
	if a.running {
		a.mu.Unlock()
		return TurnResult{}, ErrBusy
	}
	a.running = true
	a.mu.Unlock()
	defer func() {
		a.mu.Lock()
		a.running = false
		a.mu.Unlock()
	}()

	if a.cfg.Provider == nil {
		return TurnResult{}, ErrNoModel
	}

	if _, err := a.log.Append(session.Event{
		Type: "user/message",
		Data: mustJSON(map[string]any{"text": userText}),
	}); err != nil {
		return TurnResult{}, err
	}
	a.history = append(a.history, llm.Message{Role: llm.RoleUser, Text: userText})

	turnStart, err := a.log.Append(session.Event{Type: "turn/start", Data: mustJSON(map[string]any{})})
	if err != nil {
		return TurnResult{}, err
	}

	result := a.drive(ctx)

	endData := map[string]any{"reason": string(result.Reason), "steps": result.Steps}
	if result.Err != nil {
		endData["error"] = result.Err.Error()
	}
	if _, err := a.log.Append(session.Event{
		Type: "turn/end",
		Data: mustJSON(endData),
	}); err != nil {
		return result, err
	}
	// Durability at the turn boundary: a caller told the turn finished must be
	// able to find it in the log after a crash.
	if err := a.log.Flush(); err != nil {
		return result, err
	}
	_ = turnStart
	return result, nil
}

// drive runs the step loop.
func (a *Agent) drive(ctx context.Context) TurnResult {
	for step := 0; step < a.cfg.MaxSteps; step++ {
		if err := ctx.Err(); err != nil {
			return TurnResult{Reason: StopCancelled, Steps: step, Err: err}
		}
		stepStart, err := a.log.Append(session.Event{
			Type: "step/start",
			Data: mustJSON(map[string]any{"step": step}),
		})
		if err != nil {
			return TurnResult{Reason: StopModelError, Steps: step, Err: err}
		}
		_ = stepStart

		resp, err := a.callModel(ctx)
		if err != nil {
			return TurnResult{Reason: StopModelError, Steps: step + 1, Err: err}
		}

		// Record the assistant turn before running its tools: the log must show
		// what the model asked for even if a tool then fails.
		if _, err := a.log.Append(session.Event{
			Type: "assistant/message",
			Data: mustJSON(map[string]any{
				"text":      resp.Text,
				"toolCalls": toolCallsJSON(resp.ToolCalls),
			}),
		}); err != nil {
			return TurnResult{Reason: StopModelError, Steps: step + 1, Err: err}
		}
		a.history = append(a.history, llm.Message{
			Role:      llm.RoleAssistant,
			Text:      resp.Text,
			ToolCalls: resp.ToolCalls,
		})

		if len(resp.ToolCalls) == 0 {
			if _, err := a.log.Append(session.Event{
				Type: "step/end",
				Data: mustJSON(map[string]any{"step": step, "toolCalls": 0}),
			}); err != nil {
				return TurnResult{Reason: StopModelError, Steps: step + 1, Err: err}
			}
			return TurnResult{Reason: StopCompleted, Text: resp.Text, Steps: step + 1}
		}

		for _, call := range resp.ToolCalls {
			if _, err := a.log.Append(session.Event{
				Type: "tool/call",
				Data: mustJSON(map[string]any{
					"callId":    call.ID,
					"name":      call.Name,
					"arguments": call.Arguments,
				}),
			}); err != nil {
				return TurnResult{Reason: StopModelError, Steps: step + 1, Err: err}
			}

			tool, out, callErr := a.runTool(ctx, call)
			text := toolResultText(out, callErr, tool.Render)
			// The model-visible text is logged WITH the structured output, as the
			// official harness does. Replay cannot always re-derive it: Render
			// depends on the registration's current configuration and the tool
			// version, and the log is supposed to be the durable record of what
			// the model saw, not a recipe for recomputing it.
			resultData := map[string]any{
				"callId": call.ID,
				"name":   call.Name,
				"output": safeJSONValue(out),
				"text":   text,
			}
			if callErr != nil {
				resultData["error"] = callErr.Error()
			}
			if _, err := a.log.Append(session.Event{
				Type: "tool/result",
				Data: mustJSON(resultData),
			}); err != nil {
				return TurnResult{Reason: StopModelError, Steps: step + 1, Err: err}
			}
			a.history = append(a.history, llm.Message{
				Role:       llm.RoleTool,
				ToolCallID: call.ID,
				Text:       text,
			})
		}

		if _, err := a.log.Append(session.Event{
			Type: "step/end",
			Data: mustJSON(map[string]any{"step": step, "toolCalls": len(resp.ToolCalls)}),
		}); err != nil {
			return TurnResult{Reason: StopModelError, Steps: step + 1, Err: err}
		}
	}
	return TurnResult{Reason: StopMaxSteps, Steps: a.cfg.MaxSteps}
}

func (a *Agent) callModel(ctx context.Context) (llm.Response, error) {
	req := llm.Request{
		Model:    a.cfg.Model,
		System:   a.cfg.SystemPrompt,
		Messages: a.history,
	}
	if a.cfg.Registry != nil {
		req.Tools = a.cfg.Registry.Specs()
	}
	if streamed, ok := a.cfg.Provider.(llm.StreamProvider); ok {
		return streamResponse(ctx, streamed, req)
	}
	return a.cfg.Provider.Complete(ctx, req)
}

func streamResponse(ctx context.Context, provider llm.StreamProvider, req llm.Request) (llm.Response, error) {
	var text strings.Builder
	toolCalls := make([]llm.ToolCall, 0)
	var usage llm.Usage
	err := provider.Stream(ctx, req, func(chunk llm.StreamChunk) error {
		text.WriteString(chunk.TextDelta)
		if chunk.Usage != nil {
			usage = *chunk.Usage
		}
		if chunk.ToolCallDelta == nil {
			return nil
		}
		delta := chunk.ToolCallDelta
		if delta.Index < 0 {
			return fmt.Errorf("agent: stream returned negative tool-call index %d", delta.Index)
		}
		for len(toolCalls) <= delta.Index {
			toolCalls = append(toolCalls, llm.ToolCall{})
		}
		call := &toolCalls[delta.Index]
		if delta.ID != "" {
			call.ID = delta.ID
		}
		if delta.Name != "" {
			call.Name = delta.Name
		}
		call.Arguments += delta.ArgumentsDelta
		return nil
	})
	if err != nil {
		return llm.Response{}, err
	}
	for i := range toolCalls {
		if strings.TrimSpace(toolCalls[i].Arguments) == "" {
			toolCalls[i].Arguments = "{}"
		}
	}
	return llm.Response{Text: text.String(), ToolCalls: toolCalls, Usage: usage}, nil
}

// runTool dispatches one tool call and returns the resolved definition.
//
// A tool error is returned rather than swallowed: it becomes the tool result
// the model sees, which is how the model learns the call failed. The definition
// travels back with the result because the caller needs its Render hook: the
// log keeps the structured value, the model gets its rendered form. A
// definition resolved here is also the one whose schema validated the
// arguments, so the caller cannot render with a tool the call did not use.
func (a *Agent) runTool(ctx context.Context, call llm.ToolCall) (tools.Tool, any, error) {
	if a.cfg.Registry == nil {
		return tools.Tool{}, nil, fmt.Errorf("agent: no tools registered")
	}
	tool, ok := a.cfg.Registry.Lookup(call.Name)
	if !ok {
		return tools.Tool{}, nil, fmt.Errorf("agent: unknown tool %q", call.Name)
	}
	args := json.RawMessage(call.Arguments)
	if err := tools.Validate(tool, args); err != nil {
		return tool, nil, err
	}
	out, err := tool.Run(ctx, args)
	return tool, out, err
}

// History returns the model-visible messages, for inspection and tests.
func (a *Agent) History() []llm.Message {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]llm.Message(nil), a.history...)
}

// toolResultText is the text the model sees for one tool call.
//
// render is the tool's optional Render hook. It applies only to a successful
// result: an error keeps the plain "error: <msg>" shape, because that is what a
// resumed session reconstructs from the log as well, and rendering a partial
// value after a failure would hide that the call failed. A nil render keeps the
// historical JSON encoding.
func toolResultText(out any, err error, render func(any) string) string {
	if err != nil {
		return fmt.Sprintf("error: %v", err)
	}
	if render != nil {
		return render(out)
	}
	raw, mErr := json.Marshal(out)
	if mErr != nil {
		return fmt.Sprintf("%v", out)
	}
	return string(raw)
}

func toolCallsJSON(calls []llm.ToolCall) []map[string]any {
	out := make([]map[string]any, 0, len(calls))
	for _, c := range calls {
		out = append(out, map[string]any{
			"id":        c.ID,
			"name":      c.Name,
			"arguments": c.Arguments,
		})
	}
	return out
}

// safeJSONValue encodes a tool result for the session log without failing.
//
// A value the JSON encoder rejects (NaN, a function, a channel) must not lose
// the whole tool/result event: that would erase the call from the durable
// record and leave replay with a shorter conversation than the model had.
// Unencodable values are stored as their printed string, which keeps the event
// JSON-valid and keeps the logged model text alongside it authoritative.
func safeJSONValue(v any) json.RawMessage {
	raw, err := json.Marshal(v)
	if err == nil {
		return raw
	}
	fallback, fallbackErr := json.Marshal(fmt.Sprint(v))
	if fallbackErr == nil {
		return fallback
	}
	return json.RawMessage("null")
}

func mustJSON(v any) json.RawMessage {
	raw, err := json.Marshal(v)
	if err != nil {
		return json.RawMessage("null")
	}
	return raw
}
