package builtinplugins

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/qiyuan2403/Q_RP_dsh/boot"
	"github.com/qiyuan2403/Q_RP_dsh/runtime"
	"github.com/qiyuan2403/Q_RP_dsh/tools"
	toolweb "github.com/qiyuan2403/Q_RP_dsh/tools/web"
	"github.com/qiyuan2403/Q_RP_dsh/web"
	"github.com/qiyuan2403/Q_RP_dsh/web/deepseeksearch"
	"github.com/qiyuan2403/Y_Cordis/cordis"
	"github.com/qiyuan2403/Y_Cordis/loader"
)

// clearProviderEnv isolates tests from a developer's shell: web.New reads these
// variables, and a stray value would silently change provider selection.
func clearProviderEnv(t *testing.T) {
	t.Helper()
	t.Setenv("DSH_WEB_SEARCH_PROVIDER", "")
	t.Setenv("DSH_WEB_FETCH_PROVIDER", "")
}

// mountComposition mounts a hand-built one-layer composition with the shipped
// registry.
//
// The `tools` service is provided on the root before mounting because the real
// profile's `tools` row owns it; this package only consumes it, so the test
// stands in for that row instead of importing it.
func mountComposition(t *testing.T, src string) (*runtime.Runtime, *tools.Registry) {
	t.Helper()
	layer, err := boot.LoadLayerFromString("test", src)
	if err != nil {
		t.Fatalf("LoadLayerFromString: %v", err)
	}
	composition, err := boot.Compose([]boot.Layer{layer})
	if err != nil {
		t.Fatalf("Compose: %v", err)
	}
	root := cordis.NewRoot()
	registry := tools.NewRegistry()
	if err := root.Provide("tools", registry, nil); err != nil {
		t.Fatalf("Provide(tools): %v", err)
	}
	rt, err := runtime.MountComposition(context.Background(), composition, runtime.Options{
		Root:     root,
		Platform: "linux",
		Registry: Registry(),
	})
	if err != nil {
		t.Fatalf("MountComposition: %v", err)
	}
	t.Cleanup(func() { _ = rt.Dispose() })
	return rt, registry
}

// twoRowComposition builds the web plus tool-web composition, optionally
// appending a config block to the tool-web row.
func twoRowComposition(configBlock string) string {
	return `
- insert:
    - id: web
      name: '@deepseek-ai/dsh-web'
    - id: tool-web
      name: '@deepseek-ai/dsh-tool-web'
` + configBlock
}

// webOnlyComposition builds a composition with just the web row.
func webOnlyComposition(configBlock string) string {
	return `
- insert:
    - id: web
      name: '@deepseek-ai/dsh-web'
` + configBlock
}

// requireMounted pins that every row activated. A skipped row is the failure
// this helper reports, so its reason is included.
func requireMounted(t *testing.T, rt *runtime.Runtime) {
	t.Helper()
	for i, row := range rt.Rows {
		if row.Entry == nil {
			t.Fatalf("row %d has no entry", i)
		}
		if row.Skipped {
			t.Fatalf("row %q was skipped: %s", row.Entry.ID, row.Reason)
		}
		if row.Fiber == nil {
			t.Fatalf("row %q has no fiber", row.Entry.ID)
		}
		if state := row.Fiber.State(); state != cordis.Active {
			t.Fatalf("row %q fiber state = %v, want active", row.Entry.ID, state)
		}
	}
}

// TestRegistryShapes pins the registered keys and the plugin metadata a
// composition and the kernel rely on.
func TestRegistryShapes(t *testing.T) {
	registry := Registry()
	if len(registry) != 4 {
		t.Fatalf("Registry() has %d keys, want the four official base rows", len(registry))
	}
	for _, key := range []string{webRowName, toolWebRowName, fetchHTTPRowName, deepseekSearchRowName} {
		factory, ok := registry[key]
		if !ok {
			t.Fatalf("Registry() has no %q", key)
		}
		if factory == nil {
			t.Fatalf("Registry()[%q] is nil", key)
		}
		first := factory()
		if first == nil {
			t.Fatalf("Registry()[%q]() = nil", key)
		}
		if first.Apply == nil {
			t.Fatalf("Registry()[%q]() has no Apply", key)
		}
		// A fresh plugin per mount is the factory's contract: the kernel keys
		// per-plugin state and effects by name, so sharing one would leak
		// between mounts.
		if second := factory(); second == first {
			t.Fatalf("Registry()[%q]() reused a *cordis.Plugin instance", key)
		}
	}

	webBody := registry[webRowName]()
	if webBody.Name != "web" {
		t.Fatalf("web Name = %q, want web", webBody.Name)
	}
	if len(webBody.Inject) != 0 {
		t.Fatalf("web Inject = %v, want none", webBody.Inject)
	}
	if !slices.Equal(webBody.Provide, []string{"web"}) {
		t.Fatalf("web Provide = %v, want [web]", webBody.Provide)
	}

	toolBody := registry[toolWebRowName]()
	if toolBody.Name != "tool-web" {
		t.Fatalf("tool-web Name = %q, want tool-web", toolBody.Name)
	}
	if !slices.Equal(toolBody.Inject, []string{"tools", "web"}) {
		t.Fatalf("tool-web Inject = %v, want [tools web]", toolBody.Inject)
	}
	if len(toolBody.Provide) != 0 {
		t.Fatalf("tool-web Provide = %v, want none", toolBody.Provide)
	}

	fetchBody := registry[fetchHTTPRowName]()
	if fetchBody.Name != "web-fetch-http" {
		t.Fatalf("web-fetch-http Name = %q", fetchBody.Name)
	}
	if !slices.Equal(fetchBody.Inject, []string{"web"}) {
		t.Fatalf("web-fetch-http Inject = %v, want [web]", fetchBody.Inject)
	}

	searchBody := registry[deepseekSearchRowName]()
	if searchBody.Name != "web-search-deepseek" {
		t.Fatalf("web-search-deepseek Name = %q", searchBody.Name)
	}
	if !slices.Equal(searchBody.Inject, []string{"web"}) {
		t.Fatalf("web-search-deepseek Inject = %v, want [web]", searchBody.Inject)
	}
}

// TestMountsWebServiceAndTools pins the end-to-end wiring: both rows activate,
// the web service is reachable by name, and both tools land in the registry.
func TestMountsWebServiceAndTools(t *testing.T) {
	clearProviderEnv(t)
	rt, registry := mountComposition(t, twoRowComposition(""))
	requireMounted(t, rt)
	if got := len(rt.Rows); got != 2 {
		t.Fatalf("mounted %d rows, want 2", got)
	}

	provided, ok := rt.Root.Get("web").(*web.Web)
	if !ok {
		t.Fatalf("ctx.Get(\"web\") = %T, want *web.Web", rt.Root.Get("web"))
	}
	if provided == nil {
		t.Fatal("the web row provided a nil service")
	}

	for _, name := range []string{toolweb.SearchToolName, toolweb.FetchToolName} {
		if _, ok := registry.Lookup(name); !ok {
			t.Fatalf("tool %q was not registered; registry has %v", name, registry.Names())
		}
	}
}

// TestToolWebConfigControlsRegistration pins the config mapping for the two
// default-true booleans: false disables one tool, and an absent field leaves
// both enabled.
func TestToolWebConfigControlsRegistration(t *testing.T) {
	clearProviderEnv(t)
	cases := []struct {
		name        string
		configBlock string
		want        []string
		absent      []string
	}{
		{
			name: "absent config registers both",
			want: []string{toolweb.SearchToolName, toolweb.FetchToolName},
		},
		{
			name:        "search false registers only fetch",
			configBlock: "      config:\n        search: false\n",
			want:        []string{toolweb.FetchToolName},
			absent:      []string{toolweb.SearchToolName},
		},
		{
			name:        "fetch false registers only search",
			configBlock: "      config:\n        fetch: false\n",
			want:        []string{toolweb.SearchToolName},
			absent:      []string{toolweb.FetchToolName},
		},
		{
			name:        "both false registers neither",
			configBlock: "      config:\n        search: false\n        fetch: false\n",
			absent:      []string{toolweb.SearchToolName, toolweb.FetchToolName},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, registry := mountComposition(t, twoRowComposition(tc.configBlock))
			for _, name := range tc.want {
				if _, ok := registry.Lookup(name); !ok {
					t.Fatalf("tool %q was not registered; registry has %v", name, registry.Names())
				}
			}
			for _, name := range tc.absent {
				if _, ok := registry.Lookup(name); ok {
					t.Fatalf("tool %q was registered despite the config; registry has %v", name, registry.Names())
				}
			}
		})
	}
}

// fakeSearchProvider is one usable search backend that records its calls.
type fakeSearchProvider struct {
	id    string
	calls int
}

func (p *fakeSearchProvider) ID() string { return p.id }

func (p *fakeSearchProvider) Available() bool { return true }

func (p *fakeSearchProvider) Search(context.Context, web.SearchRequest) (web.SearchResult, error) {
	p.calls++
	return web.SearchResult{Content: p.id}, nil
}

// requireWebCode asserts a web failure with a specific code.
func requireWebCode(t *testing.T, err error, code string) {
	t.Helper()
	var webErr *web.Error
	if !errors.As(err, &webErr) {
		t.Fatalf("error %v is not a *web.Error", err)
	}
	if webErr.Code != code {
		t.Fatalf("error code = %q, want %q (%v)", webErr.Code, code, err)
	}
}

// TestWebConfigPinsSearchProvider pins that the row config reaches web.New.
//
// Two usable providers make the check behavioral instead of reflective: with no
// configured id, selection must report ambiguity, so a successful call to a
// specific provider can only come from the configured id.
func TestWebConfigPinsSearchProvider(t *testing.T) {
	clearProviderEnv(t)
	cases := []struct {
		name        string
		configBlock string
		check       func(t *testing.T, w *web.Web, alpha, beta *fakeSearchProvider)
	}{
		{
			name: "no configured id reports ambiguity",
			check: func(t *testing.T, w *web.Web, alpha, beta *fakeSearchProvider) {
				t.Helper()
				_, err := w.Search(context.Background(), web.SearchRequest{Query: "q"})
				requireWebCode(t, err, web.CodeProviderAmbiguous)
				if alpha.calls != 0 || beta.calls != 0 {
					t.Fatalf("ambiguity still ran a provider: alpha=%d beta=%d", alpha.calls, beta.calls)
				}
			},
		},
		{
			name:        "configured id selects that provider",
			configBlock: "      config:\n        searchProvider: beta\n",
			check: func(t *testing.T, w *web.Web, alpha, beta *fakeSearchProvider) {
				t.Helper()
				result, err := w.Search(context.Background(), web.SearchRequest{Query: "q"})
				if err != nil {
					t.Fatalf("Search: %v", err)
				}
				if result.Content != "beta" {
					t.Fatalf("Search ran %q, want the configured beta", result.Content)
				}
				if alpha.calls != 0 || beta.calls != 1 {
					t.Fatalf("call counts alpha=%d beta=%d, want 0 and 1", alpha.calls, beta.calls)
				}
			},
		},
		{
			name:        "unregistered configured id is reported",
			configBlock: "      config:\n        searchProvider: deepseek\n",
			check: func(t *testing.T, w *web.Web, alpha, beta *fakeSearchProvider) {
				t.Helper()
				_, err := w.Search(context.Background(), web.SearchRequest{Query: "q"})
				requireWebCode(t, err, web.CodeProviderConfiguredMissing)
				if alpha.calls != 0 || beta.calls != 0 {
					t.Fatalf("a missing configured id still ran a provider: alpha=%d beta=%d", alpha.calls, beta.calls)
				}
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rt, _ := mountComposition(t, webOnlyComposition(tc.configBlock))
			requireMounted(t, rt)
			w, ok := rt.Root.Get("web").(*web.Web)
			if !ok {
				t.Fatalf("ctx.Get(\"web\") = %T, want *web.Web", rt.Root.Get("web"))
			}
			alpha := &fakeSearchProvider{id: "alpha"}
			beta := &fakeSearchProvider{id: "beta"}
			for _, p := range []*fakeSearchProvider{alpha, beta} {
				if _, err := w.RegisterSearchProvider(p); err != nil {
					t.Fatalf("RegisterSearchProvider(%q): %v", p.id, err)
				}
			}
			tc.check(t, w, alpha, beta)
		})
	}
}

// TestToolWebApplyRequiresTypedServices pins the error contract: a missing or
// wrong-typed service fails with the package prefix instead of panicking, and a
// config the schema cannot accept names the row.
func TestToolWebApplyRequiresTypedServices(t *testing.T) {
	cases := []struct {
		name      string
		raw       any
		provide   func(t *testing.T, ctx *cordis.Context)
		wantParts []string
	}{
		{
			name: "tools missing",
			provide: func(t *testing.T, ctx *cordis.Context) {
				t.Helper()
				if err := ctx.Provide("web", web.New(web.Config{}), nil); err != nil {
					t.Fatalf("Provide(web): %v", err)
				}
			},
			wantParts: []string{"builtinplugins:", `service "tools" is not available`},
		},
		{
			name: "tools wrong type",
			provide: func(t *testing.T, ctx *cordis.Context) {
				t.Helper()
				if err := ctx.Provide("tools", "not a registry", nil); err != nil {
					t.Fatalf("Provide(tools): %v", err)
				}
				if err := ctx.Provide("web", web.New(web.Config{}), nil); err != nil {
					t.Fatalf("Provide(web): %v", err)
				}
			},
			wantParts: []string{"builtinplugins:", `service "tools" is string, want *tools.Registry`},
		},
		{
			name: "web missing",
			provide: func(t *testing.T, ctx *cordis.Context) {
				t.Helper()
				if err := ctx.Provide("tools", tools.NewRegistry(), nil); err != nil {
					t.Fatalf("Provide(tools): %v", err)
				}
			},
			wantParts: []string{"builtinplugins:", `service "web" is not available`},
		},
		{
			name: "config does not match the schema",
			raw:  map[string]any{"search": "false"},
			provide: func(t *testing.T, ctx *cordis.Context) {
				t.Helper()
				if err := ctx.Provide("tools", tools.NewRegistry(), nil); err != nil {
					t.Fatalf("Provide(tools): %v", err)
				}
				if err := ctx.Provide("web", web.New(web.Config{}), nil); err != nil {
					t.Fatalf("Provide(web): %v", err)
				}
			},
			wantParts: []string{"builtinplugins: " + toolWebRowName + " config:", "config does not match"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := cordis.NewRoot()
			tc.provide(t, root)
			body := Registry()[toolWebRowName]()
			err := body.Apply(root, tc.raw)
			if err == nil {
				t.Fatal("Apply accepted a mount it cannot run")
			}
			for _, want := range tc.wantParts {
				if !strings.Contains(err.Error(), want) {
					t.Fatalf("error %q does not contain %q", err, want)
				}
			}
		})
	}
}

// decodeTarget is the decode fixture: pointer fields for "absent vs zero",
// a slice, and a nested object.
type decodeTarget struct {
	Search    *bool    `json:"search"`
	TimeoutMs *int     `json:"timeoutMs"`
	Ratio     *float64 `json:"ratio"`
	Names     []string `json:"names"`
	Inner     struct {
		Value string   `json:"value"`
		Items []string `json:"items"`
	} `json:"inner"`
}

// TestDecodeConfig pins the loader-tree to struct conversion: nested objects,
// the map spelling of loader.Value, nil, and the shapes it refuses.
func TestDecodeConfig(t *testing.T) {
	t.Run("loader object with nested members", func(t *testing.T) {
		inner := loader.NewObject()
		inner.Set("value", "nested")
		inner.Set("items", []loader.Value{"a", "b"})

		root := loader.NewObject()
		root.Set("search", false)
		root.Set("timeoutMs", int64(1500))
		root.Set("ratio", 3.5)
		root.Set("names", []loader.Value{"one", "two"})
		root.Set("inner", inner)

		var got decodeTarget
		if err := decodeConfig(root, &got); err != nil {
			t.Fatalf("decodeConfig: %v", err)
		}
		if got.Search == nil || *got.Search {
			t.Fatalf("Search = %v, want an explicit false", got.Search)
		}
		if got.TimeoutMs == nil || *got.TimeoutMs != 1500 {
			t.Fatalf("TimeoutMs = %v, want 1500", got.TimeoutMs)
		}
		if got.Ratio == nil || *got.Ratio != 3.5 {
			t.Fatalf("Ratio = %v, want 3.5", got.Ratio)
		}
		if !slices.Equal(got.Names, []string{"one", "two"}) {
			t.Fatalf("Names = %v", got.Names)
		}
		if got.Inner.Value != "nested" || !slices.Equal(got.Inner.Items, []string{"a", "b"}) {
			t.Fatalf("Inner = %+v", got.Inner)
		}
	})

	t.Run("map spelling of loader.Value", func(t *testing.T) {
		raw := map[string]loader.Value{
			"timeoutMs": int64(250),
			"inner": map[string]loader.Value{
				"value": "mapped",
			},
		}
		var got decodeTarget
		if err := decodeConfig(raw, &got); err != nil {
			t.Fatalf("decodeConfig: %v", err)
		}
		if got.TimeoutMs == nil || *got.TimeoutMs != 250 {
			t.Fatalf("TimeoutMs = %v, want 250", got.TimeoutMs)
		}
		if got.Inner.Value != "mapped" {
			t.Fatalf("Inner.Value = %q, want mapped", got.Inner.Value)
		}
		if got.Search != nil {
			t.Fatalf("Search = %v, want nil for an absent member", got.Search)
		}
	})

	t.Run("nil leaves the target untouched", func(t *testing.T) {
		got := decodeTarget{Names: []string{"preset"}}
		if err := decodeConfig(nil, &got); err != nil {
			t.Fatalf("decodeConfig(nil): %v", err)
		}
		if !slices.Equal(got.Names, []string{"preset"}) {
			t.Fatalf("Names = %v, want the preset untouched", got.Names)
		}
	})

	t.Run("invalid shapes are errors", func(t *testing.T) {
		cases := []struct {
			name string
			raw  any
			want string
		}{
			{name: "channel", raw: make(chan int), want: "is not a loader value"},
			{name: "scalar", raw: "not an object", want: "config does not match"},
			{name: "sequence", raw: []loader.Value{"a"}, want: "config does not match"},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				err := decodeConfig(tc.raw, &decodeTarget{})
				if err == nil {
					t.Fatalf("decodeConfig(%#v) accepted an unusable shape", tc.raw)
				}
				if !strings.Contains(err.Error(), tc.want) {
					t.Fatalf("error %q does not contain %q", err, tc.want)
				}
			})
		}
	})
}

// TestToolWebOptionsMapping pins the numeric mapping: the official schema's
// millisecond fields become toolweb durations, an `!!js` float64 reaches an
// integer field, and an explicit zero stays zero so toolweb applies its default.
func TestToolWebOptionsMapping(t *testing.T) {
	root := loader.NewObject()
	root.Set("search", false)
	root.Set("searchMaxResults", int64(5))
	root.Set("searchMaxQueries", int64(2))
	root.Set("fetchTimeoutMs", int64(1500))
	// The jsbridge sidecar decodes every JSON number as float64.
	root.Set("searchTimeoutMs", float64(2500))
	root.Set("fetchMaxOutputChars", int64(1000))

	var wire toolWebConfig
	if err := decodeConfig(root, &wire); err != nil {
		t.Fatalf("decodeConfig: %v", err)
	}
	opts := wire.options()
	if !opts.DisableSearch {
		t.Fatal("search: false did not disable web_search")
	}
	if opts.DisableFetch {
		t.Fatal("an absent fetch field disabled web_fetch")
	}
	if opts.SearchMaxResults != 5 || opts.SearchMaxQueries != 2 {
		t.Fatalf("search limits = %d/%d, want 5/2", opts.SearchMaxResults, opts.SearchMaxQueries)
	}
	if opts.FetchTimeout.Milliseconds() != 1500 || opts.SearchTimeout.Milliseconds() != 2500 {
		t.Fatalf("timeouts = %v/%v, want 1500ms/2500ms", opts.FetchTimeout, opts.SearchTimeout)
	}
	if opts.FetchMaxOutputChars != 1000 {
		t.Fatalf("FetchMaxOutputChars = %d, want 1000", opts.FetchMaxOutputChars)
	}

	zero := loader.NewObject()
	zero.Set("searchMaxResults", int64(0))
	var zeroWire toolWebConfig
	if err := decodeConfig(zero, &zeroWire); err != nil {
		t.Fatalf("decodeConfig: %v", err)
	}
	if got := zeroWire.options(); got.SearchMaxResults != 0 || got.DisableSearch || got.DisableFetch {
		t.Fatalf("explicit zero changed the defaults: %+v", got)
	}
}

// fetchOnlyComposition builds the composition whose fetch provider row follows
// the web row, the order the official base bundle uses.
func fetchOnlyComposition() string {
	return `
- insert:
    - id: web
      name: '@deepseek-ai/dsh-web'
    - id: web-fetch-http
      name: '@deepseek-ai/dsh-web-fetch-http'
`
}

// TestFetchProviderRowRegistersAndWithdraws pins both halves of the provider
// row's contract: after mount the seam can fetch through the registered provider
// (a blocked address is the provider's own policy error, which proves it is the
// one answering), and disposing the composition withdraws the registration so a
// later row can reuse the id.
func TestFetchProviderRowRegistersAndWithdraws(t *testing.T) {
	clearProviderEnv(t)
	rt, _ := mountComposition(t, fetchOnlyComposition())
	requireMounted(t, rt)

	w, ok := rt.Root.Get("web").(*web.Web)
	if !ok || w == nil {
		t.Fatalf("web service = %T", rt.Root.Get("web"))
	}
	// 127.0.0.1 is refused by the provider's public-address policy, so the call
	// fails before any network I/O while still proving which provider ran.
	_, err := w.Fetch(context.Background(), web.FetchRequest{URL: "http://127.0.0.1/private"})
	var webErr *web.Error
	if !errors.As(err, &webErr) {
		t.Fatalf("fetch error %v is not a *web.Error", err)
	}
	if webErr.Code != "WEB_BLOCKED_URL" {
		t.Fatalf("code = %q, want the fetch provider's WEB_BLOCKED_URL", webErr.Code)
	}

	// Disposal must run the row's effect, which unregisters the provider.
	if err := rt.Dispose(); err != nil {
		t.Fatalf("Dispose: %v", err)
	}
	if _, err := w.RegisterFetchProvider(fakeFetchProvider{id: "http"}); err != nil {
		t.Fatalf("re-registering provider id http after dispose: %v", err)
	}
}

// TestDeepseekSearchRowRegistersAnUnavailableProvider pins the no-key startup
// path: the provider row still activates, the provider registers, and the seam
// reports it as unusable instead of pretending the capability is absent.
func TestDeepseekSearchRowRegistersAnUnavailableProvider(t *testing.T) {
	clearProviderEnv(t)
	t.Setenv("DEEPSEEK_API_KEY", "")
	rt, _ := mountComposition(t, `
- insert:
    - id: web
      name: '@deepseek-ai/dsh-web'
    - id: web-search-deepseek
      name: '@deepseek-ai/dsh-web-search-deepseek'
`)
	requireMounted(t, rt)
	w, _ := rt.Root.Get("web").(*web.Web)
	_, err := w.Search(context.Background(), web.SearchRequest{Query: "anything"})
	var webErr *web.Error
	if !errors.As(err, &webErr) {
		t.Fatalf("search error %v is not a *web.Error", err)
	}
	if webErr.Code != web.CodeProviderUnavailable {
		t.Fatalf("code = %q, want %q (the provider is registered but has no key)", webErr.Code, web.CodeProviderUnavailable)
	}
}

// fakeFetchProvider stands in for a provider reusing the fetch id after the
// composition was disposed.
type fakeFetchProvider struct{ id string }

func (p fakeFetchProvider) ID() string      { return p.id }
func (p fakeFetchProvider) Available() bool { return true }
func (p fakeFetchProvider) Fetch(context.Context, web.FetchRequest) (web.FetchResult, error) {
	return web.FetchResult{}, nil
}

// TestProviderConfigMapping pins the row-config to provider-config translation,
// including the millisecond boundary the official wire format uses.
func TestProviderConfigMapping(t *testing.T) {
	timeoutMs := 1500
	maxBytes := int64(1024)
	ua := "agent/1"
	fetch := fetchHTTPConfig{
		MaxResponseBytes: &maxBytes,
		TimeoutMs:        &timeoutMs,
		UserAgent:        &ua,
	}
	zeroRedirects := 0
	fetch.MaxRedirects = &zeroRedirects
	got := fetch.providerConfig()
	if got.MaxResponseBytes != maxBytes || got.Timeout.Milliseconds() != 1500 || got.UserAgent != ua {
		t.Fatalf("fetch provider config = %+v", got)
	}
	// Explicit zero must survive: upstream uses it for "follow no redirects",
	// which a plain int would collapse into "use the default".
	if got.MaxRedirects == nil || *got.MaxRedirects != 0 {
		t.Fatalf("MaxRedirects = %v, want an explicit zero", got.MaxRedirects)
	}

	key := "k"
	keyEnv := "MY_KEY"
	model := "m"
	search := deepseekSearchConfig{APIKey: &key, APIKeyEnv: &keyEnv, Model: &model}
	gotSearch := search.providerConfig()
	if gotSearch.APIKey != key || gotSearch.APIKeyEnv != keyEnv || gotSearch.Model != model {
		t.Fatalf("search provider config = %+v", gotSearch)
	}
}

// baseWebComposition is the official base bundle's web stack, in bundle order:
// the service, its two providers, then the tools.
func baseWebComposition() string {
	return `
- insert:
    - id: web
      name: '@deepseek-ai/dsh-web'
    - id: web-search-deepseek
      name: '@deepseek-ai/dsh-web-search-deepseek'
    - id: web-fetch-http
      name: '@deepseek-ai/dsh-web-fetch-http'
    - id: tool-web
      name: '@deepseek-ai/dsh-tool-web'
`
}

// TestMountsTheOfficialWebStack pins the whole base web composition end to end:
// every row activates in bundle order, both tools register, the fetch tool
// reaches the real provider (its loopback policy error proves it), and search
// reports the no-key provider as unavailable rather than missing.
func TestMountsTheOfficialWebStack(t *testing.T) {
	clearProviderEnv(t)
	t.Setenv("DEEPSEEK_API_KEY", "")
	rt, registry := mountComposition(t, baseWebComposition())
	requireMounted(t, rt)

	for _, name := range []string{toolweb.SearchToolName, toolweb.FetchToolName} {
		if _, ok := registry.Lookup(name); !ok {
			t.Fatalf("%s is missing from %v", name, registry.Names())
		}
	}

	fetch, _ := registry.Lookup(toolweb.FetchToolName)
	_, err := fetch.Run(context.Background(), json.RawMessage(`{"url":"http://127.0.0.1/private"}`))
	var webErr *web.Error
	if !errors.As(err, &webErr) || webErr.Code != "WEB_BLOCKED_URL" {
		t.Fatalf("fetch error = %v, want the fetch provider's WEB_BLOCKED_URL", err)
	}

	search, _ := registry.Lookup(toolweb.SearchToolName)
	_, err = search.Run(context.Background(), json.RawMessage(`{"queries":["q"]}`))
	if !errors.As(err, &webErr) || webErr.Code != web.CodeProviderUnavailable {
		t.Fatalf("search error = %v, want %s", err, web.CodeProviderUnavailable)
	}

	// Disposing must withdraw both providers; a stale registration would make
	// a remount fail with a duplicate id. The service handle is captured before
	// the dispose because the web row withdraws the service itself.
	w, ok := rt.Root.Get("web").(*web.Web)
	if !ok || w == nil {
		t.Fatalf("web service = %T", rt.Root.Get("web"))
	}
	if err := rt.Dispose(); err != nil {
		t.Fatalf("Dispose: %v", err)
	}
	if _, err := w.RegisterFetchProvider(fakeFetchProvider{id: "http"}); err != nil {
		t.Fatalf("fetch provider outlived the mount: %v", err)
	}
	if _, err := w.RegisterSearchProvider(&fakeSearchProvider{id: deepseeksearch.ProviderID}); err != nil {
		t.Fatalf("search provider outlived the mount: %v", err)
	}
}
