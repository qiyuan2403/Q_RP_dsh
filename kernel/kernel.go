// Package kernel wires the DSH core onto the Cordis kernel.
//
// The layering is deliberate: Y_Cordis owns the plugin kernel (service
// registry, fiber lifecycle, event dispatch) and the loader dialect; this
// repository owns the harness that runs on it (the turn/step loop, the session
// log, the tool registry, the model provider).
//
// Nothing here reimplements the kernel. It composes it.
package kernel

import (
	"fmt"

	"github.com/qiyuan2403/Y_Cordis/cordis"
)

// Service names this harness registers on the kernel context.
//
// They are the names a plugin's `inject` declaration refers to, so they are
// part of the plugin ABI rather than an internal detail.
const (
	// ServiceSession exposes the session log factory.
	ServiceSession = "session"
	// ServiceTools exposes the tool registry.
	ServiceTools = "tools"
	// ServiceProvider exposes the model provider.
	ServiceProvider = "llm"
	// ServiceAgent exposes the agent factory.
	ServiceAgent = "agent"
)

// Harness is a composed kernel context with the DSH services registered.
type Harness struct {
	// Root is the kernel's root context. Plugins mount onto it or a child.
	Root *cordis.Context
	// provided lists the harness services registered so far.
	provided []string
}

// New builds a harness: a root context ready for the DSH services.
//
// Services are provided by the deployment after composition, not here: a plugin
// that injects one stays PENDING until the value arrives, which is the kernel's
// reactivity doing the ordering.
func New() *Harness {
	return &Harness{Root: cordis.NewRoot()}
}

// Provide registers one of the harness services.
//
// The name is validated against the known set: a typo would otherwise create a
// service no plugin ever injects, and the failure would surface much later as a
// plugin that never activates.
func (h *Harness) Provide(name string, value any, check func() bool) error {
	switch name {
	case ServiceSession, ServiceTools, ServiceProvider, ServiceAgent:
	default:
		return fmt.Errorf("kernel: unknown harness service %q", name)
	}
	if err := h.Root.Provide(name, value, check); err != nil {
		return err
	}
	h.provided = append(h.provided, name)
	return nil
}

// Provided lists the harness services registered so far.
func (h *Harness) Provided() []string {
	return append([]string(nil), h.provided...)
}

// Mount loads a plugin onto the root context.
func (h *Harness) Mount(p *cordis.Plugin) (*cordis.Fiber, error) {
	return h.Root.Plugin(p, nil)
}

// Dispose tears the harness down, running every effect's disposer.
func (h *Harness) Dispose() error {
	return h.Root.Fiber().Dispose()
}
