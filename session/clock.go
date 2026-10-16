package session

import (
	"fmt"
	"sync/atomic"
	"time"
)

// nowMillis returns the current Unix epoch in milliseconds.
//
// Milliseconds, not nanoseconds: the log format is aligned to the official
// harness, which stamps `Date.now()`.
func nowMillis() int64 { return time.Now().UnixMilli() }

// NewID returns a fresh session id.
//
// The shape is `session-<millis>-<counter>`: the millis make ids sort by
// creation time, which is what a listing wants, and the counter keeps two
// sessions created in the same millisecond distinct.
func NewID() string {
	return fmt.Sprintf("session-%d-%d", nowMillis(), idCounter.Add(1))
}

var idCounter atomic.Uint64
