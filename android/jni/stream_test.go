package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/qiyuan2403/Q_RP_dsh/agent"
	"github.com/qiyuan2403/Q_RP_dsh/session"
	"github.com/qiyuan2403/Q_RP_dsh/web"
	"github.com/qiyuan2403/Q_RP_dsh/web/deepseeksearch"
)

// openTestSession creates an open session under home and registers it the way
// openSession does, so runTurnJSON can address it by id. The log stays open for
// the duration of the test, exactly as an app-owned session does across turns.
func openTestSession(t *testing.T, home, id string) *session.Log {
	t.Helper()
	log, err := session.Create(filepath.Join(home, "sessions", id), session.Header{
		ID:        id,
		CreatedAt: time.Now().UnixMilli(),
		CWD:       home,
	})
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	sessionsMu.Lock()
	sessions[id] = log
	sessionsMu.Unlock()
	t.Cleanup(func() {
		sessionsMu.Lock()
		delete(sessions, id)
		sessionsMu.Unlock()
		_ = log.Close()
	})
	return log
}

// newStreamingModelServer serves a chat-completions endpoint that streams the
// given deltas as SSE. The delay before each delta keeps the turn open long
// enough for the test to poll the merged channel, which is the only way a unit
// test can observe live frames of a real (if local) provider.
func newStreamingModelServer(t *testing.T, deltas []string, delay time.Duration) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		flusher, ok := w.(http.Flusher)
		if !ok {
			t.Errorf("streaming model server: ResponseWriter does not flush")
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		for _, delta := range deltas {
			time.Sleep(delay)
			raw, err := json.Marshal(map[string]any{
				"choices": []map[string]any{{"delta": map[string]any{"content": delta}}},
			})
			if err != nil {
				t.Errorf("streaming model server: marshal delta: %v", err)
				return
			}
			_, _ = fmt.Fprintf(w, "data: %s\n\n", raw)
			flusher.Flush()
		}
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
		flusher.Flush()
	}))
	t.Cleanup(srv.Close)
	return srv
}

// newBlockingModelServer serves an SSE response that emits one delta and then
// holds the request open until its context is cancelled. That is the shape a
// cancelled turn has to unwind: the model is mid-stream, not between steps.
func newBlockingModelServer(t *testing.T, started chan<- struct{}) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		flusher, ok := w.(http.Flusher)
		if !ok {
			t.Errorf("blocking model server: ResponseWriter does not flush")
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"partial\"}}]}\n\n"))
		flusher.Flush()
		select {
		case started <- struct{}{}:
		default:
		}
		<-r.Context().Done()
	}))
	t.Cleanup(srv.Close)
	return srv
}

// newCompletingModelServer serves a non-SSE completion after delay.
//
// A plain JSON body is not an SSE stream, so the provider emits no text
// deltas; the turn still writes its durable log events, which is what the
// merged-channel test needs. The delay keeps the turn in flight while the test
// drains that channel.
func newCompletingModelServer(t *testing.T, delay time.Duration) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if delay > 0 {
			time.Sleep(delay)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"done"},"finish_reason":"stop"}]}`))
	}))
	t.Cleanup(srv.Close)
	return srv
}

// waitForEventStream waits until runTurn has published the session's merged
// channel, then returns it. The turn removes the entry during teardown, so a
// test that misses the window must fail rather than read a stale channel.
func waitForEventStream(t *testing.T, sessionID string) *eventStream {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		streamsMu.Lock()
		stream := eventStreams[sessionID]
		streamsMu.Unlock()
		if stream != nil {
			return stream
		}
		if time.Now().After(deadline) {
			t.Fatalf("no live event stream for session %q", sessionID)
		}
		time.Sleep(time.Millisecond)
	}
}

// nextAssistantFrame reads the merged channel until one assistant-stream frame
// arrives and decodes it. Durable log events on the same channel are skipped:
// the point of the merge is that both kinds share one poll, not that frames are
// the only traffic.
func nextAssistantFrame(t *testing.T, stream *eventStream) agent.AssistantStreamFrame {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		select {
		case ev, ok := <-stream.ch:
			if !ok {
				t.Fatal("merged stream closed before an assistant-stream frame arrived")
			}
			if ev.Type != assistantStreamEventType {
				continue
			}
			if ev.Seq != 0 {
				t.Fatalf("temporary frame has seq %d, want 0", ev.Seq)
			}
			var frame agent.AssistantStreamFrame
			if err := json.Unmarshal(ev.Data, &frame); err != nil {
				t.Fatalf("frame data is not JSON: %v (%s)", err, ev.Data)
			}
			return frame
		case <-deadline:
			t.Fatal("timed out waiting for an assistant-stream frame")
		}
	}
}

// TestMergedStreamCarriesAssistantFrames pins the transport behavior the app
// depends on: a streaming model's incremental output reaches pollEvent's
// channel, and it does so without ever entering the session log.
func TestMergedStreamCarriesAssistantFrames(t *testing.T) {
	home := t.TempDir()
	id := "stream-1"
	log := openTestSession(t, home, id)
	droppedAssistantStreamFrames.Store(0)

	srv := newStreamingModelServer(t, []string{"Hel", "lo"}, 30*time.Millisecond)

	done := make(chan string, 1)
	go func() {
		done <- runTurnJSON(runRequest{
			Task:      "stream please",
			Home:      home,
			WorkDir:   home,
			BaseURL:   srv.URL,
			Model:     "test",
			SessionID: id,
		})
	}()
	// If the assertions below fail early, the turn must still be stopped or the
	// httptest server cleanup would wait on a stream nobody aborts.
	t.Cleanup(func() { cancelTurnForSession(id) })

	stream := waitForEventStream(t, id)

	// The first frames are start and the deltas; the assertion is on a chunk in
	// the middle of the attempt, which is what the UI renders as live text.
	var delta string
	for delta == "" {
		frame := nextAssistantFrame(t, stream)
		if frame.Type == "chunk" && frame.Chunk != nil {
			delta = frame.Chunk.TextDelta
		}
	}
	if delta != "Hel" && delta != "lo" {
		t.Fatalf("chunk text delta = %q, want one of the served deltas", delta)
	}

	var raw string
	select {
	case raw = <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("the turn did not finish after streaming")
	}
	var resp runResponse
	if err := json.Unmarshal([]byte(raw), &resp); err != nil {
		t.Fatalf("response is not JSON: %v\n%s", err, raw)
	}
	if !resp.OK || resp.Reason != string(agent.StopCompleted) {
		t.Fatalf("response = %+v, want a completed turn", resp)
	}
	if resp.Text != "Hello" {
		t.Fatalf("turn text = %q, want the joined deltas", resp.Text)
	}

	// The frames are notifications, not facts: the log must hold the durable
	// message and no assistant-stream event at all. A log carrying both would
	// give replay a second source of truth for the same reply.
	events, err := log.Events()
	if err != nil {
		t.Fatalf("read session log: %v", err)
	}
	foundMessage := false
	for _, ev := range events {
		if ev.Type == assistantStreamEventType {
			t.Fatalf("assistant-stream frame was written to the session log: %+v", ev)
		}
		if ev.Type == "assistant/message" {
			var data struct {
				Text string `json:"text"`
			}
			if json.Unmarshal(ev.Data, &data) == nil && data.Text == "Hello" {
				foundMessage = true
			}
		}
	}
	if !foundMessage {
		t.Fatal("the durable assistant/message was not recorded")
	}
}

// TestPublisherDropsFramesInsteadOfBlocking pins the overload policy: the
// callback runs on the model's goroutine, so a full merged channel must cost a
// frame, never a stalled model call.
func TestPublisherDropsFramesInsteadOfBlocking(t *testing.T) {
	droppedAssistantStreamFrames.Store(0)
	stream := newEventStream()

	const extra = 7
	for i := 0; i < eventStreamBuffer+extra; i++ {
		idx := i
		stream.publishFrame(agent.AssistantStreamFrame{Type: "chunk", Index: &idx})
	}
	if got := droppedAssistantStreamFrames.Load(); got != extra {
		t.Fatalf("dropped frames = %d, want %d", got, extra)
	}
	if got := len(stream.ch); got != eventStreamBuffer {
		t.Fatalf("buffered frames = %d, want %d", got, eventStreamBuffer)
	}

	// A frame published after teardown must be ignored rather than panic on a
	// closed channel: the callback's contract is synchronous, but the stream
	// must not depend on that to stay safe.
	stream.close()
	ignore := 0
	stream.publishFrame(agent.AssistantStreamFrame{Type: "chunk", Index: &ignore})
	if got := droppedAssistantStreamFrames.Load(); got != extra {
		t.Fatalf("dropped frames after close = %d, want still %d", got, extra)
	}
}

// waitForCancellationRegistration waits until runTurnJSON has registered the
// turn's cancel function. Cancelling before that point would be a no-op and the
// test would hang on a model stream that was never aborted.
func waitForCancellationRegistration(t *testing.T, sessionID string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		cancelMu.Lock()
		_, ok := cancelRegistry[sessionID]
		cancelMu.Unlock()
		if ok {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("turn for session %q was never registered as cancellable", sessionID)
		}
		time.Sleep(time.Millisecond)
	}
}

// TestCancelTurnStopsARunningTurn pins the cancellation path end to end: the
// merged channel exists, the model is mid-stream, cancelTurnForSession aborts
// it, and the response reports cancelled rather than a model failure.
func TestCancelTurnStopsARunningTurn(t *testing.T) {
	home := t.TempDir()
	id := "cancel-1"
	log := openTestSession(t, home, id)

	started := make(chan struct{}, 1)
	srv := newBlockingModelServer(t, started)

	done := make(chan string, 1)
	go func() {
		done <- runTurnJSON(runRequest{
			Task:      "cancel please",
			Home:      home,
			WorkDir:   home,
			BaseURL:   srv.URL,
			Model:     "test",
			SessionID: id,
		})
	}()
	t.Cleanup(func() { cancelTurnForSession(id) })

	waitForCancellationRegistration(t, id)
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("the model stream was never opened")
	}

	// Two taps in a row are possible while the UI is still busy; the second
	// must be a harmless no-op, not a panic or a second cancellation error.
	cancelTurnForSession(id)
	cancelTurnForSession(id)

	var raw string
	select {
	case raw = <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("the cancelled turn did not return")
	}
	var resp runResponse
	if err := json.Unmarshal([]byte(raw), &resp); err != nil {
		t.Fatalf("response is not JSON: %v\n%s", err, raw)
	}
	if resp.OK {
		t.Fatalf("a cancelled turn reported success: %+v", resp)
	}
	if resp.Reason != string(agent.StopCancelled) {
		t.Fatalf("reason = %q, want %q", resp.Reason, agent.StopCancelled)
	}
	if resp.Error != "" {
		t.Fatalf("cancelled turn carried an error to display: %q", resp.Error)
	}

	// The cancellation itself is not a separate log record: turn/end's reason
	// is the durable account of why the turn stopped.
	events, err := log.Events()
	if err != nil {
		t.Fatalf("read session log: %v", err)
	}
	var end struct {
		Reason string `json:"reason"`
	}
	foundEnd := false
	for _, ev := range events {
		if ev.Type == "turn/end" {
			foundEnd = true
			if err := json.Unmarshal(ev.Data, &end); err != nil {
				t.Fatalf("turn/end data is not JSON: %v", err)
			}
		}
	}
	if !foundEnd {
		t.Fatal("the cancelled turn left no turn/end record")
	}
	if end.Reason != string(agent.StopCancelled) {
		t.Fatalf("turn/end reason = %q, want %q", end.Reason, agent.StopCancelled)
	}
}

// TestCancelTurnWithoutARunningTurnIsANoOp pins the fire-and-forget contract:
// a UI cancel racing a finished turn must be harmless, not an error or panic.
func TestCancelTurnWithoutARunningTurnIsANoOp(t *testing.T) {
	cancelTurnForSession("no-such-session")
	cancelTurnForSession("")
}

// TestSearchProviderAvailabilityFollowsTheRequestKey pins where the search key
// comes from on Android: the request carries it. The environment is cleared so
// a developer's DEEPSEEK_API_KEY cannot make the missing-key case pass.
func TestSearchProviderAvailabilityFollowsTheRequestKey(t *testing.T) {
	t.Setenv("DEEPSEEK_API_KEY", "")
	t.Setenv("DEEPSEEK_SEARCH_BASE_URL", "")
	t.Setenv("DSH_WEB_SEARCH_PROVIDER", "")

	withoutKey, err := newSearchProvider("")
	if err != nil {
		t.Fatalf("newSearchProvider without a key: %v", err)
	}
	if withoutKey.Available() {
		t.Fatal("the search provider is available without a key")
	}

	withKey, err := newSearchProvider("sk-test")
	if err != nil {
		t.Fatalf("newSearchProvider with a key: %v", err)
	}
	if !withKey.Available() {
		t.Fatal("the search provider is unavailable although the request carries a key")
	}
	if withKey.ID() != deepseeksearch.ProviderID {
		t.Fatalf("provider id = %q, want %q", withKey.ID(), deepseeksearch.ProviderID)
	}

	// The seam must still report the capability as unusable rather than hiding
	// the tool: the model gets a structured error it can act on.
	service, err := newWebService("")
	if err != nil {
		t.Fatalf("newWebService: %v", err)
	}
	_, err = service.Search(context.Background(), web.SearchRequest{Query: "q", MaxResults: 5})
	var webErr *web.Error
	if !errors.As(err, &webErr) || webErr.Code != web.CodeProviderUnavailable {
		t.Fatalf("search without a key returned %v, want %s", err, web.CodeProviderUnavailable)
	}
}

// newGatedModelServer holds every model request until the test unblocks it or
// the client cancels. Each request reports on started before waiting, so the
// test knows a turn is genuinely mid-call rather than still preparing.
func newGatedModelServer(t *testing.T, started chan<- struct{}) *httptest.Server {
	t.Helper()
	release := make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case started <- struct{}{}:
		default:
		}
		select {
		case <-release:
		case <-r.Context().Done():
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"ok\"}}]}\n\n"))
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	t.Cleanup(func() {
		unblock()
		srv.Close()
	})
	return srv
}

// TestOverlappingTurnsForTheSameSessionAreRejected pins the single-turn guard.
//
// A second runTurn for a session whose turn is still preparing or running must
// be refused before it registers anything. Otherwise it would replace the live
// stream and cancel handle under the same session id, and its teardown would
// remove them while the first turn kept going: no pollEvent channel, cancel
// turned into a no-op, and two turns interleaving their durable events.
func TestOverlappingTurnsForTheSameSessionAreRejected(t *testing.T) {
	home := t.TempDir()
	id := "overlap-1"
	openTestSession(t, home, id)

	started := make(chan struct{}, 2)
	srv := newGatedModelServer(t, started)

	firstDone := make(chan string, 1)
	go func() {
		firstDone <- runTurnJSON(runRequest{
			Task:      "first",
			Home:      home,
			WorkDir:   home,
			BaseURL:   srv.URL,
			Model:     "test",
			SessionID: id,
		})
	}()
	t.Cleanup(func() { cancelTurnForSession(id) })

	firstStream := waitForEventStream(t, id)
	waitForCancellationRegistration(t, id)
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("the first turn never reached the model")
	}

	cancelMu.Lock()
	firstCancel := cancelRegistry[id]
	cancelMu.Unlock()
	if firstCancel == nil {
		t.Fatal("the first turn has no cancel registration while it is running")
	}

	secondDone := make(chan string, 1)
	go func() {
		secondDone <- runTurnJSON(runRequest{
			Task:      "second",
			Home:      home,
			WorkDir:   home,
			BaseURL:   srv.URL,
			Model:     "test",
			SessionID: id,
		})
	}()
	var secondRaw string
	select {
	case secondRaw = <-secondDone:
	case <-time.After(5 * time.Second):
		t.Fatal("the overlapping turn was not rejected before reaching the model")
	}
	var second runResponse
	if err := json.Unmarshal([]byte(secondRaw), &second); err != nil {
		t.Fatalf("second response is not JSON: %v\n%s", err, secondRaw)
	}
	if second.OK {
		t.Fatalf("the overlapping turn reported success: %+v", second)
	}
	if !strings.Contains(second.Error, "already running") {
		t.Fatalf("second turn error = %q, want it to name the running turn", second.Error)
	}
	if !strings.HasPrefix(second.Error, "jni:") {
		t.Fatalf("second turn error = %q, want the jni: prefix", second.Error)
	}

	// Rejection must leave the first turn's registrations untouched: these are
	// the handles pollEvent and cancelTurn use.
	streamsMu.Lock()
	gotStream := eventStreams[id]
	streamsMu.Unlock()
	if gotStream != firstStream {
		t.Fatal("the rejected turn replaced the running turn's event stream")
	}
	cancelMu.Lock()
	gotCancel := cancelRegistry[id]
	cancelMu.Unlock()
	if gotCancel != firstCancel {
		t.Fatal("the rejected turn replaced the running turn's cancel handle")
	}

	// The first turn is still cancellable, and reports cancellation rather
	// than a model failure.
	cancelTurnForSession(id)
	var firstRaw string
	select {
	case firstRaw = <-firstDone:
	case <-time.After(10 * time.Second):
		t.Fatal("the first turn did not stop after cancellation")
	}
	var first runResponse
	if err := json.Unmarshal([]byte(firstRaw), &first); err != nil {
		t.Fatalf("first response is not JSON: %v\n%s", err, firstRaw)
	}
	if first.OK || first.Reason != string(agent.StopCancelled) {
		t.Fatalf("first turn response = %+v, want a cancelled turn", first)
	}

	// Its teardown removes its own entries, not any later turn's.
	streamsMu.Lock()
	_, streamLeft := eventStreams[id]
	streamsMu.Unlock()
	if streamLeft {
		t.Fatal("the first turn left its event stream registered")
	}
	cancelMu.Lock()
	_, cancelLeft := cancelRegistry[id]
	cancelMu.Unlock()
	if cancelLeft {
		t.Fatal("the first turn left its cancel handle registered")
	}
	turnsMu.Lock()
	_, turnLeft := activeTurns[id]
	turnsMu.Unlock()
	if turnLeft {
		t.Fatal("the first turn left its active-turn claim behind")
	}

	// With the first turn fully gone, the session accepts a new turn.
	quick := newCompletingModelServer(t, 0)
	thirdRaw := runTurnJSON(runRequest{
		Task:      "third",
		Home:      home,
		WorkDir:   home,
		BaseURL:   quick.URL,
		Model:     "test",
		SessionID: id,
	})
	var third runResponse
	if err := json.Unmarshal([]byte(thirdRaw), &third); err != nil {
		t.Fatalf("third response is not JSON: %v\n%s", err, thirdRaw)
	}
	if !third.OK {
		t.Fatalf("the turn after the cancelled one failed: %+v", third)
	}
}

// TestCancelDuringPreparationIsObserved pins the timing fix for cancellation:
// the turn context is registered before the node probe, toolset build and
// history replay. A stop tapped during that window must already be visible on
// the context the preparation checkpoints read.
func TestCancelDuringPreparationIsObserved(t *testing.T) {
	id := "prep-window-1"
	ctx, cancel, entry := newTurnContext(id)
	defer cancel()
	defer unregisterTurnCancel(id, entry)

	cancelTurnForSession(id)
	if !errors.Is(ctx.Err(), context.Canceled) {
		t.Fatalf("ctx.Err() = %v, want context.Canceled", ctx.Err())
	}
	resp := cancelledResponse()
	if resp.OK || resp.Reason != string(agent.StopCancelled) || resp.Error != "" {
		t.Fatalf("early cancellation response = %+v", resp)
	}
}

// TestRepeatedCancelIsSafe pins the fire-and-forget cancel contract: the UI may
// tap stop repeatedly, and a cancel may race the turn's own teardown. Neither
// may panic, and the first cancel must have stopped the context.
func TestRepeatedCancelIsSafe(t *testing.T) {
	id := "repeat-cancel-1"
	ctx, cancel, entry := newTurnContext(id)
	defer cancel()

	for i := 0; i < 3; i++ {
		cancelTurnForSession(id)
	}
	select {
	case <-ctx.Done():
	default:
		t.Fatal("repeated cancellation did not stop the turn context")
	}
	unregisterTurnCancel(id, entry)

	// The turn is gone: a late tap finds no entry and is a no-op.
	cancelTurnForSession(id)
}

// TestCancelCleanupKeepsANewerRegistration pins ownership for the cancel map.
// A delayed teardown of an older turn must not delete a handle a newer turn
// registered after the handover.
func TestCancelCleanupKeepsANewerRegistration(t *testing.T) {
	id := "cancel-ownership-1"
	oldCtx, oldCancel, oldEntry := newTurnContext(id)
	defer oldCancel()
	newCtx, newCancel, newEntry := newTurnContext(id)
	defer newCancel()

	unregisterTurnCancel(id, oldEntry)
	cancelTurnForSession(id)
	if !errors.Is(newCtx.Err(), context.Canceled) {
		t.Fatal("the newer turn was not cancellable after the older cleanup ran")
	}
	if oldCtx.Err() != nil {
		t.Fatal("the older turn's cleanup cancelled the newer turn's context")
	}
	unregisterTurnCancel(id, newEntry)
}

// TestEventStreamCleanupKeepsANewerStream pins the same ownership rule for the
// poll map: deleting by session id alone would take down a newer turn's stream.
func TestEventStreamCleanupKeepsANewerStream(t *testing.T) {
	id := "stream-ownership-1"
	first := newEventStream()
	second := newEventStream()
	streamsMu.Lock()
	eventStreams[id] = first
	streamsMu.Unlock()
	t.Cleanup(func() {
		streamsMu.Lock()
		delete(eventStreams, id)
		streamsMu.Unlock()
	})

	// The newer turn takes the key before the older teardown runs.
	streamsMu.Lock()
	eventStreams[id] = second
	streamsMu.Unlock()

	removeEventStream(id, first)
	streamsMu.Lock()
	got := eventStreams[id]
	streamsMu.Unlock()
	if got != second {
		t.Fatal("the older teardown removed the newer turn's stream")
	}

	removeEventStream(id, second)
	streamsMu.Lock()
	got = eventStreams[id]
	streamsMu.Unlock()
	if got != nil {
		t.Fatal("the owning teardown did not remove the stream")
	}
}

// TestMergedStreamCarriesDurableEvents pins the other half of the merge: the
// channel pollEvent reads is not frames-only. A turn whose model response is a
// plain JSON body produces no text deltas, so the durable user/message must
// still arrive through the same channel.
func TestMergedStreamCarriesDurableEvents(t *testing.T) {
	home := t.TempDir()
	id := "durable-1"
	openTestSession(t, home, id)

	srv := newCompletingModelServer(t, 200*time.Millisecond)

	done := make(chan string, 1)
	go func() {
		done <- runTurnJSON(runRequest{
			Task:      "durable please",
			Home:      home,
			WorkDir:   home,
			BaseURL:   srv.URL,
			Model:     "test",
			SessionID: id,
		})
	}()
	t.Cleanup(func() { cancelTurnForSession(id) })

	stream := waitForEventStream(t, id)
	deadline := time.After(5 * time.Second)
	var user session.Event
	for user.Type == "" {
		select {
		case ev, ok := <-stream.ch:
			if !ok {
				t.Fatal("the merged stream closed before the durable user/message arrived")
			}
			if ev.Type == "user/message" {
				user = ev
			}
		case <-deadline:
			t.Fatal("timed out waiting for a durable event on the merged stream")
		}
	}
	if user.Seq == 0 {
		t.Fatal("the durable event on the merged stream carries no sequence number")
	}
	var data struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal(user.Data, &data); err != nil {
		t.Fatalf("user/message data is not JSON: %v (%s)", err, user.Data)
	}
	if data.Text != "durable please" {
		t.Fatalf("user/message text = %q, want the task", data.Text)
	}

	var raw string
	select {
	case raw = <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("the non-streaming turn did not finish")
	}
	var resp runResponse
	if err := json.Unmarshal([]byte(raw), &resp); err != nil {
		t.Fatalf("response is not JSON: %v\n%s", err, raw)
	}
	if !resp.OK || resp.Reason != string(agent.StopCompleted) {
		t.Fatalf("response = %+v, want a completed turn", resp)
	}
}

// TestTeardownWithAFullMergedBuffer pins the overloaded teardown path.
//
// The copier forwards durable events with a blocking send, so a reader that
// stopped draining can hold it on a full buffer. stopCopy waits a short grace
// period, then aborts the copier. The queued events survive the subsequent
// close, but an event still in flight when the abort happens is dropped. That
// is safe because durable events are already in the session log, which the UI
// replays after the turn; this test fixes that semantic so a future change
// cannot silently turn it into a panic or a lost queue.
func TestTeardownWithAFullMergedBuffer(t *testing.T) {
	stream := newEventStream()
	want := make([]session.Event, eventStreamBuffer)
	for i := range want {
		want[i] = session.Event{Type: "test/queued", Seq: int64(i + 1)}
		stream.ch <- want[i]
	}

	sub := make(chan session.Event, 1)
	stream.copyLog(sub)
	sub <- session.Event{Type: "test/in-flight", Seq: int64(eventStreamBuffer + 1)}
	close(sub)

	done := make(chan struct{})
	go func() {
		stream.shutdown()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("teardown blocked on a full merged channel")
	}

	if got := len(stream.ch); got != eventStreamBuffer {
		t.Fatalf("queued events after teardown = %d, want %d (the in-flight event is the expected loss)",
			got, eventStreamBuffer)
	}
	for i := 0; i < eventStreamBuffer; i++ {
		ev, ok := <-stream.ch
		if !ok {
			t.Fatalf("merged channel closed with queued events left at index %d", i)
		}
		if ev.Type != want[i].Type || ev.Seq != want[i].Seq {
			t.Fatalf("queued event %d = %+v, want %+v", i, ev, want[i])
		}
	}
	if _, ok := <-stream.ch; ok {
		t.Fatal("merged channel is still open after shutdown")
	}
}
