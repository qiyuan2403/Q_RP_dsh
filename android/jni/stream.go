package main

import (
	"context"
	"encoding/json"
	"sync"
	"sync/atomic"
	"time"

	"github.com/qiyuan2403/Q_RP_dsh/agent"
	"github.com/qiyuan2403/Q_RP_dsh/session"
)

// assistantStreamEventType is the event type the app receives for live
// assistant output. It is deliberately absent from session's event vocabulary:
// these frames are process-local notifications, not facts. A log containing one
// would give replay a second, non-durable source of truth for the same reply.
const assistantStreamEventType = "agent/assistant-stream"

// Buffer sizes for the two channels a turn feeds.
//
// The merged channel is the only one the app polls, so it has to absorb both
// the handful of durable events a turn writes and the burst of stream frames a
// fast model produces between two polls.
const (
	eventStreamBuffer     = 256
	logSubscriptionBuffer = 128
)

// droppedAssistantStreamFrames counts frames discarded because the merged
// channel was full. It is process-wide rather than per turn so a test can read
// it without holding a reference to the turn's stream; the count is diagnostic,
// not a control input.
var droppedAssistantStreamFrames atomic.Int64

// eventStream is one session's merged live channel: durable log events plus the
// unlogged assistant-stream frames of the running turn.
//
// The two sources get different delivery policies because they carry different
// kinds of truth. A log event is durable and is forwarded with a blocking send:
// losing it live means the UI silently skips a recorded step, while blocking
// only delays the copier goroutine, never the agent. A frame is a transient
// hint and is sent without blocking: a slow observer must not stall the model
// call that produces it. A dropped frame costs a few visible tokens, and the
// durable assistant/message that follows still reaches the UI when the turn
// ends and the log is replayed.
type eventStream struct {
	// mu guards closed and the frame send. It is held across the non-blocking
	// send so close cannot race a frame into a closed channel.
	mu     sync.Mutex
	ch     chan session.Event
	closed bool
	// stop aborts the log copier when a reader has stopped draining the merged
	// channel and the copier is blocked on a full buffer.
	stop chan struct{}
	wg   sync.WaitGroup
}

// newEventStream returns an empty merged channel for one turn.
func newEventStream() *eventStream {
	return &eventStream{
		ch:   make(chan session.Event, eventStreamBuffer),
		stop: make(chan struct{}),
	}
}

// publishFrame wraps one assistant-stream frame as a temporary event and offers
// it to the merged channel.
//
// The send is non-blocking on purpose: the agent calls this synchronously from
// the turn goroutine, so blocking here would let a slow JNI reader stall the
// model call itself. Dropping is safe because frames are not durable; the final
// assistant/message is in the session log and the UI replays it after the turn.
func (s *eventStream) publishFrame(frame agent.AssistantStreamFrame) {
	ev := session.Event{
		Type: assistantStreamEventType,
		Time: time.Now().UnixMilli(),
		Data: mustJSON(frame),
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	select {
	case s.ch <- ev:
	default:
		droppedAssistantStreamFrames.Add(1)
	}
}

// copyLog starts forwarding the durable log subscription into the merged
// channel. The subscription is owned by the caller and must be ended with its
// unsubscribe function before stopCopy is used.
func (s *eventStream) copyLog(sub <-chan session.Event) {
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		for ev := range sub {
			// The stop case only matters when a reader has gone away and the
			// merged buffer is full: without it teardown would wait forever for
			// a send nobody will receive. The events left in the subscription
			// are durable in the log, so the UI can replay them.
			select {
			case s.ch <- ev:
			case <-s.stop:
				return
			}
		}
	}()
}

// stopCopy ends the log copier and waits for it to finish.
//
// The subscription is closed first by the caller, so normally the copier drains
// what it already received and exits at once. A short grace period lets that
// happen before the abort path is taken; the abort exists only so a full merged
// channel cannot keep the turn's goroutine from returning.
func (s *eventStream) stopCopy() {
	done := make(chan struct{})
	go func() {
		s.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
		return
	case <-time.After(100 * time.Millisecond):
		close(s.stop)
		<-done
	}
}

// close closes the merged channel. It must run after stopCopy, so no copier is
// left to send on it; closing it is what tells pollEvent the stream has ended.
func (s *eventStream) close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	s.closed = true
	close(s.ch)
}

// shutdown tears a stream down in the order the transport requires: stop the
// copier first, then close the merged channel the app polls.
func (s *eventStream) shutdown() {
	s.stopCopy()
	s.close()
}

// turnCancel is one running turn's cancellation handle.
//
// The registry entries are pointers rather than bare CancelFunc values so
// teardown can prove ownership by identity. Comparing funcs is not possible in
// Go, and comparing session ids alone is exactly the bug that let an older
// turn's cleanup delete a newer turn's registration.
type turnCancel struct {
	cancel context.CancelFunc
}

// cancelRegistry holds the cancel handle of every running turn, keyed by
// session id. Cancelling through the map rather than through a per-turn handle
// is what lets the JNI call name nothing but the session the app already knows.
var (
	cancelMu       sync.Mutex
	cancelRegistry = map[string]*turnCancel{}
)

// registerTurnCancel makes a running turn cancellable by session id and returns
// the turn's own entry.
//
// It is called immediately after the per-session turn slot is claimed, before
// the slow preparation (node probe, toolset, history replay), so a cancel that
// arrives while the turn is still preparing is not lost. The returned entry is
// passed back to unregister so cleanup can only remove this turn's handle.
func registerTurnCancel(sessionID string, cancel context.CancelFunc) *turnCancel {
	entry := &turnCancel{cancel: cancel}
	cancelMu.Lock()
	defer cancelMu.Unlock()
	cancelRegistry[sessionID] = entry
	return entry
}

// unregisterTurnCancel drops entry, but only while it is still the registered
// one. A later turn for the same session must keep its handle even if an
// earlier turn's teardown is delayed past the later turn's start.
func unregisterTurnCancel(sessionID string, entry *turnCancel) {
	cancelMu.Lock()
	defer cancelMu.Unlock()
	if cancelRegistry[sessionID] == entry {
		delete(cancelRegistry, sessionID)
	}
}

// cancelTurnForSession cancels the running turn of sessionID, if any.
//
// A missing entry is not an error: the turn may have finished between the
// app's decision to cancel and this call, and the app has no way to tell the
// two apart. Doing nothing is the correct answer for an already-finished turn.
// Calling it repeatedly is safe: one context.CancelFunc tolerates concurrent
// and duplicate calls, and the entry may already be gone by the second call.
func cancelTurnForSession(sessionID string) {
	cancelMu.Lock()
	entry := cancelRegistry[sessionID]
	cancelMu.Unlock()
	if entry != nil {
		entry.cancel()
	}
}

// mustJSON encodes an event payload for transport.
//
// A value that cannot encode becomes JSON null rather than removing the event:
// the event type still tells the app what happened, and dropping it silently
// would leave the merged stream shorter than the log it mirrors.
func mustJSON(v any) json.RawMessage {
	raw, err := json.Marshal(v)
	if err != nil {
		return json.RawMessage("null")
	}
	return raw
}
