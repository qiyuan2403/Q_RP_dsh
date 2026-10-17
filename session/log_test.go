package session

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func newLog(t *testing.T) (*Log, string) {
	t.Helper()
	dir := t.TempDir()
	log, err := Create(dir, Header{ID: "s-1", CreatedAt: 1000, CWD: "/w"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	t.Cleanup(func() { _ = log.Close() })
	return log, dir
}

// TestAppendAssignsSeqAndTime pins that the log stamps the sequence and the
// timestamp, so a caller never has to.
func TestAppendAssignsSeqAndTime(t *testing.T) {
	log, _ := newLog(t)
	for i := 1; i <= 3; i++ {
		got, err := log.Append(Event{Type: "step/start", Data: json.RawMessage(`{"step":1}`)})
		if err != nil {
			t.Fatalf("Append: %v", err)
		}
		if got.Seq != int64(i) {
			t.Fatalf("Seq = %d, want %d", got.Seq, i)
		}
		if got.Time == 0 {
			t.Fatal("Time was not stamped")
		}
	}
}

// TestAppendRefusesUnknownRequiredType pins the fail-loud rule: a type this
// build does not know is refused unless the caller marked it ignorable, because
// writing one would produce a log this build cannot read back.
func TestAppendRefusesUnknownRequiredType(t *testing.T) {
	log, _ := newLog(t)
	_, err := log.Append(Event{Type: "not-a-real/event", Data: json.RawMessage(`{}`)})
	if !errors.Is(err, ErrUnknownEventType) {
		t.Fatalf("error = %v, want ErrUnknownEventType", err)
	}

	// The same type with the ignorable marker is accepted.
	if _, err := log.Append(Event{Type: "not-a-real/event", Data: json.RawMessage(`{}`), Ignorable: true}); err != nil {
		t.Fatalf("an ignorable unknown type was refused: %v", err)
	}
}

// TestReadRefusesUnknownRequiredType pins the read side of the same rule.
func TestReadRefusesUnknownRequiredType(t *testing.T) {
	dir := t.TempDir()
	if _, err := Create(dir, Header{ID: "s", CreatedAt: 1}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	// Write a line with an unknown, non-ignorable type directly, simulating a
	// log from a newer harness.
	path := filepath.Join(dir, LogFilename)
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	_, _ = f.WriteString(`{"type":"future/event","seq":1,"time":1,"data":{}}` + "\n")
	_ = f.Close()

	_, err = ReadLog(dir)
	if !errors.Is(err, ErrUnknownEventType) {
		t.Fatalf("error = %v, want ErrUnknownEventType", err)
	}
}

// TestReadAcceptsIgnorableUnknownType pins that an event marked ignorable does
// not block reconstruction.
func TestReadAcceptsIgnorableUnknownType(t *testing.T) {
	dir := t.TempDir()
	if _, err := Create(dir, Header{ID: "s", CreatedAt: 1}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	path := filepath.Join(dir, LogFilename)
	f, _ := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	_, _ = f.WriteString(`{"type":"future/event","seq":1,"time":1,"data":{},"ignorable":true}` + "\n")
	_ = f.Close()

	events, err := ReadLog(dir)
	if err != nil {
		t.Fatalf("ReadLog: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("got %d events, want 1", len(events))
	}
}

// TestFormatVersionMismatchRefused pins that a log from another format version
// is refused rather than guessed at.
func TestFormatVersionMismatchRefused(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, HeaderFilename),
		[]byte(`{"version":99,"id":"s","createdAt":1,"isSeeded":false}`), 0o600); err != nil {
		t.Fatalf("write header: %v", err)
	}
	if _, err := ReadLog(dir); !errors.Is(err, ErrFormatVersion) {
		t.Fatalf("error = %v, want ErrFormatVersion", err)
	}
	if _, err := Open(dir); !errors.Is(err, ErrFormatVersion) {
		t.Fatalf("Open error = %v, want ErrFormatVersion", err)
	}
}

// TestReopenResumesSequence pins that reopening continues the numbering rather
// than restarting it, which would break monotonicity.
func TestReopenResumesSequence(t *testing.T) {
	log, dir := newLog(t)
	for range 3 {
		if _, err := log.Append(Event{Type: "step/start", Data: json.RawMessage(`{}`)}); err != nil {
			t.Fatalf("Append: %v", err)
		}
	}
	if err := log.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	reopened, err := Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer reopened.Close()
	got, err := reopened.Append(Event{Type: "step/start", Data: json.RawMessage(`{}`)})
	if err != nil {
		t.Fatalf("Append after reopen: %v", err)
	}
	if got.Seq != 4 {
		t.Fatalf("Seq = %d, want 4 (continuing the existing log)", got.Seq)
	}
}

// TestNonMonotonicLogRefused pins that a hand-edited log is caught at open,
// before more is appended on top of an untrustworthy order.
func TestNonMonotonicLogRefused(t *testing.T) {
	dir := t.TempDir()
	if _, err := Create(dir, Header{ID: "s", CreatedAt: 1}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	path := filepath.Join(dir, LogFilename)
	f, _ := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	_, _ = f.WriteString(`{"type":"step/start","seq":5,"time":1,"data":{}}` + "\n")
	_, _ = f.WriteString(`{"type":"step/start","seq":2,"time":2,"data":{}}` + "\n")
	_ = f.Close()

	if _, err := Open(dir); err == nil {
		t.Fatal("a non-monotonic log was opened")
	}
}

// TestRoundTripPreservesEvents pins that what is appended is what is read.
func TestRoundTripPreservesEvents(t *testing.T) {
	log, dir := newLog(t)
	payload := json.RawMessage(`{"callId":"c1","name":"read","arguments":"{}"}`)
	if _, err := log.Append(Event{Type: "tool/call", Data: payload}); err != nil {
		t.Fatalf("Append: %v", err)
	}
	if err := log.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	events, err := ReadLog(dir)
	if err != nil {
		t.Fatalf("ReadLog: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("got %d events, want 1", len(events))
	}
	if events[0].Type != "tool/call" {
		t.Fatalf("Type = %q", events[0].Type)
	}
	if string(events[0].Data) != string(payload) {
		t.Fatalf("Data = %s, want %s", events[0].Data, payload)
	}
}

// TestHeaderIsWrittenBeforeAnyEvent pins the ordering guarantee: a crash cannot
// leave a log whose metadata is missing.
func TestHeaderIsWrittenBeforeAnyEvent(t *testing.T) {
	dir := t.TempDir()
	if _, err := Create(dir, Header{ID: "s", CreatedAt: 42, CWD: "/work"}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, HeaderFilename))
	if err != nil {
		t.Fatalf("header missing: %v", err)
	}
	var h Header
	if err := json.Unmarshal(raw, &h); err != nil {
		t.Fatalf("header not JSON: %v", err)
	}
	if h.Version != FormatVersion {
		t.Fatalf("Version = %d, want %d", h.Version, FormatVersion)
	}
	if h.ID != "s" || h.CWD != "/work" {
		t.Fatalf("header = %+v", h)
	}
}

// TestCreateRequiresIdentity pins fail-loud on a header without an id or a
// creation time.
func TestCreateRequiresIdentity(t *testing.T) {
	if _, err := Create(t.TempDir(), Header{CreatedAt: 1}); err == nil {
		t.Fatal("a header without an id was accepted")
	}
	if _, err := Create(t.TempDir(), Header{ID: "s"}); err == nil {
		t.Fatal("a header without createdAt was accepted")
	}
}

// TestAppendRejectsInvalidJSONData pins that a payload that is not JSON is
// refused at write time rather than producing an unreadable line.
func TestAppendRejectsInvalidJSONData(t *testing.T) {
	log, _ := newLog(t)
	if _, err := log.Append(Event{Type: "step/start", Data: json.RawMessage(`{not json`)}); err == nil {
		t.Fatal("invalid JSON data was accepted")
	}
}

// TestConcurrentAppendsAreSerialized pins that concurrent writers produce a
// monotonic log rather than interleaved lines.
func TestConcurrentAppendsAreSerialized(t *testing.T) {
	log, dir := newLog(t)
	const n = 50
	var wg sync.WaitGroup
	for range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := log.Append(Event{Type: "step/start", Data: json.RawMessage(`{}`)}); err != nil {
				t.Errorf("Append: %v", err)
			}
		}()
	}
	wg.Wait()
	if err := log.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	events, err := ReadLog(dir)
	if err != nil {
		t.Fatalf("ReadLog: %v", err)
	}
	if len(events) != n {
		t.Fatalf("got %d events, want %d", len(events), n)
	}
	for i, e := range events {
		if e.Seq != int64(i+1) {
			t.Fatalf("event %d has seq %d, want %d", i, e.Seq, i+1)
		}
	}
}

// TestSurfaceIntentRoundTrips pins that a surface event's intent survives, which
// is what lets a projection fold the message list.
func TestSurfaceIntentRoundTrips(t *testing.T) {
	log, dir := newLog(t)
	op := json.RawMessage(`{"op":"replace","start":2,"end":4}`)
	if _, err := log.Append(Event{
		Type:            "assistant/message",
		Data:            json.RawMessage(`{"text":"hi"}`),
		SurfaceOp:       op,
		SourceEventSeqs: []int64{1, 2},
	}); err != nil {
		t.Fatalf("Append: %v", err)
	}
	_ = log.Close()

	events, err := ReadLog(dir)
	if err != nil {
		t.Fatalf("ReadLog: %v", err)
	}
	if string(events[0].SurfaceOp) != string(op) {
		t.Fatalf("SurfaceOp = %s, want %s", events[0].SurfaceOp, op)
	}
	if len(events[0].SourceEventSeqs) != 2 {
		t.Fatalf("SourceEventSeqs = %v", events[0].SourceEventSeqs)
	}
}

// TestVocabularyIsPinned pins the vocabulary's size and a few members, so an
// accidental edit to the compatibility surface is caught.
func TestVocabularyIsPinned(t *testing.T) {
	// 62 is the official KNOWN_SESSION_EVENT_TYPES member count; a change here
	// means the compatibility surface drifted.
	if got := len(KnownEventTypes); got != 62 {
		t.Fatalf("KnownEventTypes has %d members, want 62", got)
	}
	// The surface set has exactly five members upstream.
	if got := len(SurfaceEventTypes); got != 5 {
		t.Fatalf("SurfaceEventTypes has %d members, want 5", got)
	}
	if IsSurface("tool/call") {
		t.Error("tool/call is not a surface event: its result is what the model sees")
	}
	if !IsSurface("tool/result") {
		t.Error("tool/result should be a surface event")
	}
	for _, name := range []string{"turn/start", "turn/end", "step/start", "step/end", "tool/call", "tool/result", "user/message", "assistant/message"} {
		if !IsKnown(name) {
			t.Errorf("%q should be a known event type", name)
		}
	}
	if IsKnown("definitely-not-an-event") {
		t.Error("an invented type is reported as known")
	}
	if FormatVersion != 4 {
		t.Fatalf("FormatVersion = %d, want 4", FormatVersion)
	}
}

// TestEventsReadsTheLiveLog pins that a live log's reader returns the events it
// appended, including the buffered tail. The regression this guards is a path
// bug: l.path is already the events file, so joining LogFilename onto it again
// asked for "<dir>/events.jsonl/events.jsonl" and failed with ENOTDIR. Every
// live reader (the Android session list and replay) depended on this working.
func TestEventsReadsTheLiveLog(t *testing.T) {
	log, dir := newLog(t)
	if _, err := log.Append(Event{Type: "user/message", Data: json.RawMessage(`{"text":"hi"}`)}); err != nil {
		t.Fatalf("Append: %v", err)
	}
	events, err := log.Events()
	if err != nil {
		t.Fatalf("Events: %v", err)
	}
	if len(events) != 1 || events[0].Type != "user/message" {
		t.Fatalf("Events = %+v, want one user/message", events)
	}
	// The directory reader and the live reader must agree: they are the same
	// file, and a disagreement would mean one view is stale.
	fromDisk, err := ReadLog(dir)
	if err != nil {
		t.Fatalf("ReadLog: %v", err)
	}
	if len(fromDisk) != len(events) || fromDisk[0].Seq != events[0].Seq {
		t.Fatalf("disk %+v disagrees with live %+v", fromDisk, events)
	}
	// A closed log still answers from the file rather than reporting a
	// closed-writer error: readers outlive the writer.
	if err := log.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	after, err := log.Events()
	if err != nil {
		t.Fatalf("Events after close: %v", err)
	}
	if len(after) != 1 {
		t.Fatalf("Events after close = %+v, want one event", after)
	}
}
