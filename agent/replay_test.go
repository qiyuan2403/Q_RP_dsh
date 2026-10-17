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

// registryRender resolves Render hooks from a registry the way a caller that
// mounts the tools would. The lookup, not the registry, is what RebuildHistory
// takes: replaying a log must not require every tool.
func registryRender(reg *tools.Registry) RenderLookup {
	return func(name string) func(value any) string {
		tool, ok := reg.Lookup(name)
		if !ok {
			return nil
		}
		return tool.Render
	}
}

// renderedEcho is the Go type a live tool returns. Its JSON form is an object,
// so the replayed value arrives as map[string]any instead; a Render that obeys
// the tools.Tool contract must produce the same text for both.
type renderedEcho struct {
	Echoed string `json:"echoed"`
}

func renderEcho(value any) string {
	switch v := value.(type) {
	case renderedEcho:
		return "echoed: " + v.Echoed
	case map[string]any:
		echoed, _ := v["echoed"].(string)
		return "echoed: " + echoed
	default:
		return "echoed: <unknown>"
	}
}

// TestRebuildHistoryAppliesRender is the round-trip invariant with a Render
// hook: the live history holds rendered text, so the rebuilt history must hold
// exactly the same text rather than the structured JSON the log stores.
func TestRebuildHistoryAppliesRender(t *testing.T) {
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
	tool := echoTool()
	tool.Run = func(_ context.Context, raw json.RawMessage) (any, error) {
		var in struct {
			V string `json:"v"`
		}
		_ = json.Unmarshal(raw, &in)
		return renderedEcho{Echoed: in.V}, nil
	}
	tool.Render = renderEcho
	if err := reg.Register(tool); err != nil {
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
	rebuilt, err := RebuildHistory(events, registryRender(reg))
	if err != nil {
		t.Fatalf("RebuildHistory: %v", err)
	}

	if !reflect.DeepEqual(live, rebuilt) {
		t.Fatalf("replayed history differs from the live history\nlive:    %+v\nrebuilt: %+v", live, rebuilt)
	}

	var toolText string
	for _, m := range rebuilt {
		if m.Role == llm.RoleTool {
			toolText = m.Text
		}
	}
	if toolText != "echoed: hi" {
		t.Fatalf("tool text = %q, want the rendered value", toolText)
	}
	if toolText == `{"echoed":"hi"}` {
		t.Fatal("replay used the raw JSON, not the tool's Render output")
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
	rebuilt, err := RebuildHistory(events, registryRender(reg))
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
	got, err := RebuildHistory(events, nil)
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
	rendered := false
	lookup := func(string) func(any) string {
		return func(any) string {
			rendered = true
			return "rendered"
		}
	}
	got, err := RebuildHistory(events, lookup)
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
	if rendered {
		t.Fatal("Render was consulted for a failed tool call, but an error keeps the plain error text")
	}
}

// TestRebuildHistoryFallsBackToJSONWithoutRender pins the compatibility path:
// a caller with no tool definition, or one whose registry lacks the tool that
// produced the event, still gets the exact JSON the log recorded. Losing that
// fallback would make an old session unreadable whenever its tools are gone.
func TestRebuildHistoryFallsBackToJSONWithoutRender(t *testing.T) {
	events := []session.Event{
		{Type: "tool/result", Seq: 1, Data: json.RawMessage(
			`{"callId":"c1","name":"echo","output":{"echoed":"hi"}}`)},
	}
	cases := []struct {
		name   string
		render RenderLookup
	}{
		{"nil lookup", nil},
		{"unknown tool", func(string) func(any) string { return nil }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := RebuildHistory(events, tc.render)
			if err != nil {
				t.Fatalf("RebuildHistory: %v", err)
			}
			if len(got) != 1 {
				t.Fatalf("got %d messages, want 1", len(got))
			}
			if want := `{"echoed":"hi"}`; got[0].Text != want {
				t.Fatalf("tool text = %q, want the raw JSON %q", got[0].Text, want)
			}
		})
	}
}

// TestRebuildHistoryFallsBackWhenOutputCannotDecode pins that an output value
// the hook cannot receive does not fail the resume: the raw JSON is used and
// Render is skipped. 1e999 is syntactically valid JSON but overflows a float64,
// which is exactly the shape of a log value that has no Go representation.
func TestRebuildHistoryFallsBackWhenOutputCannotDecode(t *testing.T) {
	cases := []struct {
		name   string
		data   string
		output string
	}{
		{"undecodable number", `{"callId":"c1","name":"echo","output":1e999}`, "1e999"},
		{"missing output", `{"callId":"c1","name":"echo"}`, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rendered := false
			lookup := func(string) func(any) string {
				return func(any) string {
					rendered = true
					return "rendered"
				}
			}
			got, err := RebuildHistory([]session.Event{
				{Type: "tool/result", Seq: 1, Data: json.RawMessage(tc.data)},
			}, lookup)
			if err != nil {
				t.Fatalf("RebuildHistory: %v", err)
			}
			if len(got) != 1 {
				t.Fatalf("got %d messages, want 1", len(got))
			}
			if got[0].Text != tc.output {
				t.Fatalf("tool text = %q, want the raw output %q", got[0].Text, tc.output)
			}
			if rendered {
				t.Fatal("Render was consulted for an output that could not be decoded")
			}
		})
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

// TestRebuildHistoryKeepsAnEmptyErrorMessage pins the branch condition: the live
// loop treats a failed call as failed even when the error message is empty, so
// replay must branch on the presence of the error, not on its length. A string
// field would replay this as the output's JSON and silently change the
// conversation the model sees.
func TestRebuildHistoryKeepsAnEmptyErrorMessage(t *testing.T) {
	events := []session.Event{{
		Type: "tool/result",
		Seq:  1,
		Data: json.RawMessage(`{"callId":"call-1","name":"echo","output":null,"error":""}`),
	}}
	got, err := RebuildHistory(events, nil)
	if err != nil {
		t.Fatalf("RebuildHistory: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d messages, want 1", len(got))
	}
	if got[0].Text != "error: " {
		t.Fatalf("text = %q, want the failed-call text", got[0].Text)
	}
}
