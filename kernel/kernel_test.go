package kernel

import (
	"testing"

	"github.com/qiyuan2403/Y_Cordis/cordis"
)

// TestProvideRejectsUnknownServiceName pins the typo guard: a misspelled
// service name would otherwise register something no plugin ever injects, and
// the failure would appear much later as a plugin that never activates.
func TestProvideRejectsUnknownServiceName(t *testing.T) {
	h := New()
	if err := h.Provide("sesion", 1, nil); err == nil {
		t.Fatal("a misspelled service name was accepted")
	}
	if err := h.Provide(ServiceSession, 1, nil); err != nil {
		t.Fatalf("Provide(%s): %v", ServiceSession, err)
	}
}

// TestProvidedListsWhatWasRegistered pins the bookkeeping.
func TestProvidedListsWhatWasRegistered(t *testing.T) {
	h := New()
	if len(h.Provided()) != 0 {
		t.Fatalf("a fresh harness reports %v", h.Provided())
	}
	if err := h.Provide(ServiceTools, "registry", nil); err != nil {
		t.Fatalf("Provide: %v", err)
	}
	got := h.Provided()
	if len(got) != 1 || got[0] != ServiceTools {
		t.Fatalf("Provided = %v", got)
	}
}

// TestWebServiceNameIsAccepted pins that the web seam is part of the service
// ABI. A web tool plugin injects "web"; if the name were rejected here the
// plugin could never leave PENDING and the failure would look like a loader
// bug rather than a missing service registration.
func TestWebServiceNameIsAccepted(t *testing.T) {
	h := New()
	if err := h.Provide(ServiceWeb, "web-runtime", nil); err != nil {
		t.Fatalf("Provide(%s): %v", ServiceWeb, err)
	}
	if ServiceWeb != "web" {
		t.Fatalf("ServiceWeb = %q, want the official name", ServiceWeb)
	}
	if got := h.Provided(); len(got) != 1 || got[0] != ServiceWeb {
		t.Fatalf("Provided = %v", got)
	}
}

// TestPluginStaysPendingUntilItsInjectionArrives pins that the harness composes
// with the kernel's reactivity rather than working around it: a plugin that
// injects a service waits for it.
func TestPluginStaysPendingUntilItsInjectionArrives(t *testing.T) {
	h := New()
	applied := false
	f, err := h.Mount(&cordis.Plugin{
		Name:   "consumer",
		Inject: []string{ServiceTools},
		Apply: func(*cordis.Context, any) error {
			applied = true
			return nil
		},
	})
	if err != nil {
		t.Fatalf("Mount: %v", err)
	}
	if f.State() != cordis.Pending {
		t.Fatalf("state = %v, want PENDING before the injection resolves", f.State())
	}
	if applied {
		t.Fatal("the plugin ran before its injection resolved")
	}

	if err := h.Provide(ServiceTools, "registry", nil); err != nil {
		t.Fatalf("Provide: %v", err)
	}
	if f.State() != cordis.Active {
		t.Fatalf("state = %v, want ACTIVE after the injection resolved", f.State())
	}
	if !applied {
		t.Fatal("the plugin did not run after its injection resolved")
	}
}

// TestDisposeTearsDownEffects pins that the harness's disposal reaches the
// kernel's effect chain.
func TestDisposeTearsDownEffects(t *testing.T) {
	h := New()
	disposed := false
	if err := h.Root.Fiber().Effect(func() (cordis.Disposer, error) {
		return func() error { disposed = true; return nil }, nil
	}); err != nil {
		t.Fatalf("Effect: %v", err)
	}
	if err := h.Dispose(); err != nil {
		t.Fatalf("Dispose: %v", err)
	}
	if !disposed {
		t.Fatal("Dispose did not run the registered disposer")
	}
}
