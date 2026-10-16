package agent

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"

	"github.com/qiyuan2403/Q_RP_dsh/llm"
	"github.com/qiyuan2403/Q_RP_dsh/session"
	"github.com/qiyuan2403/Q_RP_dsh/tools"
)

// echoTool returns its input, so a tool round trip produces a known result.
func echoTool() tools.Tool {
	return tools.Tool{
		Name:        "echo",
		Description: "Echo the input.",
		Parameters:  json.RawMessage(`{"type":"object","properties":{"v":{"type":"string"}}}`),
		Run: func(_ context.Context, raw json.RawMessage) (any, error) {
			var in struct {
				V string `json:"v"`
			}
			_ = json.Unmarshal(raw, &in)
			return map[string]any{"echoed": in.V}, nil
		},
	}
}

// TestRebuildHistoryMatchesTheLiveHistory is the round-trip invariant: what the
// loop holds in memory after a turn must equal what the log replays. If these
// diverge, resuming a session replays a DIFFERENT conversation to the model,
// which is exactly the failure the session log exists to prevent.
//
// The turn includes a tool call, because that is where the reconstruction is
// easy to get wrong: the assistant message and its tool result are two events
// that must rejoin into the right roles and ids.
func TestRebuildHistoryMatchesTheLiveHistory(t *testing.T) {
	p := &scriptedProvider{responses: []llm.Response{
		{
			Text: "let me check",
			ToolCalls: []llm.ToolCall{
				{ID: "call-1", Name: "echo", Arguments: `{"v":"hi"}`},
			},
		},
		{Text: "all done"},
	}}
	reg := tools.NewRegistry()
	if err := reg.Register(echoTool()); err != nil {
		t.Fatalf("Register: %v", err)
	}
	a, dir := newAgent(t, p, reg)

	if _, err := a.RunTurn(context.Background(), "please echo"); err != nil {
		t.Fatalf("RunTurn: %v", err)
	}
	live := a.History()

	events, err := session.ReadLog(dir)
	if err != nil {
		t.Fatalf("ReadLog: %v", err)
	}
	rebuilt, err := RebuildHistory(events)
	if err != nil {
		t.Fatalf("RebuildHistory: %v", err)
	}

	if !reflect.DeepEqual(live, rebuilt) {
		t.Fatalf("replayed history differs from the live history\nlive:    %+v\nrebuilt: %+v", live, rebuilt)
	}
	if len(rebuilt) == 0 {
		t.Fatal("the rebuilt history is empty, so the comparison proved nothing")
	}
}

// TestRebuildHistorySkipsNonSurfaceEvents pins that the audit events do not
// become messages: only user, assistant, and tool results reach the model.
func TestRebuildHistorySkipsNonSurfaceEvents(t *testing.T) {
	events := []session.Event{
		{Type: "turn/start", Seq: 1, Data: json.RawMessage(`{}`)},
		{Type: "step/start", Seq: 2, Data: json.RawMessage(`{"step":0}`)},
		{Type: "user/message", Seq: 3, Data: json.RawMessage(`{"text":"hi"}`)},
		{Type: "step/end", Seq: 4, Data: json.RawMessage(`{"step":0}`)},
		{Type: "turn/end", Seq: 5, Data: json.RawMessage(`{"reason":"completed"}`)},
	}
	got, err := RebuildHistory(events)
	if err != nil {
		t.Fatalf("RebuildHistory: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d messages, want 1: %+v", len(got), got)
	}
	if got[0].Role != llm.RoleUser || got[0].Text != "hi" {
		t.Fatalf("message = %+v, want the user message", got[0])
	}
}

// TestRebuildHistoryReconstructsAToolError pins that a failed tool call comes
// back as the same text the model saw, so a resumed session does not silently
// turn an error into a success.
func TestRebuildHistoryReconstructsAToolError(t *testing.T) {
	events := []session.Event{
		{Type: "assistant/message", Seq: 1, Data: json.RawMessage(
			`{"text":"","toolCalls":[{"id":"c1","name":"bash","arguments":"{}"}]}`)},
		{Type: "tool/result", Seq: 2, Data: json.RawMessage(
			`{"callId":"c1","name":"bash","output":null,"error":"boom"}`)},
	}
	got, err := RebuildHistory(events)
	if err != nil {
		t.Fatalf("RebuildHistory: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d messages, want 2", len(got))
	}
	if got[1].Role != llm.RoleTool || got[1].ToolCallID != "c1" {
		t.Fatalf("tool message = %+v", got[1])
	}
	if got[1].Text != "error: boom" {
		t.Fatalf("tool text = %q, want the error text", got[1].Text)
	}
}

// TestSeedReplacesHistory pins that resuming a session installs the replayed
// history rather than appending to an empty one.
func TestSeedReplacesHistory(t *testing.T) {
	p := &scriptedProvider{responses: []llm.Response{{Text: "ok"}}}
	a, _ := newAgent(t, p, nil)

	a.Seed([]llm.Message{
		{Role: llm.RoleUser, Text: "earlier"},
		{Role: llm.RoleAssistant, Text: "reply"},
	})
	got := a.History()
	if len(got) != 2 || got[0].Text != "earlier" {
		t.Fatalf("History = %+v, want the seeded messages", got)
	}

	// The seeded history must be copied, not aliased.
	got[0].Text = "mutated"
	if a.History()[0].Text != "earlier" {
		t.Fatal("History returned a slice that aliases the agent's own")
	}
}
