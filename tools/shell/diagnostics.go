package shell

import (
	"strings"
)

// RunnerFailureRule is one backend's structured runner-failure dialect.
//
// THE 0004 RULE. A substring plus any nonzero exit is NOT runner evidence:
// on hosts with an older Landlock ABI the launcher prints a benign
// "partial enforcement" notice before every child, so ripgrep's no-match
// exit 1 carried that prefix and was misreported as SANDBOX_UNAVAILABLE.
// The rule therefore requires a nonzero exit, its optional exit-code gate,
// and a fatal signature on ONE stderr line after exact informational lines
// are excluded. Exit status alone never proves runner failure.
//
// Ported from the official packages/sandbox/sandbox/src/diagnostics.ts.
type RunnerFailureRule struct {
	// AllowedExitCodes are the nonzero exit codes on which this rule may
	// match; omitted permits any nonzero exit.
	AllowedExitCodes []int
	// FatalSignatures are non-empty substrings identifying a fatal runner
	// diagnostic on one stderr line.
	FatalSignatures []string
	// InformationalLines are benign stderr lines excluded by exact full-line
	// equality before fatal matching.
	InformationalLines []string
}

// DenialSignatures are case-insensitive stderr substrings identifying the
// active wrap's denial dialect. A denial is a POLICY DECISION the model must
// not retry another way; a runner failure is an infrastructure fault.
type DenialSignatures []string

// MatchDenial reports whether a failed run matches the denial dialect.
//
// The gate is the exit code: exit 0 and signal termination never match, so a
// successful command whose output happens to contain a signature word is not
// a denial. Ported from the official matchesSignature.
func MatchDenial(exitCode *int, stderr string, signatures DenialSignatures) bool {
	if exitCode == nil || *exitCode == 0 {
		return false
	}
	lowered := strings.ToLower(stderr)
	for _, signature := range signatures {
		if signature == "" {
			continue
		}
		if strings.Contains(lowered, strings.ToLower(signature)) {
			return true
		}
	}
	return false
}

// MatchRunnerFailure returns the first matching fatal stderr line, or an empty
// string when the evidence is insufficient.
//
// Each rule requires a nonzero exit, its optional exit-code gate, and a fatal
// signature on one stderr line after exact informational lines are excluded.
// A signature that is empty or whitespace is not meaningful evidence and is
// ignored while any valid signatures beside it stay active.
//
// Ported from the official classifyRunnerFailure: the fix for post-mortem 0004.
func MatchRunnerFailure(exitCode *int, stderr string, rules []RunnerFailureRule) string {
	if exitCode == nil || *exitCode == 0 {
		return ""
	}
	lines := strings.Split(stderr, "\n")
	for _, rule := range rules {
		if len(rule.AllowedExitCodes) > 0 {
			gated := false
			for _, code := range rule.AllowedExitCodes {
				if code == *exitCode {
					gated = true
					break
				}
			}
			if !gated {
				continue
			}
		}
		informational := make(map[string]bool, len(rule.InformationalLines))
		for _, line := range rule.InformationalLines {
			informational[strings.ToLower(line)] = true
		}
		var fatal []string
		for _, signature := range rule.FatalSignatures {
			if strings.TrimSpace(signature) == "" {
				continue
			}
			fatal = append(fatal, strings.ToLower(signature))
		}
		for _, line := range lines {
			lowered := strings.ToLower(line)
			if informational[lowered] {
				continue
			}
			for _, signature := range fatal {
				if strings.Contains(lowered, signature) {
					return line
				}
			}
		}
	}
	return ""
}
