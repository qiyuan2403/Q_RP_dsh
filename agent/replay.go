package agent

import (
	"encoding/json"
	"fmt"

	"github.com/qiyuan2403/Q_RP_dsh/llm"
	"github.com/qiyuan2403/Q_RP_dsh/session"
)

// RenderLookup resolves a tool's model-facing projection by tool name.
//
// Rebuilding history must apply the SAME projection the live loop applied,
// otherwise a resumed session shows the model a different conversation than
// the one the log records. A nil return means the tool is unknown here, and
// the caller falls back to the log's raw JSON.
type RenderLookup func(toolName string) func(value any) string

// RebuildHistory derives the model-visible message list from a session log's
// events.
//
// It is the exact inverse of what the loop writes: the loop records an event
// and appends to its in-memory history, so a resumed agent must be able to
// recover the same history from the log alone. Getting this wrong is not
// cosmetic — a history that differs from the log's meaning replays a different
// conversation to the model, which is the failure mode the session log exists
// to prevent.
//
// Events that carry no model-visible content (step/start, turn/end, and the
// rest) are skipped: the log is an audit trail, and only the surface events
// contribute messages.
//
// render resolves the tool's Render hook by name. The live loop renders a tool
// result before putting it in history, and the log only keeps the structured
// value, so reconstruction must apply the same projection. Without it a
// resumed session shows the model JSON where the live one showed rendered
// text. It is a lookup rather than the registry itself so a caller holding
// only a log can rebuild without mounting every tool; nil means "no tool is
// known", which falls back to the raw JSON, as does a lookup miss or a value
// the log cannot decode.
func RebuildHistory(events []session.Event, render RenderLookup) ([]llm.Message, error) {
	var out []llm.Message
	for _, e := range events {
		switch e.Type {
		case "user/message":
			var data struct {
				Text string `json:"text"`
			}
			if err := json.Unmarshal(e.Data, &data); err != nil {
				return nil, fmt.Errorf("agent: user/message at seq %d: %w", e.Seq, err)
			}
			out = append(out, llm.Message{Role: llm.RoleUser, Text: data.Text})

		case "assistant/message":
			var data struct {
				Text      string `json:"text"`
				ToolCalls []struct {
					ID        string `json:"id"`
					Name      string `json:"name"`
					Arguments string `json:"arguments"`
				} `json:"toolCalls"`
			}
			if err := json.Unmarshal(e.Data, &data); err != nil {
				return nil, fmt.Errorf("agent: assistant/message at seq %d: %w", e.Seq, err)
			}
			msg := llm.Message{Role: llm.RoleAssistant, Text: data.Text}
			for _, c := range data.ToolCalls {
				msg.ToolCalls = append(msg.ToolCalls, llm.ToolCall{
					ID:        c.ID,
					Name:      c.Name,
					Arguments: c.Arguments,
				})
			}
			out = append(out, msg)

		case "tool/result":
			var data struct {
				CallID string          `json:"callId"`
				Name   string          `json:"name"`
				Output json.RawMessage `json:"output"`
				// Text is the model-visible text the live loop logged. It is
				// authoritative when present: replay shows exactly what the model
				// saw instead of recomputing a projection that depends on the
				// current tool configuration and version.
				Text *string `json:"text"`
				// Error's presence marks a failed call; a pointer, because an
				// empty message is still a failure and the live loop branches on
				// err != nil, not on the message.
				Error *string `json:"error"`
			}
			if err := json.Unmarshal(e.Data, &data); err != nil {
				return nil, fmt.Errorf("agent: tool/result at seq %d: %w", e.Seq, err)
			}
			// Prefer the logged text. The legacy path exists for logs written
			// before the text field: mirror toolResultText there too (an error
			// becomes its message and never consults Render; a successful output
			// goes through the same Render the live loop used; anything else keeps
			// the raw logged JSON so a missing or unusable hook cannot make a
			// resume fail).
			text := ""
			switch {
			case data.Text != nil:
				text = *data.Text
			case data.Error != nil:
				text = "error: " + *data.Error
			default:
				text = string(data.Output)
				var hook func(value any) string
				if render != nil {
					hook = render(data.Name)
				}
				if hook != nil {
					var value any
					if err := json.Unmarshal(data.Output, &value); err == nil {
						text = hook(value)
					}
				}
			}
			out = append(out, llm.Message{
				Role:       llm.RoleTool,
				ToolCallID: data.CallID,
				Text:       text,
			})
		}
	}
	return out, nil
}

// Seed sets the initial history, for resuming a session opened from disk.
//
// It is separate from New because a fresh session has no history and a resumed
// one must not run a turn before its history is in place.
func (a *Agent) Seed(history []llm.Message) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.history = append([]llm.Message(nil), history...)
}
