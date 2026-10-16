package boot

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/qiyuan2403/Y_Cordis/loader"
)

// writeJSONFile writes a JSON file, creating parent directories.
func writeJSONFile(t *testing.T, path string, v any) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	raw, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// buildInstallation lays out an installation anchor plus a bundle package that
// declares the given patch file.
func buildInstallation(t *testing.T, bundleName, patchSrc string) (anchor, bundleDir string) {
	t.Helper()
	root := t.TempDir()
	bundleDir = filepath.Join(root, "node_modules", bundleName)
	if err := os.MkdirAll(bundleDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	writeJSONFile(t, filepath.Join(bundleDir, "package.json"), map[string]any{
		"name": bundleName,
		"dsh":  map[string]any{"bundle": map[string]any{"patch": "./cordis.patch.yml"}},
	})
	if err := os.WriteFile(filepath.Join(bundleDir, "cordis.patch.yml"), []byte(patchSrc), 0o644); err != nil {
		t.Fatalf("write patch: %v", err)
	}
	// The anchor is a package.json the resolver walks up from.
	anchor = filepath.Join(root, "package.json")
	writeJSONFile(t, anchor, map[string]any{"name": "installation"})
	return anchor, bundleDir
}

// TestComposeAppliesLayersInOrder pins the ordering contract: a later layer's
// patch addresses a row an earlier layer inserted, because the layers are
// concatenated into ONE patch pass.
func TestComposeAppliesLayersInOrder(t *testing.T) {
	first, err := LoadLayerFromString("first", `
- insert:
    - id: row
      name: pkg
`)
	if err != nil {
		t.Fatalf("layer 1: %v", err)
	}
	// A patch overrides a field OTHER than `name`: in a patch row, `name` is a
	// GUARD (the target's name must equal it) rather than a value to write.
	second, err := LoadLayerFromString("second", `
- id: row
  disabled: true
`)
	if err != nil {
		t.Fatalf("layer 2: %v", err)
	}

	got, err := Compose([]Layer{first, second})
	if err != nil {
		t.Fatalf("Compose: %v", err)
	}
	if len(got.Entries) != 1 {
		t.Fatalf("got %d entries, want 1", len(got.Entries))
	}
	if got.Entries[0].Disabled != true {
		t.Fatalf("Disabled = %v, want the later layer's override", got.Entries[0].Disabled)
	}
}

// TestComposeOrderIsObservable pins that reversing the layers changes the
// result, which is why the order is a contract rather than a set.
func TestComposeOrderIsObservable(t *testing.T) {
	insert, _ := LoadLayerFromString("insert", "- insert:\n    - id: row\n      name: pkg\n")
	patch, _ := LoadLayerFromString("patch", "- id: row\n  disabled: true\n")

	forward, err := Compose([]Layer{insert, patch})
	if err != nil {
		t.Fatalf("Compose(forward): %v", err)
	}
	// Reversed, the patch runs BEFORE the row exists, so it matches nothing and
	// warns; the insert then lands unmodified. That asymmetry is what makes the
	// layer order observable rather than a set.
	reversed, err := Compose([]Layer{patch, insert})
	if err != nil {
		t.Fatalf("Compose(reversed): %v", err)
	}
	if forward.Entries[0].Disabled != true {
		t.Fatalf("forward Disabled = %v, want the patch applied", forward.Entries[0].Disabled)
	}
	if len(forward.Warnings) != 0 {
		t.Fatalf("forward warnings = %v", forward.Warnings)
	}
	if reversed.Entries[0].Disabled != nil {
		t.Fatalf("reversed Disabled = %v, want the patch to have run before the row existed", reversed.Entries[0].Disabled)
	}
	if len(reversed.Warnings) != 1 {
		t.Fatalf("reversed warnings = %v, want the unmatched patch reported", reversed.Warnings)
	}
}

// TestComposeIsRepeatable pins that composing the same layers twice yields the
// same result.
//
// This is the property a hot-reloaded config depends on. It caught a real bug
// in the patch algorithm: the inserted rows were not cloned, so applying a
// cached patch list mutated it, and the SECOND application saw the mutation.
func TestComposeIsRepeatable(t *testing.T) {
	layer, err := LoadLayer("l", "testdata/base.cordis.patch.yml")
	if err != nil {
		t.Fatalf("LoadLayer: %v", err)
	}
	first, err := Compose([]Layer{layer})
	if err != nil {
		t.Fatalf("first Compose: %v", err)
	}
	second, err := Compose([]Layer{layer})
	if err != nil {
		t.Fatalf("second Compose: %v", err)
	}
	if len(first.Entries) != len(second.Entries) {
		t.Fatalf("entry counts differ: %d then %d", len(first.Entries), len(second.Entries))
	}
	for i := range first.Entries {
		if first.Entries[i].ID != second.Entries[i].ID {
			t.Fatalf("entry %d id changed across compositions: %q then %q",
				i, first.Entries[i].ID, second.Entries[i].ID)
		}
	}
}

// TestComposeReportsSkippedPatches pins that a patch matching nothing produces
// a warning rather than silence: a typo'd id would otherwise be invisible.
func TestComposeReportsSkippedPatches(t *testing.T) {
	layer, _ := LoadLayerFromString("l", "- id: missing\n  disabled: true\n")
	got, err := Compose([]Layer{layer})
	if err != nil {
		t.Fatalf("Compose: %v", err)
	}
	if len(got.Warnings) != 1 {
		t.Fatalf("warnings = %v, want one", got.Warnings)
	}
	if !strings.Contains(got.Warnings[0], "missing") {
		t.Fatalf("warning = %q, want it to name the id", got.Warnings[0])
	}
}

// TestComposeRealBaseBundle pins that the shipped base bundle composes and
// yields the expected row count.
func TestComposeRealBaseBundle(t *testing.T) {
	layer, err := LoadLayer("base", "testdata/base.cordis.patch.yml")
	if err != nil {
		t.Fatalf("LoadLayer: %v", err)
	}
	got, err := Compose([]Layer{layer})
	if err != nil {
		t.Fatalf("Compose: %v", err)
	}
	if len(got.Entries) != 95 {
		t.Fatalf("got %d entries, want 95", len(got.Entries))
	}
	// A few rows the composition must contain.
	byID := map[string]*loader.Entry{}
	for _, e := range got.Entries {
		byID[e.ID] = e
	}
	for _, id := range []string{"session", "agent", "tools", "llm-deepseek"} {
		if byID[id] == nil {
			t.Errorf("composed base bundle is missing row %q", id)
		}
	}
}

// TestBaseThenWebAppLayersCompose pins a two-layer composition over the REAL
// bundles: web-app's 26 id-targeted overrides must land on base's rows.
func TestBaseThenWebAppLayersCompose(t *testing.T) {
	base, err := LoadLayer("base", "testdata/base.cordis.patch.yml")
	if err != nil {
		t.Fatalf("base: %v", err)
	}
	web, err := LoadLayer("web-app", "testdata/web-app.cordis.patch.yml")
	if err != nil {
		t.Fatalf("web-app: %v", err)
	}
	got, err := Compose([]Layer{base, web})
	if err != nil {
		t.Fatalf("Compose: %v", err)
	}
	// Every web-app override targets a base row, so no warning may be reported.
	if len(got.Warnings) != 0 {
		t.Fatalf("unexpected warnings from a well-formed composition: %v", got.Warnings)
	}
	if len(got.Entries) < 95 {
		t.Fatalf("got %d entries, want at least the base's 95", len(got.Entries))
	}
}

// TestLoadProfileComposesBundlesThenUserPatch pins the full layer order:
// bundles in declared order, then the profile's own patch file, then launcher
// layers.
func TestLoadProfileComposesBundlesThenUserPatch(t *testing.T) {
	anchor, _ := buildInstallation(t, "@test/bundle", `
- insert:
    - id: from-bundle
      name: bundle-row
`)
	profileDir := t.TempDir()
	writeJSONFile(t, filepath.Join(profileDir, "package.json"), map[string]any{
		"name": "test-profile",
		"dsh":  map[string]any{"profile": map[string]any{"bundles": []string{"@test/bundle"}}},
	})
	// The user's patch overrides the bundle's row.
	if err := os.WriteFile(filepath.Join(profileDir, ProfilePatchFilename), []byte(`
- id: from-bundle
  disabled: true
`), 0o644); err != nil {
		t.Fatalf("write user patch: %v", err)
	}

	got, err := LoadProfile(profileDir, anchor, nil)
	if err != nil {
		t.Fatalf("LoadProfile: %v", err)
	}
	if len(got.Entries) != 1 {
		t.Fatalf("got %d entries, want 1", len(got.Entries))
	}
	if got.Entries[0].Disabled != true {
		t.Fatalf("Disabled = %v, want the user patch to win over the bundle", got.Entries[0].Disabled)
	}
	if len(got.Warnings) != 0 {
		t.Fatalf("warnings = %v", got.Warnings)
	}
	if len(got.Layers) != 2 {
		t.Fatalf("layers = %d, want 2 (bundle then user)", len(got.Layers))
	}
	if got.Layers[0].Name != "@test/bundle" || got.Layers[1].Name != "profile" {
		t.Fatalf("layer order = %s, %s", got.Layers[0].Name, got.Layers[1].Name)
	}
}

// TestLauncherLayersApplyLast pins that extra layers win over both the bundle
// and the user's patch.
func TestLauncherLayersApplyLast(t *testing.T) {
	anchor, _ := buildInstallation(t, "@test/bundle", "- insert:\n    - id: row\n      name: pkg\n")
	profileDir := t.TempDir()
	writeJSONFile(t, filepath.Join(profileDir, "package.json"), map[string]any{
		"name": "p",
		"dsh":  map[string]any{"profile": map[string]any{"bundles": []string{"@test/bundle"}}},
	})
	extra, err := LoadLayerFromString("--patch", "- id: row\n  disabled: true\n")
	if err != nil {
		t.Fatalf("extra layer: %v", err)
	}

	got, err := LoadProfile(profileDir, anchor, []Layer{extra})
	if err != nil {
		t.Fatalf("LoadProfile: %v", err)
	}
	if got.Entries[0].Disabled != true {
		t.Fatalf("Disabled = %v, want the launcher layer to win", got.Entries[0].Disabled)
	}
}

// TestUnresolvableBundleFailsLoud pins that a bundle the installation cannot
// resolve is an error, not a silent omission: omitting it would compose a
// harness missing a whole layer of configuration.
func TestUnresolvableBundleFailsLoud(t *testing.T) {
	anchor := filepath.Join(t.TempDir(), "package.json")
	writeJSONFile(t, anchor, map[string]any{"name": "install"})
	profileDir := t.TempDir()
	writeJSONFile(t, filepath.Join(profileDir, "package.json"), map[string]any{
		"name": "p",
		"dsh":  map[string]any{"profile": map[string]any{"bundles": []string{"@test/absent"}}},
	})

	_, err := LoadProfile(profileDir, anchor, nil)
	if err == nil {
		t.Fatal("an unresolvable bundle was accepted")
	}
	if !strings.Contains(err.Error(), "@test/absent") {
		t.Fatalf("error = %v, want it to name the bundle", err)
	}
}

// TestMissingBundlePatchFailsLoud pins that a declared-but-absent patch file is
// an error.
func TestMissingBundlePatchFailsLoud(t *testing.T) {
	root := t.TempDir()
	bundleDir := filepath.Join(root, "node_modules", "@test/broken")
	if err := os.MkdirAll(bundleDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	writeJSONFile(t, filepath.Join(bundleDir, "package.json"), map[string]any{
		"name": "@test/broken",
		"dsh":  map[string]any{"bundle": map[string]any{"patch": "./missing.yml"}},
	})
	anchor := filepath.Join(root, "package.json")
	writeJSONFile(t, anchor, map[string]any{"name": "install"})

	_, err := ResolveBundles([]string{"@test/broken"}, anchor, "")
	if err != nil {
		t.Fatalf("ResolveBundles: %v", err)
	}
	bm, err := ReadManifest(filepath.Join(bundleDir, "package.json"))
	if err != nil {
		t.Fatalf("ReadManifest: %v", err)
	}
	if _, err := BundlePatchPaths(bundleDir, bm.DSH.Bundle); err == nil {
		t.Fatal("a missing patch file was accepted")
	}
}

// TestBundleWithoutPatchContributesNothing pins that the patch block is
// optional: a bundle carrying only plugin entries is legitimate.
func TestBundleWithoutPatchContributesNothing(t *testing.T) {
	paths, err := BundlePatchPaths(t.TempDir(), nil)
	if err != nil {
		t.Fatalf("BundlePatchPaths(nil): %v", err)
	}
	if len(paths) != 0 {
		t.Fatalf("paths = %v, want none", paths)
	}
}

// TestPatchListAcceptsStringOrArray pins the manifest shape: `patch` may be one
// path or an ordered list.
func TestPatchListAcceptsStringOrArray(t *testing.T) {
	var one BundleManifest
	if err := json.Unmarshal([]byte(`{"patch":"./a.yml"}`), &one); err != nil {
		t.Fatalf("single: %v", err)
	}
	if len(one.Patch) != 1 || one.Patch[0] != "./a.yml" {
		t.Fatalf("single = %v", one.Patch)
	}

	var many BundleManifest
	if err := json.Unmarshal([]byte(`{"patch":["./a.yml","./b.yml"]}`), &many); err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(many.Patch) != 2 {
		t.Fatalf("list = %v", many.Patch)
	}
}

// TestInstallationResolvesBeforeProfile pins the two-anchor order: an in-box
// bundle comes from the installation, not from a profile-local copy that could
// be a different version.
func TestInstallationResolvesBeforeProfile(t *testing.T) {
	anchor, installDir := buildInstallation(t, "@test/shared", "- insert:\n    - id: from-install\n      name: x\n")

	// A profile-local copy with the SAME name but a different patch.
	profileDir := t.TempDir()
	localDir := filepath.Join(profileDir, "node_modules", "@test/shared")
	if err := os.MkdirAll(localDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	writeJSONFile(t, filepath.Join(localDir, "package.json"), map[string]any{"name": "@test/shared"})

	got, err := ResolveBundles([]string{"@test/shared"}, anchor, profileDir)
	if err != nil {
		t.Fatalf("ResolveBundles: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d dirs, want 1", len(got))
	}
	if got[0] != installDir {
		t.Fatalf("resolved %q, want the installation's %q", got[0], installDir)
	}
}

// TestProfileWithoutManifestFailsLoud pins the missing-manifest error.
func TestProfileWithoutManifestFailsLoud(t *testing.T) {
	_, err := LoadProfile(t.TempDir(), "", nil)
	if err == nil {
		t.Fatal("a profile without a manifest was accepted")
	}
	if !strings.Contains(err.Error(), "package.json") {
		t.Fatalf("error = %v, want it to name the manifest", err)
	}
}
