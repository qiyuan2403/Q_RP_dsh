package agent

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/qiyuan2403/Q_RP_dsh/llm"
	"github.com/qiyuan2403/Q_RP_dsh/session"
	"github.com/qiyuan2403/Q_RP_dsh/tools"
)

// scriptedProvider replays a fixed sequence of responses.
type scriptedProvider struct {
	mu        sync.Mutex
	responses []llm.Response
	errs      []error
	calls     []llm.Request
}

func (p *scriptedProvider) Complete(_ context.Context, req llm.Request) (llm.Response, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls = append(p.calls, req)
	i := len(p.calls) - 1
	if i < len(p.errs) && p.errs[i] != nil {
		return llm.Response{}, p.errs[i]
	}
	if i >= len(p.responses) {
		return llm.Response{}, errors.New("scripted provider exhausted")
	}
	return p.responses[i], nil
}

func (p *scriptedProvider) requestCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.calls)
}

func newAgent(t *testing.T, provider llm.Provider, registry *tools.Registry) (*Agent, string) {
	t.Helper()
	dir := t.TempDir()
	log, err := session.Create(dir, session.Header{ID: "s", CreatedAt: 1})
	if err != nil {
		t.Fatalf("session.Create: %v", err)
	}
	t.Cleanup(func() { _ = log.Close() })
	a := New(Config{
		Provider:     provider,
		Registry:     registry,
		Model:        "test-model",
		SystemPrompt: "you are a test",
	}, log)
	return a, dir
}

// TestTurnCompletesWithoutTools pins the simplest path: one model call with no
// tool requests ends the turn.
func TestTurnCompletesWithoutTools(t *testing.T) {
	p := &scriptedProvider{responses: []llm.Response{{Text: "done"}}}
	a, dir := newAgent(t, p, nil)

	res, err := a.RunTurn(context.Background(), "hello")
	if err != nil {
		t.Fatalf("RunTurn: %v", err)
	}
	if res.Reason != StopCompleted {
		t.Fatalf("Reason = %q, want completed", res.Reason)
	}
	if res.Text != "done" {
		t.Fatalf("Text = %q", res.Text)
	}
	if res.Steps != 1 {
		t.Fatalf("Steps = %d, want 1", res.Steps)
	}

	// The log must bracket the turn and record the message.
	events, err := session.ReadLog(dir)
	if err != nil {
		t.Fatalf("ReadLog: %v", err)
	}
	types := eventTypes(events)
	for _, want := range []string{"user/message", "turn/start", "step/start", "assistant/message", "step/end", "turn/end"} {
		if !contains(types, want) {
			t.Errorf("log is missing %q; got %v", want, types)
		}
	}
	// turn/start must precede turn/end.
	if indexOf(types, "turn/start") > indexOf(types, "turn/end") {
		t.Error("turn/end precedes turn/start")
	}
}

// TestTurnRunsToolsAndContinues pins the multi-step path: the model requests a
// tool, the tool runs, and the model is called again with the result.
func TestTurnRunsToolsAndContinues(t *testing.T) {
	registry := tools.NewRegistry()
	ran := false
	if err := registry.Register(tools.Tool{
		Name:        "echo",
		Description: "echo the input",
		Parameters:  json.RawMessage(`{"type":"object","properties":{"text":{"type":"string"}},"required":["text"]}`),
		Run: func(_ context.Context, args json.RawMessage) (any, error) {
			ran = true
			var in struct {
				Text string `json:"text"`
			}
			_ = json.Unmarshal(args, &in)
			return map[string]any{"echoed": in.Text}, nil
		},
	}); err != nil {
		t.Fatalf("Register: %v", err)
	}

	p := &scriptedProvider{responses: []llm.Response{
		{Text: "", ToolCalls: []llm.ToolCall{{ID: "c1", Name: "echo", Arguments: `{"text":"hi"}`}}},
		{Text: "final answer"},
	}}
	a, dir := newAgent(t, p, registry)

	res, err := a.RunTurn(context.Background(), "use the tool")
	if err != nil {
		t.Fatalf("RunTurn: %v", err)
	}
	if res.Reason != StopCompleted {
		t.Fatalf("Reason = %q, want completed", res.Reason)
	}
	if res.Steps != 2 {
		t.Fatalf("Steps = %d, want 2", res.Steps)
	}
	if res.Text != "final answer" {
		t.Fatalf("Text = %q", res.Text)
	}
	if !ran {
		t.Fatal("the tool did not run")
	}

	// The second request must carry the tool result back to the model.
	p.mu.Lock()
	second := p.calls[1]
	p.mu.Unlock()
	found := false
	for _, m := range second.Messages {
		if m.Role == llm.RoleTool && m.ToolCallID == "c1" && strings.Contains(m.Text, "hi") {
			found = true
		}
	}
	if !found {
		t.Fatalf("the tool result was not sent back: %+v", second.Messages)
	}

	events, _ := session.ReadLog(dir)
	types := eventTypes(events)
	for _, want := range []string{"tool/call", "tool/result"} {
		if !contains(types, want) {
			t.Errorf("log is missing %q; got %v", want, types)
		}
	}
}

// TestToolRenderShapesModelVisibleText pins the two audiences of one result:
// the model reads the tool's rendered text while the session log keeps the
// structured value. A tool that renders must not force the model to parse JSON,
// and rendering must not change what is persisted.
func TestToolRenderShapesModelVisibleText(t *testing.T) {
	const rendered = "Sources:\n- [Example](https://example.com)"
	registry := tools.NewRegistry()
	if err := registry.Register(tools.Tool{
		Name:        "search",
		Description: "search the web",
		Parameters:  json.RawMessage(`{"type":"object","properties":{}}`),
		Run: func(context.Context, json.RawMessage) (any, error) {
			return map[string]any{
				"sources": []map[string]any{{"url": "https://example.com", "title": "Example"}},
			}, nil
		},
		Render: func(value any) string {
			if _, ok := value.(map[string]any); !ok {
				t.Errorf("Render got %T, want the structured value", value)
			}
			return rendered
		},
	}); err != nil {
		t.Fatalf("Register(search): %v", err)
	}
	if err := registry.Register(tools.Tool{
		Name:       "plain",
		Parameters: json.RawMessage(`{"type":"object","properties":{}}`),
		Run: func(context.Context, json.RawMessage) (any, error) {
			return map[string]any{"raw": true}, nil
		},
	}); err != nil {
		t.Fatalf("Register(plain): %v", err)
	}

	p := &scriptedProvider{responses: []llm.Response{
		{ToolCalls: []llm.ToolCall{
			{ID: "c1", Name: "search", Arguments: `{}`},
			{ID: "c2", Name: "plain", Arguments: `{}`},
		}},
		{Text: "done"},
	}}
	a, dir := newAgent(t, p, registry)
	if _, err := a.RunTurn(context.Background(), "go"); err != nil {
		t.Fatalf("RunTurn: %v", err)
	}

	p.mu.Lock()
	second := p.calls[1]
	p.mu.Unlock()
	toolText := map[string]string{}
	for _, m := range second.Messages {
		if m.Role == llm.RoleTool {
			toolText[m.ToolCallID] = m.Text
		}
	}
	if got := toolText["c1"]; got != rendered {
		t.Fatalf("rendered tool text = %q, want %q", got, rendered)
	}
	if strings.Contains(toolText["c1"], `"sources"`) {
		t.Fatalf("the model still received the structured JSON: %q", toolText["c1"])
	}
	// A tool without a hook keeps the historical JSON encoding.
	if got := toolText["c2"]; got != `{"raw":true}` {
		t.Fatalf("JSON tool text = %q", got)
	}

	// The log keeps the structured value the tool returned, not the render.
	events, err := session.ReadLog(dir)
	if err != nil {
		t.Fatalf("ReadLog: %v", err)
	}
	var sawSearch struct{ Output json.RawMessage }
	for _, e := range events {
		if e.Type != "tool/result" {
			continue
		}
		var data struct {
			CallID string          `json:"callId"`
			Output json.RawMessage `json:"output"`
		}
		if err := json.Unmarshal(e.Data, &data); err != nil {
			t.Fatalf("tool/result at seq %d: %v", e.Seq, err)
		}
		if data.CallID == "c1" {
			sawSearch.Output = data.Output
		}
	}
	if len(sawSearch.Output) == 0 {
		t.Fatal("the search tool result was not logged")
	}
	var output map[string]any
	if err := json.Unmarshal(sawSearch.Output, &output); err != nil {
		t.Fatalf("logged output is not structured JSON: %v (%s)", err, sawSearch.Output)
	}
	if _, ok := output["sources"]; !ok {
		t.Fatalf("logged output lost its structure: %s", sawSearch.Output)
	}
}

// TestToolFailureBecomesAResult pins that a failing tool does not abort the
// turn: the error is reported to the model, which is how it learns the call
// failed.
func TestToolFailureBecomesAResult(t *testing.T) {
	registry := tools.NewRegistry()
	if err := registry.Register(tools.Tool{
		Name:       "boom",
		Parameters: json.RawMessage(`{"type":"object","properties":{}}`),
		Run: func(context.Context, json.RawMessage) (any, error) {
			return nil, errors.New("exploded")
		},
	}); err != nil {
		t.Fatalf("Register: %v", err)
	}

	p := &scriptedProvider{responses: []llm.Response{
		{ToolCalls: []llm.ToolCall{{ID: "c1", Name: "boom", Arguments: `{}`}}},
		{Text: "recovered"},
	}}
	a, dir := newAgent(t, p, registry)

	res, err := a.RunTurn(context.Background(), "go")
	if err != nil {
		t.Fatalf("RunTurn: %v", err)
	}
	if res.Reason != StopCompleted {
		t.Fatalf("Reason = %q, want the turn to continue past the failure", res.Reason)
	}

	events, _ := session.ReadLog(dir)
	var sawError bool
	for _, e := range events {
		if e.Type == "tool/result" && strings.Contains(string(e.Data), "exploded") {
			sawError = true
		}
	}
	if !sawError {
		t.Fatal("the tool error was not recorded in the log")
	}
}

// TestToolRenderIsNotUsedForErrors pins that a failed call keeps the plain
// error text even when the tool has a Render hook: rendering a partial value
// would hide from the model that the call failed.
func TestToolRenderIsNotUsedForErrors(t *testing.T) {
	registry := tools.NewRegistry()
	if err := registry.Register(tools.Tool{
		Name:       "boom",
		Parameters: json.RawMessage(`{"type":"object","properties":{}}`),
		Run: func(context.Context, json.RawMessage) (any, error) {
			return map[string]any{"partial": true}, errors.New("exploded")
		},
		Render: func(any) string {
			t.Error("Render was called for a failed call")
			return "rendered"
		},
	}); err != nil {
		t.Fatalf("Register: %v", err)
	}

	p := &scriptedProvider{responses: []llm.Response{
		{ToolCalls: []llm.ToolCall{{ID: "c1", Name: "boom", Arguments: `{}`}}},
		{Text: "recovered"},
	}}
	a, _ := newAgent(t, p, registry)
	if _, err := a.RunTurn(context.Background(), "go"); err != nil {
		t.Fatalf("RunTurn: %v", err)
	}

	p.mu.Lock()
	second := p.calls[1]
	p.mu.Unlock()
	var text string
	for _, m := range second.Messages {
		if m.Role == llm.RoleTool && m.ToolCallID == "c1" {
			text = m.Text
		}
	}
	if text != "error: exploded" {
		t.Fatalf("tool text = %q, want the error text", text)
	}
}

// TestToolResultText pins the three text paths: a hook replaces JSON encoding,
// no hook falls back to JSON, an error wins over both, and a value that cannot
// be marshalled falls back to %v rather than an empty result.
func TestToolResultText(t *testing.T) {
	render := func(any) string { return "rendered" }
	cases := []struct {
		name     string
		out      any
		err      error
		renderFn func(any) string
		want     string
	}{
		{name: "render hook", out: map[string]any{"a": 1}, renderFn: render, want: "rendered"},
		{name: "json fallback", out: map[string]any{"a": 1}, want: `{"a":1}`},
		{name: "error wins over render", out: map[string]any{"a": 1}, err: errors.New("boom"), renderFn: render, want: "error: boom"},
		{name: "unmarshalable value", out: math.NaN(), want: "NaN"},
	}
	for _, tc := range cases {
		if got := toolResultText(tc.out, tc.err, tc.renderFn); got != tc.want {
			t.Errorf("%s: text = %q, want %q", tc.name, got, tc.want)
		}
	}
}

// TestUnknownToolIsReported pins that a hallucinated tool name becomes a result
// the model can act on rather than a panic.
func TestUnknownToolIsReported(t *testing.T) {
	p := &scriptedProvider{responses: []llm.Response{
		{ToolCalls: []llm.ToolCall{{ID: "c1", Name: "does-not-exist", Arguments: `{}`}}},
		{Text: "ok"},
	}}
	a, _ := newAgent(t, p, tools.NewRegistry())

	res, err := a.RunTurn(context.Background(), "go")
	if err != nil {
		t.Fatalf("RunTurn: %v", err)
	}
	if res.Reason != StopCompleted {
		t.Fatalf("Reason = %q", res.Reason)
	}
}

// TestInvalidToolArgumentsAreRefusedBeforeRunning pins that a model's malformed
// arguments do not reach the tool.
func TestInvalidToolArgumentsAreRefusedBeforeRunning(t *testing.T) {
	registry := tools.NewRegistry()
	ran := false
	if err := registry.Register(tools.Tool{
		Name:       "needs_text",
		Parameters: json.RawMessage(`{"type":"object","properties":{"text":{"type":"string"}},"required":["text"]}`),
		Run: func(context.Context, json.RawMessage) (any, error) {
			ran = true
			return "ok", nil
		},
	}); err != nil {
		t.Fatalf("Register: %v", err)
	}

	p := &scriptedProvider{responses: []llm.Response{
		{ToolCalls: []llm.ToolCall{{ID: "c1", Name: "needs_text", Arguments: `{}`}}},
		{Text: "fixed"},
	}}
	a, _ := newAgent(t, p, registry)
	if _, err := a.RunTurn(context.Background(), "go"); err != nil {
		t.Fatalf("RunTurn: %v", err)
	}
	if ran {
		t.Fatal("a tool ran with arguments that violate its schema")
	}
}

// TestMaxStepsStopsARunawayLoop pins the step cap: a model that keeps asking
// for tools stops with a reported reason instead of burning tokens forever.
func TestMaxStepsStopsARunawayLoop(t *testing.T) {
	registry := tools.NewRegistry()
	if err := registry.Register(tools.Tool{
		Name:       "loop",
		Parameters: json.RawMessage(`{"type":"object","properties":{}}`),
		Run:        func(context.Context, json.RawMessage) (any, error) { return "again", nil },
	}); err != nil {
		t.Fatalf("Register: %v", err)
	}

	// Always requests the tool.
	var responses []llm.Response
	for range 100 {
		responses = append(responses, llm.Response{
			ToolCalls: []llm.ToolCall{{ID: "c", Name: "loop", Arguments: `{}`}},
		})
	}
	p := &scriptedProvider{responses: responses}

	dir := t.TempDir()
	log, err := session.Create(dir, session.Header{ID: "s", CreatedAt: 1})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	defer log.Close()
	a := New(Config{Provider: p, Registry: registry, Model: "m", MaxSteps: 3}, log)

	res, err := a.RunTurn(context.Background(), "go")
	if err != nil {
		t.Fatalf("RunTurn: %v", err)
	}
	if res.Reason != StopMaxSteps {
		t.Fatalf("Reason = %q, want max-steps", res.Reason)
	}
	if res.Steps != 3 {
		t.Fatalf("Steps = %d, want 3", res.Steps)
	}
	if p.requestCount() != 3 {
		t.Fatalf("provider was called %d times, want 3", p.requestCount())
	}
}

// TestModelErrorIsReportedWithStopReason pins that a provider failure ends the
// turn with a reason the caller can distinguish from a normal completion.
func TestModelErrorIsReportedWithStopReason(t *testing.T) {
	p := &scriptedProvider{errs: []error{errors.New("connection refused")}}
	a, dir := newAgent(t, p, nil)

	res, err := a.RunTurn(context.Background(), "hello")
	if err != nil {
		t.Fatalf("RunTurn: %v", err)
	}
	if res.Reason != StopModelError {
		t.Fatalf("Reason = %q, want model-error", res.Reason)
	}
	if res.Err == nil || !strings.Contains(res.Err.Error(), "connection refused") {
		t.Fatalf("Err = %v", res.Err)
	}

	// The turn must still be closed in the log.
	events, _ := session.ReadLog(dir)
	if !contains(eventTypes(events), "turn/end") {
		t.Fatal("the failed turn was not closed in the log")
	}
}

// TestCancellationStopsTheTurn pins that a cancelled context ends the loop with
// the cancellation reason.
func TestCancellationStopsTheTurn(t *testing.T) {
	p := &scriptedProvider{responses: []llm.Response{{Text: "x"}}}
	a, _ := newAgent(t, p, nil)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	res, err := a.RunTurn(ctx, "hello")
	if err != nil {
		t.Fatalf("RunTurn: %v", err)
	}
	if res.Reason != StopCancelled {
		t.Fatalf("Reason = %q, want cancelled", res.Reason)
	}
}

// TestSecondTurnIsRefusedWhileBusy pins the single-flight guard.
func TestSecondTurnIsRefusedWhileBusy(t *testing.T) {
	block := make(chan struct{})
	p := newBlockingProvider(block)
	a, _ := newAgent(t, p, nil)

	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = a.RunTurn(context.Background(), "first")
	}()
	// Wait until the provider is in flight.
	<-p.entered

	if _, err := a.RunTurn(context.Background(), "second"); !errors.Is(err, ErrBusy) {
		t.Fatalf("error = %v, want ErrBusy", err)
	}
	close(block)
	<-done
}

// TestNoProviderIsRefused pins fail-loud on an unconfigured agent.
func TestNoProviderIsRefused(t *testing.T) {
	dir := t.TempDir()
	log, err := session.Create(dir, session.Header{ID: "s", CreatedAt: 1})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	defer log.Close()
	a := New(Config{}, log)
	if _, err := a.RunTurn(context.Background(), "x"); !errors.Is(err, ErrNoModel) {
		t.Fatalf("error = %v, want ErrNoModel", err)
	}
}

// TestSystemPromptAndToolsAreSentOnEveryCall pins that the request carries the
// composed system prompt and the registered tool specs.
func TestSystemPromptAndToolsAreSentOnEveryCall(t *testing.T) {
	registry := tools.NewRegistry()
	if err := registry.Register(tools.Tool{
		Name:        "t",
		Description: "a tool",
		Parameters:  json.RawMessage(`{"type":"object","properties":{}}`),
		Run:         func(context.Context, json.RawMessage) (any, error) { return nil, nil },
	}); err != nil {
		t.Fatalf("Register: %v", err)
	}
	p := &scriptedProvider{responses: []llm.Response{{Text: "ok"}}}
	a, _ := newAgent(t, p, registry)
	if _, err := a.RunTurn(context.Background(), "x"); err != nil {
		t.Fatalf("RunTurn: %v", err)
	}

	p.mu.Lock()
	defer p.mu.Unlock()
	req := p.calls[0]
	if req.System != "you are a test" {
		t.Fatalf("System = %q", req.System)
	}
	if len(req.Tools) != 1 || req.Tools[0].Name != "t" {
		t.Fatalf("Tools = %+v", req.Tools)
	}
	if req.Model != "test-model" {
		t.Fatalf("Model = %q", req.Model)
	}
}

// blockingProvider blocks until released, so the busy guard can be tested.
//
// `entered` is closed by the first call, which is how the test knows the turn
// is in flight before racing a second one at it.
type blockingProvider struct {
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func newBlockingProvider(release chan struct{}) *blockingProvider {
	return &blockingProvider{entered: make(chan struct{}), release: release}
}

func (p *blockingProvider) Complete(ctx context.Context, _ llm.Request) (llm.Response, error) {
	p.once.Do(func() { close(p.entered) })
	<-p.release
	return llm.Response{Text: "ok"}, nil
}

func eventTypes(events []session.Event) []string {
	out := make([]string, 0, len(events))
	for _, e := range events {
		out = append(out, e.Type)
	}
	return out
}

func contains(haystack []string, needle string) bool { return indexOf(haystack, needle) >= 0 }

func indexOf(haystack []string, needle string) int {
	for i, v := range haystack {
		if v == needle {
			return i
		}
	}
	return -1
}

type streamingProvider struct {
	chunks  []llm.StreamChunk
	streams [][]llm.StreamChunk
	called  bool
	index   int
}

func (p *streamingProvider) Complete(context.Context, llm.Request) (llm.Response, error) {
	return llm.Response{}, errors.New("Complete should not be called for a streaming provider")
}

func (p *streamingProvider) Stream(_ context.Context, _ llm.Request, yield func(llm.StreamChunk) error) error {
	p.called = true
	chunks := p.chunks
	if p.streams != nil {
		if p.index >= len(p.streams) {
			return errors.New("streaming provider exhausted")
		}
		chunks = p.streams[p.index]
		p.index++
	}
	for _, chunk := range chunks {
		if err := yield(chunk); err != nil {
			return err
		}
	}
	return nil
}

func TestTurnConsumesStreamingProvider(t *testing.T) {
	p := &streamingProvider{chunks: []llm.StreamChunk{
		{TextDelta: "hel"},
		{TextDelta: "lo"},
	}}
	a, _ := newAgent(t, p, nil)
	res, err := a.RunTurn(context.Background(), "hello")
	if err != nil {
		t.Fatalf("RunTurn: %v", err)
	}
	if !p.called {
		t.Fatal("Stream was not called")
	}
	if res.Reason != StopCompleted || res.Text != "hello" {
		t.Fatalf("result = %+v", res)
	}
}

func TestTurnAssemblesStreamingToolCall(t *testing.T) {
	registry := tools.NewRegistry()
	ran := false
	if err := registry.Register(tools.Tool{
		Name: "echo", Parameters: json.RawMessage(`{"type":"object","properties":{"text":{"type":"string"}},"required":["text"]}`),
		Run: func(_ context.Context, args json.RawMessage) (any, error) {
			ran = true
			var input struct {
				Text string `json:"text"`
			}
			if err := json.Unmarshal(args, &input); err != nil {
				return nil, err
			}
			return input.Text, nil
		},
	}); err != nil {
		t.Fatalf("Register: %v", err)
	}
	p := &streamingProvider{streams: [][]llm.StreamChunk{
		{
			{ToolCallDelta: &llm.ToolCallDelta{Index: 0, ID: "c1", Name: "echo", ArgumentsDelta: `{"text":"`}},
			{ToolCallDelta: &llm.ToolCallDelta{Index: 0, ArgumentsDelta: `hi"}`}},
		},
		{{TextDelta: "done"}},
	}}
	a, _ := newAgent(t, p, registry)
	res, err := a.RunTurn(context.Background(), "use echo")
	if err != nil {
		t.Fatalf("RunTurn: %v", err)
	}
	if !ran {
		t.Fatal("streamed tool call did not execute")
	}
	if res.Reason != StopCompleted || res.Text != "done" {
		t.Fatalf("result = %+v", res)
	}
}

// TestLoggedToolTextIsAuthoritativeOnReplay pins the durable record: the text
// the model saw is stored on the event, so replay must use it rather than
// recompute a Render whose result depends on the current tool configuration.
// The lookup deliberately returns a different projection to prove it is not the
// one being used.
func TestLoggedToolTextIsAuthoritativeOnReplay(t *testing.T) {
	registry := tools.NewRegistry()
	tool := tools.Tool{
		Name:        "value",
		Description: "returns a value",
		Parameters:  json.RawMessage(`{"type":"object","properties":{},"additionalProperties":false}`),
		Run: func(context.Context, json.RawMessage) (any, error) {
			return map[string]any{"n": 1}, nil
		},
		Render: func(any) string { return "rendered at run time" },
	}
	if err := registry.Register(tool); err != nil {
		t.Fatalf("Register: %v", err)
	}
	p := &scriptedProvider{responses: []llm.Response{
		{ToolCalls: []llm.ToolCall{{ID: "c1", Name: "value", Arguments: `{}`}}},
		{Text: "done"},
	}}
	a, dir := newAgent(t, p, registry)
	if _, err := a.RunTurn(context.Background(), "go"); err != nil {
		t.Fatalf("RunTurn: %v", err)
	}
	live := a.History()

	log, err := session.Open(dir)
	if err != nil {
		t.Fatalf("session.Open: %v", err)
	}
	defer log.Close()
	events, err := log.Events()
	if err != nil {
		t.Fatalf("Events: %v", err)
	}
	drifted := RenderLookup(func(string) func(any) string {
		return func(any) string { return "rendered NOW" }
	})
	rebuilt, err := RebuildHistory(events, drifted)
	if err != nil {
		t.Fatalf("RebuildHistory: %v", err)
	}
	if len(rebuilt) != len(live) {
		t.Fatalf("rebuilt %d messages, live %d", len(rebuilt), len(live))
	}
	for i := range live {
		if !reflect.DeepEqual(rebuilt[i], live[i]) {
			t.Fatalf("message %d differs:\nlive %+v\nrebuilt %+v", i, live[i], rebuilt[i])
		}
	}
}

// TestUnencodableToolOutputKeepsTheEvent pins the log's durability: a value the
// JSON encoder rejects must not drop the tool/result event or make the logged
// text disagree with the live history.
func TestUnencodableToolOutputKeepsTheEvent(t *testing.T) {
	registry := tools.NewRegistry()
	tool := tools.Tool{
		Name:        "nan",
		Description: "returns NaN",
		Parameters:  json.RawMessage(`{"type":"object","properties":{},"additionalProperties":false}`),
		Run: func(context.Context, json.RawMessage) (any, error) {
			return math.NaN(), nil
		},
	}
	if err := registry.Register(tool); err != nil {
		t.Fatalf("Register: %v", err)
	}
	p := &scriptedProvider{responses: []llm.Response{
		{ToolCalls: []llm.ToolCall{{ID: "c1", Name: "nan", Arguments: `{}`}}},
		{Text: "done"},
	}}
	a, dir := newAgent(t, p, registry)
	if _, err := a.RunTurn(context.Background(), "go"); err != nil {
		t.Fatalf("RunTurn: %v", err)
	}
	live := a.History()

	log, err := session.Open(dir)
	if err != nil {
		t.Fatalf("session.Open: %v", err)
	}
	defer log.Close()
	events, err := log.Events()
	if err != nil {
		t.Fatalf("Events: %v", err)
	}
	rebuilt, err := RebuildHistory(events, nil)
	if err != nil {
		t.Fatalf("RebuildHistory: %v", err)
	}
	for i := range live {
		if !reflect.DeepEqual(rebuilt[i], live[i]) {
			t.Fatalf("message %d differs:\nlive %+v\nrebuilt %+v", i, live[i], rebuilt[i])
		}
	}
}

// TestEmptyErrorTextIsLogged pins that a failed call with an empty message is
// recorded as a failure and replayed as one: the live history stores
// "error: ", so the log stores that same text.
func TestEmptyErrorTextIsLogged(t *testing.T) {
	registry := tools.NewRegistry()
	tool := tools.Tool{
		Name:        "fail",
		Description: "fails silently",
		Parameters:  json.RawMessage(`{"type":"object","properties":{},"additionalProperties":false}`),
		Run: func(context.Context, json.RawMessage) (any, error) {
			return nil, errors.New("")
		},
	}
	if err := registry.Register(tool); err != nil {
		t.Fatalf("Register: %v", err)
	}
	p := &scriptedProvider{responses: []llm.Response{
		{ToolCalls: []llm.ToolCall{{ID: "c1", Name: "fail", Arguments: `{}`}}},
		{Text: "done"},
	}}
	a, dir := newAgent(t, p, registry)
	if _, err := a.RunTurn(context.Background(), "go"); err != nil {
		t.Fatalf("RunTurn: %v", err)
	}
	live := a.History()

	log, err := session.Open(dir)
	if err != nil {
		t.Fatalf("session.Open: %v", err)
	}
	defer log.Close()
	events, err := log.Events()
	if err != nil {
		t.Fatalf("Events: %v", err)
	}
	rebuilt, err := RebuildHistory(events, nil)
	if err != nil {
		t.Fatalf("RebuildHistory: %v", err)
	}
	for i := range live {
		if !reflect.DeepEqual(rebuilt[i], live[i]) {
			t.Fatalf("message %d differs:\nlive %+v\nrebuilt %+v", i, live[i], rebuilt[i])
		}
	}
}
