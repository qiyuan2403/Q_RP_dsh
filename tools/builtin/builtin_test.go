package builtintools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/qiyuan2403/Q_RP_dsh/tools"
)

func newRegistry(t *testing.T, opts Options) *tools.Registry {
	t.Helper()
	r := tools.NewRegistry()
	if err := Register(r, opts); err != nil {
		t.Fatalf("Register: %v", err)
	}
	return r
}

func run(t *testing.T, r *tools.Registry, name string, args string) (any, error) {
	t.Helper()
	tool, ok := r.Lookup(name)
	if !ok {
		t.Fatalf("tool %q is not registered", name)
	}
	return tool.Run(context.Background(), json.RawMessage(args))
}

// TestReadReturnsFileContents pins the basic read.
func TestReadReturnsFileContents(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("line1\nline2\nline3\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	r := newRegistry(t, Options{Root: root})
	got, err := run(t, r, "read", `{"path":"a.txt"}`)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	m := got.(map[string]any)
	if !strings.Contains(m["text"].(string), "line2") {
		t.Fatalf("text = %v", m["text"])
	}
}

// TestReadSupportsOffsetAndLimit pins windowed reads, which is how a large file
// is inspected without loading all of it.
func TestReadSupportsOffsetAndLimit(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("1\n2\n3\n4\n5\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	r := newRegistry(t, Options{Root: root})
	got, err := run(t, r, "read", `{"path":"a.txt","offset":2,"limit":2}`)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	m := got.(map[string]any)
	if m["text"].(string) != "2\n3" {
		t.Fatalf("text = %q, want \"2\\n3\"", m["text"])
	}
}

// TestReadRefusesADirectory pins a clear error rather than a confusing one.
func TestReadRefusesADirectory(t *testing.T) {
	root := t.TempDir()
	r := newRegistry(t, Options{Root: root})
	if _, err := run(t, r, "read", `{"path":"."}`); err == nil {
		t.Fatal("reading a directory was accepted")
	} else if !strings.Contains(err.Error(), "directory") {
		t.Fatalf("error = %v, want it to name the problem", err)
	}
}

// TestReadRespectsTheSizeLimit pins the read cap.
func TestReadRespectsTheSizeLimit(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "big.txt"), []byte(strings.Repeat("x", 5000)), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	r := newRegistry(t, Options{Root: root, MaxReadBytes: 100})
	if _, err := run(t, r, "read", `{"path":"big.txt"}`); err == nil {
		t.Fatal("a file over the limit was read")
	}
}

// TestWriteCreatesParentDirectories pins that a nested path works.
func TestWriteCreatesParentDirectories(t *testing.T) {
	root := t.TempDir()
	r := newRegistry(t, Options{Root: root})
	if _, err := run(t, r, "write", `{"path":"a/b/c.txt","content":"hi"}`); err != nil {
		t.Fatalf("write: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(root, "a", "b", "c.txt"))
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if string(data) != "hi" {
		t.Fatalf("content = %q", data)
	}
}

// TestEditReplacesAUniqueString pins the happy path.
func TestEditReplacesAUniqueString(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "a.txt")
	if err := os.WriteFile(path, []byte("alpha beta gamma"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	r := newRegistry(t, Options{Root: root})
	if _, err := run(t, r, "edit", `{"path":"a.txt","old":"beta","new":"BETA"}`); err != nil {
		t.Fatalf("edit: %v", err)
	}
	data, _ := os.ReadFile(path)
	if string(data) != "alpha BETA gamma" {
		t.Fatalf("content = %q", data)
	}
}

// TestEditRefusesAmbiguousText pins that a non-unique match is refused rather
// than guessed at: replacing the wrong occurrence silently corrupts the file.
func TestEditRefusesAmbiguousText(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "a.txt")
	if err := os.WriteFile(path, []byte("dup dup dup"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	r := newRegistry(t, Options{Root: root})
	_, err := run(t, r, "edit", `{"path":"a.txt","old":"dup","new":"x"}`)
	if err == nil {
		t.Fatal("an ambiguous edit was applied")
	}
	if !strings.Contains(err.Error(), "3 times") {
		t.Fatalf("error = %v, want it to report the match count", err)
	}
	// The file must be untouched.
	data, _ := os.ReadFile(path)
	if string(data) != "dup dup dup" {
		t.Fatalf("the file was modified despite the refusal: %q", data)
	}
}

// TestEditRefusesMissingText pins the not-found case.
func TestEditRefusesMissingText(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("abc"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	r := newRegistry(t, Options{Root: root})
	if _, err := run(t, r, "edit", `{"path":"a.txt","old":"zzz","new":"y"}`); err == nil {
		t.Fatal("an edit with no match was applied")
	}
}

// TestEditRefusesEmptyOldText pins that an empty old text is refused: it would
// match everywhere, and creating a file is the write tool's job.
func TestEditRefusesEmptyOldText(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("abc"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	r := newRegistry(t, Options{Root: root})
	if _, err := run(t, r, "edit", `{"path":"a.txt","old":"","new":"y"}`); err == nil {
		t.Fatal("an empty old text was accepted")
	}
}

// TestListReturnsEntries pins the directory listing.
func TestListReturnsEntries(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("x"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := os.Mkdir(filepath.Join(root, "sub"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	r := newRegistry(t, Options{Root: root})
	got, err := run(t, r, "list", `{}`)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	entries := got.(map[string]any)["entries"].([]map[string]any)
	if len(entries) != 2 {
		t.Fatalf("got %d entries, want 2", len(entries))
	}
}

// TestToolsRefusePathsOutsideTheRoot is the containment test.
//
// It covers the three ways out — a relative traversal, an absolute path, and a
// symlink — because a guard that catches only one of them is not a guard.
func TestToolsRefusePathsOutsideTheRoot(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	secret := filepath.Join(outside, "secret.txt")
	if err := os.WriteFile(secret, []byte("classified"), 0o644); err != nil {
		t.Fatalf("write secret: %v", err)
	}
	// A symlink inside the root pointing outside it.
	link := filepath.Join(root, "escape")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("cannot create a symlink: %v", err)
	}

	r := newRegistry(t, Options{Root: root, AllowShell: false})

	cases := map[string]string{
		"relative traversal": `{"path":"../secret.txt"}`,
		"absolute path":      `{"path":"` + secret + `"}`,
		"symlink escape":     `{"path":"escape/secret.txt"}`,
	}
	for label, args := range cases {
		if _, err := run(t, r, "read", args); err == nil {
			t.Errorf("%s: read succeeded, want a refusal", label)
		}
		if _, err := run(t, r, "write", `{"path":`+args[9:len(args)-1]+`,"content":"x"}`); err == nil {
			t.Errorf("%s: write succeeded, want a refusal", label)
		}
	}
}

// TestListRefusesAnEscape pins the same guard on the listing tool.
func TestListRefusesAnEscape(t *testing.T) {
	root := t.TempDir()
	r := newRegistry(t, Options{Root: root})
	if _, err := run(t, r, "list", `{"path":".."}`); err == nil {
		t.Fatal("listing outside the root was accepted")
	}
}

// TestShellToolIsOnlyRegisteredWhenAllowed pins the separate switch.
func TestShellToolIsOnlyRegisteredWhenAllowed(t *testing.T) {
	off := newRegistry(t, Options{Root: t.TempDir()})
	if _, ok := off.Lookup("bash"); ok {
		t.Fatal("the shell tool was registered without AllowShell")
	}
	on := newRegistry(t, Options{Root: t.TempDir(), AllowShell: true})
	if _, ok := on.Lookup("bash"); !ok {
		t.Fatal("the shell tool is missing with AllowShell")
	}
}

// TestShellReportsExitCodeAndOutput pins that a non-zero exit is a RESULT, not
// a tool failure: the output explains the failure to the model.
func TestShellReportsExitCodeAndOutput(t *testing.T) {
	root := t.TempDir()
	r := newRegistry(t, Options{Root: root, AllowShell: true})
	got, err := run(t, r, "bash", `{"command":"echo hello; exit 3"}`)
	if err != nil {
		t.Fatalf("a non-zero exit was reported as a tool failure: %v", err)
	}
	m := got.(map[string]any)
	if !strings.Contains(m["output"].(string), "hello") {
		t.Fatalf("output = %v", m["output"])
	}
	if m["exitCode"] != 3 {
		t.Fatalf("exitCode = %v, want 3", m["exitCode"])
	}
}

// TestShellRunsInTheRootDirectory pins that a command starts where the tools
// are bounded, so a relative path in a command means the same thing as in a
// tool call.
func TestShellRunsInTheRootDirectory(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "marker.txt"), []byte("x"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	r := newRegistry(t, Options{Root: root, AllowShell: true})
	got, err := run(t, r, "bash", `{"command":"ls"}`)
	if err != nil {
		t.Fatalf("bash: %v", err)
	}
	if !strings.Contains(got.(map[string]any)["output"].(string), "marker.txt") {
		t.Fatalf("output = %v, want the marker file", got.(map[string]any)["output"])
	}
}

// TestShellTimesOut pins the timeout, so a hung command cannot stall a turn.
func TestShellTimesOut(t *testing.T) {
	r := newRegistry(t, Options{Root: t.TempDir(), AllowShell: true, ShellTimeout: 200 * time.Millisecond})
	start := time.Now()
	got, err := run(t, r, "bash", `{"command":"sleep 5"}`)
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("a hung command was not reported")
	}
	if elapsed > 3*time.Second {
		t.Fatalf("timeout took %s", elapsed)
	}
	if m, ok := got.(map[string]any); ok && m["timedOut"] != true {
		t.Fatalf("result = %v, want timedOut", m)
	}
}

// TestShellTruncatesLargeOutput pins the output cap.
func TestShellTruncatesLargeOutput(t *testing.T) {
	r := newRegistry(t, Options{Root: t.TempDir(), AllowShell: true, MaxOutputBytes: 200})
	got, err := run(t, r, "bash", `{"command":"yes hello | head -1000"}`)
	if err != nil {
		t.Fatalf("bash: %v", err)
	}
	output := got.(map[string]any)["output"].(string)
	if len(output) > 400 {
		t.Fatalf("output is %d bytes, want it truncated", len(output))
	}
	if !strings.Contains(output, "truncated") {
		t.Fatalf("output does not say it was truncated: %q", output)
	}
}

// TestShellRefusesAnEmptyCommand pins the precondition.
func TestShellRefusesAnEmptyCommand(t *testing.T) {
	r := newRegistry(t, Options{Root: t.TempDir(), AllowShell: true})
	if _, err := run(t, r, "bash", `{"command":"   "}`); err == nil {
		t.Fatal("an empty command was accepted")
	}
}
