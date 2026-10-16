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
	if rt.opts.SeedRow != nil {
		if err := rt.opts.SeedRow(e, child); err != nil {
			return nil, fmt.Errorf("runtime: seeding row %q: %w", e.ID, err)
		}
	}

	if e.Group {
		// A carrier contributes its own context but no plugin.
		return nil, nil
	}

	plugin := &cordis.Plugin{
		Name:   e.Name,
		Inject: e.InjectNames(),
		// Apply is intentionally empty until a plugin body exists for the row.
		// The row still activates and its config still resolves, which is what
		// makes the composition observable; a plugin registry that maps a row
		// name to a Go implementation replaces this.
		Apply: func(*cordis.Context, any) error { return nil },
		ResolveConfig: func(*cordis.Context, any) (any, error) {
			return e.ResolveConfig(rt.ctxOrBackground(), rt.Evaluator, rt.Mounter.Scopes)
		},
	}
	return child.Plugin(plugin, e.Config)
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
func (rt *Runtime) Dispose() error {
	return rt.Root.Fiber().Dispose()
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
