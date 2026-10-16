package runtime

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"

	"github.com/qiyuan2403/Q_RP_dsh/boot"
	"github.com/qiyuan2403/Y_Cordis/cordis"
	"github.com/qiyuan2403/Y_Cordis/loader"
)

// fakeEvaluator answers the expressions the shipped bundles use, so the mount
// path is exercised without a Node runtime.
type fakeEvaluator struct {
	profileContext any
}

func (f *fakeEvaluator) Eval(_ context.Context, expr string, scope *loader.EvalScope) (any, error) {
	switch expr {
	case "!ctx.get('profileContext')":
		return f.profileContext == nil, nil
	case "process.platform === 'win32'":
		return false, nil
	case "process.platform !== 'win32'":
		return true, nil
	case "someExpression()":
		return true, nil
	}
	// An unexpected expression is a test bug, not a value to invent: returning a
	// plausible default would hide a mismatch between the fixture and the
	// bundle.
	return nil, fmt.Errorf("fakeEvaluator: unexpected expression %q", expr)
}

// TestMountDecidesDisabledFromTheExpression pins that a `!!js` disabled flag is
// decided by the evaluator, so the mount decision is driven by the expression
// rather than by the parsed literal.
func TestMountDecidesDisabledFromTheExpression(t *testing.T) {
	layer, err := boot.LoadLayerFromString("l", `
- insert:
    - id: gated
      name: pkg
      disabled: !!js process.platform === 'win32'
    - id: free
      name: pkg2
`)
	if err != nil {
		t.Fatalf("layer: %v", err)
	}
	composition, err := boot.Compose([]boot.Layer{layer})
	if err != nil {
		t.Fatalf("Compose: %v", err)
	}

	// On linux the expression is false, so the row mounts.
	linux, err := MountComposition(context.Background(), composition, Options{
		Evaluator: &win32Evaluator{win32: false},
		Platform:  "linux",
	})
	if err != nil {
		t.Fatalf("MountComposition: %v", err)
	}
	defer linux.Dispose()
	if linux.Rows[0].Skipped {
		t.Fatalf("on linux the row should mount, got %+v", linux.Rows[0])
	}

	// On win32 the SAME row is disabled. This is the property that matters: the
	// decision is re-evaluated from the expression, not baked at parse time.
	win, err := MountComposition(context.Background(), composition, Options{
		Evaluator: &win32Evaluator{win32: true},
		Platform:  "win32",
	})
	if err != nil {
		t.Fatalf("MountComposition: %v", err)
	}
	defer win.Dispose()
	if !win.Rows[0].Skipped || win.Rows[0].Reason != "disabled" {
		t.Fatalf("on win32 the row should be disabled, got %+v", win.Rows[0])
	}
}

// win32Evaluator answers the platform probe from a field, so one test can run
// both branches of the same expression.
type win32Evaluator struct{ win32 bool }

func (w *win32Evaluator) Eval(_ context.Context, expr string, _ *loader.EvalScope) (any, error) {
	if expr == "process.platform === 'win32'" {
		return w.win32, nil
	}
	return nil, fmt.Errorf("win32Evaluator: unexpected expression %q", expr)
}

// TestMountReportsPendingRows pins that a row whose injection nobody provides
// stays pending instead of activating.
func TestMountReportsPendingRows(t *testing.T) {
	layer, err := boot.LoadLayerFromString("l", `
- insert:
    - id: needs-service
      name: pkg
      inject: [nobody-provides-this]
    - id: free
      name: pkg2
`)
	if err != nil {
		t.Fatalf("layer: %v", err)
	}
	composition, err := boot.Compose([]boot.Layer{layer})
	if err != nil {
		t.Fatalf("Compose: %v", err)
	}

	rt, err := MountComposition(context.Background(), composition, Options{
		Evaluator: &fakeEvaluator{},
		Platform:  "linux",
	})
	if err != nil {
		t.Fatalf("MountComposition: %v", err)
	}
	defer rt.Dispose()

	if len(rt.Rows) != 2 {
		t.Fatalf("got %d rows, want 2", len(rt.Rows))
	}
	if !rt.Rows[0].Skipped || rt.Rows[0].Reason != "injections pending" {
		t.Fatalf("row 0 = %+v, want pending", rt.Rows[0])
	}
	if rt.Rows[1].Skipped {
		t.Fatalf("row 1 = %+v, want mounted", rt.Rows[1])
	}
}

// TestRowConfigIsResolvedAgainstTheRowsOwnContext pins the second half of the
// two-phase contract: a row's config expression is evaluated against THAT ROW's
// context, not the loader's.
//
// The probe is a service the ROW provides. Note that this does NOT distinguish
// the row's context from the loader's by identity: the kernel's service store is
// flat, so an unisolated name resolves the same from either. What it verifies is
// that the config path supplies a scope carrying the row's services at all —
// a scope built before the row loaded would miss them.
func TestRowConfigIsResolvedAgainstTheRowsOwnContext(t *testing.T) {
	layer, err := boot.LoadLayerFromString("l", `
- insert:
    - id: config-row
      name: pkg
      config:
        value: !!js ctx.rowService.marker
`)
	if err != nil {
		t.Fatalf("layer: %v", err)
	}
	composition, err := boot.Compose([]boot.Layer{layer})
	if err != nil {
		t.Fatalf("Compose: %v", err)
	}

	ev := &rowServiceEvaluator{}
	rt, err := MountComposition(context.Background(), composition, Options{
		Evaluator: ev,
		Platform:  "linux",
		// The harness seeds each row's context with its own services through
		// this hook, which is how a plugin's own context becomes readable.
		SeedRow: func(e *loader.Entry, row *cordis.Context) error {
			return row.Provide("rowService", map[string]any{"marker": "from-the-row"}, nil)
		},
	})
	if err != nil {
		t.Fatalf("MountComposition: %v", err)
	}
	defer rt.Dispose()

	if !ev.called {
		t.Fatal("the config expression was never evaluated")
	}
	if ev.sawValue != "from-the-row" {
		t.Fatalf("the expression saw %v, want the row's own service", ev.sawValue)
	}
}

// rowServiceEvaluator resolves the row-scoped probe from the scope it receives.
type rowServiceEvaluator struct {
	called   bool
	sawValue any
}

func (s *rowServiceEvaluator) Eval(_ context.Context, expr string, scope *loader.EvalScope) (any, error) {
	if expr != "ctx.rowService.marker" {
		return nil, fmt.Errorf("rowServiceEvaluator: unexpected expression %q", expr)
	}
	s.called = true
	if scope == nil {
		return nil, errors.New("no scope was supplied")
	}
	svc, ok := scope.Services["rowService"]
	if !ok {
		return nil, errors.New("the scope does not carry the row's service")
	}
	m, ok := svc.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("rowService is %T", svc)
	}
	s.sawValue = m["marker"]
	return m["marker"], nil
}

// TestMountLeavesGroupCarriersWithoutAFiber pins that a group row is a carrier,
// not a plugin.
func TestMountLeavesGroupCarriersWithoutAFiber(t *testing.T) {
	layer, err := boot.LoadLayerFromString("l", `
- insert:
    - id: grp
      name: group
      group: true
      config: []
`)
	if err != nil {
		t.Fatalf("layer: %v", err)
	}
	composition, err := boot.Compose([]boot.Layer{layer})
	if err != nil {
		t.Fatalf("Compose: %v", err)
	}
	rt, err := MountComposition(context.Background(), composition, Options{
		Evaluator: &fakeEvaluator{},
		Platform:  "linux",
	})
	if err != nil {
		t.Fatalf("MountComposition: %v", err)
	}
	defer rt.Dispose()
	if rt.Rows[0].Fiber != nil {
		t.Fatal("a group carrier was given a fiber")
	}
}

// TestMountWithoutEvaluatorRefusesExpressions pins fail-loud: with no evaluator
// a `!!js` row fails rather than being treated as a literal.
func TestMountWithoutEvaluatorRefusesExpressions(t *testing.T) {
	layer, err := boot.LoadLayerFromString("l", `
- insert:
    - id: gated
      name: pkg
      disabled: !!js someExpression()
`)
	if err != nil {
		t.Fatalf("layer: %v", err)
	}
	composition, err := boot.Compose([]boot.Layer{layer})
	if err != nil {
		t.Fatalf("Compose: %v", err)
	}
	if _, err := MountComposition(context.Background(), composition, Options{}); err == nil {
		t.Fatal("an expression was accepted with no evaluator")
	}
}

// TestDisposeTearsDownTheMountedTree pins teardown.
func TestDisposeTearsDownTheMountedTree(t *testing.T) {
	layer, _ := boot.LoadLayerFromString("l", "- insert:\n    - id: a\n      name: pkg\n")
	composition, _ := boot.Compose([]boot.Layer{layer})
	rt, err := MountComposition(context.Background(), composition, Options{
		Evaluator: &fakeEvaluator{},
		Platform:  "linux",
	})
	if err != nil {
		t.Fatalf("MountComposition: %v", err)
	}
	if err := rt.Dispose(); err != nil {
		t.Fatalf("Dispose: %v", err)
	}
	if rt.Root.Fiber().State() != cordis.Disposed {
		t.Fatalf("state = %v, want DISPOSED", rt.Root.Fiber().State())
	}
}

var _ = os.Getenv
