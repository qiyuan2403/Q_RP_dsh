package builtintools

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// osWriteFile seeds a file inside the bounded root through the same resolve
// the tools use, so the test cannot accidentally address outside it.
func osWriteFile(root, rel, content string) error {
	full, err := resolve(root, rel)
	if err != nil {
		return err
	}
	return os.WriteFile(full, []byte(content), 0o644)
}

// TestDetectLineEndingsPinsTheDominantStyle pins the sample-based classifier.
func TestDetectLineEndingsPinsTheDominantStyle(t *testing.T) {
	for _, tc := range []struct {
		raw  string
		want LineEndings
	}{
		{"a\nb\n", LF},
		{"a\r\nb\r\n", CRLF},
		{"a\r\nb\r\nc\n", CRLF},
		{"a\nb\nc\r\n", LF},
		{"a\rb\n", LF},
		{"", LF},
	} {
		if got := detectLineEndings(tc.raw); got != tc.want {
			t.Fatalf("detectLineEndings(%q) = %q, want %q", tc.raw, got, tc.want)
		}
	}
}

// TestNormalizeLeavesLoneCR pins that a lone \r is not mangled.
func TestNormalizeLeavesLoneCR(t *testing.T) {
	got := normalizeLineEndings("a\rb\r\nc\n")
	if !strings.Contains(got, "a\rb") {
		t.Fatalf("lone \\r lost: %q", got)
	}
	if strings.Contains(got, "\r\n") {
		t.Fatalf("CRLF survived: %q", got)
	}
}

// TestRestoreDoesNotDoubleCRLF pins the re-normalize before restore.
func TestRestoreDoesNotDoubleCRLF(t *testing.T) {
	got := restoreLineEndings("a\r\nb", CRLF)
	if strings.Contains(got, "\r\r\n") {
		t.Fatalf("CRLF doubled: %q", got)
	}
	if got != "a\r\nb" {
		t.Fatalf("got %q", got)
	}
}

// TestEditOnCRLFFilePreservesLineEndings pins the cross-platform error class:
// an LF `old` matches a CRLF file, and the file stays CRLF after the edit.
func TestEditOnCRLFFilePreservesLineEndings(t *testing.T) {
	root := t.TempDir()
	if err := osWriteFile(root, "config.txt", "alpha\r\nbeta\r\n"); err != nil {
		t.Fatalf("write: %v", err)
	}
	r := newRegistry(t, Options{Root: root, AllowShell: true})
	if _, err := run(t, r, "edit", `{"path":"config.txt","old":"beta","new":"BETA"}`); err != nil {
		t.Fatalf("edit refused: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(root, "config.txt"))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !strings.Contains(string(data), "BETA") {
		t.Fatalf("edit did not apply: %q", data)
	}
	if !strings.Contains(string(data), "\r\n") {
		t.Fatalf("CRLF file was rewritten to LF: %q", data)
	}
	if strings.Contains(string(data), "\r\r") {
		t.Fatalf("CRLF doubled: %q", data)
	}
}

// TestEditOnLFFileStaysLF pins the symmetric case.
func TestEditOnLFFileStaysLF(t *testing.T) {
	root := t.TempDir()
	if err := osWriteFile(root, "f.txt", "alpha\nbeta\n"); err != nil {
		t.Fatalf("write: %v", err)
	}
	r := newRegistry(t, Options{Root: root, AllowShell: true})
	if _, err := run(t, r, "edit", `{"path":"f.txt","old":"beta","new":"BETA"}`); err != nil {
		t.Fatalf("edit: %v", err)
	}
	data, _ := os.ReadFile(filepath.Join(root, "f.txt"))
	if strings.Contains(string(data), "\r") {
		t.Fatalf("LF file gained CR: %q", data)
	}
}
