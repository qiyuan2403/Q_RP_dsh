package runtime

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/qiyuan2403/Q_RP_dsh/boot"
	"github.com/qiyuan2403/Y_Cordis/cordis"
	"github.com/qiyuan2403/Y_Cordis/loader"
)

// exprEvaluator answers named expressions from a table. A test that reaches an
// expression it did not declare fails rather than inventing a value.
type exprEvaluator map[string]any

func (e exprEvaluator) Eval(_ context.Context, expr string, _ *loader.EvalScope) (any, error) {
	v, ok := e[expr]
	if !ok {
		return nil, fmt.Errorf("exprEvaluator: unexpected expression %q", expr)
	}
	return v, nil
}

// composeSingle builds a one-layer composition from an entry list.
func composeSingle(t *testing.T, src string) *boot.Composition {
	t.Helper()
	layer, err := boot.LoadLayerFromString("l", src)
	if err != nil {
		t.Fatalf("layer: %v", err)
	}
	composition, err := boot.Compose([]boot.Layer{layer})
	if err != nil {
		t.Fatalf("Compose: %v", err)
	}
	return composition
}

// configMember reads one member of a resolved config object.
func configMember(t *testing.T, cfg any, key string) any {
	t.Helper()
	obj, ok := cfg.(*loader.Object)
	if !ok {
		t.Fatalf("config is %T, want *loader.Object", cfg)
	}
	v, ok := obj.Get(key)
	if !ok {
		t.Fatalf("config has no %q member", key)
	}
	return v
}

// TestRegistryMountsPluginBody pins that a registered row runs the factory's
// Apply with the row's resolved config, and that the row name wins for
// diagnostics.
func TestRegistryMountsPluginBody(t *testing.T) {
	composition := composeSingle(t, `
- insert:
    - id: registered
      name: example/plugin
      inject: [row-service]
      config:
        marker: !!js registryMount()
`)

	var applied bool
	var gotCtx *cordis.Context
	var gotConfig any
	rt, err := MountComposition(context.Background(), composition, Options{
		Evaluator: exprEvaluator{"registryMount()": "resolved-value"},
		Platform:  "linux",
		SeedRow: func(_ *loader.Entry, row *cordis.Context) error {
			return row.Provide("row-service", map[string]any{"marker": "seeded"}, nil)
		},
		Registry: map[string]PluginFactory{
			"example/plugin": func() *cordis.Plugin {
				return &cordis.Plugin{
					// The row name must override this, so diagnostics can name
					// the composition row that produced the fiber.
					Name: "implementation-name",
					Apply: func(ctx *cordis.Context, cfg any) error {
						applied = true
						gotCtx = ctx
						gotConfig = cfg
						return nil
					},
				}
			},
		},
	})
	if err != nil {
		t.Fatalf("MountComposition: %v", err)
	}
	defer rt.Dispose()

	if rt.Rows[0].Skipped {
		t.Fatalf("registered row was skipped: %+v", rt.Rows[0])
	}
	if !applied {
		t.Fatal("the registered plugin body never ran")
	}
	if gotCtx == nil {
		t.Fatal("Apply received no context")
	}
	if gotCtx.Get("row-service") == nil {
		t.Fatal("Apply's context cannot resolve the row's service")
	}
	if v := configMember(t, gotConfig, "marker"); v != "resolved-value" {
		t.Fatalf("Apply saw marker %v, want the evaluated value", v)
	}
	if name := rt.Rows[0].Fiber.Name(); name != "example/plugin" {
		t.Fatalf("fiber name = %q, want the row name", name)
	}
}

// TestRegistryMergesPluginAndRowInjections pins that the plugin's own inject
// declarations and the row's are both enforced: the row stays pending when
// only one side is available, and activates when both are.
func TestRegistryMergesPluginAndRowInjections(t *testing.T) {
	composition := composeSingle(t, `
- insert:
    - id: merged
      name: example/merged
      inject: [row-b]
`)

	factory := func() *cordis.Plugin {
		return &cordis.Plugin{
			Inject: []string{"plugin-a"},
			Apply:  func(*cordis.Context, any) error { return nil },
		}
	}

	// Only the row's own service exists. The row must stay pending, which
	// proves the PLUGIN's inject was kept: providing only row-b would activate
	// this row if mergeInject had dropped what the plugin declared.
	rt, err := MountComposition(context.Background(), composition, Options{
		Evaluator: exprEvaluator{},
		Platform:  "linux",
		SeedRow: func(_ *loader.Entry, row *cordis.Context) error {
			return row.Provide("row-b", struct{}{}, nil)
		},
		Registry: map[string]PluginFactory{"example/merged": factory},
	})
	if err != nil {
		t.Fatalf("MountComposition: %v", err)
	}
	if !rt.Rows[0].Skipped || rt.Rows[0].Reason != "injections pending" {
		t.Fatalf("row = %+v, want pending on the row's injection", rt.Rows[0])
	}
	_ = rt.Dispose()

	// Both resolve: the row activates, so the row's injection was merged too.
	rt, err = MountComposition(context.Background(), composition, Options{
		Evaluator: exprEvaluator{},
		Platform:  "linux",
		SeedRow: func(_ *loader.Entry, row *cordis.Context) error {
			if err := row.Provide("plugin-a", struct{}{}, nil); err != nil {
				return err
			}
			return row.Provide("row-b", struct{}{}, nil)
		},
		Registry: map[string]PluginFactory{"example/merged": factory},
	})
	if err != nil {
		t.Fatalf("MountComposition: %v", err)
	}
	defer rt.Dispose()
	if rt.Rows[0].Skipped {
		t.Fatalf("row = %+v, want mounted with both injections", rt.Rows[0])
	}
}

// TestMergeInjectOrderAndDedup pins the exact merged set: the plugin's names
// first, then the row's additions, first occurrence winning.
func TestMergeInjectOrderAndDedup(t *testing.T) {
	cases := []struct {
		name string
		own  []string
		row  []string
		want []string
	}{
		{name: "both empty", want: nil},
		{name: "plugin only", own: []string{"a"}, want: []string{"a"}},
		{name: "row appended", own: []string{"a"}, row: []string{"b"}, want: []string{"a", "b"}},
		{name: "row duplicate dropped", own: []string{"a"}, row: []string{"a", "b"}, want: []string{"a", "b"}},
		{name: "plugin duplicate dropped", own: []string{"a", "a"}, row: []string{"b", "a"}, want: []string{"a", "b"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := mergeInject(tc.own, tc.row)
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("mergeInject(%v, %v) = %v, want %v", tc.own, tc.row, got, tc.want)
			}
		})
	}
}

// TestRegistryResolvesRowConfigBeforePluginConfig pins the order of the config
// waterfall: the row's `!!js` is interpolated first, then the plugin's
// ResolveConfig normalizes the evaluated value.
func TestRegistryResolvesRowConfigBeforePluginConfig(t *testing.T) {
	composition := composeSingle(t, `
- insert:
    - id: ordered
      name: example/ordered
      config:
        stage: !!js registryOrder()
`)

	var pluginSaw any
	var applied any
	rt, err := MountComposition(context.Background(), composition, Options{
		Evaluator: exprEvaluator{"registryOrder()": "evaluated"},
		Platform:  "linux",
		Registry: map[string]PluginFactory{
			"example/ordered": func() *cordis.Plugin {
				return &cordis.Plugin{
					ResolveConfig: func(_ *cordis.Context, raw any) (any, error) {
						pluginSaw = raw
						return map[string]any{"normalized": raw}, nil
					},
					Apply: func(_ *cordis.Context, cfg any) error {
						applied = cfg
						return nil
					},
				}
			},
		},
	})
	if err != nil {
		t.Fatalf("MountComposition: %v", err)
	}
	defer rt.Dispose()

	// The plugin must see the interpolated object, never the raw JsExpr tree.
	if v := configMember(t, pluginSaw, "stage"); v != "evaluated" {
		t.Fatalf("plugin ResolveConfig saw stage %v, want the interpolated value", v)
	}
	// And Apply must see the plugin's normalized result, not the row's.
	m, ok := applied.(map[string]any)
	if !ok {
		t.Fatalf("Apply config is %T, want the plugin's map", applied)
	}
	if m["normalized"] != pluginSaw {
		t.Fatalf("Apply saw %v, want the plugin's normalized output", applied)
	}
}

// TestUnregisteredRowKeepsPlaceholderBody pins the no-registry behavior: the
// row still activates, and no factory is consulted for a name that has no
// entry.
func TestUnregisteredRowKeepsPlaceholderBody(t *testing.T) {
	composition := composeSingle(t, `
- insert:
    - id: plain
      name: plain/plugin
`)

	factoryCalled := false
	rt, err := MountComposition(context.Background(), composition, Options{
		Evaluator: exprEvaluator{},
		Platform:  "linux",
		Registry: map[string]PluginFactory{
			"other/plugin": func() *cordis.Plugin {
				factoryCalled = true
				return &cordis.Plugin{Apply: func(*cordis.Context, any) error { return nil }}
			},
		},
	})
	if err != nil {
		t.Fatalf("MountComposition: %v", err)
	}
	defer rt.Dispose()

	if factoryCalled {
		t.Fatal("a factory ran for a row whose name is not registered")
	}
	if rt.Rows[0].Skipped {
		t.Fatalf("unregistered row was skipped: %+v", rt.Rows[0])
	}
	if rt.Rows[0].Fiber == nil || rt.Rows[0].Fiber.State() != cordis.Active {
		t.Fatalf("unregistered row did not activate: %+v", rt.Rows[0])
	}
}

// TestRegistryNilFactoryFailsMount pins fail-loud: a registered name whose
// factory returns nil is an error, and the message names the row.
func TestRegistryNilFactoryFailsMount(t *testing.T) {
	composition := composeSingle(t, `
- insert:
    - id: nil-body
      name: example/nil
`)

	rt, err := MountComposition(context.Background(), composition, Options{
		Evaluator: exprEvaluator{},
		Platform:  "linux",
		Registry: map[string]PluginFactory{
			"example/nil": func() *cordis.Plugin { return nil },
		},
	})
	if rt != nil {
		defer rt.Dispose()
	}
	if err == nil {
		t.Fatal("a nil plugin implementation was accepted")
	}
	for _, want := range []string{"runtime:", "nil-body", "example/nil"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error %q does not mention %q", err, want)
		}
	}
}

// TestDisposeRunsRowEffects pins that unmounting a composition withdraws what
// its rows registered. The kernel does not cascade a parent dispose into child
// fibers, so without the runtime's own LIFO walk every plugin effect (registered
// tools, providers, servers) would survive Dispose, and a later mount would see
// duplicate registrations from a harness that claims to be torn down.
func TestDisposeRunsRowEffects(t *testing.T) {
	layer, err := boot.LoadLayerFromString("test", `
- insert:
    - id: plugin
      name: 'test-plugin'
`)
	if err != nil {
		t.Fatalf("LoadLayerFromString: %v", err)
	}
	composition, err := boot.Compose([]boot.Layer{layer})
	if err != nil {
		t.Fatalf("Compose: %v", err)
	}

	var disposed int
	rt, err := MountComposition(context.Background(), composition, Options{
		Platform: "linux",
		Registry: map[string]PluginFactory{
			"test-plugin": func() *cordis.Plugin {
				return &cordis.Plugin{
					Name: "test-plugin",
					Apply: func(ctx *cordis.Context, _ any) error {
						return ctx.Fiber().Effect(func() (cordis.Disposer, error) {
							return func() error {
								disposed++
								return nil
							}, nil
						})
					},
				}
			},
		},
	})
	if err != nil {
		t.Fatalf("MountComposition: %v", err)
	}
	if err := rt.Dispose(); err != nil {
		t.Fatalf("Dispose: %v", err)
	}
	if disposed != 1 {
		t.Fatalf("the row's effect disposer ran %d times, want exactly once", disposed)
	}
}

// TestNilRegistryEntryFailsLoudly pins that a registry key with no factory is
// refused instead of falling back to the placeholder body. Falling back would
// activate the row as if nothing were configured, which hides the wiring
// mistake the registry entry was supposed to catch.
func TestNilRegistryEntryFailsLoudly(t *testing.T) {
	layer, err := boot.LoadLayerFromString("test", `
- insert:
    - id: plugin
      name: 'test-plugin'
`)
	if err != nil {
		t.Fatalf("LoadLayerFromString: %v", err)
	}
	composition, err := boot.Compose([]boot.Layer{layer})
	if err != nil {
		t.Fatalf("Compose: %v", err)
	}
	_, err = MountComposition(context.Background(), composition, Options{
		Platform: "linux",
		Registry: map[string]PluginFactory{"test-plugin": nil},
	})
	if err == nil {
		t.Fatal("a nil registry entry was accepted")
	}
	if !strings.Contains(err.Error(), "registry entry is nil") {
		t.Fatalf("error = %v, want the registry-entry diagnostic", err)
	}
}

// TestDisposeReleasesSeedRowServices pins that a row scope's services are
// withdrawn with the mount. The kernel does not cascade a dispose into child
// fibers, so without the runtime's own row-scope walk a later mount on the same
// root would fail with "service already registered" after the first mount
// claimed to be torn down.
func TestDisposeReleasesSeedRowServices(t *testing.T) {
	root := cordis.NewRoot()
	composition := composeSingle(t, `
- insert:
    - id: seed
      name: example/seed
`)
	opts := Options{
		Platform: "linux",
		Root:     root,
		SeedRow: func(_ *loader.Entry, row *cordis.Context) error {
			return row.Provide("seed-svc", struct{}{}, nil)
		},
		Registry: map[string]PluginFactory{
			"example/seed": func() *cordis.Plugin {
				return &cordis.Plugin{Name: "seed", Apply: func(*cordis.Context, any) error { return nil }}
			},
		},
	}

	rt, err := MountComposition(context.Background(), composition, opts)
	if err != nil {
		t.Fatalf("first MountComposition: %v", err)
	}
	if root.Get("seed-svc") == nil {
		t.Fatal("the seeded service was never provided")
	}
	if err := rt.Dispose(); err != nil {
		t.Fatalf("Dispose: %v", err)
	}
	if got := root.Get("seed-svc"); got != nil {
		t.Fatalf("seed service outlived the mount: %v", got)
	}

	// Reusing the same root is the assertion: it only works if the previous
	// mount released everything it registered.
	rt2, err := MountComposition(context.Background(), composition, opts)
	if err != nil {
		t.Fatalf("second MountComposition on the same root: %v", err)
	}
	if err := rt2.Dispose(); err != nil {
		t.Fatalf("second Dispose: %v", err)
	}
}
