package main

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestVersionIsReported pins that the symbol the JVM looks up returns a value.
func TestVersionIsReported(t *testing.T) {
	if Version == "" {
		t.Fatal("Version is empty")
	}
}

// TestRunTurnRejectsAnEmptyTask pins fail-loud before any network call.
func TestRunTurnRejectsAnEmptyTask(t *testing.T) {
	var resp runResponse
	if err := json.Unmarshal([]byte(runTurnJSON(runRequest{BaseURL: "http://x", Model: "m"})), &resp); err != nil {
		t.Fatalf("response is not JSON: %v", err)
	}
	if resp.OK {
		t.Fatal("an empty task was accepted")
	}
	if !strings.Contains(resp.Error, "task") {
		t.Fatalf("error = %q, want it to name the task", resp.Error)
	}
}

// TestRunTurnRequiresEndpointAndModel pins the other precondition.
func TestRunTurnRequiresEndpointAndModel(t *testing.T) {
	var resp runResponse
	_ = json.Unmarshal([]byte(runTurnJSON(runRequest{Task: "hi"})), &resp)
	if resp.OK || !strings.Contains(resp.Error, "baseUrl") {
		t.Fatalf("response = %+v, want a missing-endpoint error", resp)
	}
}

// TestRunTurnReportsAModelFailureAsJSON pins that a failed turn still produces
// a parseable response: the app must be able to show the reason rather than
// crash on malformed output.
func TestRunTurnReportsAModelFailureAsJSON(t *testing.T) {
	// An endpoint that cannot be reached: the provider fails, and the failure
	// must come back as JSON rather than a panic across the JNI boundary.
	out := runTurnJSON(runRequest{
		Task:    "hello",
		WorkDir: t.TempDir(),
		Home:    t.TempDir(),
		BaseURL: "http://127.0.0.1:1",
		Model:   "test",
	})
	var resp runResponse
	if err := json.Unmarshal([]byte(out), &resp); err != nil {
		t.Fatalf("response is not JSON: %v\n%s", err, out)
	}
	if resp.OK {
		t.Fatal("an unreachable endpoint reported success")
	}
	if resp.Error == "" {
		t.Fatalf("response carries no error: %+v", resp)
	}
}

// TestRunTurnWritesASessionLog pins that a turn leaves a durable record even
// when it fails, which is what makes a mobile run auditable.
func TestRunTurnWritesASessionLog(t *testing.T) {
	home := t.TempDir()
	out := runTurnJSON(runRequest{
		Task:    "hello",
		WorkDir: t.TempDir(),
		Home:    home,
		BaseURL: "http://127.0.0.1:1",
		Model:   "test",
	})
	_ = out

	sessionsDir := home + "/sessions"
	entries, err := readDir(sessionsDir)
	if err != nil {
		t.Fatalf("no session directory was written: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("got %d session directories, want 1", len(entries))
	}
}
