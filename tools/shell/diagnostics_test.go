package shell

import (
	"strings"
	"testing"
)

// TestPostmortem0004Equivalence replays the official post-mortem 0004
// scenario end to end: a benign launcher notice precedes a child-controlled
// nonzero exit, and the classification must NOT misreport it as a runner
// failure.
func TestPostmortem0004Equivalence(t *testing.T) {
	// The official incident: a partial-ABI Landlock launcher prints the
	// notice before every child; ripgrep's no-match is exit 1.
	const notice = "landlock-run: partial enforcement (older Landlock ABI)"
	rules := []RunnerFailureRule{{
		AllowedExitCodes:   []int{125},
		FatalSignatures:    []string{"landlock-run: "},
		InformationalLines: []string{notice},
	}}

	// The 0004 defect shape: substring + any nonzero exit.
	for _, tc := range []struct {
		name     string
		exitCode *int
		stderr   string
	}{
		{"ripgrep no-match exit 1", new(1), notice},
		{"invalid-pattern exit 2", new(2), notice},
		{"child-selected exit 125 without fatal line", new(125), notice + "\nchild output\n"},
	} {
		if got := MatchRunnerFailure(tc.exitCode, tc.stderr, rules); got != "" {
			t.Fatalf("%s: misreported as runner failure: %q", tc.name, got)
		}
	}

	// A real launcher failure: fatal line on the gated exit code.
	fatal := MatchRunnerFailure(new(125), notice+"\nlandlock-run: fatal: profile missing\n", rules)
	if fatal == "" || !strings.Contains(fatal, "fatal") {
		t.Fatalf("a real runner failure was missed: %q", fatal)
	}
}

// TestPostmortem0004DenialVsRunner pins the two classifications against each
// other: a denial and a runner failure are different facts a model must not
// confuse.
func TestPostmortem0004DenialVsRunner(t *testing.T) {
	exitCode := new(1)
	stderr := "landlock-run: partial enforcement (older Landlock ABI)\n"
	// Denial gate: nonzero exit matches the dialect substring...
	if !MatchDenial(exitCode, stderr, DenialSignatures{"landlock-run: "}) {
		t.Fatal("denial gate unexpectedly closed")
	}
	// ...but the structured runner rule requires the gated fatal line, so the
	// two never conflate: this run is a child outcome, not infrastructure.
	rules := []RunnerFailureRule{{
		AllowedExitCodes:   []int{125},
		FatalSignatures:    []string{"landlock-run: "},
		InformationalLines: []string{stderr},
	}}
	if got := MatchRunnerFailure(exitCode, stderr, rules); got != "" {
		t.Fatalf("child outcome misreported as runner failure: %q", got)
	}
}

// TestEmptyInformationalLinesPinsNoBypass pins that an empty informational
// line cannot bypass the fatal match.
func TestEmptyInformationalLinesPinsNoBypass(t *testing.T) {
	rules := []RunnerFailureRule{{
		FatalSignatures:    []string{"runner:"},
		InformationalLines: []string{"", "  "},
	}}
	if got := MatchRunnerFailure(new(1), "runner: gone\n", rules); got == "" {
		t.Fatal("blank informational lines bypassed the fatal match")
	}
}
