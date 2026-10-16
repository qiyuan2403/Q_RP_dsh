package shell

import (
	"bytes"
	"context"
	"os/exec"
	"syscall"
	"time"
)

// Budgets bound one foreground run. The values mirror the official
// bash-local defaults: a 120s default deadline, a 10-minute ceiling on
// per-call overrides, and a 64 KiB in-memory cap per stream.
//
// Unlike the official executor this contract has no spill files: a stream
// over the cap keeps the tail in memory and reports Truncated. A spill
// directory is a deployment concern the caller can add.
type Budgets struct {
	// DefaultTimeout bounds one run when the caller asks for none.
	DefaultTimeout time.Duration
	// MaxTimeout is the largest per-call override accepted. A larger
	// requested timeout clamps to this rather than erroring: the model
	// guessing a big number must not fail the call.
	MaxTimeout time.Duration
	// MaxOutputBytes caps one captured stream's in-memory tail.
	MaxOutputBytes int
	// Env is the managed `DSH_*` overlay merged over the scrubbed parent
	// base and the model-friendly overrides. Nil means the standard
	// environment. Host credentials are NEVER inherited by default: the
	// scrub in Run owns that guarantee, and this field only ADDS trusted
	// harness facts.
	Env map[string]string
}

func (b Budgets) withDefaults() Budgets {
	out := b
	if out.DefaultTimeout <= 0 {
		out.DefaultTimeout = 120 * time.Second
	}
	if out.MaxTimeout <= 0 {
		out.MaxTimeout = 10 * time.Minute
	}
	if out.MaxOutputBytes <= 0 {
		out.MaxOutputBytes = 128 << 10
	}
	return out
}

// Executor runs one foreground command and returns its outcome as a
// RunResult, never as an error, unless the run could not start at all.
//
// THE CONTRACT (ported from the official packages/shell/shell):
//   - A nonzero exit, a timeout, and an abort are RESULTS with distinct
//     markers; only infrastructure failures are errors.
//   - TimedOut and Aborted are mutually exclusive: one fused deadline drives
//     both, and the first cause wins.
//   - Stopped marks an outside kill; the model reads the reason and must not
//     retry.
type Executor struct {
	// Command is the interpreter: "bash" on POSIX, "pwsh" on Windows, and
	// Android's "/system/bin/sh" (mksh) where no bash exists.
	Command string
	// Args is the interpreter's command vector; nil defaults to ["-c"]. A
	// Windows executor passes the pwsh vector instead.
	Args []string
	// Preamble is prepended to every command. The official pwsh-local uses
	// it to pin UTF-8 output: Windows PowerShell 5.1, the last-resort
	// executable, writes the console/OEM code page by default and garbles
	// non-ASCII output; pwsh 7 defaults to UTF-8. The statements ride on
	// line 1 after "; " separators so error line numbers stay accurate.
	Preamble string
	// Budgets bound the run.
	Budgets Budgets
	// Denials classify a failed run as a policy decision. Empty disables
	// denial classification, which is correct for an unsandboxed executor.
	Denials DenialSignatures
	// RunnerRules classify a failed run as a broken confinement runner.
	RunnerRules []RunnerFailureRule
}

// PlatformInterpreter returns the Executor for the platform the build
// targets: bash on POSIX, mksh through /system/bin/sh on Android, and pwsh
// with the UTF-8 preamble on Windows. Exactly one executor is mounted per
// composition; a second mount fails loudly rather than silently replacing.
func PlatformInterpreter(goos string) Executor {
	switch goos {
	case "windows":
		return Executor{
			Command:  ResolveInterpreter("", "windows"),
			Args:     []string{"-NoLogo", "-NoProfile", "-NonInteractive", "-Command"},
			Preamble: pwshEncodingPreamble,
		}
	case "android":
		return Executor{Command: ResolveInterpreter("", "android")}
	default:
		// POSIX shares one candidate list; the caller names its platform key.
		return Executor{Command: ResolveInterpreter("", "posix")}
	}
}

// pwshEncodingPreamble is the official ENCODING_PREAMBLE: UTF-8 output
// pinning prepended to every Windows command.
const pwshEncodingPreamble = "[Console]::OutputEncoding = [System.Text.UTF8Encoding]::new($false); " +
	"$OutputEncoding = [System.Text.UTF8Encoding]::new($false); "

// Run executes the command and resolves with the outcome.
//
// The spawn error is returned as an error because a command that never ran
// produced no outcome: `bash -c` missing, the working directory gone. A run
// that started has an outcome, whatever its exit.
func (e Executor) Run(parent context.Context, workdir string, command string) (RunResult, error) {
	budgets := e.Budgets.withDefaults()
	runCtx := parent
	effective := budgets.DefaultTimeout
	if effective > budgets.MaxTimeout {
		effective = budgets.MaxTimeout
	}
	var cancel context.CancelFunc
	runCtx, cancel = context.WithTimeout(parent, effective)
	defer cancel()

	args := e.Args
	if args == nil {
		args = []string{"-c"}
	}
	if e.Preamble != "" {
		command = e.Preamble + command
	}
	cmd := exec.CommandContext(runCtx, e.Command, append(append([]string{}, args...), command)...)
	cmd.Dir = workdir
	// THE ENVIRONMENT. A command starts from the scrubbed parent base
	// (credentials and ambient DSH_* removed), plus the model-friendly
	// overrides, plus the managed DSH_* overlay when the caller supplied one.
	// A nil Budgets.Env means "the standard environment", never "inherit
	// everything": an ambient credential must not reach a shell implicitly.
	cmd.Env = MergedEnv(budgets.Env)

	stdout := newLimitedBuffer(budgets.MaxOutputBytes)
	stderr := newLimitedBuffer(budgets.MaxOutputBytes)
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	runErr := cmd.Run()
	// THE CONTRACT: a run that started has an OUTCOME, whatever its exit.
	// Only a spawn failure (the command never ran) is an error, because there
	// is nothing for the model to read. exec.Cmd reports both as err, so tell
	// them apart by whether a process was ever started. A pre-cancelled
	// context is the one abort that never spawns: it is an outcome (Aborted),
	// because the model asked for a command and got no run, which is a fact
	// it can act on rather than an infrastructure fault.
	var result RunResult
	if runErr != nil && cmd.ProcessState == nil && parent.Err() == nil {
		return result, runErr
	}
	result = RunResult{
		Status:     e.classify(parent, cmd, runCtx, effective),
		Stdout:     stdout.Output(),
		Stderr:     stderr.Output(),
		Denied:     MatchDenial(exitCodeOf(cmd), stderr.String(), e.Denials),
		DeniedMode: "", // An unsandboxed executor carries no mode fact.
	}
	if result.Denied {
		result.DeniedMode = "command"
	}
	return result, nil
}

// classify maps the process outcome onto the first-cause exit status.
//
// The deadline is fused: the executor's own timeout and the caller's
// cancellation both cancel runCtx, and classify tells them apart by the
// deadline's error versus the parent's. A process that traps SIGTERM and
// exits 0 after the deadline still reports TimedOut: the interruption is
// the fact the model needs.
func (e Executor) classify(parent context.Context, cmd *exec.Cmd, runCtx context.Context, effective time.Duration) ExitStatus {
	status := ExitStatus{TimeoutMS: effective.Milliseconds()}
	switch {
	case runCtx.Err() == context.DeadlineExceeded && parent.Err() != context.Canceled:
		status.TimedOut = true
	case parent.Err() == context.Canceled:
		status.Aborted = true
	}
	if cmd.ProcessState != nil {
		if sig, ok := cmd.ProcessState.Sys().(syscall.WaitStatus); ok && sig.Signaled() {
			// A signal termination carries the signal name; an exit carries
			// its code. The two are mutually exclusive facts.
			status.Signal = sig.Signal().String()
		}
		if status.Signal == "" && status.TimedOut {
			// The deadline killed the process: report the interruption even
			// when the shell exited 0 after trapping SIGTERM.
			status.ExitCode = nil
		} else if status.Signal == "" {
			status.ExitCode = new(cmd.ProcessState.ExitCode())
		}
	}
	if cmd.ProcessState == nil && (status.TimedOut || status.Aborted) {
		// Killed during preparation: no process existed to produce a status.
		status.ExitCode = nil
	}
	return status
}

// exitCodeOf extracts the exit code for the denial classifiers, which gate on
// a nonzero exit; signal termination carries no code.
func exitCodeOf(cmd *exec.Cmd) *int {
	if cmd.ProcessState == nil {
		return nil
	}
	code := cmd.ProcessState.ExitCode()
	if code < 0 {
		// A negative code means signal termination: no exit code fact.
		return nil
	}
	return new(code)
}

// limitedBuffer collects one stream up to a cap. Over the cap it keeps the
// TAIL (the official contract: long output is truncated to its tail) and
// reports the truncation.
type limitedBuffer struct {
	buf     bytes.Buffer
	cap     int
	written int64
	cut     bool
}

func newLimitedBuffer(cap int) *limitedBuffer { return &limitedBuffer{cap: cap} }

func (b *limitedBuffer) Write(p []byte) (int, error) {
	b.written += int64(len(p))
	if b.cap <= 0 {
		b.buf.Write(p)
		return len(p), nil
	}
	b.buf.Write(p)
	if b.buf.Len() > b.cap {
		b.cut = true
		// Keep the TAIL, not the head: the official contract truncates long
		// output to its tail, because the end of a stream carries what the
		// command finished with. Leave the copy bounded so a huge line
		// cannot grow the buffer without limit.
		keep := b.cap / 2
		if keep > 0 {
			s := b.buf.Bytes()
			copy(s, s[len(s)-keep:])
			b.buf.Truncate(keep)
		}
	}
	return len(p), nil
}

func (b *limitedBuffer) String() string { return b.buf.String() }

func (b *limitedBuffer) Output() Output {
	return Output{Text: b.buf.String(), Truncated: b.cut}
}
