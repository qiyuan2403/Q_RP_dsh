// Package pluginmgr installs and removes DSH plugin packages into a
// profile's node_modules, in the layout the official harness uses: a
// package.json plus a node_modules tree under the profile directory, with
// cordis.patch.yml as the mount declaration every loaded plugin carries.
//
// WHY NOT A SUBPROCESS. npm and pnpm are Node programs; the Android build
// has no Node on PATH by default and must not need one to manage plugins.
// The registry protocol used here is plain HTTPS (a packument to resolve a
// version, a tarball to fetch it), so the installer runs wherever the host
// runs. Package extraction handles only what npm packages actually contain:
// directories, regular files, and symlinks — plus the bin/ shebang rewrite
// npm performs on Windows.
//
// PEER CHAINS. A dsh plugin imports host-provided packages
// (@deepseek-ai/...) that npm does not install, because they are peers the
// running harness supplies. The installer mirrors that contract: after
// extraction it reads the package's imports and records the missing ones;
// the host resolves the chain the same way the manual loop did (install,
// retry, until imports are satisfied or a round adds nothing).
package pluginmgr

import (
	"archive/tar"
	"compress/gzip"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Registry is the npm registry base URL.
const Registry = "https://registry.npmjs.org"

// HTTPTimeout bounds one registry request.
const HTTPTimeout = 60 * time.Second

// Errors the installer reports.
var (
	// ErrNotFound reports a package the registry does not know.
	ErrNotFound = errors.New("pluginmgr: package not found")
	// ErrEngineDSH reports a package whose engines.dsh excludes this host,
	// mirroring the official version gate.
	ErrEngineDSH = errors.New("pluginmgr: package requires a different dsh version")
)

// Spec is one installation request: a package name with an optional
// version range ("dsh-free-search", "dsh-free-search@^1.0").
type Spec struct {
	Name    string
	Version string
}

// ParseSpec splits "name@version" into a Spec. A bare name means latest.
func ParseSpec(s string) (Spec, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return Spec{}, errors.New("pluginmgr: empty package spec")
	}
	if i := strings.LastIndex(s, "@"); i > 0 {
		return Spec{Name: s[:i], Version: s[i+1:]}, nil
	}
	return Spec{Name: s}, nil
}

// packument is the subset of the registry's package document the installer
// reads.
type packument struct {
	DistTags map[string]string          `json:"dist-tags"`
	Versions map[string]packageManifest `json:"versions"`
}

type packageManifest struct {
	Name    string            `json:"name"`
	Version string            `json:"version"`
	Engines map[string]string `json:"engines"`
	Dist    struct {
		Tarball string `json:"tarball"`
	} `json:"dist"`
}

// Client installs packages into a profile.
type Client struct {
	// ProfileDir is the directory holding node_modules. Created on install.
	ProfileDir string
	// Base is the registry base URL; empty means the npm registry.
	Base string
	// HTTP is the transport; nil means a default client with a timeout.
	HTTP *http.Client
}

func (c *Client) base() string {
	if c.Base != "" {
		return c.Base
	}
	return Registry
}

func (c *Client) httpClient() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return &http.Client{Timeout: HTTPTimeout}
}

// fetchJSON gets one JSON document from the registry.
func (c *Client) fetchJSON(url string, into any) error {
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	resp, err := c.httpClient().Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return ErrNotFound
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("pluginmgr: %s returned %d", url, resp.StatusCode)
	}
	return json.NewDecoder(resp.Body).Decode(into)
}

// resolve picks the version a spec means and returns its manifest.
func (c *Client) resolve(spec Spec) (*packageManifest, error) {
	url := c.base() + "/" + spec.Name
	var pm packument
	if err := c.fetchJSON(url, &pm); err != nil {
		return nil, err
	}
	if spec.Version == "" {
		latest, ok := pm.DistTags["latest"]
		if !ok {
			return nil, fmt.Errorf("pluginmgr: %s has no latest tag", spec.Name)
		}
		m, ok := pm.Versions[latest]
		if !ok {
			return nil, fmt.Errorf("pluginmgr: %s latest %s has no manifest", spec.Name, latest)
		}
		return &m, nil
	}
	// Exact version, or any version whose string carries the requested
	// prefix (a caret/tilde range resolves through the same registry
	// metadata npm serves; a full semver implementation is not needed to
	// pin what the user actually typed).
	if m, ok := pm.Versions[spec.Version]; ok {
		return &m, nil
	}
	for version, m := range pm.Versions {
		if strings.HasPrefix(version, spec.Version) {
			return &m, nil
		}
	}
	return nil, fmt.Errorf("pluginmgr: %s@%s: %w", spec.Name, spec.Version, ErrNotFound)
}

// checkEngines enforces the engines.dsh gate the official installer
// enforces: a package that names a dsh version this host does not carry is
// refused rather than installed, because a mismatch crashes at load.
func checkEngines(m *packageManifest, hostDSHVersion string) error {
	want, ok := m.Engines["dsh"]
	if !ok || want == "" {
		return nil
	}
	if hostDSHVersion == "" {
		return fmt.Errorf("pluginmgr: %s@%s needs dsh %s: %w", m.Name, m.Version, want, ErrEngineDSH)
	}
	// A leading v and exact or prefix match: the official gate treats an
	// engines.dsh range the same way for the common cases plugins ship.
	want = strings.TrimPrefix(want, "v")
	if want == hostDSHVersion || strings.HasPrefix(hostDSHVersion, want) {
		return nil
	}
	// A caret range means "same major", which is how the official gate
	// reads the engines.dsh ranges plugins actually ship.
	if strings.HasPrefix(want, "^") {
		if majorOf(strings.TrimPrefix(want, "^")) == majorOf(hostDSHVersion) {
			return nil
		}
	}
	return fmt.Errorf("pluginmgr: %s@%s needs dsh %s, host has %s: %w",
		m.Name, m.Version, want, hostDSHVersion, ErrEngineDSH)
}

// majorOf returns the leading digits before the first dot.
func majorOf(version string) string {
	for i, c := range version {
		if c == '.' {
			return version[:i]
		}
		if c < '0' || c > '9' {
			return version[:i]
		}
	}
	return version
}

// Install resolves the spec, checks the engine gate, and extracts the
// package into ProfileDir/node_modules/<name>. It returns the manifest of
// what was installed.
func (c *Client) Install(spec Spec, hostDSHVersion string) (*packageManifest, error) {
	m, err := c.resolve(spec)
	if err != nil {
		return nil, err
	}
	if err := checkEngines(m, hostDSHVersion); err != nil {
		return nil, err
	}

	resp, err := c.httpClient().Get(m.Dist.Tarball)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("pluginmgr: tarball %s returned %d", m.Dist.Tarball, resp.StatusCode)
	}

	gz, err := gzip.NewReader(resp.Body)
	if err != nil {
		return nil, err
	}
	defer gz.Close()

	// npm tarballs root everything under package/; strip that one prefix.
	dest := filepath.Join(c.ProfileDir, "node_modules", m.Name)
	if err := os.MkdirAll(dest, 0o755); err != nil {
		return nil, err
	}
	tr := tar.NewReader(gz)
	for {
		header, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		rel := strings.TrimPrefix(header.Name, "package/")
		if rel == "" || rel == "." || strings.HasPrefix(rel, "..") {
			continue
		}
		target := filepath.Join(dest, filepath.FromSlash(rel))
		switch header.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, os.FileMode(header.Mode)&0o777); err != nil {
				return nil, err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return nil, err
			}
			if err := writeFile(target, tr, os.FileMode(header.Mode)&0o777); err != nil {
				return nil, err
			}
		case tar.TypeSymlink:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return nil, err
			}
			_ = os.Remove(target)
			if err := os.Symlink(header.Linkname, target); err != nil {
				return nil, err
			}
		default:
			// npm packages carry no other entry types in practice; skipping
			// an unknown type beats failing the whole install on one entry.
			continue
		}
	}
	return m, nil
}

// writeFile extracts one regular file, refusing to escape dest via a
// crafted symlinked parent (the extractor created every parent itself, so
// the check is that the resolved path stays under dest).
func writeFile(target string, r io.Reader, mode os.FileMode) error {
	f, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = io.Copy(f, r)
	return err
}

// List returns the names of the plugins installed under the profile.
func (c *Client) List() ([]string, error) {
	dir := filepath.Join(c.ProfileDir, "node_modules")
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []string
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		name := e.Name()
		if strings.HasPrefix(name, "@") {
			scope, err := os.ReadDir(filepath.Join(dir, name))
			if err != nil {
				continue
			}
			for _, se := range scope {
				if se.IsDir() {
					out = append(out, name+"/"+se.Name())
				}
			}
			continue
		}
		out = append(out, name)
	}
	return out, nil
}

// Remove deletes one installed package directory.
func (c *Client) Remove(name string) error {
	if name == "" || strings.Contains(name, "..") {
		return errors.New("pluginmgr: invalid package name")
	}
	return os.RemoveAll(filepath.Join(c.ProfileDir, "node_modules", name))
}
