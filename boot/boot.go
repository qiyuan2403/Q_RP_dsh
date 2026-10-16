// Package boot composes a runnable harness from bundle layers.
//
// A PROFILE is a directory holding an ordered list of bundles plus the user's
// own patch layer. Composing means applying each bundle's patch list, in order,
// over an empty entry list, then the user's patches, then any launcher layers —
// one `applyEntryPatches` call over the concatenated layers.
//
// The order is the contract. A patch addresses rows by id and replaces whole
// fields, so applying the same layers in a different order produces a different
// composition. The layer sequence is therefore explicit rather than a set:
//
//  1. each bundle's patch list, in the profile's declared bundle order
//  2. the profile's own cordis.patch.yml
//  3. launcher layers (flag-derived patches)
//
// COMPATIBILITY. This mirrors the official `loadProfile` + `composeEntries`
// (packages/boot/app-boot/src/profile.ts). The composition itself is delegated
// to the loader's `applyEntryPatches`, which the kernel repository owns, so the
// two cannot drift.
package boot

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/qiyuan2403/Y_Cordis/loader"
)

// Errors the composer reports.
var (
	// ErrNoProfileManifest reports a profile directory without a manifest.
	ErrNoProfileManifest = errors.New("boot: profile has no package.json")
	// ErrNoBundlePatch reports a bundle whose declared patch file is missing.
	ErrNoBundlePatch = errors.New("boot: bundle patch file is missing")
)

// ProfilePatchFilename is the user patch layer inside a profile directory.
const ProfilePatchFilename = "cordis.patch.yml"

// ProfilesDir is the directory under the harness home holding every profile.
const ProfilesDir = "profiles"

// BundleManifest is the `dsh.bundle` block of a bundle package's manifest.
type BundleManifest struct {
	// Patch is the bundle's patch file, or an ordered list of them.
	Patch PatchList `json:"patch"`
}

// PatchList accepts either a single path or a list, which is how the official
// manifest type is written.
type PatchList []string

// UnmarshalJSON accepts a string or an array of strings.
func (p *PatchList) UnmarshalJSON(data []byte) error {
	var one string
	if err := json.Unmarshal(data, &one); err == nil {
		*p = []string{one}
		return nil
	}
	var many []string
	if err := json.Unmarshal(data, &many); err != nil {
		return fmt.Errorf("boot: bundle patch must be a string or a list of strings: %w", err)
	}
	*p = many
	return nil
}

// ProfileManifest is the `dsh.profile` block plus the bundle manifest.
type ProfileManifest struct {
	Name    string `json:"name"`
	Version string `json:"version"`
	DSH     struct {
		Bundle *BundleManifest `json:"bundle,omitempty"`
		// Profile is the profile-specific block, present on a bundle package
		// that also serves as a profile.
		Profile *struct {
			// Bundles is the ordered bundle list.
			Bundles []string `json:"bundles"`
		} `json:"profile,omitempty"`
	} `json:"dsh"`
}

// Layer is one patch layer with its provenance, kept so a composition failure
// can name the file that caused it.
type Layer struct {
	// Name identifies the layer in diagnostics.
	Name string
	// Path is the layer's file, when it came from disk.
	Path string
	// Patches are the parsed patch rows.
	Patches []*loader.Patch
}

// Composition is the composed entry list.
type Composition struct {
	// Entries are the composed rows, in composition order.
	Entries []*loader.Entry
	// Layers records what was applied, in order.
	Layers []Layer
	// Warnings are the skipped-patch diagnostics from the patch algorithm.
	Warnings []string
}

// ReadManifest reads a package manifest.
func ReadManifest(path string) (*ProfileManifest, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("%w: %s", ErrNoProfileManifest, path)
		}
		return nil, err
	}
	var m ProfileManifest
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, fmt.Errorf("boot: %s is not valid JSON: %w", path, err)
	}
	return &m, nil
}

// BundlePatchPaths resolves a bundle package's patch files, in order.
//
// A bundle that declares no patch contributes nothing rather than failing: the
// official manifest makes the block optional, and a bundle with only a plugin
// entry is legitimate.
func BundlePatchPaths(packageDir string, bundle *BundleManifest) ([]string, error) {
	if bundle == nil || len(bundle.Patch) == 0 {
		return nil, nil
	}
	out := make([]string, 0, len(bundle.Patch))
	for _, rel := range bundle.Patch {
		path := filepath.Join(packageDir, filepath.FromSlash(rel))
		if _, err := os.Stat(path); err != nil {
			return nil, fmt.Errorf("%w: %s (declared by %s)", ErrNoBundlePatch, path, packageDir)
		}
		out = append(out, path)
	}
	return out, nil
}

// LoadLayer reads one patch file into a layer.
func LoadLayer(name, path string) (Layer, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return Layer{}, fmt.Errorf("boot: reading layer %s: %w", path, err)
	}
	parsed, err := loader.Parse(raw)
	if err != nil {
		return Layer{}, fmt.Errorf("boot: parsing layer %s: %w", path, err)
	}
	patches, err := loader.ParsePatches(parsed)
	if err != nil {
		return Layer{}, fmt.Errorf("boot: %s: %w", path, err)
	}
	return Layer{Name: name, Path: path, Patches: patches}, nil
}

// Compose applies the layers in order and returns the composed entries.
//
// The layers are concatenated and applied in ONE patch pass. That is what makes
// the order meaningful: a later layer's patch can target a row an earlier layer
// inserted, because the patch algorithm indexes inserted rows as it goes.
func Compose(layers []Layer) (*Composition, error) {
	var all []*loader.Patch
	for _, l := range layers {
		all = append(all, l.Patches...)
	}
	var warnings []string
	warn := func(msg string, args ...any) {
		warnings = append(warnings, fmt.Sprintf(strings.ReplaceAll(msg, "%C", "%v"), args...))
	}
	rows := loader.ApplyPatches(nil, all, warn)

	entries, err := loader.ParseEntries(rows)
	if err != nil {
		return nil, fmt.Errorf("boot: composed entries are malformed: %w", err)
	}
	return &Composition{Entries: entries, Layers: layers, Warnings: warnings}, nil
}

// ResolveBundles resolves an ordered bundle list against an installation anchor
// and a profile directory.
//
// Resolution is TWO-ANCHOR by construction: a bundle resolves from the
// installation first, then from the profile. Installation-first is the contract
// that in-box bundles always come from the running installation rather than a
// profile-local copy, which could be a different version.
func ResolveBundles(bundles []string, installAnchor, profileDir string) ([]string, error) {
	out := make([]string, 0, len(bundles))
	for _, name := range bundles {
		dir := packageDirFromAnchor(installAnchor, name)
		if dir == "" {
			dir = packageDirFromAnchor(profileDir, name)
		}
		if dir == "" {
			return nil, fmt.Errorf("boot: cannot resolve bundle %q from the installation or the profile; install its dependency first", name)
		}
		out = append(out, dir)
	}
	return out, nil
}

// packageDirFromAnchor walks up from an anchor looking for
// `node_modules/<name>/package.json`.
func packageDirFromAnchor(anchor, name string) string {
	if anchor == "" {
		return ""
	}
	dir := filepath.Dir(anchor)
	for {
		candidate := filepath.Join(dir, "node_modules", filepath.FromSlash(name))
		if _, err := os.Stat(filepath.Join(candidate, "package.json")); err == nil {
			return candidate
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return ""
		}
		dir = parent
	}
}

// LoadProfile composes a profile directory.
//
// The layer order is: each bundle's patches in the declared order, then the
// profile's own patch file, then the extra launcher layers.
func LoadProfile(profileDir, installAnchor string, extra []Layer) (*Composition, error) {
	manifestPath := filepath.Join(profileDir, "package.json")
	manifest, err := ReadManifest(manifestPath)
	if err != nil {
		return nil, err
	}

	var layers []Layer
	if manifest.DSH.Profile != nil {
		dirs, err := ResolveBundles(manifest.DSH.Profile.Bundles, installAnchor, profileDir)
		if err != nil {
			return nil, err
		}
		for i, dir := range dirs {
			bm, err := ReadManifest(filepath.Join(dir, "package.json"))
			if err != nil {
				return nil, err
			}
			paths, err := BundlePatchPaths(dir, bm.DSH.Bundle)
			if err != nil {
				return nil, err
			}
			name := manifest.DSH.Profile.Bundles[i]
			for _, path := range paths {
				layer, err := LoadLayer(name, path)
				if err != nil {
					return nil, err
				}
				layers = append(layers, layer)
			}
		}
	}

	// The user's own patch layer, applied after every bundle layer.
	userPatch := filepath.Join(profileDir, ProfilePatchFilename)
	if _, err := os.Stat(userPatch); err == nil {
		layer, err := LoadLayer("profile", userPatch)
		if err != nil {
			return nil, err
		}
		layers = append(layers, layer)
	}

	layers = append(layers, extra...)
	return Compose(layers)
}

// LoadLayerFromString parses a patch list held in memory.
//
// It exists for flag-derived layers and tests, where the patches have no file
// to read; the path-based loader stays the normal route so a composition
// failure can name the file that caused it.
func LoadLayerFromString(name, src string) (Layer, error) {
	parsed, err := loader.Parse([]byte(src))
	if err != nil {
		return Layer{}, fmt.Errorf("boot: parsing layer %s: %w", name, err)
	}
	patches, err := loader.ParsePatches(parsed)
	if err != nil {
		return Layer{}, fmt.Errorf("boot: layer %s: %w", name, err)
	}
	return Layer{Name: name, Patches: patches}, nil
}
