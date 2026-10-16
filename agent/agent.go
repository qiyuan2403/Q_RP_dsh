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

			out, callErr := a.runTool(ctx, call)
			resultData := map[string]any{"callId": call.ID, "name": call.Name, "output": out}
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
				Text:       toolResultText(out, callErr),
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
	return a.cfg.Provider.Complete(ctx, req)
}

// runTool dispatches one tool call.
//
// A tool error is returned rather than swallowed: it becomes the tool result
// the model sees, which is how the model learns the call failed.
func (a *Agent) runTool(ctx context.Context, call llm.ToolCall) (any, error) {
	if a.cfg.Registry == nil {
		return nil, fmt.Errorf("agent: no tools registered")
	}
	tool, ok := a.cfg.Registry.Lookup(call.Name)
	if !ok {
		return nil, fmt.Errorf("agent: unknown tool %q", call.Name)
	}
	args := json.RawMessage(call.Arguments)
	if err := tools.Validate(tool, args); err != nil {
		return nil, err
	}
	return tool.Run(ctx, args)
}

// History returns the model-visible messages, for inspection and tests.
func (a *Agent) History() []llm.Message {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]llm.Message(nil), a.history...)
}

func toolResultText(out any, err error) string {
	if err != nil {
		return fmt.Sprintf("error: %v", err)
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

func mustJSON(v any) json.RawMessage {
	raw, err := json.Marshal(v)
	if err != nil {
		return json.RawMessage("null")
	}
	return raw
}
