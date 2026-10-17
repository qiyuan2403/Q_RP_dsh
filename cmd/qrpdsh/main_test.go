package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/qiyuan2403/Q_RP_dsh/systemprompt"
	"github.com/qiyuan2403/Q_RP_dsh/web"
)

// TestAssemblePromptIncludesIdentityAndPersona pins the composition order: the
// harness identity comes before the deployment persona.
func TestAssemblePromptIncludesIdentityAndPersona(t *testing.T) {
	assembly, err := assemblePrompt("You are a helpful assistant.", nil)
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
	assembly, err := assemblePrompt("", nil)
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
// command execution, plus the web tools whose provider is a deployment choice.
func TestBuildRegistryOffersTheFileTools(t *testing.T) {
	r, _, err := buildToolset(t.TempDir(), "", false)
	if err != nil {
		t.Fatalf("buildToolset: %v", err)
	}
	got := r.Names()
	want := []string{"edit", "list", "read", "web_fetch", "web_search", "write"}
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
	r, _, err := buildToolset(t.TempDir(), "", true)
	if err != nil {
		t.Fatalf("buildToolset: %v", err)
	}
	if _, ok := r.Lookup("bash"); !ok {
		t.Fatalf("the shell tool is missing from %v", r.Names())
	}
}

// TestWebToolsUseTheShippedProviders pins the wiring: the web tools are always
// offered, the fetch provider answers (its loopback policy refuses the URL
// before any network I/O), and search reports the no-key provider as
// unavailable. The environment is cleared BEFORE the tools are built because
// the search provider snapshots its key at construction.
func TestWebToolsUseTheShippedProviders(t *testing.T) {
	t.Setenv("DEEPSEEK_API_KEY", "")
	t.Setenv("DEEPSEEK_SEARCH_BASE_URL", "")
	r, _, err := buildToolset(t.TempDir(), "", false)
	if err != nil {
		t.Fatalf("buildToolset: %v", err)
	}
	tool, ok := r.Lookup("web_fetch")
	if !ok {
		t.Fatalf("web_fetch is missing from %v", r.Names())
	}
	// The shipped fetch provider refuses a loopback address before any network
	// I/O, which proves the tool reached a registered provider rather than the
	// seam's "no provider" error.
	_, err = tool.Run(context.Background(), json.RawMessage(`{"url":"http://127.0.0.1/private"}`))
	if err == nil {
		t.Fatal("fetching a loopback address succeeded")
	}
	var webErr *web.Error
	if !errors.As(err, &webErr) {
		t.Fatalf("error %v is not a *web.Error", err)
	}
	if webErr.Code != "WEB_BLOCKED_URL" {
		t.Fatalf("code = %q, want the provider's WEB_BLOCKED_URL", webErr.Code)
	}

	// The search provider is registered too, but without DEEPSEEK_API_KEY it is
	// unavailable; the seam reports that, not a missing provider.
	search, ok := r.Lookup("web_search")
	if !ok {
		t.Fatalf("web_search is missing from %v", r.Names())
	}
	_, err = search.Run(context.Background(), json.RawMessage(`{"queries":["q"]}`))
	if !errors.As(err, &webErr) {
		t.Fatalf("search error %v is not a *web.Error", err)
	}
	if webErr.Code != web.CodeProviderUnavailable {
		t.Fatalf("search code = %q, want %q", webErr.Code, web.CodeProviderUnavailable)
	}
}

// TestPreviewPromptMatchesTheDeployment pins that -print-prompt previews the
// prompt a turn would actually send: the same toolset flags decide the tool
// sections, so -allow-shell must show the bash guidance a turn would carry.
func TestPreviewPromptMatchesTheDeployment(t *testing.T) {
	t.Setenv("DEEPSEEK_API_KEY", "")
	t.Setenv("DEEPSEEK_SEARCH_BASE_URL", "")
	home := t.TempDir()
	work := t.TempDir()

	off, err := previewPrompt(runConfig{home: home, workDir: work})
	if err != nil {
		t.Fatalf("previewPrompt without shell: %v", err)
	}
	if strings.Contains(off, "[exit code: N]") {
		t.Errorf("the no-shell preview carries bash guidance:\n%s", off)
	}
	if !strings.Contains(off, "web_search results are external") {
		t.Errorf("the preview is missing the web tool guidance:\n%s", off)
	}

	on, err := previewPrompt(runConfig{home: home, workDir: work, allowShell: true})
	if err != nil {
		t.Fatalf("previewPrompt with shell: %v", err)
	}
	if !strings.Contains(on, "[exit code: N]") {
		t.Errorf("the shell preview is missing bash guidance:\n%s", on)
	}
}
