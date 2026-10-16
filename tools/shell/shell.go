// Package shell is the executor contract the model-facing shell tools run on.
//
// WHY A CONTRACT RATHER THAN A RAW EXEC RESULT. The official harness
// (packages/shell/shell) learned from post-mortem 0004 that a failed run is a
// RESULT the model must be able to reason about, not an exception to swallow.
// A nonzero exit, a timeout, and an abort are outcomes with distinct markers;
// only infrastructure failures (spawn errors) are errors. This package ports
// that contract: the result type, the exit markers the model reads, and the
// denial/runner-failure classifiers that keep a policy denial from being
// mistaken for a broken tool.
package shell

import (
	"fmt"
	"strings"
)

// ExitStatus is the outcome of one foreground run.
type ExitStatus struct {
	// ExitCode is the process exit code; nil when the process died from a
	// signal or was killed before it started.
	ExitCode *int
	// Signal is the terminating signal, if any.
	Signal string
	// TimedOut is true when the executor's own deadline was the FIRST cause
	// to cut the run short. Mutually exclusive with Aborted: one fused
	// deadline drives both the timeout and the caller's cancellation, so a
	// timeout and an abort racing before the process closes report the single
	// first-abort cause.
	TimedOut bool
	// Aborted is true when the caller's context cancellation was the FIRST
	// cause to kill the run, and it was not the executor's own timeout.
	Aborted bool
	// TimeoutMS is the effective timeout applied to this run.
	TimeoutMS int64
	// Stopped is the reason an outside actor (the human, a job kill) stopped
	// the run, if one did. A stop is not a command failure: the reason tells
	// the model not to retry.
	Stopped string
}

// Output is one captured stream with its truncation fact.
type Output struct {
	Text      string
	Truncated bool
}

// RunResult is the outcome of one foreground run.
type RunResult struct {
	Status ExitStatus
	Stdout Output
	Stderr Output
	// Denied marks a policy denial, classified by the executor's diagnostics.
	Denied bool
	// DeniedMode is the mode a denial happened under.
	DeniedMode string
}

const (
	// MarkExit is appended when the process exited nonzero.
	MarkExit = "[exit code: %d]"
	// MarkSignal is appended when the process died from a signal.
	MarkSignal = "[killed by signal: %s]"
	// MarkTimedOut is appended when the deadline killed the command. A command
	// may trap SIGTERM and exit 0 after the deadline; the marker still reports
	// the interruption.
	MarkTimedOut = "[timed out after %dms]"
	// MarkStopped is appended when an outside actor stopped the run.
	MarkStopped = "[stopped: %s]"
	// MarkDenied is appended for a policy denial: the model must not retry the
	// command another way.
	MarkDenied = "[sandbox: file access denied under %s mode]"
)

// Render shapes one finished run into the text the model reads: stdout, then a
// marked stderr section, then exit-status markers, each on its own line. The
// exit marker stays LAST because ParseExitStatus anchors there.
//
// Nonzero exits are reported, not errored: the model decides how to react.
func Render(r RunResult) string {
	var body strings.Builder
	body.WriteString(r.Stdout.Text)
	if r.Stdout.Truncated {
		body.WriteString("\n[output truncated]")
	}
	if r.Stderr.Text != "" {
		s := body.String()
		if s != "" && !strings.HasSuffix(s, "\n") {
			body.WriteString("\n")
		}
		body.WriteString("[stderr]\n")
		body.WriteString(r.Stderr.Text)
		if r.Stderr.Truncated {
			body.WriteString("\n[output truncated]")
		}
	}
	text := body.String()
	if text == "" {
		text = "(no output)"
	}

	var markers []string
	if r.Denied {
		markers = append(markers, fmt.Sprintf(MarkDenied, r.DeniedMode))
	}
	if r.Status.TimedOut {
		markers = append(markers, fmt.Sprintf(MarkTimedOut, r.Status.TimeoutMS))
	}
	if r.Status.Stopped != "" {
		markers = append(markers, fmt.Sprintf(MarkStopped, r.Status.Stopped))
	}
	if r.Status.Signal != "" {
		markers = append(markers, fmt.Sprintf(MarkSignal, r.Status.Signal))
	} else if r.Status.ExitCode != nil && *r.Status.ExitCode != 0 {
		markers = append(markers, fmt.Sprintf(MarkExit, *r.Status.ExitCode))
	}
	if len(markers) == 0 {
		return text
	}
	if !strings.HasSuffix(text, "\n") {
		text += "\n"
	}
	return text + strings.Join(markers, "\n")
}

// ParsedExitStatus is the status recovered from a rendered result.
type ParsedExitStatus struct {
	// Body is the output with the consumed marker removed, so a terminal
	// presentation does not render the exit status twice.
	Body string
	// Signal is the terminating signal, when the run was killed by one.
	Signal string
	// ExitCode is the exit code; zero when neither marker matched.
	ExitCode int
}

// ParseExitStatus splits a rendered result into its output body and structured
// exit status: the inverse of the markers Render appends. A killed marker
// wins over an exit marker; absent both means a clean exit 0.
//
// Requiring a leading newline and the end of the string keeps ordinary output
// that merely ends with marker-like text from matching unless the final line
// is indistinguishable from a real marker.
func ParseExitStatus(text string) ParsedExitStatus {
	if s, ok := lastMarker(text, "[killed by signal: ", ']'); ok {
		return ParsedExitStatus{Body: text[:s.start], Signal: s.value}
	}
	if s, ok := lastMarker(text, "[exit code: ", ']'); ok {
		if code := parseExitCode(s.value); code >= 0 {
			return ParsedExitStatus{Body: text[:s.start], ExitCode: code}
		}
	}
	return ParsedExitStatus{Body: text, ExitCode: 0}
}

type markerSpan struct {
	start int
	value string
}

// lastMarker finds "\n<prefix>...<suffix>" exactly at the end of the text.
func lastMarker(text, prefix string, suffix byte) (markerSpan, bool) {
	idx := strings.LastIndex(text, "\n"+prefix)
	if idx < 0 {
		return markerSpan{}, false
	}
	rest := text[idx+1+len(prefix):]
	if len(rest) == 0 || rest[len(rest)-1] != suffix || strings.Contains(rest[:len(rest)-1], "\n") {
		return markerSpan{}, false
	}
	return markerSpan{start: idx, value: rest[:len(rest)-1]}, true
}

func parseExitCode(s string) int {
	if s == "" {
		return -1
	}
	n := 0
	for _, c := range s {
		if c < '0' || c > '9' {
			return -1
		}
		n = n*10 + int(c-'0')
	}
	return n
}
