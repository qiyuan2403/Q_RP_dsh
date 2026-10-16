package session

import (
	"encoding/json"
	"testing"
	"time"
)

// TestSubscribeReceivesAppendedEvents pins that a reader follows the log live:
// every event appended after Subscribe arrives, in order.
func TestSubscribeReceivesAppendedEvents(t *testing.T) {
	log, _ := newLog(t)
	ch, unsub := log.Subscribe(8)
	defer unsub()

	for i := 1; i <= 3; i++ {
		if _, err := log.Append(Event{Type: "step/start", Data: json.RawMessage(`{"step":1}`)}); err != nil {
			t.Fatalf("Append: %v", err)
		}
	}

	for want := int64(1); want <= 3; want++ {
		select {
		case got := <-ch:
			if got.Seq != want {
				t.Fatalf("Seq = %d, want %d", got.Seq, want)
			}
			if got.Type != "step/start" {
				t.Fatalf("Type = %q, want step/start", got.Type)
			}
		case <-time.After(time.Second):
			t.Fatalf("no event %d arrived", want)
		}
	}
}

// TestSubscribeDeliversAfterTheLockIsReleased pins the re-entrancy contract: a
// subscriber that appends in response to an event must not deadlock. This is
// the case a naive "deliver while holding the lock" implementation hangs on.
func TestSubscribeDeliversAfterTheLockIsReleased(t *testing.T) {
	log, _ := newLog(t)
	ch, unsub := log.Subscribe(8)
	defer unsub()

	done := make(chan struct{})
	go func() {
		defer close(done)
		// React to the first event by appending another.
		first := <-ch
		if _, err := log.Append(Event{Type: "step/end", Data: json.RawMessage(`{}`)}); err != nil {
			t.Errorf("re-entrant Append: %v", err)
		}
		_ = first
	}()

	if _, err := log.Append(Event{Type: "step/start", Data: json.RawMessage(`{}`)}); err != nil {
		t.Fatalf("Append: %v", err)
	}

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("a subscriber appending in response to an event deadlocked")
	}
}

// TestUnsubscribeStopsDeliveryAndCloses pins that unsubscribing ends the
// stream: the reader sees a closed channel rather than waiting forever.
func TestUnsubscribeStopsDeliveryAndCloses(t *testing.T) {
	log, _ := newLog(t)
	ch, unsub := log.Subscribe(8)
	unsub()

	if _, open := <-ch; open {
		t.Fatal("the channel is still open after unsubscribe")
	}

	// Appending after unsubscribe must not panic on a closed channel.
	if _, err := log.Append(Event{Type: "step/start", Data: json.RawMessage(`{}`)}); err != nil {
		t.Fatalf("Append after unsubscribe: %v", err)
	}
}

// TestUnsubscribeIsIdempotent pins that a caller may defer unsub and also call
// it directly without a double close.
func TestUnsubscribeIsIdempotent(t *testing.T) {
	log, _ := newLog(t)
	_, unsub := log.Subscribe(8)
	unsub()
	unsub()
}

// TestCloseClosesSubscriberChannels pins that a reader learns the log ended
// instead of blocking on a log that will never produce another event.
func TestCloseClosesSubscriberChannels(t *testing.T) {
	log, _ := newLog(t)
	ch, unsub := log.Subscribe(8)
	defer unsub()

	if err := log.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if _, open := <-ch; open {
		t.Fatal("the channel is still open after the log closed")
	}
}

// TestSubscribeOnAClosedLogReturnsAClosedChannel pins the race where a reader
// subscribes to a log that has already ended.
func TestSubscribeOnAClosedLogReturnsAClosedChannel(t *testing.T) {
	log, _ := newLog(t)
	if err := log.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	ch, unsub := log.Subscribe(8)
	defer unsub()
	if _, open := <-ch; open {
		t.Fatal("subscribing to a closed log returned an open channel")
	}
}

// TestSlowSubscriberDoesNotBlockAppend pins the backpressure policy: a reader
// that never drains must not stall the agent loop. The event is dropped, not
// awaited, because the log file remains the durable record.
func TestSlowSubscriberDoesNotBlockAppend(t *testing.T) {
	log, _ := newLog(t)
	// Buffer of 1, then never read: the second append must not block.
	_, unsub := log.Subscribe(1)
	defer unsub()

	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 100; i++ {
			if _, err := log.Append(Event{Type: "step/start", Data: json.RawMessage(`{}`)}); err != nil {
				t.Errorf("Append: %v", err)
				return
			}
		}
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("a slow subscriber blocked Append")
	}
}
