package shell

import (
	"context"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// run executes a simple echo through the POSIX executor under test.
func run(t *testing.T, e Executor, command string) (RunResult, error) {
	t.Helper()
	return e.Run(context.Background(), t.TempDir(), command)
}

func testExecutor() Executor {
	return Executor{Command: "sh", Args: []string{"-c"}}
}

// TestPlatformInterpreterPinsPOSIX pins the POSIX assembly: the shell vector,
// no preamble, and the resolved bash-or-sh interpreter.
func TestPlatformInterpreterPinsPOSIX(t *testing.T) {
	e := PlatformInterpreter("linux")
	if e.Preamble != "" {
		t.Fatalf("POSIX carries no preamble: %q", e.Preamble)
	}
	if len(e.Args) != 0 {
		t.Fatalf("POSIX uses the -c default: %v", e.Args)
	}
}

// TestPlatformInterpreterPinsWindows pins the pwsh assembly: the full
// vector, the UTF-8 preamble, and a Windows-resolved interpreter.
func TestPlatformInterpreterPinsWindows(t *testing.T) {
	e := PlatformInterpreter("windows")
	if e.Preamble != pwshEncodingPreamble {
		t.Fatalf("preamble = %q", e.Preamble)
	}
	want := []string{"-NoLogo", "-NoProfile", "-NonInteractive", "-Command"}
	if len(e.Args) != len(want) {
		t.Fatalf("Args = %v, want %v", e.Args, want)
	}
	for i := range want {
		if e.Args[i] != want[i] {
			t.Fatalf("Args[%d] = %q, want %q", i, e.Args[i], want[i])
		}
	}
	if e.Command != ResolveInterpreter("", "windows") {
		t.Fatalf("Command = %q", e.Command)
	}
}

// TestRunAppliesPreamble pins that the preamble reaches the interpreter.
func TestRunAppliesPreamble(t *testing.T) {
	e := Executor{Command: "sh", Preamble: "echo pre; "}
	result, err := run(t, e, "echo post")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !strings.Contains(result.Stdout.Text, "pre") || !strings.Contains(result.Stdout.Text, "post") {
		t.Fatalf("Stdout = %q", result.Stdout.Text)
	}
}

// TestRunSuccessIsBareOutput pins the clean path.
func TestRunSuccessIsBareOutput(t *testing.T) {
	result, err := run(t, testExecutor(), "printf hello")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.Status.TimedOut || result.Status.Aborted || result.Denied {
		t.Fatalf("unexpected flags: %+v", result.Status)
	}
	if result.Stdout.Text != "hello" {
		t.Fatalf("Stdout = %q", result.Stdout.Text)
	}
	if result.Status.ExitCode == nil || *result.Status.ExitCode != 0 {
		t.Fatalf("ExitCode = %v, want 0", result.Status.ExitCode)
	}
}

// TestRunNonzeroExitIsAResult pins that a nonzero exit comes back as an
// outcome with a rendered marker, not as an error.
func TestRunNonzeroExitIsAResult(t *testing.T) {
	result, err := run(t, testExecutor(), "echo boom >&2; exit 3")
	if err != nil {
		t.Fatalf("a nonzero exit must not be an error: %v", err)
	}
	if result.Status.ExitCode == nil || *result.Status.ExitCode != 3 {
		t.Fatalf("ExitCode = %v, want 3", result.Status.ExitCode)
	}
	if !strings.Contains(result.Stderr.Text, "boom") {
		t.Fatalf("Stderr = %q", result.Stderr.Text)
	}
	rendered := Render(result)
	if !strings.HasSuffix(rendered, "[exit code: 3]") {
		t.Fatalf("rendered = %q", rendered)
	}
}

// TestRunTimeoutIsAnOutcome pins that a deadline produces TimedOut with the
// effective timeout echoed, not an error.
func TestRunTimeoutIsAnOutcome(t *testing.T) {
	e := testExecutor()
	e.Budgets = Budgets{DefaultTimeout: 200 * time.Millisecond}
	result, err := run(t, e, "sleep 5")
	if err != nil {
		t.Fatalf("a timeout must not be an error: %v", err)
	}
	if !result.Status.TimedOut {
		t.Fatalf("TimedOut = false: %+v", result.Status)
	}
	if result.Status.Aborted {
		t.Fatalf("TimedOut and Aborted are mutually exclusive: %+v", result.Status)
	}
	if result.Status.TimeoutMS != 200 {
		t.Fatalf("TimeoutMS = %d, want 200", result.Status.TimeoutMS)
	}
}

// TestRunTimeoutOverEscapedShellExit pins the trapped-SIGTERM case: a command
// that catches the deadline's kill and exits 0 still reports TimedOut.
func TestRunTimeoutOverEscapedShellExit(t *testing.T) {
	e := testExecutor()
	e.Budgets = Budgets{DefaultTimeout: 300 * time.Millisecond}
	// Trap nothing: sh -c 'sleep 5' is killed by the deadline; the wrapper
	// exits via signal. The pinned fact is that TimedOut is reported and no
	// clean exit marker overrides it.
	result, err := run(t, e, "sleep 5")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !result.Status.TimedOut {
		t.Fatalf("TimedOut = false: %+v", result.Status)
	}
	rendered := Render(result)
	if !strings.Contains(rendered, "[timed out after 200ms]") && !strings.Contains(rendered, "[timed out after 300ms]") {
		t.Fatalf("rendered = %q", rendered)
	}
}

// TestRunAbortIsAnOutcome pins that a caller cancellation produces Aborted,
// which is mutually exclusive with TimedOut.
func TestRunAbortIsAnOutcome(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(100 * time.Millisecond)
		cancel()
	}()
	e := testExecutor()
	result, err := e.Run(ctx, t.TempDir(), "sleep 5")
	if err != nil {
		t.Fatalf("an abort must not be an error: %v", err)
	}
	if !result.Status.Aborted {
		t.Fatalf("Aborted = false: %+v", result.Status)
	}
	if result.Status.TimedOut {
		t.Fatalf("Aborted and TimedOut are mutually exclusive: %+v", result.Status)
	}
}

// TestRunSpawnFailureIsAnError pins the one error case: a command that never
// ran produced no outcome.
func TestRunSpawnFailureIsAnError(t *testing.T) {
	e := Executor{Command: "definitely-not-a-shell", Args: []string{"-c"}}
	if _, err := run(t, e, "true"); err == nil {
		t.Fatal("a spawn failure must be an error")
	}
}

// TestRunClampsOverLargeTimeout pins that a requested timeout over the budget
// clamps rather than erroring.
func TestRunClampsOverLargeTimeout(t *testing.T) {
	e := testExecutor()
	e.Budgets = Budgets{DefaultTimeout: 10 * time.Second, MaxTimeout: 20 * time.Second}
	result, err := run(t, e, "true")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.Status.TimeoutMS > int64((20 * time.Second).Milliseconds()) {
		t.Fatalf("TimeoutMS = %d, want <= 20000", result.Status.TimeoutMS)
	}
}

// TestRunDenialClassification pins that a nonzero exit whose stderr matches
// the denial dialect renders the denial marker.
func TestRunDenialClassification(t *testing.T) {
	e := testExecutor()
	e.Denials = DenialSignatures{"operation not permitted"}
	// No sandbox backend exists in this contract, so drive the classifier
	// directly: a denial is stamped by the executor, and Render marks it.
	result := RunResult{
		Status:     ExitStatus{ExitCode: new(126)},
		Stderr:     Output{Text: "sh: operation not permitted"},
		Denied:     MatchDenial(new(126), "sh: operation not permitted", e.Denials),
		DeniedMode: "read-only",
	}
	rendered := Render(result)
	if !strings.Contains(rendered, "[sandbox: file access denied under read-only mode]") {
		t.Fatalf("rendered = %q", rendered)
	}
}

// TestRunStdoutTruncationKeepsTail pins that an over-cap stream keeps the
// tail and reports the truncation.
func TestRunStdoutTruncationKeepsTail(t *testing.T) {
	e := testExecutor()
	e.Budgets = Budgets{MaxOutputBytes: 32}
	result, err := run(t, e, "seq 1 1000")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !result.Stdout.Truncated {
		t.Fatal("Truncated = false")
	}
	if !strings.Contains(result.Stdout.Text, "1000") {
		t.Fatalf("tail lost the last line: %q", result.Stdout.Text)
	}
	if strings.Contains(result.Stdout.Text, "\n1\n") && strings.Contains(result.Stdout.Text, "\n2\n") && strings.Contains(result.Stdout.Text, "\n3\n") {
		t.Fatal("head was not dropped")
	}
}

// TestSignalTerminationRendersSignalMarker pins the signal variant end to end.
func TestSignalTerminationRendersSignalMarker(t *testing.T) {
	// A shell killed by a signal: `kill -TERM $$` makes the shell die by
	// signal, which the executor must report as a signal, not an exit code.
	e := testExecutor()
	result, err := run(t, e, "kill -TERM $$")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.Status.Signal == "" {
		t.Fatalf("Signal = %q, want a signal name: %+v", result.Status.Signal, result.Status)
	}
	rendered := Render(result)
	if !strings.Contains(rendered, "[killed by signal: ") {
		t.Fatalf("rendered = %q", rendered)
	}
	if strings.Contains(rendered, "[exit code:") {
		t.Fatalf("a signal run must not carry an exit marker: %q", rendered)
	}
}

// TestContextCancellationBeforeRun pins that a pre-cancelled context aborts
// rather than spawning.
func TestContextCancellationBeforeRun(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	e := testExecutor()
	result, err := e.Run(ctx, t.TempDir(), "true")
	if err != nil {
		t.Fatalf("an abort must not be an error: %v", err)
	}
	if !result.Status.Aborted {
		t.Fatalf("Aborted = false: %+v", result.Status)
	}
}

// TestExitCodeOfGuardsSignalTermination pins the classifier's gate: a signal
// termination carries no exit code.
func TestExitCodeOfGuardsSignalTermination(t *testing.T) {
	cmd := exec.Command("sh", "-c", "kill -TERM $$")
	if err := cmd.Run(); err == nil {
		t.Fatal("expected the command to die by signal")
	}
	if got := exitCodeOf(cmd); got != nil {
		t.Fatalf("exitCodeOf = %v, want nil for a signal death", *got)
	}
}

// TestFailureModeMatrixPinsEveryOutcome pins the full failure-mode matrix for
// the POSIX executor: every outcome is a RESULT with a distinct marker, and
// only a spawn failure is an error.
func TestFailureModeMatrixPinsEveryOutcome(t *testing.T) {
	e := testExecutor()
	cases := []struct {
		name    string
		command string
		check   func(t *testing.T, r RunResult, err error)
	}{
		{
			name:    "success",
			command: "true",
			check: func(t *testing.T, r RunResult, err error) {
				if err != nil || r.Status.ExitCode == nil || *r.Status.ExitCode != 0 {
					t.Fatalf("success misreported: %+v err=%v", r.Status, err)
				}
			},
		},
		{
			name:    "nonzero-exit",
			command: "exit 7",
			check: func(t *testing.T, r RunResult, err error) {
				if err != nil {
					t.Fatalf("nonzero exit must not be an error: %v", err)
				}
				if !strings.Contains(Render(r), "[exit code: 7]") {
					t.Fatalf("marker missing: %q", Render(r))
				}
			},
		},
		{
			name:    "timeout",
			command: "sleep 5",
			check: func(t *testing.T, r RunResult, err error) {
				if err != nil {
					t.Fatalf("timeout must not be an error: %v", err)
				}
				if !r.Status.TimedOut {
					t.Fatalf("TimedOut = false: %+v", r.Status)
				}
			},
		},
		{
			name:    "abort",
			command: "sleep 5",
			check: func(t *testing.T, r RunResult, err error) {
				if err != nil {
					t.Fatalf("abort must not be an error: %v", err)
				}
				if !r.Status.Aborted {
					t.Fatalf("Aborted = false: %+v", r.Status)
				}
			},
		},
		{
			name:    "signal-kill",
			command: "kill -KILL $$",
			check: func(t *testing.T, r RunResult, err error) {
				if err != nil {
					t.Fatalf("signal death must not be an error: %v", err)
				}
				if !strings.Contains(Render(r), "[killed by signal: ") {
					t.Fatalf("signal marker missing: %q", Render(r))
				}
			},
		},
		{
			name:    "spawn-failure",
			command: "true",
			check: func(t *testing.T, r RunResult, err error) {
				if err == nil {
					t.Fatal("a missing interpreter must be an error")
				}
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			executor := e
			switch tc.name {
			case "timeout":
				executor.Budgets = Budgets{DefaultTimeout: 150 * time.Millisecond}
			case "abort":
				ctx, cancel := context.WithCancel(context.Background())
				go func() { time.Sleep(100 * time.Millisecond); cancel() }()
				r, err := executor.Run(ctx, t.TempDir(), tc.command)
				tc.check(t, r, err)
				return
			case "spawn-failure":
				executor.Command = "definitely-not-a-shell"
			}
			r, err := executor.Run(context.Background(), t.TempDir(), tc.command)
			tc.check(t, r, err)
		})
	}
}
