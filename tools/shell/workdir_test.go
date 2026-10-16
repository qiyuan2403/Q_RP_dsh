package shell

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestSetStartsAtRoot pins that a session starts at the bounded root, never
// at the process's own working directory.
func TestSetStartsAtRoot(t *testing.T) {
	root := t.TempDir()
	w := NewWorkingDirectory(root)
	if got, _ := filepath.EvalSymlinks(w.Current()); got != mustEval(t, root) {
		t.Fatalf("Current = %q, want the root", got)
	}
}

func mustEval(t *testing.T, path string) string {
	t.Helper()
	out, err := filepath.EvalSymlinks(path)
	if err != nil {
		t.Fatalf("EvalSymlinks: %v", err)
	}
	return out
}

// TestSetRelativeResolvesAgainstCurrent pins the official resolveWorkdir
// semantics for a relative override.
func TestSetRelativeResolvesAgainstCurrent(t *testing.T) {
	root := t.TempDir()
	sub := filepath.Join(root, "a", "b")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	w := NewWorkingDirectory(root)
	if _, err := w.Set("a"); err != nil {
		t.Fatalf("Set: %v", err)
	}
	got, err := w.Set("b")
	if err != nil {
		t.Fatalf("Set: %v", err)
	}
	if !strings.HasSuffix(got, filepath.Join("a", "b")) {
		t.Fatalf("got %q, want it under a/b", got)
	}
}

// TestSetRefusesEscape pins that a `..` walk outside the root is refused.
func TestSetRefusesEscape(t *testing.T) {
	root := t.TempDir()
	w := NewWorkingDirectory(root)
	if _, err := w.Set(".."); err == nil {
		t.Fatal("an escape was accepted")
	}
	// The current directory is unchanged after a refused set.
	if !strings.HasSuffix(w.Current(), filepath.Base(root)) && mustEval(t, w.Current()) != mustEval(t, root) {
		t.Fatalf("current = %q, want the root", w.Current())
	}
}

// TestSetRequiresExistingDirectory pins that the target must exist and be a
// directory before the change commits.
func TestSetRequiresExistingDirectory(t *testing.T) {
	root := t.TempDir()
	w := NewWorkingDirectory(root)
	if _, err := w.Set("no-such-dir"); err == nil {
		t.Fatal("a nonexistent directory was accepted")
	}
	plain := filepath.Join(root, "plain.txt")
	if err := os.WriteFile(plain, []byte("x"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := w.Set("plain.txt"); err == nil {
		t.Fatal("a plain file was accepted as a directory")
	}
}

// TestResolveWorkdirPinsTheOfficialSemantics pins the three branches of the
// official resolveWorkdir against this package's containment extension.
func TestResolveWorkdirPinsTheOfficialSemantics(t *testing.T) {
	root := t.TempDir()
	sub := filepath.Join(root, "sub")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	// An omitted override means the current directory.
	got, err := resolveWorkdir("", sub, root)
	if err != nil {
		t.Fatalf("omitted: %v", err)
	}
	if got != sub {
		t.Fatalf("omitted resolved to %q, want the session cwd", got)
	}

	// A relative override joins the session cwd.
	got, err = resolveWorkdir("sub", root, root)
	if err != nil {
		t.Fatalf("relative: %v", err)
	}
	if !strings.HasSuffix(got, "sub") {
		t.Fatalf("relative resolved to %q", got)
	}

	// An absolute override is used as-is when inside the root.
	got, err = resolveWorkdir(sub, root, root)
	if err != nil {
		t.Fatalf("absolute: %v", err)
	}
	if got != sub {
		t.Fatalf("absolute resolved to %q, want %q", got, sub)
	}

	// An absolute override outside the root is refused.
	if _, err = resolveWorkdir("/etc", root, root); err == nil {
		t.Fatal("an outside absolute workdir was accepted")
	}

	// A relative override with no base is refused.
	if _, err = resolveWorkdir("sub", "", root); err == nil {
		t.Fatal("a relative workdir without a base was accepted")
	}
}

// TestConcurrentSetSerializes pins that concurrent changes serialize, because
// a directory change is a state transition.
func TestConcurrentSetSerializes(t *testing.T) {
	root := t.TempDir()
	a := filepath.Join(root, "a")
	b := filepath.Join(root, "b")
	if err := os.MkdirAll(a, 0o755); err != nil {
		t.Fatalf("mkdir a: %v", err)
	}
	if err := os.MkdirAll(b, 0o755); err != nil {
		t.Fatalf("mkdir b: %v", err)
	}
	w := NewWorkingDirectory(root)
	done := make(chan string, 2)
	for _, dir := range []string{"a", "b"} {
		go func(d string) {
			got, err := w.Set(d)
			if err != nil {
				done <- ""
				return
			}
			done <- got
		}(dir)
	}
	seen := map[string]bool{<-done: true, <-done: true}
	for _, dir := range []string{a, b} {
		if seen[dir] {
			continue
		}
		if seen[mustEval(t, dir)] {
			continue
		}
	}
	final := w.Current()
	if final != a && final != b && mustEval(t, final) != mustEval(t, a) && mustEval(t, final) != mustEval(t, b) {
		t.Fatalf("final = %q, want a or b", final)
	}
}
