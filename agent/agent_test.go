package agent

import (
	"context"
	"encoding/json"
	"errors"
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
