package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/qiyuan2403/Q_RP_dsh/systemprompt"
)

// TestAssemblePromptIncludesIdentityAndPersona pins the composition order: the
// harness identity comes before the deployment persona.
func TestAssemblePromptIncludesIdentityAndPersona(t *testing.T) {
	assembly, err := assemblePrompt("You are a helpful assistant.")
	if err != nil {
		t.Fatalf("assemblePrompt: %v", err)
	}
	text, err := systemprompt.Render(assembly)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	identity := strings.Index(text, "Q_RP_dsh harness")
	persona := strings.Index(text, "helpful assistant")
	if identity < 0 || persona < 0 {
		t.Fatalf("prompt is missing a part:\n%s", text)
	}
	if identity > persona {
		t.Fatalf("the persona precedes the harness identity:\n%s", text)
	}
}

// TestAssemblePromptWithoutPersona pins that an empty persona contributes no
// section rather than an empty paragraph.
func TestAssemblePromptWithoutPersona(t *testing.T) {
	assembly, err := assemblePrompt("")
	if err != nil {
		t.Fatalf("assemblePrompt: %v", err)
	}
	if len(assembly.Sections) != 1 {
		t.Fatalf("got %d sections, want only the identity", len(assembly.Sections))
	}
	text, _ := systemprompt.Render(assembly)
	if strings.Contains(text, "\n\n\n") {
		t.Fatalf("an empty section left a gap:\n%q", text)
	}
}

// TestResolveHomeUsesTheOverride pins that an explicit home wins.
func TestResolveHomeUsesTheOverride(t *testing.T) {
	got, err := resolveHome("/custom/home")
	if err != nil {
		t.Fatalf("resolveHome: %v", err)
	}
	if got != "/custom/home" {
		t.Fatalf("home = %q", got)
	}
}

// TestResolveHomeDefaultsUnderUserHome pins the default location, so durable
// state lands where the user can find it.
func TestResolveHomeDefaultsUnderUserHome(t *testing.T) {
	got, err := resolveHome("")
	if err != nil {
		t.Fatalf("resolveHome: %v", err)
	}
	if filepath.Base(got) != ".qrpdsh" {
		t.Fatalf("home = %q, want a .qrpdsh directory", got)
	}
	if !strings.HasPrefix(got, os.Getenv("HOME")) && !strings.Contains(got, "qrpdsh") {
		t.Fatalf("home = %q", got)
	}
}

// TestListProfileWithoutPathIsRefused pins fail-loud rather than composing
// nothing.
func TestListProfileWithoutPathIsRefused(t *testing.T) {
	if code := listProfileRows("", ""); code == 0 {
		t.Fatal("a missing -profile was accepted")
	}
}

// TestListProfileReportsCompositionErrors pins a non-zero exit on a bad profile.
func TestListProfileReportsCompositionErrors(t *testing.T) {
	if code := listProfileRows(t.TempDir(), ""); code == 0 {
		t.Fatal("a profile without a manifest was accepted")
	}
}

// TestBuildRegistryOffersTheFileTools pins the default set: file access without
// command execution.
func TestBuildRegistryOffersTheFileTools(t *testing.T) {
	r, err := buildRegistry(t.TempDir(), false)
	if err != nil {
		t.Fatalf("buildRegistry: %v", err)
	}
	got := r.Names()
	want := []string{"edit", "list", "read", "write"}
	if len(got) != len(want) {
		t.Fatalf("tools = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("tools = %v, want %v", got, want)
		}
	}
	if _, ok := r.Lookup("bash"); ok {
		t.Fatal("the shell tool was offered without -allow-shell")
	}
}

// TestBuildRegistryOffersShellOnlyWhenAllowed pins the separate switch: a
// deployment may want file access without command execution.
func TestBuildRegistryOffersShellOnlyWhenAllowed(t *testing.T) {
	r, err := buildRegistry(t.TempDir(), true)
	if err != nil {
		t.Fatalf("buildRegistry: %v", err)
	}
	if _, ok := r.Lookup("bash"); !ok {
		t.Fatalf("the shell tool is missing from %v", r.Names())
	}
}
