package agent

import (
	"encoding/json"
	"fmt"

	"github.com/qiyuan2403/Q_RP_dsh/llm"
	"github.com/qiyuan2403/Q_RP_dsh/session"
)

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
func RebuildHistory(events []session.Event) ([]llm.Message, error) {
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
				Error  string          `json:"error"`
			}
			if err := json.Unmarshal(e.Data, &data); err != nil {
				return nil, fmt.Errorf("agent: tool/result at seq %d: %w", e.Seq, err)
			}
			// Mirror toolResultText exactly: an error becomes its message, and
			// an output is the JSON the tool returned.
			text := ""
			if data.Error != "" {
				text = "error: " + data.Error
			} else {
				text = string(data.Output)
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
