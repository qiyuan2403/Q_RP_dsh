package shell

import (
	"strings"
	"testing"
)

func code(i int) *int { return new(i) }

// TestRenderSuccessOmitsMarkers pins that a clean run renders bare output.
func TestRenderSuccessOmitsMarkers(t *testing.T) {
	got := Render(RunResult{Status: ExitStatus{ExitCode: code(0)}, Stdout: Output{Text: "hello\n"}})
	if got != "hello\n" {
		t.Fatalf("got %q", got)
	}
}

// TestRenderNonzeroExitIsReportedNotErrored pins the 0004 lesson: a nonzero
// exit is an outcome the model reads, not an error.
func TestRenderNonzeroExitIsReportedNotErrored(t *testing.T) {
	got := Render(RunResult{Status: ExitStatus{ExitCode: code(1)}, Stderr: Output{Text: "no match\n"}})
	want := "no match\n[exit code: 1]"
	if !strings.HasSuffix(got, want) {
		t.Fatalf("got %q, want suffix %q", got, want)
	}
}

// TestRenderMarkerOrderPinsTheExitMarkerLast pins that the exit marker stays
// last: ParseExitStatus anchors there.
func TestRenderMarkerOrderPinsTheExitMarkerLast(t *testing.T) {
	got := Render(RunResult{
		Status:     ExitStatus{ExitCode: code(1), TimedOut: true, TimeoutMS: 60000},
		Stdout:     Output{Text: "partial"},
		Denied:     true,
		DeniedMode: "read-only",
	})
	want := "partial\n[sandbox: file access denied under read-only mode]\n" +
		"[timed out after 60000ms]\n[exit code: 1]"
	if got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
}

// TestRenderStderrSection pins the [stderr] section between streams.
func TestRenderStderrSection(t *testing.T) {
	got := Render(RunResult{Status: ExitStatus{ExitCode: code(0)}, Stdout: Output{Text: "out\n"}, Stderr: Output{Text: "err"}})
	want := "out\n[stderr]\nerr"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

// TestRenderEmptyOutput pins the (no output) placeholder.
func TestRenderEmptyOutput(t *testing.T) {
	got := Render(RunResult{Status: ExitStatus{ExitCode: code(0)}})
	if got != "(no output)" {
		t.Fatalf("got %q", got)
	}
}

// TestRenderStoppedIsNotACommandFailure pins that an outside stop renders its
// own marker telling the model not to retry.
func TestRenderStoppedIsNotACommandFailure(t *testing.T) {
	got := Render(RunResult{Status: ExitStatus{ExitCode: code(0), Stopped: "stopped by the user"}})
	if !strings.Contains(got, "[stopped: stopped by the user]") {
		t.Fatalf("got %q", got)
	}
}

// TestRenderSignalOverExit pins that a signal-killed run renders the signal
// marker, not an exit marker.
func TestRenderSignalOverExit(t *testing.T) {
	got := Render(RunResult{Status: ExitStatus{Signal: "SIGKILL"}})
	if !strings.HasSuffix(got, "[killed by signal: SIGKILL]") {
		t.Fatalf("got %q", got)
	}
	if strings.Contains(got, "[exit code") {
		t.Fatalf("signal run must not carry an exit marker: %q", got)
	}
}

// TestParseRoundTrip pins that ParseExitStatus recovers what Render wrote and
// strips the consumed marker from the body.
func TestParseRoundTrip(t *testing.T) {
	raw := Render(RunResult{Status: ExitStatus{ExitCode: code(2)}, Stdout: Output{Text: "body text"}})
	parsed := ParseExitStatus(raw)
	if parsed.ExitCode != 2 {
		t.Fatalf("ExitCode = %d, want 2", parsed.ExitCode)
	}
	if parsed.Body != "body text" {
		t.Fatalf("Body = %q, want %q", parsed.Body, "body text")
	}
	if parsed.Signal != "" {
		t.Fatalf("Signal = %q, want empty", parsed.Signal)
	}
}

// TestParseSignalRoundTrip pins the signal variant.
func TestParseSignalRoundTrip(t *testing.T) {
	raw := Render(RunResult{Status: ExitStatus{Signal: "SIGTERM"}, Stdout: Output{Text: "x"}})
	parsed := ParseExitStatus(raw)
	if parsed.Signal != "SIGTERM" || parsed.ExitCode != 0 {
		t.Fatalf("got %+v", parsed)
	}
}

// TestParseTreatsMarkerLikeTailAsOutput pins that ordinary output which merely
// ends with marker-like text is NOT consumed as a marker.
func TestParseTreatsMarkerLikeTailAsOutput(t *testing.T) {
	for _, text := range []string{
		"[exit code: 1]",          // no leading newline
		"x\n[exit code: 1]\n",     // not at the end
		"x\n[exit code: 1]\nmore", // not at the end
		"x\n[exit code: abc]",     // not a number
	} {
		parsed := ParseExitStatus(text)
		if parsed.ExitCode != 0 {
			t.Fatalf("%q: ExitCode = %d, want 0", text, parsed.ExitCode)
		}
	}
}

// TestParseCleanExit pins that a body without markers parses as exit 0.
func TestParseCleanExit(t *testing.T) {
	parsed := ParseExitStatus("plain output")
	if parsed.ExitCode != 0 || parsed.Body != "plain output" || parsed.Signal != "" {
		t.Fatalf("got %+v", parsed)
	}
}

// TestMatchDenialRequiresNonzeroExit pins the 0004 gate: exit 0 never matches,
// so a successful command whose output contains a signature word is not a denial.
func TestMatchDenialRequiresNonzeroExit(t *testing.T) {
	sigs := DenialSignatures{"operation not permitted"}
	if MatchDenial(code(0), "operation not permitted", sigs) {
		t.Fatal("exit 0 matched a denial")
	}
	if MatchDenial(nil, "operation not permitted", sigs) {
		t.Fatal("signal termination matched a denial")
	}
	if !MatchDenial(code(1), "Operation Not Permitted: /x", sigs) {
		t.Fatal("nonzero exit with signature did not match")
	}
}

// TestMatchDenialIgnoresEmptySignatures pins that a whitespace signature is
// not evidence.
func TestMatchDenialIgnoresEmptySignatures(t *testing.T) {
	if MatchDenial(code(1), "anything", DenialSignatures{"", "  "}) {
		t.Fatal("empty signature matched")
	}
}

// TestMatchRunnerFailureRequiresStructuredEvidence pins the 0004 fix shape:
// nonzero exit + exit-code gate + fatal line, with informational lines excluded.
func TestMatchRunnerFailureRequiresStructuredEvidence(t *testing.T) {
	// The 0004 scenario: a benign launcher notice precedes a child-controlled
	// nonzero exit. The substring-only rule would misreport it.
	rule := RunnerFailureRule{
		AllowedExitCodes:   []int{125},
		FatalSignatures:    []string{"landlock-run:"},
		InformationalLines: []string{"landlock-run: partial enforcement (older Landlock ABI)"},
	}
	stderr := "landlock-run: partial enforcement (older Landlock ABI)\n"
	if got := MatchRunnerFailure(code(1), stderr, []RunnerFailureRule{rule}); got != "" {
		t.Fatalf("benign notice + child exit 1 matched: %q", got)
	}
	got := MatchRunnerFailure(code(125), stderr+"landlock-run: fatal: no such profile\n", []RunnerFailureRule{rule})
	if got == "" {
		t.Fatal("fatal line on gated exit did not match")
	}
	if !strings.Contains(got, "fatal") {
		t.Fatalf("matched the wrong line: %q", got)
	}
}

// TestMatchRunnerFailureExitCodeGate pins that a rule gated on one exit code
// does not fire on another.
func TestMatchRunnerFailureExitCodeGate(t *testing.T) {
	rule := RunnerFailureRule{AllowedExitCodes: []int{125}, FatalSignatures: []string{"runner:"}}
	if got := MatchRunnerFailure(code(2), "runner: gone\n", []RunnerFailureRule{rule}); got != "" {
		t.Fatalf("ungated exit matched: %q", got)
	}
}
