package pluginmgr

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

// registryStub serves a minimal npm-shaped registry: one packument with a
// tarball URL pointing back at itself.
func registryStub(t *testing.T, name, version, enginesDSH string) *httptest.Server {
	t.Helper()
	tarball := buildTarball(t, name)
	mux := http.NewServeMux()
	mux.HandleFunc("/"+name, func(w http.ResponseWriter, r *http.Request) {
		doc := `{"dist-tags":{"latest":"` + version + `"},"versions":{"` + version + `":{` +
			`"name":"` + name + `","version":"` + version + `",` +
			`"engines":{` + enginesDSH + `},` +
			`"dist":{"tarball":"http://` + r.Host + `/tarball/` + name + `"}}}}`
		_, _ = w.Write([]byte(doc))
	})
	mux.HandleFunc("/tarball/"+name, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(tarball)
	})
	return httptest.NewServer(mux)
}

func buildTarball(t *testing.T, name string) []byte {
	t.Helper()
	// Files sit directly under dir: tarGzDir adds the package/ root prefix
	// itself, matching the npm tarball layout the extractor strips.
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "lib"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "package.json"),
		[]byte(`{"name":"`+name+`","main":"lib/index.js"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "lib", "index.js"),
		[]byte("export default { name: '"+name+"' }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return tarGzDir(t, dir)
}

// TestInstallRoundTrip pins the happy path: resolve, gate-check, extract.
func TestInstallRoundTrip(t *testing.T) {
	srv := registryStub(t, "dsh-sample", "1.2.3", "")
	c := &Client{ProfileDir: t.TempDir(), Base: srv.URL}
	m, err := c.Install(Spec{Name: "dsh-sample"}, "1.0.0")
	if err != nil {
		t.Fatalf("Install: %v", err)
	}
	if m.Version != "1.2.3" {
		t.Fatalf("Version = %q", m.Version)
	}
	installed := filepath.Join(c.ProfileDir, "node_modules", "dsh-sample")
	if _, err := os.Stat(filepath.Join(installed, "package.json")); err != nil {
		t.Fatalf("package.json missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(installed, "lib", "index.js")); err != nil {
		t.Fatalf("lib/index.js missing: %v", err)
	}
	list, err := c.List()
	if err != nil || len(list) != 1 || list[0] != "dsh-sample" {
		t.Fatalf("List = %v, err = %v", list, err)
	}
}

// TestInstallEngineGate pins the engines.dsh refusal: a package that names a
// dsh version the host does not carry is refused before any extraction.
func TestInstallEngineGate(t *testing.T) {
	srv := registryStub(t, "dsh-gated", "2.0.0", `"dsh":"9.9.9"`)
	c := &Client{ProfileDir: t.TempDir(), Base: srv.URL}
	if _, err := c.Install(Spec{Name: "dsh-gated"}, "1.0.0"); err == nil {
		t.Fatal("an engine-gated package installed anyway")
	}
	if _, err := os.Stat(filepath.Join(c.ProfileDir, "node_modules", "dsh-gated")); !os.IsNotExist(err) {
		t.Fatal("a gated package left files behind")
	}
}

// TestInstallEngineGatePasses pins the gate opening for a matching host.
func TestInstallEngineGatePasses(t *testing.T) {
	srv := registryStub(t, "dsh-matched", "2.0.0", `"dsh":"^1.0.0"`)
	c := &Client{ProfileDir: t.TempDir(), Base: srv.URL}
	if _, err := c.Install(Spec{Name: "dsh-matched"}, "1.2.0"); err != nil {
		t.Fatalf("Install: %v", err)
	}
}

// TestRemovePinsTheBound pins that a traversal-shaped name is refused.
func TestRemovePinsTheBound(t *testing.T) {
	c := &Client{ProfileDir: t.TempDir()}
	if err := c.Remove("../escape"); err == nil {
		t.Fatal("a traversal name was accepted")
	}
}

// TestParseSpecPinsTheShape pins name@version splitting.
func TestParseSpecPinsTheShape(t *testing.T) {
	for _, tc := range []struct {
		in      string
		name    string
		version string
		wantErr bool
	}{
		{"dsh-sample", "dsh-sample", "", false},
		{"dsh-sample@1.2.3", "dsh-sample", "1.2.3", false},
		{"@scope/pkg@1.0", "@scope/pkg", "1.0", false},
		{"@scope/pkg", "@scope/pkg", "", false},
		{"", "", "", true},
	} {
		got, err := ParseSpec(tc.in)
		if tc.wantErr {
			if err == nil {
				t.Fatalf("%q: expected an error", tc.in)
			}
			continue
		}
		if err != nil || got.Name != tc.name || got.Version != tc.version {
			t.Fatalf("%q: got %+v err %v", tc.in, got, err)
		}
	}
}
