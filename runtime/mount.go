// Package runtime mounts a composed profile onto the kernel.
//
// This is the seam between the two halves: `boot` produces an ordered entry
// list from bundle layers, and the kernel mounts plugins onto a context. This
// package drives that, and it is where the loader's two-phase `!!js` evaluation
// becomes real — `disabled` is decided against the loader's scope at each mount
// decision, and `config` is resolved against the row's own fiber context once
// its injections are active.
//
// A row whose injections never resolve stays PENDING: its config is never
// evaluated and its body never runs. That is the official behavior, and it is
// what lets a profile list plugins whose services arrive later.
//
// ON THE TWO SCOPES. The kernel's service store is FLAT, keyed by isolation
// label, so for an unisolated name the loader scope and a row's scope resolve
// the SAME implementation — a parent sees a descendant's service. What differs
// is WHEN each scope is built: the loader scope at the mount decision (few
// services exist yet), the row scope at activation (more exist). The seam
// therefore matters for timing and for isolation, and a composition that
// isolates a service would see the two diverge by content as well.
package runtime

import (
	"context"
	"errors"
	"fmt"
	"os"
	"runtime"
	"strings"

	"github.com/qiyuan2403/Q_RP_dsh/boot"
	"github.com/qiyuan2403/Y_Cordis/cordis"
	"github.com/qiyuan2403/Y_Cordis/loader"
	"github.com/qiyuan2403/Y_Cordis/mount"
)

// Evaluator resolves `!!js` expressions. The sidecar implements it; a nil
// evaluator is allowed, and any expression then fails loudly rather than being
// silently treated as a literal.
type Evaluator = loader.Evaluator

// PluginFactory builds the Go implementation of one composition row.
//
// It returns a fresh plugin per mount instead of a shared instance so each
// mount gets its own closures: a body that captures a service, a counter, or a
// config value must not carry that state into another mount. The kernel already
// keeps inject sets and resolved config per fiber, so those are not the reason.
//
// A body's ResolveConfig hook must NOT mutate the config it receives: the raw
// value can be the composition's own object, and upstream interpolation yields a
// copy before the plugin sees it. A hook that wants to normalize should return a
// new value.
type PluginFactory func() *cordis.Plugin

// Options configures a mount.
type Options struct {
	// Evaluator resolves `!!js`. Nil means expressions are refused.
	Evaluator Evaluator
	// Home is the harness home, used by `dshHomePath` and `process.cwd`.
	Home string
	// Cwd is `process.cwd()` for expressions.
	Cwd string
	// Env is `process.env` for expressions.
	Env map[string]string
	// Platform is `process.platform`; empty derives it from the host.
	Platform string
	// Root is the kernel context to mount onto. Nil creates a fresh one.
	Root *cordis.Context
	// SeedRow registers a row's own services on its context before its config
	// is resolved.
	//
	// A plugin's context normally carries the services the plugin provided; the
	// hook exists so a composition can supply them without a plugin body, which
	// is what makes a row's `config` expression able to read `ctx.<service>`.
	SeedRow func(e *loader.Entry, row *cordis.Context) error
	// Registry maps a loader row name to its Go plugin body. A row with no entry
	// keeps the current placeholder body: it activates but does nothing.
	//
	// The loader composes plugins this host does not resolve yet, so a profile's
	// name is the only handle it has on a Go implementation. Keying by row Name
	// rather than ID is what lets one implementation serve every row that names
	// it; nil means "no implementations registered" and leaves the placeholder
	// path untouched.
	//
	// Inject precedence: the plugin's own list comes first, then the row's names
	// are appended (first occurrence wins). The kernel's Plugin.Inject is a
	// []string without values, so a row cannot override an intercept config yet;
	// when it can, this merge must let the row's value win, as upstream does.
	Registry map[string]PluginFactory
}

// Runtime is a mounted harness.
type Runtime struct {
	// Root is the kernel root context.
	Root *cordis.Context
	// Mounter drives rows through the two-phase evaluation.
	Mounter *mount.Mounter
	// Rows records each row's outcome, in composition order.
	Rows []mount.Mounted
	// Warnings are the composition's skipped-patch diagnostics.
	Warnings []string
	// Evaluator is the `!!js` evaluator in use, when any.
	Evaluator Evaluator
	// opts is retained so the row loader can consult the seeding hook.
	opts Options
	// rowContexts are the per-row scopes created during the mount, in
	// composition order. Their fibers own the services SeedRow provided, and the
	// kernel does not cascade a dispose into child fibers, so Dispose must
	// withdraw them itself or a reuse of Options.Root would see stale services.
	rowContexts []*cordis.Context
}

// Mount composes a profile and mounts it.
func Mount(ctx context.Context, profileDir, installAnchor string, opts Options) (*Runtime, error) {
	composition, err := boot.LoadProfile(profileDir, installAnchor, nil)
	if err != nil {
		return nil, err
	}
	return MountComposition(ctx, composition, opts)
}

// MountComposition mounts an already-composed entry list.
//
// The rows are mounted in composition order. That order is what makes a later
// layer's configuration of an earlier layer's row observable, so it is
// preserved rather than sorted.
func MountComposition(ctx context.Context, composition *boot.Composition, opts Options) (*Runtime, error) {
	root := opts.Root
	if root == nil {
		root = cordis.NewRoot()
	}
	// The root fiber must be ACTIVE for the loader scope to resolve services:
	// `Get` only returns implementations whose providing fiber is active.
	root.Fiber().SetStateForTest(cordis.Active)

	platform := opts.Platform
	if platform == "" {
		platform = hostPlatform()
	}
	scopes := &mount.Scopes{
		LoaderCtx: root,
		Env:       opts.Env,
		Cwd:       opts.Cwd,
		Platform:  platform,
		Home:      opts.Home,
		Helpers:   []string{"dshHomePath"},
	}

	rt := &Runtime{
		Root:      root,
		Evaluator: opts.Evaluator,
		Warnings:  composition.Warnings,
		opts:      opts,
	}

	// The row's own context is not known until the row loads, so it is recorded
	// during Load and read back by RowScope. This is what keeps `config`
	// evaluation bound to the row that owns it.
	rowContexts := map[*loader.Entry]*cordis.Context{}
	scopes.RowCtx = func(e *loader.Entry) (*cordis.Context, error) {
		ctx, ok := rowContexts[e]
		if !ok {
			return nil, fmt.Errorf("runtime: no context recorded for row %q", e.ID)
		}
		return ctx, nil
	}

	mounter := &mount.Mounter{
		Eval:   opts.Evaluator,
		Scopes: scopes,
		Ctx:    ctx,
		Load: func(e *loader.Entry) (*cordis.Fiber, error) {
			return rt.loadRow(e, rowContexts)
		},
	}
	rt.Mounter = mounter

	rows, err := mounter.MountAll(composition.Entries)
	rt.Rows = rows
	if err != nil {
		return rt, err
	}
	return rt, nil
}

// loadRow mounts one row onto the kernel.
//
// A group row is a carrier, not a plugin: its `config` holds child rows, so it
// is recorded without a fiber and its children are mounted by the caller's
// order. Mounting a carrier as a plugin would try to run a plugin body that
// does not exist.
func (rt *Runtime) loadRow(e *loader.Entry, rowContexts map[*loader.Entry]*cordis.Context) (*cordis.Fiber, error) {
	child := rt.Root.Child()
	child.Fiber().SetStateForTest(cordis.Active)
	rowContexts[e] = child
	rt.rowContexts = append(rt.rowContexts, child)
	if rt.opts.SeedRow != nil {
		if err := rt.opts.SeedRow(e, child); err != nil {
			return nil, fmt.Errorf("runtime: seeding row %q: %w", e.ID, err)
		}
	}

	if e.Group {
		// A carrier contributes its own context but no plugin.
		return nil, nil
	}

	// Resolving the row config is shared by both bodies: the placeholder still
	// evaluates `!!js`, and a registered plugin needs the evaluated result
	// before its own normalization runs.
	resolveRowConfig := func(*cordis.Context, any) (any, error) {
		return e.ResolveConfig(rt.ctxOrBackground(), rt.Evaluator, rt.Mounter.Scopes)
	}

	factory, registered := rt.opts.Registry[e.Name]
	// A present key with a nil value is a wiring bug, not "no implementation":
	// treating it as unregistered would activate the row with the placeholder
	// body and hide the mistake until someone wonders why the plugin does
	// nothing. Refuse it here, where the row that names it is known.
	if registered && factory == nil {
		return nil, fmt.Errorf("runtime: plugin %q for row %q: registry entry is nil", e.Name, e.ID)
	}
	if !registered {
		plugin := &cordis.Plugin{
			Name:   e.Name,
			Inject: e.InjectNames(),
			// Apply is intentionally empty: without a Registry entry the row
			// still activates and its config still resolves, which keeps the
			// composition observable while no Go body exists for the plugin.
			Apply:         func(*cordis.Context, any) error { return nil },
			ResolveConfig: resolveRowConfig,
		}
		return child.Plugin(plugin, e.Config)
	}

	impl := factory()
	if impl == nil {
		return nil, fmt.Errorf("runtime: plugin %q for row %q: registry factory returned nil", e.Name, e.ID)
	}

	name := e.Name
	if name == "" {
		name = impl.Name
	}
	plugin := &cordis.Plugin{
		// The row name wins: it is what the composition and its diagnostics
		// name, and what identifies the row to the operator. A row without a
		// name can still carry the implementation's own.
		Name:    name,
		Inject:  mergeInject(impl.Inject, e.InjectNames()),
		Provide: impl.Provide,
		Apply:   impl.Apply,
		ResolveConfig: func(ctx *cordis.Context, raw any) (any, error) {
			// Row interpolation first, plugin normalization second: the official
			// loader interpolates in the `internal/config` waterfall and the
			// plugin's Config schema sees the evaluated object (index.ts:112).
			resolved, err := resolveRowConfig(ctx, raw)
			if err != nil {
				return nil, err
			}
			if impl.ResolveConfig == nil {
				return resolved, nil
			}
			return impl.ResolveConfig(ctx, resolved)
		},
	}
	return child.Plugin(plugin, e.Config)
}

// mergeInject appends the row's declared injections to the plugin's own.
//
// The official loader resolves an entry's inject INTO the fiber's inject map
// after the plugin's own has been seeded (vendor/loader/src/index.ts:134), so a
// row can add requirements but never drop or reorder the plugin's. First
// occurrence wins, which keeps the merged order stable for diagnostics and makes
// duplicate declarations harmless.
func mergeInject(own, row []string) []string {
	if len(own) == 0 && len(row) == 0 {
		return nil
	}
	out := make([]string, 0, len(own)+len(row))
	seen := make(map[string]struct{}, len(own)+len(row))
	for _, list := range [][]string{own, row} {
		for _, name := range list {
			if _, ok := seen[name]; ok {
				continue
			}
			seen[name] = struct{}{}
			out = append(out, name)
		}
	}
	return out
}

func (rt *Runtime) ctxOrBackground() context.Context {
	if rt.Mounter != nil && rt.Mounter.Ctx != nil {
		return rt.Mounter.Ctx
	}
	return context.Background()
}

// Counts summarizes the mount outcome.
func (rt *Runtime) Counts() (mounted, skippedDisabled, skippedPending, skippedFailed int) {
	for _, row := range rt.Rows {
		switch {
		case !row.Skipped:
			mounted++
		case row.Reason == "disabled":
			skippedDisabled++
		case row.Reason == "injections pending":
			skippedPending++
		case row.Reason == "activation failed":
			skippedFailed++
		}
	}
	return
}

// Describe renders a one-line summary of the mount.
func (rt *Runtime) Describe() string {
	mounted, disabled, pending, failed := rt.Counts()
	var b strings.Builder
	fmt.Fprintf(&b, "%d rows: %d mounted", len(rt.Rows), mounted)
	if disabled > 0 {
		fmt.Fprintf(&b, ", %d disabled", disabled)
	}
	if pending > 0 {
		fmt.Fprintf(&b, ", %d pending injections", pending)
	}
	if failed > 0 {
		fmt.Fprintf(&b, ", %d failed", failed)
	}
	if len(rt.Warnings) > 0 {
		fmt.Fprintf(&b, ", %d composition warnings", len(rt.Warnings))
	}
	return b.String()
}

// Dispose tears the mounted harness down.
//
// Rows are disposed in reverse composition order, then the root: a row's effects
// were registered while mounting, so LIFO is the order that withdraws dependents
// before their dependencies. The kernel does not propagate a parent's dispose
// into child fibers (the upstream model does, by having ctx.plugin register the
// child's disposer as an effect of the parent), so the runtime has to walk its
// own rows. Without that walk every row's registered tools, providers, and
// servers would outlive the mount.
//
// A fiber mounted BY a plugin body (ctx.plugin inside Apply) is still not
// reachable from here; this walks one level, which is what the loader creates.
func (rt *Runtime) Dispose() error {
	var errs []error
	for i := len(rt.Rows) - 1; i >= 0; i-- {
		fiber := rt.Rows[i].Fiber
		if fiber == nil {
			continue
		}
		if err := fiber.Dispose(); err != nil {
			errs = append(errs, fmt.Errorf("runtime: disposing row %q: %w", rt.Rows[i].Entry.ID, err))
		}
	}
	// The row scopes are disposed after their plugin fibers, deepest effect
	// first: a scope's fiber owns the services SeedRow provided, and leaving it
	// active would keep them visible to a later mount on the same root.
	for i := len(rt.rowContexts) - 1; i >= 0; i-- {
		ctx := rt.rowContexts[i]
		if ctx == nil || ctx.Fiber() == nil {
			continue
		}
		if err := ctx.Fiber().Dispose(); err != nil {
			errs = append(errs, fmt.Errorf("runtime: disposing row scope %d: %w", i, err))
		}
	}
	if rt.Root != nil {
		if err := rt.Root.Fiber().Dispose(); err != nil {
			errs = append(errs, fmt.Errorf("runtime: disposing root: %w", err))
		}
	}
	return errors.Join(errs...)
}

// hostPlatform maps the Go OS name to the Node-style platform name expressions
// expect, since a `!!js` expression tests `process.platform === 'win32'`.
func hostPlatform() string {
	switch runtime.GOOS {
	case "windows":
		return "win32"
	case "darwin":
		return "darwin"
	default:
		return runtime.GOOS
	}
}

// HomeDir resolves the harness home for expression scopes.
func HomeDir() string {
	if dir, err := os.UserHomeDir(); err == nil {
		return dir
	}
	return ""
}
