package shell

import (
	"os"
	"strings"
	"testing"
)

func envMap(entries []string) map[string]string {
	m := map[string]string{}
	for _, e := range entries {
		name, value, _ := strings.Cut(e, "=")
		m[name] = value
	}
	return m
}

// TestScrubRemovesCredentialShapedNames pins that KEY/PASSWORD/SECRET/TOKEN
// shaped names never reach a child implicitly.
func TestScrubRemovesCredentialShapedNames(t *testing.T) {
	t.Setenv("DEEPSEEK_API_KEY", "k")
	t.Setenv("MY_PASSWORD", "p")
	t.Setenv("GITHUB_TOKEN", "t")
	t.Setenv("SECRET_PATH", "s")
	t.Setenv("PATH", os.Getenv("PATH"))
	got := envMap(ScrubbedParentEnv())
	for _, name := range []string{"DEEPSEEK_API_KEY", "MY_PASSWORD", "GITHUB_TOKEN", "SECRET_PATH"} {
		if _, exists := got[name]; exists {
			t.Fatalf("%s survived the scrub", name)
		}
	}
	if _, exists := got["PATH"]; !exists {
		t.Fatal("PATH did not survive: child CLIs would break")
	}
}

// TestScrubRemovesDSHNames pins that ambient DSH_* facts never leak
// implicitly, case-insensitively.
func TestScrubRemovesDSHNames(t *testing.T) {
	t.Setenv("DSH_HOME", "/ambient")
	t.Setenv("dsh_profile", "ambient")
	t.Setenv("PATH", os.Getenv("PATH"))
	got := envMap(ScrubbedParentEnv())
	if _, exists := got["DSH_HOME"]; exists {
		t.Fatal("DSH_HOME survived the scrub")
	}
	if _, exists := got["dsh_profile"]; exists {
		t.Fatal("lowercase dsh_ entry survived the scrub")
	}
}

// TestScrubKeepsOrdinaryNames pins that locale and ordinary variables
// survive, so child CLIs run normally.
func TestScrubKeepsOrdinaryNames(t *testing.T) {
	t.Setenv("LC_ALL", "C")
	t.Setenv("EDITOR", "vi")
	got := envMap(ScrubbedParentEnv())
	if got["LC_ALL"] != "C" {
		t.Fatalf("LC_ALL = %q", got["LC_ALL"])
	}
	if got["EDITOR"] != "vi" {
		t.Fatalf("EDITOR = %q", got["EDITOR"])
	}
}

// TestMergedEnvAppliesOverrides pins that a command whose base lacks an
// override gets it, and that the managed overlay wins over the base.
func TestMergedEnvAppliesOverrides(t *testing.T) {
	got := envMap(MergedEnv(map[string]string{"DSH_HOME": "/managed"}))
	if got["NO_COLOR"] != "1" {
		t.Fatalf("NO_COLOR = %q", got["NO_COLOR"])
	}
	if got["TERM"] != "dumb" {
		t.Fatalf("TERM = %q", got["TERM"])
	}
	if got["DSH_HOME"] != "/managed" {
		t.Fatalf("DSH_HOME = %q, want the managed overlay to win", got["DSH_HOME"])
	}
}

// TestMergedEnvManagedWinsOverAmbient pins that an ambient DSH_ entry cannot
// shadow the managed overlay: the scrub removes it first, so the managed
// value is the only one left.
func TestMergedEnvManagedWinsOverAmbient(t *testing.T) {
	t.Setenv("DSH_HOME", "/ambient")
	got := envMap(MergedEnv(map[string]string{"DSH_HOME": "/managed"}))
	if got["DSH_HOME"] != "/managed" {
		t.Fatalf("DSH_HOME = %q", got["DSH_HOME"])
	}
}

// TestMergedEnvPAGEROverrideDoesNotShadowAnExplicitCaller pins the merge
// order: a trusted caller's own PAGER survives, because an explicit entry in
// the ambient base wins over the override.
func TestMergedEnvPAGEROverrideDoesNotShadowAnExplicitCaller(t *testing.T) {
	t.Setenv("PAGER", "less")
	got := envMap(MergedEnv(nil))
	if got["PAGER"] != "less" {
		t.Fatalf("PAGER = %q, want the caller's own entry to win", got["PAGER"])
	}
}

// TestMergedEnvIsDeterministic pins that the same inputs produce the same
// environment across calls, so a command does not drift.
func TestMergedEnvIsDeterministic(t *testing.T) {
	managed := map[string]string{"DSH_HOME": "/h"}
	first := strings.Join(MergedEnv(managed), "\n")
	second := strings.Join(MergedEnv(managed), "\n")
	if first != second {
		t.Fatal("the merged environment drifted between calls")
	}
}
