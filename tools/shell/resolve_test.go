package shell

import (
	"runtime"
	"testing"
)

// TestResolveTrustsConfiguredPinsTheExplicitPath pins that an explicitly
// configured interpreter is used as-is, never probed: a deployment that names
// its shell knows its own mount table.
func TestResolveTrustsConfiguredPinsTheExplicitPath(t *testing.T) {
	got := ResolveInterpreter("/some/unusual/shell", "posix")
	if got != "/some/unusual/shell" {
		t.Fatalf("got %q", got)
	}
}

// TestResolveFallsBackWhenNothingSpawns pins that the fallback is the one
// path every target of the platform has, so resolution always returns
// something and the spawn failure surfaces as the executor's own error.
func TestResolveFallsBackWhenNothingSpawns(t *testing.T) {
	for _, platform := range []string{"posix", "android", "windows"} {
		candidates := CandidateInterpreters(platform)
		fallback := FallbackInterpreter(platform)
		if len(candidates) == 0 {
			t.Fatalf("%s: candidate list is empty", platform)
		}
		if last := candidates[len(candidates)-1]; last != fallback {
			t.Fatalf("%s: the fallback %q must be the last candidate, got %q", platform, fallback, last)
		}
	}
}

// TestResolveReturnsSomethingOnThisHost pins that resolution succeeds on the
// build host, which is the same POSIX surface the desktop build targets.
func TestResolveReturnsSomethingOnThisHost(t *testing.T) {
	got := ResolveInterpreter("", runtime.GOOS)
	if got == "" {
		t.Fatal("resolution returned an empty path")
	}
}

// TestAndroidCandidateListPinsTheMkshFallback pins that Android's candidate
// list ends at /system/bin/sh, the one interpreter every device ships, and
// that Termux's bash precedes it when present.
func TestAndroidCandidateListPinsTheMkshFallback(t *testing.T) {
	candidates := CandidateInterpreters("android")
	if len(candidates) == 0 || candidates[len(candidates)-1] != "/system/bin/sh" {
		t.Fatalf("android candidates = %v", candidates)
	}
	termuxIdx, systemIdx := -1, -1
	for i, c := range candidates {
		switch c {
		case "/data/data/com.termux/files/usr/bin/bash":
			termuxIdx = i
		case "/system/bin/sh":
			systemIdx = i
		}
	}
	if termuxIdx < 0 || systemIdx < 0 || termuxIdx > systemIdx {
		t.Fatalf("termux bash must precede the mksh fallback: %v", candidates)
	}
}

// TestWindowsCandidateListPinsThePwshOrder pins that Windows resolves
// PowerShell 7 before the 5.1 last resort.
func TestWindowsCandidateListPinsThePwshOrder(t *testing.T) {
	candidates := CandidateInterpreters("windows")
	if len(candidates) < 2 {
		t.Fatalf("windows candidates = %v", candidates)
	}
	if candidates[0] != `C:\Program Files\PowerShell\7\pwsh.exe` {
		t.Fatalf("first windows candidate = %q", candidates[0])
	}
	if candidates[len(candidates)-1] != `C:\Windows\System32\WindowsPowerShell\v1.0\powershell.exe` {
		t.Fatalf("last windows candidate = %q", candidates[len(candidates)-1])
	}
}

// TestExecutableFileRejectsDirectories pins the cheap gate.
func TestExecutableFileRejectsDirectories(t *testing.T) {
	if executableFile("") {
		t.Fatal("empty path passed")
	}
	if executableFile(t.TempDir()) {
		t.Fatal("a directory passed")
	}
}
