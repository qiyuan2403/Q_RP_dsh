package session

import "time"

// nowMillis returns the current Unix epoch in milliseconds.
//
// Milliseconds, not nanoseconds: the log format is aligned to the official
// harness, which stamps `Date.now()`.
func nowMillis() int64 { return time.Now().UnixMilli() }
