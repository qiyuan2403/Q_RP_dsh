package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/qiyuan2403/Q_RP_dsh/llm"
	"github.com/qiyuan2403/Q_RP_dsh/session"
	"github.com/qiyuan2403/Q_RP_dsh/tools"
)

// frameCollector records live frames in delivery order.
//
// No lock is needed: frames are delivered synchronously on the goroutine that
// called RunTurn, which is also the one reading them after it returns.
type frameCollector struct {
	frames []AssistantStreamFrame
}

func (c *frameCollector) collect(frame AssistantStreamFrame) { c.frames = append(c.frames, frame) }

func (c *frameCollector) all() []AssistantStreamFrame {
	return append([]AssistantStreamFrame(nil), c.frames...)
}

// newStreamAgent builds an agent whose live frames go to onFrame.
func newStreamAgent(t *testing.T, provider llm.Provider, registry *tools.Registry, onFrame func(AssistantStreamFrame)) (*Agent, string) {
	t.Helper()
	a, _, dir := newStreamAgentWithLog(t, provider, registry, onFrame)
	return a, dir
}

// newStreamAgentWithLog also hands back the session log, for tests that break
// the log mid-turn.
func newStreamAgentWithLog(t *testing.T, provider llm.Provider, registry *tools.Registry, onFrame func(AssistantStreamFrame)) (*Agent, *session.Log, string) {
	t.Helper()
	dir := t.TempDir()
	log, err := session.Create(dir, session.Header{ID: "s", CreatedAt: 1})
	if err != nil {
		t.Fatalf("session.Create: %v", err)
	}
	t.Cleanup(func() { _ = log.Close() })
	a := New(Config{
		Provider:          provider,
		Registry:          registry,
		Model:             "test-model",
		SystemPrompt:      "you are a test",
		OnAssistantStream: onFrame,
	}, log)
	return a, log, dir
}

func framesOfType(frames []AssistantStreamFrame, typ string) []AssistantStreamFrame {
	var out []AssistantStreamFrame
	for _, frame := range frames {
		if frame.Type == typ {
			out = append(out, frame)
		}
	}
	return out
}

// assertDenseRevisions pins the Agent-lifetime revision sequence: 1, 2, 3, ...
// across every frame of every attempt.
func assertDenseRevisions(t *testing.T, frames []AssistantStreamFrame) {
	t.Helper()
	for i, frame := range frames {
		if frame.Revision != i+1 {
			t.Fatalf("frame %d revision = %d, want %d", i, frame.Revision, i+1)
		}
	}
}

func assertSingleTerminal(t *testing.T, frames []AssistantStreamFrame) AssistantStreamFrame {
	t.Helper()
	ends := framesOfType(frames, "end")
	if len(ends) != 1 {
		t.Fatalf("%d end frames, want 1: %+v", len(ends), frames)
	}
	return ends[0]
}

// assertFrameInt checks one positional frame field.
//
// A nil pointer is the interesting failure: it is how the wire format loses a
// required zero, so the message names the field instead of printing a value.
func assertFrameInt(t *testing.T, got *int, want int, name string) {
	t.Helper()
	if got == nil {
		t.Fatalf("%s is absent, want %d", name, want)
	}
	if *got != want {
		t.Fatalf("%s = %d, want %d", name, *got, want)
	}
}

// frameJSON decodes a frame's wire form, for tests that must assert which
// fields exist rather than what the Go value happens to be.
func frameJSON(t *testing.T, frame AssistantStreamFrame) map[string]json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(frame)
	if err != nil {
		t.Fatalf("marshal %s frame: %v", frame.Type, err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatalf("unmarshal %s frame JSON: %v", frame.Type, err)
	}
	return fields
}

func assertJSONInt(t *testing.T, fields map[string]json.RawMessage, key string, want int) {
	t.Helper()
	raw, ok := fields[key]
	if !ok {
		t.Fatalf("frame JSON is missing %q: %v", key, fields)
	}
	var got int
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("frame JSON %q = %s, want an integer: %v", key, raw, err)
	}
	if got != want {
		t.Fatalf("frame JSON %q = %d, want %d", key, got, want)
	}
}

func assertJSONPositiveInt(t *testing.T, fields map[string]json.RawMessage, key string) {
	t.Helper()
	raw, ok := fields[key]
	if !ok {
		t.Fatalf("frame JSON is missing %q: %v", key, fields)
	}
	var got int64
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("frame JSON %q = %s, want an integer: %v", key, raw, err)
	}
	if got <= 0 {
		t.Fatalf("frame JSON %q = %d, want a positive value", key, got)
	}
}

func assertJSONAbsent(t *testing.T, fields map[string]json.RawMessage, key string) {
	t.Helper()
	if raw, ok := fields[key]; ok {
		t.Fatalf("frame JSON unexpectedly contains %q = %s", key, raw)
	}
}

func readEvents(t *testing.T, dir string) []session.Event {
	t.Helper()
	events, err := session.ReadLog(dir)
	if err != nil {
		t.Fatalf("ReadLog: %v", err)
	}
	return events
}

// TestStreamFramesSingleStepText pins the happy path frame sequence for one
// model call: start, one chunk per stream chunk, then the committed end whose
// seq names the assistant/message event the frames became.
func TestStreamFramesSingleStepText(t *testing.T) {
	wantDeltas := []string{"hel", "lo", "!"}
	chunks := make([]llm.StreamChunk, 0, len(wantDeltas))
	for _, delta := range wantDeltas {
		chunks = append(chunks, llm.StreamChunk{TextDelta: delta})
	}
	p := &streamingProvider{chunks: chunks}
	var frames frameCollector
	a, dir := newStreamAgent(t, p, nil, frames.collect)

	res, err := a.RunTurn(context.Background(), "hi")
	if err != nil {
		t.Fatalf("RunTurn: %v", err)
	}
	if res.Reason != StopCompleted || res.Text != "hello!" {
		t.Fatalf("result = %+v", res)
	}

	got := frames.all()
	if len(got) != len(wantDeltas)+2 {
		t.Fatalf("got %d frames, want %d: %+v", len(got), len(wantDeltas)+2, got)
	}
	assertDenseRevisions(t, got)

	start := got[0]
	if start.Type != "start" || start.AttemptID != "s:1" {
		t.Fatalf("start frame = %+v", start)
	}
	assertFrameInt(t, start.Turn, 1, "start turn")
	assertFrameInt(t, start.Step, 0, "start step")
	for i, wantDelta := range wantDeltas {
		chunk := got[i+1]
		if chunk.Type != "chunk" || chunk.AttemptID != "s:1" {
			t.Fatalf("chunk %d frame = %+v", i, chunk)
		}
		assertFrameInt(t, chunk.Index, i, fmt.Sprintf("chunk %d index", i))
		if chunk.Chunk == nil || chunk.Chunk.TextDelta != wantDelta {
			t.Fatalf("chunk %d payload = %+v, want %q", i, chunk.Chunk, wantDelta)
		}
		if chunk.Time <= 0 {
			t.Fatalf("chunk %d time = %d, want a Unix millisecond stamp", i, chunk.Time)
		}
	}

	end := got[len(got)-1]
	if end.Type != "end" || end.AttemptID != "s:1" {
		t.Fatalf("end frame = %+v", end)
	}
	assertFrameInt(t, end.Index, len(wantDeltas), "end index")
	if end.Outcome == nil || end.Outcome.Kind != "committed" || end.Outcome.EventType != "assistant/message" {
		t.Fatalf("end outcome = %+v", end.Outcome)
	}

	events := readEvents(t, dir)
	messages := 0
	var wantSeq int64
	for _, event := range events {
		if event.Type == "assistant/message" {
			messages++
			wantSeq = event.Seq
		}
	}
	if messages != 1 {
		t.Fatalf("log has %d assistant/message events, want 1", messages)
	}
	if end.Outcome.Seq != wantSeq {
		t.Fatalf("end seq = %d, want the assistant/message seq %d", end.Outcome.Seq, wantSeq)
	}
}

// TestStreamFrameJSONRequiredFields pins the wire contract: a start frame
// carries step 0, a first chunk carries index 0, and fields the frame type does
// not own stay absent. The upstream union marks these fields required on their
// frame type, so an observer that validates cannot treat a missing zero as a
// field without meaning.
func TestStreamFrameJSONRequiredFields(t *testing.T) {
	p := &streamingProvider{chunks: []llm.StreamChunk{
		{TextDelta: "a"},
		{TextDelta: "b"},
	}}
	var frames frameCollector
	a, _ := newStreamAgent(t, p, nil, frames.collect)

	if _, err := a.RunTurn(context.Background(), "hi"); err != nil {
		t.Fatalf("RunTurn: %v", err)
	}
	got := frames.all()
	if len(got) != 4 {
		t.Fatalf("got %d frames, want start/chunk/chunk/end: %+v", len(got), got)
	}

	start := frameJSON(t, got[0])
	assertJSONInt(t, start, "turn", 1)
	assertJSONInt(t, start, "step", 0)
	assertJSONAbsent(t, start, "index")
	assertJSONAbsent(t, start, "time")
	assertJSONAbsent(t, start, "chunk")
	assertJSONAbsent(t, start, "outcome")

	first := frameJSON(t, got[1])
	assertJSONInt(t, first, "index", 0)
	assertJSONPositiveInt(t, first, "time")
	assertJSONAbsent(t, first, "turn")
	assertJSONAbsent(t, first, "step")
	assertJSONAbsent(t, first, "outcome")

	end := frameJSON(t, got[3])
	assertJSONInt(t, end, "index", 2)
	assertJSONAbsent(t, end, "turn")
	assertJSONAbsent(t, end, "step")
	assertJSONAbsent(t, end, "time")
	assertJSONAbsent(t, end, "chunk")
}

// TestStreamUsageOnlyChunkIsFramed pins that token accounting is not a special
// case: a chunk with no text still reaches the observer, because the app needs
// the usage report and a silent drop would make the frame stream disagree with
// the provider's chunk count.
func TestStreamUsageOnlyChunkIsFramed(t *testing.T) {
	p := &streamingProvider{chunks: []llm.StreamChunk{
		{Usage: &llm.Usage{PromptTokens: 3, CompletionTokens: 1, TotalTokens: 4}},
	}}
	var frames frameCollector
	a, _ := newStreamAgent(t, p, nil, frames.collect)

	res, err := a.RunTurn(context.Background(), "hi")
	if err != nil {
		t.Fatalf("RunTurn: %v", err)
	}
	if res.Reason != StopCompleted || res.Text != "" {
		t.Fatalf("result = %+v", res)
	}

	chunks := framesOfType(frames.all(), "chunk")
	if len(chunks) != 1 {
		t.Fatalf("%d chunk frames, want 1: %+v", len(chunks), frames.all())
	}
	chunk := chunks[0]
	if chunk.Chunk == nil || chunk.Chunk.Usage == nil {
		t.Fatalf("chunk payload = %+v, want a usage report", chunk.Chunk)
	}
	if chunk.Chunk.TextDelta != "" || chunk.Chunk.Usage.TotalTokens != 4 {
		t.Fatalf("chunk payload = %+v", chunk.Chunk)
	}
	if chunk.Time <= 0 {
		t.Fatalf("chunk time = %d, want a Unix millisecond stamp", chunk.Time)
	}
	wire := frameJSON(t, chunk)
	assertJSONInt(t, wire, "index", 0)
	assertJSONPositiveInt(t, wire, "time")
}

// TestStreamFramesSpanTwoTurns pins that the live numbering is a property of
// the Agent, not of a turn: turn increments, revisions keep counting, and the
// attempt id sequence continues, so an observer can order frames without
// knowing where one RunTurn ended.
func TestStreamFramesSpanTwoTurns(t *testing.T) {
	p := &streamingProvider{streams: [][]llm.StreamChunk{
		{{TextDelta: "one"}},
		{{TextDelta: "two"}},
	}}
	var frames frameCollector
	a, _ := newStreamAgent(t, p, nil, frames.collect)

	first, err := a.RunTurn(context.Background(), "first")
	if err != nil {
		t.Fatalf("first RunTurn: %v", err)
	}
	if first.Reason != StopCompleted || first.Text != "one" {
		t.Fatalf("first result = %+v", first)
	}
	firstCount := len(frames.all())

	second, err := a.RunTurn(context.Background(), "second")
	if err != nil {
		t.Fatalf("second RunTurn: %v", err)
	}
	if second.Reason != StopCompleted || second.Text != "two" {
		t.Fatalf("second result = %+v", second)
	}

	got := frames.all()
	assertDenseRevisions(t, got)
	if got[firstCount].Revision != firstCount+1 {
		t.Fatalf("second turn starts at revision %d, want %d", got[firstCount].Revision, firstCount+1)
	}

	starts := framesOfType(got, "start")
	if len(starts) != 2 {
		t.Fatalf("%d start frames, want 2: %+v", len(starts), starts)
	}
	assertFrameInt(t, starts[0].Turn, 1, "first turn")
	assertFrameInt(t, starts[1].Turn, 2, "second turn")
	assertFrameInt(t, starts[0].Step, 0, "first step")
	assertFrameInt(t, starts[1].Step, 0, "second step")
	if starts[0].AttemptID != "s:1" || starts[1].AttemptID != "s:2" {
		t.Fatalf("attempt ids = %q, %q; want s:1, s:2", starts[0].AttemptID, starts[1].AttemptID)
	}

	ends := framesOfType(got, "end")
	if len(ends) != 2 {
		t.Fatalf("%d end frames, want 2: %+v", len(ends), ends)
	}
	if ends[0].AttemptID != "s:1" || ends[1].AttemptID != "s:2" {
		t.Fatalf("end attempt ids = %q, %q", ends[0].AttemptID, ends[1].AttemptID)
	}
	assertFrameInt(t, ends[0].Index, 1, "first end index")
	assertFrameInt(t, ends[1].Index, 1, "second end index")
}

// TestStreamFramesSpanTwoAttempts pins attempt identity: a tool-call step and
// the step after it are separate attempts, each bracketed by its own start and
// committed end, while revisions keep counting across the boundary.
func TestStreamFramesSpanTwoAttempts(t *testing.T) {
	registry := tools.NewRegistry()
	if err := registry.Register(tools.Tool{
		Name:       "echo",
		Parameters: json.RawMessage(`{"type":"object","properties":{"text":{"type":"string"}},"required":["text"]}`),
		Run: func(context.Context, json.RawMessage) (any, error) {
			return "ok", nil
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
	var frames frameCollector
	a, dir := newStreamAgent(t, p, registry, frames.collect)

	res, err := a.RunTurn(context.Background(), "use echo")
	if err != nil {
		t.Fatalf("RunTurn: %v", err)
	}
	if res.Reason != StopCompleted || res.Text != "done" || res.Steps != 2 {
		t.Fatalf("result = %+v", res)
	}

	got := frames.all()
	// 4 frames for the tool-call attempt (2 argument fragments), 3 for the
	// text attempt.
	if len(got) != 7 {
		t.Fatalf("got %d frames, want 7: %+v", len(got), got)
	}
	assertDenseRevisions(t, got)

	if got[0].Type != "start" || got[0].AttemptID != "s:1" {
		t.Fatalf("first start = %+v", got[0])
	}
	assertFrameInt(t, got[0].Turn, 1, "first start turn")
	assertFrameInt(t, got[0].Step, 0, "first start step")
	if got[4].Type != "start" || got[4].AttemptID != "s:2" {
		t.Fatalf("second start = %+v", got[4])
	}
	assertFrameInt(t, got[4].Turn, 1, "second start turn")
	assertFrameInt(t, got[4].Step, 1, "second start step")
	if got[0].AttemptID == got[4].AttemptID {
		t.Fatalf("both attempts share id %q", got[0].AttemptID)
	}
	for i, frame := range got[1:3] {
		if frame.Type != "chunk" || frame.Chunk == nil || frame.Chunk.ToolCallDelta == nil {
			t.Fatalf("tool chunk %d = %+v", i, frame)
		}
		assertFrameInt(t, frame.Index, i, fmt.Sprintf("tool chunk %d index", i))
	}
	if got[5].Type != "chunk" || got[5].Chunk == nil || got[5].Chunk.TextDelta != "done" {
		t.Fatalf("text chunk = %+v", got[5])
	}
	assertFrameInt(t, got[5].Index, 0, "text chunk index")

	ends := framesOfType(got, "end")
	if len(ends) != 2 {
		t.Fatalf("%d end frames, want 2: %+v", len(ends), ends)
	}
	if ends[0].AttemptID != "s:1" {
		t.Fatalf("first end = %+v", ends[0])
	}
	assertFrameInt(t, ends[0].Index, 2, "first end index")
	if ends[1].AttemptID != "s:2" {
		t.Fatalf("second end = %+v", ends[1])
	}
	assertFrameInt(t, ends[1].Index, 1, "second end index")

	// Each committed end names its own step's durable event.
	var messageSeqs []int64
	for _, event := range readEvents(t, dir) {
		if event.Type == "assistant/message" {
			messageSeqs = append(messageSeqs, event.Seq)
		}
	}
	if len(messageSeqs) != 2 {
		t.Fatalf("log has %d assistant/message events, want 2", len(messageSeqs))
	}
	for i, end := range ends {
		if end.Outcome == nil || end.Outcome.Kind != "committed" || end.Outcome.EventType != "assistant/message" {
			t.Fatalf("end %d outcome = %+v", i, end.Outcome)
		}
		if end.Outcome.Seq != messageSeqs[i] {
			t.Fatalf("end %d seq = %d, want %d", i, end.Outcome.Seq, messageSeqs[i])
		}
	}
}

// failingStreamProvider delivers its chunks, then fails the call.
type failingStreamProvider struct {
	chunks []llm.StreamChunk
	err    error
}

func (p *failingStreamProvider) Complete(context.Context, llm.Request) (llm.Response, error) {
	return llm.Response{}, errors.New("Complete should not be called for a streaming provider")
}

func (p *failingStreamProvider) Stream(_ context.Context, _ llm.Request, yield func(llm.StreamChunk) error) error {
	for _, chunk := range p.chunks {
		if err := yield(chunk); err != nil {
			return err
		}
	}
	return p.err
}

// TestStreamFailureAbandonsWithoutDurableMessage pins failure settlement: a
// broken stream closes its live attempt as abandoned and leaves no
// assistant/message in the log, so no observer is told of a commit that did not
// happen.
func TestStreamFailureAbandonsWithoutDurableMessage(t *testing.T) {
	streamErr := errors.New("provider exploded")
	p := &failingStreamProvider{
		chunks: []llm.StreamChunk{{TextDelta: "par"}},
		err:    streamErr,
	}
	var frames frameCollector
	a, dir := newStreamAgent(t, p, nil, frames.collect)

	res, err := a.RunTurn(context.Background(), "hi")
	if err != nil {
		t.Fatalf("RunTurn: %v", err)
	}
	if res.Reason != StopModelError || !errors.Is(res.Err, streamErr) {
		t.Fatalf("result = %+v", res)
	}

	got := frames.all()
	if len(got) != 3 {
		t.Fatalf("got %d frames, want start/chunk/end: %+v", len(got), got)
	}
	assertDenseRevisions(t, got)
	end := assertSingleTerminal(t, got)
	assertFrameInt(t, end.Index, 1, "end index")
	if end.Outcome == nil || end.Outcome.Kind != "abandoned" || end.Outcome.EventType != "" || end.Outcome.Seq != 0 {
		t.Fatalf("end outcome = %+v, want abandoned without a durable event", end.Outcome)
	}
	if types := eventTypes(readEvents(t, dir)); contains(types, "assistant/message") {
		t.Fatalf("failed turn wrote an assistant/message: %v", types)
	}
}

// TestZeroChunkStreamFailure pins the terminal frame of an attempt that failed
// before its first chunk: end still carries index 0, so the observer can close
// the live segment it opened on start, and nothing was committed.
func TestZeroChunkStreamFailure(t *testing.T) {
	streamErr := errors.New("provider failed before yielding")
	p := &failingStreamProvider{err: streamErr}
	var frames frameCollector
	a, dir := newStreamAgent(t, p, nil, frames.collect)

	res, err := a.RunTurn(context.Background(), "hi")
	if err != nil {
		t.Fatalf("RunTurn: %v", err)
	}
	if res.Reason != StopModelError || !errors.Is(res.Err, streamErr) {
		t.Fatalf("result = %+v", res)
	}

	got := frames.all()
	if len(got) != 2 {
		t.Fatalf("got %d frames, want only start and end: %+v", len(got), got)
	}
	if got[0].Type != "start" || got[1].Type != "end" {
		t.Fatalf("frames = %+v, want start/end", got)
	}
	assertDenseRevisions(t, got)
	if chunks := framesOfType(got, "chunk"); len(chunks) != 0 {
		t.Fatalf("%d chunk frames, want none: %+v", len(chunks), chunks)
	}

	end := assertSingleTerminal(t, got)
	assertFrameInt(t, end.Index, 0, "end index")
	if end.Outcome == nil || end.Outcome.Kind != "abandoned" || end.Outcome.Seq != 0 {
		t.Fatalf("end outcome = %+v, want abandoned", end.Outcome)
	}
	wire := frameJSON(t, end)
	assertJSONInt(t, wire, "index", 0)
	assertJSONAbsent(t, wire, "turn")
	assertJSONAbsent(t, wire, "step")

	if types := eventTypes(readEvents(t, dir)); contains(types, "assistant/message") {
		t.Fatalf("failed turn wrote an assistant/message: %v", types)
	}
}

// closingLogStreamProvider closes the session log after its chunks, which is
// the shortest way to make the following durable append fail.
type closingLogStreamProvider struct {
	chunks []llm.StreamChunk
	log    *session.Log
}

func (p *closingLogStreamProvider) Complete(context.Context, llm.Request) (llm.Response, error) {
	return llm.Response{}, errors.New("Complete should not be called for a streaming provider")
}

func (p *closingLogStreamProvider) Stream(_ context.Context, _ llm.Request, yield func(llm.StreamChunk) error) error {
	for _, chunk := range p.chunks {
		if err := yield(chunk); err != nil {
			return err
		}
	}
	return p.log.Close()
}

// TestAppendFailureAbandonsBeforeReturning pins the second abandonment trigger:
// frames already went out for a model call whose assistant/message append
// failed, so that attempt must close as abandoned, never committed.
func TestAppendFailureAbandonsBeforeReturning(t *testing.T) {
	var frames frameCollector
	p := &closingLogStreamProvider{chunks: []llm.StreamChunk{{TextDelta: "par"}}}
	a, log, dir := newStreamAgentWithLog(t, p, nil, frames.collect)
	p.log = log

	res, err := a.RunTurn(context.Background(), "hi")
	if err == nil {
		t.Fatal("RunTurn succeeded even though its log was closed mid-turn")
	}
	if res.Reason != StopModelError {
		t.Fatalf("Reason = %q, want model-error", res.Reason)
	}

	got := frames.all()
	if len(got) != 3 {
		t.Fatalf("got %d frames, want start/chunk/end: %+v", len(got), got)
	}
	assertDenseRevisions(t, got)
	end := assertSingleTerminal(t, got)
	assertFrameInt(t, end.Index, 1, "end index")
	if end.Outcome == nil || end.Outcome.Kind != "abandoned" || end.Outcome.Seq != 0 {
		t.Fatalf("end outcome = %+v, want abandoned without a durable event", end.Outcome)
	}
	if types := eventTypes(readEvents(t, dir)); contains(types, "assistant/message") {
		t.Fatalf("a failed append still wrote an assistant/message: %v", types)
	}
}

// cancellingStreamProvider cancels the turn's context mid-stream and then
// reports the context error, which is how a provider reacts to an aborted
// request.
type cancellingStreamProvider struct {
	cancel context.CancelFunc
}

func (p *cancellingStreamProvider) Complete(context.Context, llm.Request) (llm.Response, error) {
	return llm.Response{}, errors.New("Complete should not be called for a streaming provider")
}

func (p *cancellingStreamProvider) Stream(ctx context.Context, _ llm.Request, yield func(llm.StreamChunk) error) error {
	if err := yield(llm.StreamChunk{TextDelta: "par"}); err != nil {
		return err
	}
	p.cancel()
	<-ctx.Done()
	return ctx.Err()
}

// TestStreamCancellationAbandonsAndReportsCancelled pins cancellation
// settlement: the interrupted attempt is closed live as abandoned, and the turn
// still reports cancellation rather than a model failure.
func TestStreamCancellationAbandonsAndReportsCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	p := &cancellingStreamProvider{cancel: cancel}
	var frames frameCollector
	a, dir := newStreamAgent(t, p, nil, frames.collect)

	res, err := a.RunTurn(ctx, "hi")
	if err != nil {
		t.Fatalf("RunTurn: %v", err)
	}
	if res.Reason != StopCancelled {
		t.Fatalf("Reason = %q, want cancelled", res.Reason)
	}
	if !errors.Is(res.Err, context.Canceled) {
		t.Fatalf("Err = %v, want context.Canceled", res.Err)
	}

	got := frames.all()
	assertDenseRevisions(t, got)
	end := assertSingleTerminal(t, got)
	if end.Outcome == nil || end.Outcome.Kind != "abandoned" {
		t.Fatalf("end outcome = %+v, want abandoned", end.Outcome)
	}
	assertFrameInt(t, end.Index, 1, "end index")
	if types := eventTypes(readEvents(t, dir)); contains(types, "assistant/message") {
		t.Fatalf("cancelled turn wrote an assistant/message: %v", types)
	}
}

// cancelThenFailStreamProvider cancels the turn and then reports its own
// transport failure, which is how a real client reacts to an aborted request:
// the returned error is not the context error even though the caller cancelled.
type cancelThenFailStreamProvider struct {
	cancel context.CancelFunc
}

func (p *cancelThenFailStreamProvider) Complete(context.Context, llm.Request) (llm.Response, error) {
	return llm.Response{}, errors.New("Complete should not be called for a streaming provider")
}

func (p *cancelThenFailStreamProvider) Stream(ctx context.Context, _ llm.Request, yield func(llm.StreamChunk) error) error {
	if err := yield(llm.StreamChunk{TextDelta: "par"}); err != nil {
		return err
	}
	p.cancel()
	<-ctx.Done()
	return errors.New("transport closed")
}

// TestCancelledContextWinsOverProviderError pins the classification rule: the
// caller's cancelled context decides the stop reason even when the provider
// reports a plain error, because the intent to stop is the fact the app acts on.
func TestCancelledContextWinsOverProviderError(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var frames frameCollector
	a, _ := newStreamAgent(t, &cancelThenFailStreamProvider{cancel: cancel}, nil, frames.collect)

	res, err := a.RunTurn(ctx, "hi")
	if err != nil {
		t.Fatalf("RunTurn: %v", err)
	}
	if res.Reason != StopCancelled || !errors.Is(res.Err, context.Canceled) {
		t.Fatalf("result = %+v, want cancelled with context.Canceled", res)
	}

	got := frames.all()
	assertDenseRevisions(t, got)
	end := assertSingleTerminal(t, got)
	if end.Outcome == nil || end.Outcome.Kind != "abandoned" {
		t.Fatalf("end outcome = %+v, want abandoned", end.Outcome)
	}
	assertFrameInt(t, end.Index, 1, "end index")
}

// lateChunkStreamProvider keeps yielding after its turn is cancelled, which is
// what a provider without its own abort check does.
type lateChunkStreamProvider struct {
	cancel context.CancelFunc
}

func (p *lateChunkStreamProvider) Complete(context.Context, llm.Request) (llm.Response, error) {
	return llm.Response{}, errors.New("Complete should not be called for a streaming provider")
}

func (p *lateChunkStreamProvider) Stream(_ context.Context, _ llm.Request, yield func(llm.StreamChunk) error) error {
	for i, delta := range []string{"one", "two", "three", "four"} {
		if err := yield(llm.StreamChunk{TextDelta: delta}); err != nil {
			return err
		}
		if i == 1 {
			p.cancel()
		}
	}
	return nil
}

// TestStreamCancellationStopsChunkFrames pins that a chunk yielded after the
// cancellation is neither framed nor accumulated: the observer closed the live
// segment on the terminal frame, and a late chunk would reopen it with content
// the turn will never commit.
func TestStreamCancellationStopsChunkFrames(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var frames frameCollector
	a, dir := newStreamAgent(t, &lateChunkStreamProvider{cancel: cancel}, nil, frames.collect)

	res, err := a.RunTurn(ctx, "hi")
	if err != nil {
		t.Fatalf("RunTurn: %v", err)
	}
	if res.Reason != StopCancelled || !errors.Is(res.Err, context.Canceled) {
		t.Fatalf("result = %+v, want cancelled with context.Canceled", res)
	}

	got := frames.all()
	assertDenseRevisions(t, got)
	chunks := framesOfType(got, "chunk")
	wantDeltas := []string{"one", "two"}
	if len(chunks) != len(wantDeltas) {
		t.Fatalf("got %d chunk frames, want the %d delivered before cancellation: %+v", len(chunks), len(wantDeltas), chunks)
	}
	for i, wantDelta := range wantDeltas {
		assertFrameInt(t, chunks[i].Index, i, fmt.Sprintf("chunk %d index", i))
		if chunks[i].Chunk == nil || chunks[i].Chunk.TextDelta != wantDelta {
			t.Fatalf("chunk %d payload = %+v, want %q", i, chunks[i].Chunk, wantDelta)
		}
	}

	end := assertSingleTerminal(t, got)
	if end.Outcome == nil || end.Outcome.Kind != "abandoned" {
		t.Fatalf("end outcome = %+v, want abandoned", end.Outcome)
	}
	assertFrameInt(t, end.Index, len(wantDeltas), "end index")
	if types := eventTypes(readEvents(t, dir)); contains(types, "assistant/message") {
		t.Fatalf("cancelled turn wrote an assistant/message: %v", types)
	}
}

// deadlineStreamProvider waits for the turn's context to expire and then reports
// the context error, which is how a provider that respects a deadline fails.
type deadlineStreamProvider struct{}

func (p *deadlineStreamProvider) Complete(context.Context, llm.Request) (llm.Response, error) {
	return llm.Response{}, errors.New("Complete should not be called for a streaming provider")
}

func (p *deadlineStreamProvider) Stream(ctx context.Context, _ llm.Request, _ func(llm.StreamChunk) error) error {
	<-ctx.Done()
	return ctx.Err()
}

// TestStreamDeadlineExceededIsModelError pins that a deadline is not a
// cancellation: nobody asked to stop, the call failed to finish in time, and
// the app must present that failure instead of clearing the live reply as if
// the user had interrupted it.
func TestStreamDeadlineExceededIsModelError(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	var frames frameCollector
	a, dir := newStreamAgent(t, &deadlineStreamProvider{}, nil, frames.collect)

	res, err := a.RunTurn(ctx, "hi")
	if err != nil {
		t.Fatalf("RunTurn: %v", err)
	}
	if res.Reason != StopModelError {
		t.Fatalf("Reason = %q, want model-error", res.Reason)
	}
	if !errors.Is(res.Err, context.DeadlineExceeded) {
		t.Fatalf("Err = %v, want context.DeadlineExceeded", res.Err)
	}

	got := frames.all()
	if len(got) != 2 {
		t.Fatalf("got %d frames, want only start and end: %+v", len(got), got)
	}
	end := assertSingleTerminal(t, got)
	if end.Outcome == nil || end.Outcome.Kind != "abandoned" {
		t.Fatalf("end outcome = %+v, want abandoned", end.Outcome)
	}
	assertFrameInt(t, end.Index, 0, "end index")
	if types := eventTypes(readEvents(t, dir)); contains(types, "assistant/message") {
		t.Fatalf("timed-out turn wrote an assistant/message: %v", types)
	}
}

// TestExpiredDeadlineAtStepStartIsModelError pins the same classification at the
// step boundary: a deadline that expired between steps is a model failure, not a
// cancellation, because the caller never asked to stop.
func TestExpiredDeadlineAtStepStartIsModelError(t *testing.T) {
	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()
	p := &scriptedProvider{responses: []llm.Response{{Text: "never"}}}
	a, _ := newStreamAgent(t, p, nil, nil)

	res, err := a.RunTurn(ctx, "hi")
	if err != nil {
		t.Fatalf("RunTurn: %v", err)
	}
	if res.Reason != StopModelError || !errors.Is(res.Err, context.DeadlineExceeded) {
		t.Fatalf("result = %+v, want model-error with DeadlineExceeded", res)
	}
	if res.Steps != 0 {
		t.Fatalf("Steps = %d, want 0", res.Steps)
	}
	if p.requestCount() != 0 {
		t.Fatalf("provider was called %d times after the deadline", p.requestCount())
	}
}

// TestNonStreamingProviderEmitsNoFrames pins the fallback: a provider without
// Stream produces no transient frames, because there is nothing transient to
// report.
func TestNonStreamingProviderEmitsNoFrames(t *testing.T) {
	p := &scriptedProvider{responses: []llm.Response{{Text: "done"}}}
	var frames frameCollector
	a, _ := newStreamAgent(t, p, nil, frames.collect)

	res, err := a.RunTurn(context.Background(), "hi")
	if err != nil {
		t.Fatalf("RunTurn: %v", err)
	}
	if res.Reason != StopCompleted || res.Text != "done" {
		t.Fatalf("result = %+v", res)
	}
	if got := frames.all(); len(got) != 0 {
		t.Fatalf("non-streaming provider emitted %d frames: %+v", len(got), got)
	}
}

// TestNilStreamCallbackKeepsBehaviour pins that framing is optional: with no
// observer configured a streamed turn behaves exactly as before.
func TestNilStreamCallbackKeepsBehaviour(t *testing.T) {
	p := &streamingProvider{chunks: []llm.StreamChunk{
		{TextDelta: "hel"},
		{TextDelta: "lo"},
	}}
	a, _ := newStreamAgent(t, p, nil, nil)

	res, err := a.RunTurn(context.Background(), "hi")
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

// TestAttemptIdentityWithoutLog pins the nil-log fallback: an agent built
// without a session log still frames, using an empty session id in the attempt
// id instead of panicking.
func TestAttemptIdentityWithoutLog(t *testing.T) {
	var frames frameCollector
	a := &Agent{cfg: Config{OnAssistantStream: frames.collect}}

	attempt := a.beginAttempt(1, 0)
	attempt.commit(7)

	got := frames.all()
	if len(got) != 2 {
		t.Fatalf("got %d frames, want start and end: %+v", len(got), got)
	}
	if got[0].AttemptID != ":1" {
		t.Fatalf("attempt id = %q, want %q", got[0].AttemptID, ":1")
	}
	if got[1].Outcome == nil || got[1].Outcome.Seq != 7 {
		t.Fatalf("end outcome = %+v", got[1].Outcome)
	}
}
