package session

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
)

// Errors reported by the log reader.
var (
	// ErrUnknownEventType reports an event whose type this build does not know
	// and which is not marked ignorable.
	//
	// Refusing is deliberate: an unrecognized REQUIRED event may change how the
	// rest of the log is interpreted, so skipping it would reconstruct a
	// different conversation. The official reader behaves the same way.
	ErrUnknownEventType = errors.New("session: log contains an unknown required event type")
	// ErrFormatVersion reports a log written by a different format version.
	ErrFormatVersion = errors.New("session: unsupported format version")
)

// Header is the session's immutable metadata, kept OUTSIDE the event log.
//
// Separating it means the log stays a pure append-only event stream, and the
// metadata is validated once rather than re-derived from an event.
type Header struct {
	// Version is the logical format version.
	Version int `json:"version"`
	// ID is the session id.
	ID string `json:"id"`
	// CreatedAt is Unix epoch milliseconds.
	CreatedAt int64 `json:"createdAt"`
	// CWD is the absolute working directory the session was created in.
	CWD string `json:"cwd,omitempty"`
	// ParentSession is the session this one was forked from, if any.
	ParentSession string `json:"parentSession,omitempty"`
	// IsSeeded reports whether this session contains a fork-inherited prefix.
	IsSeeded bool `json:"isSeeded"`
	// Origin classifies a subagent child session.
	Origin string `json:"origin,omitempty"`
	// DelegationDepth is the subagent nesting depth; absent means top level.
	//
	// It is persisted so a recursion budget survives a restart: a runtime-only
	// depth would reset a resumed child to top level.
	DelegationDepth *int `json:"delegationDepth,omitempty"`
	// AgentPreset is the preset this session's agent was composed from.
	//
	// Durable because the preset decides the session's tools and prompt: a
	// resume that restored a different composition would replay history the
	// model can no longer act on.
	AgentPreset string `json:"agentPreset,omitempty"`
}

// Event is one record in the log.
//
// Field names are the wire vocabulary and must not change: a log written here
// is read by the official harness and vice versa.
type Event struct {
	// Type is the event type, e.g. "turn/start".
	Type string `json:"type"`
	// Seq is the monotonic sequence number within the session.
	Seq int64 `json:"seq"`
	// Time is Unix epoch milliseconds.
	Time int64 `json:"time"`
	// Data is the event payload.
	Data json.RawMessage `json:"data"`
	// Ignorable marks an event a reader may safely skip when it does not
	// recognize Type.
	//
	// Absent means REQUIRED. A writer sets it only on purely informational
	// records: defaulting to required makes a forgotten marker over-refuse,
	// which is an inconvenience, rather than silently resume a gutted session.
	Ignorable bool `json:"ignorable,omitempty"`
	// SurfaceOp is the surface intent for a surface event: "append", or an
	// object describing a replacement over a range.
	SurfaceOp json.RawMessage `json:"surfaceOp,omitempty"`
	// SourceEventSeqs traces a derived event back to the events it summarizes.
	SourceEventSeqs []int64 `json:"sourceEventSeqs,omitempty"`
}

// Log is an append-only session event log backed by one JSONL file.
//
// It is also the live event source for readers: a subscriber registered with
// Subscribe receives every event as it is appended. That is what lets a UI
// follow a running turn without polling the file, and it keeps the log the one
// authority for what happened — a reader never reconstructs state from a
// separate channel that could disagree with the file.
type Log struct {
	mu     sync.Mutex
	path   string
	file   *os.File
	writer *bufio.Writer
	header Header
	// seq is the last written sequence number.
	seq int64
	// closed reports whether the log has been closed.
	closed bool
	// subs are the live subscribers, keyed by id so removal is O(1) and a
	// subscriber that unsubscribes during delivery does not corrupt the walk.
	subs map[int64]chan Event
	// nextSubID hands out subscriber ids.
	nextSubID int64
}

// Subscribe returns a channel receiving every event appended after this call,
// and a function that unsubscribes and closes the channel.
//
// The channel is buffered but bounded: a reader that falls behind loses events
// rather than stalling the agent loop, because blocking an append on a slow
// reader would let a UI freeze a turn. A dropped event is recoverable — the log
// file is the durable record, and a reader that needs certainty re-reads it —
// whereas a stalled append is not.
func (l *Log) Subscribe(buffer int) (<-chan Event, func()) {
	if buffer <= 0 {
		buffer = 64
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		ch := make(chan Event)
		close(ch)
		return ch, func() {}
	}
	if l.subs == nil {
		l.subs = make(map[int64]chan Event)
	}
	l.nextSubID++
	id := l.nextSubID
	ch := make(chan Event, buffer)
	l.subs[id] = ch

	var once sync.Once
	return ch, func() {
		once.Do(func() {
			l.mu.Lock()
			defer l.mu.Unlock()
			if c, ok := l.subs[id]; ok {
				delete(l.subs, id)
				close(c)
			}
		})
	}
}

// publish delivers an event to every subscriber.
//
// The caller must NOT hold l.mu: a subscriber may append to the log in
// response to an event, and delivering under the lock would deadlock that
// re-entry. Delivery therefore happens after the lock is released, which is
// also why publish takes a snapshot of the channels.
func (l *Log) publish(e Event) {
	l.mu.Lock()
	subs := make([]chan Event, 0, len(l.subs))
	for _, ch := range l.subs {
		subs = append(subs, ch)
	}
	l.mu.Unlock()

	for _, ch := range subs {
		select {
		case ch <- e:
		default:
			// The subscriber is behind. Drop rather than block: the log file
			// still holds the event, so a reader can recover by re-reading.
		}
	}
}

// HeaderFilename is the metadata file's name beside the log.
const HeaderFilename = "header.json"

// LogFilename is the event log's name.
const LogFilename = "events.jsonl"

// Create initializes a session directory and returns its log.
//
// The header is written first and fsynced before any event, so a crash cannot
// leave a log whose metadata is missing.
func Create(dir string, header Header) (*Log, error) {
	if header.ID == "" {
		return nil, errors.New("session: header id is required")
	}
	if header.CreatedAt == 0 {
		return nil, errors.New("session: header createdAt is required")
	}
	header.Version = FormatVersion
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	raw, err := json.MarshalIndent(header, "", "  ")
	if err != nil {
		return nil, err
	}
	raw = append(raw, '\n')
	if err := writeFileSync(filepath.Join(dir, HeaderFilename), raw); err != nil {
		return nil, err
	}
	return Open(dir)
}

// Open opens an existing session directory for appending.
func Open(dir string) (*Log, error) {
	headerPath := filepath.Join(dir, HeaderFilename)
	raw, err := os.ReadFile(headerPath)
	if err != nil {
		return nil, fmt.Errorf("session: reading header: %w", err)
	}
	var header Header
	if err := json.Unmarshal(raw, &header); err != nil {
		return nil, fmt.Errorf("session: header is not valid JSON: %w", err)
	}
	if header.Version != FormatVersion {
		return nil, fmt.Errorf("%w: log is version %d, this build reads %d", ErrFormatVersion, header.Version, FormatVersion)
	}

	logPath := filepath.Join(dir, LogFilename)
	// Scan the existing log so the sequence counter resumes correctly and a
	// corrupt tail is detected before more is appended.
	last, err := scanLog(logPath)
	if err != nil {
		return nil, err
	}

	f, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil, err
	}
	return &Log{
		path:   logPath,
		file:   f,
		writer: bufio.NewWriter(f),
		header: header,
		seq:    last,
	}, nil
}

// Header returns the session's metadata.
func (l *Log) Header() Header { return l.header }

// Path returns the log file's path.
func (l *Log) Path() string { return l.path }

// Append writes one event, assigning its sequence number and timestamp when
// they are zero.
//
// An unknown type is refused unless the caller marked it ignorable: writing a
// type this build does not understand would produce a log it cannot read back.
func (l *Log) Append(e Event) (Event, error) {
	e, err := l.append(e)
	if err != nil {
		return Event{}, err
	}
	// Published outside the lock: a subscriber may append in response, and
	// holding the lock across delivery would deadlock that re-entry.
	l.publish(e)
	return e, nil
}

// append writes one event under the lock and returns it. It does not publish;
// Append does, after the lock is released.
func (l *Log) append(e Event) (Event, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return Event{}, errors.New("session: log is closed")
	}
	if e.Type == "" {
		return Event{}, errors.New("session: event type is required")
	}
	if !IsKnown(e.Type) && !e.Ignorable {
		return Event{}, fmt.Errorf("%w: %q", ErrUnknownEventType, e.Type)
	}
	if e.Data == nil {
		e.Data = json.RawMessage("null")
	}
	if !json.Valid(e.Data) {
		return Event{}, fmt.Errorf("session: event %q data is not valid JSON", e.Type)
	}
	l.seq++
	e.Seq = l.seq
	if e.Time == 0 {
		e.Time = nowMillis()
	}

	line, err := json.Marshal(e)
	if err != nil {
		return Event{}, err
	}
	line = append(line, '\n')
	if _, err := l.writer.Write(line); err != nil {
		return Event{}, err
	}
	return e, nil
}

// Flush writes buffered events to the file and fsyncs.
//
// A caller that needs durability at a specific point (before reporting success
// to a user, before a tool with side effects) calls this; otherwise the buffer
// amortizes the syscalls.
func (l *Log) Flush() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return nil
	}
	if err := l.writer.Flush(); err != nil {
		return err
	}
	return l.file.Sync()
}

// Close flushes and closes the log, then closes every subscriber's channel.
//
// Closing the channels is what tells a reader the log has ended: a receiver
// sees the close and stops waiting, instead of blocking forever on a log that
// will never produce another event.
func (l *Log) Close() error {
	l.mu.Lock()
	if l.closed {
		l.mu.Unlock()
		return nil
	}
	l.closed = true
	for id, ch := range l.subs {
		delete(l.subs, id)
		close(ch)
	}
	err := l.writer.Flush()
	if err == nil {
		err = l.file.Sync()
	}
	closeErr := l.file.Close()
	l.mu.Unlock()
	if err != nil {
		return err
	}
	return closeErr
}

// ReadLog reads every event from a session directory.
//
// The whole log is validated before any event is returned: a partially returned
// conversation would be indistinguishable from a complete one to the caller.
func ReadLog(dir string) ([]Event, error) {
	raw, err := os.ReadFile(filepath.Join(dir, HeaderFilename))
	if err != nil {
		return nil, fmt.Errorf("session: reading header: %w", err)
	}
	var header Header
	if err := json.Unmarshal(raw, &header); err != nil {
		return nil, fmt.Errorf("session: header is not valid JSON: %w", err)
	}
	if header.Version != FormatVersion {
		return nil, fmt.Errorf("%w: log is version %d, this build reads %d", ErrFormatVersion, header.Version, FormatVersion)
	}
	return readEvents(filepath.Join(dir, LogFilename))
}

func readEvents(path string) ([]Event, error) {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	defer f.Close()

	var events []Event
	scanner := bufio.NewScanner(f)
	// Events can carry large payloads (a tool result, an image reference), so
	// the default 64 KiB line limit is raised.
	scanner.Buffer(make([]byte, 0, 64*1024), 16<<20)
	lineNo := 0
	for scanner.Scan() {
		lineNo++
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}
		var e Event
		if err := json.Unmarshal(line, &e); err != nil {
			return nil, fmt.Errorf("session: line %d is not valid JSON: %w", lineNo, err)
		}
		if !IsKnown(e.Type) && !e.Ignorable {
			return nil, fmt.Errorf("%w: %q at line %d", ErrUnknownEventType, e.Type, lineNo)
		}
		events = append(events, e)
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("session: reading log: %w", err)
	}
	return events, nil
}

// scanLog returns the last sequence number and validates the existing log.
func scanLog(path string) (int64, error) {
	events, err := readEvents(path)
	if err != nil {
		return 0, err
	}
	if len(events) == 0 {
		return 0, nil
	}
	last := events[len(events)-1].Seq
	// A non-monotonic sequence means the file was edited or truncated; appending
	// on top of it would produce a log whose order cannot be trusted.
	for i, e := range events {
		if i > 0 && e.Seq <= events[i-1].Seq {
			return 0, fmt.Errorf("session: sequence is not monotonic at line %d (seq %d after %d)", i+1, e.Seq, events[i-1].Seq)
		}
	}
	return last, nil
}

// writeFileSync writes a file and fsyncs it, so a crash cannot leave a
// zero-length header.
func writeFileSync(path string, data []byte) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}

// Discard drains a reader, used where a body must be consumed but not kept.
func Discard(r io.Reader) { _, _ = io.Copy(io.Discard, r) }

// Events returns every event recorded so far. It flushes the write buffer
// first: an appended event that still sits in the buffer IS recorded — the
// live view must agree with the file, and a reader that misses the tail
// would render a conversation the log says happened.
func (l *Log) Events() ([]Event, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	// l.path IS the event log file (see Open), so it is passed through as-is.
	// Joining LogFilename onto it again reads "<dir>/events.jsonl/events.jsonl"
	// and fails with ENOTDIR, which would break every live log reader.
	if l.closed {
		return readEvents(l.path)
	}
	if err := l.writer.Flush(); err != nil {
		return nil, err
	}
	return readEvents(l.path)
}
