// Package web owns provider selection for the two web capabilities, search and
// fetch.
//
// Search and fetch share one seam so configuration, provider selection, and
// error reporting have a single owner, while each capability keeps its own
// request and result types and its own registry.
//
// Selection is resolved at call time, not at construction. Providers are
// registered and disposed over the life of a session, and a provider's
// Available result depends on local configuration that can change between
// calls; resolving once would freeze a choice before the providers exist. Auto-
// selection also never depends on registration order: it requires exactly one
// usable provider and reports ambiguity instead of picking the first one,
// because "first registered" is plugin load order, not user intent.
//
// Two deliberate semantic differences from the upstream implementation are
// recorded here because Go's types erase a distinction upstream can make, not
// because the product behavior is meant to differ:
//
//   - An empty Config field means unset and falls back to the environment
//     variable, while upstream treats an explicitly empty string as a
//     configured empty id and fails. The empty string cannot carry the
//     distinction at this seam: Config's zero value is indistinguishable from a
//     deliberately empty field, and os.Getenv returns "" for both an unset
//     variable and one set to empty. Folding both into the fallback also matches
//     the product rule that a missing provider configuration should auto-select
//     a usable provider rather than fail at startup. The only observable
//     difference is that an explicitly empty configuration no longer errors;
//     every non-empty configured id selects exactly as upstream does.
//   - MaxResults <= 0 means "no bound", while upstream lets only an undefined
//     bound mean unbounded. Go's int has no absent state, and adding a separate
//     "bound was set" flag to the request would burden every caller for one
//     control. tools/web rejects non-positive limits before building a request,
//     so the convention only reaches direct callers of Web.Search, who must
//     pass a positive number to get a bound.
package web

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"
)

// Environment variables read by New when the matching Config field is empty.
const (
	envSearchProvider = "DSH_WEB_SEARCH_PROVIDER"
	envFetchProvider  = "DSH_WEB_FETCH_PROVIDER"
)

// Config pins which provider runs each capability.
//
// Both fields are optional. An empty field, whether omitted or explicitly set
// to "", falls back to the matching environment variable (see New); when that
// is empty too, the capability auto-selects the single usable provider. The
// explicit-empty case differs from upstream on purpose; see the package
// documentation. There is no other priority chain: the environment variable
// feeds the same field a caller can set directly.
type Config struct {
	// SearchProvider is the search provider id to use. Empty means read
	// DSH_WEB_SEARCH_PROVIDER, then auto-select.
	SearchProvider string
	// FetchProvider is the fetch provider id to use. Empty means read
	// DSH_WEB_FETCH_PROVIDER, then auto-select.
	FetchProvider string
}

// Web is the registry and provider-selecting executor for search and fetch.
//
// Methods are safe for concurrent use. Provider calls, including the cheap
// Available check, run without the registry lock held, so a slow provider cannot
// block registration.
//
// Selection semantics, resolved per call:
//   - A configured id that is registered and Available reports that provider.
//   - A configured id that is not registered reports
//     CodeProviderConfiguredMissing.
//   - A configured id that is registered but not Available reports
//     CodeProviderConfiguredUnavailable.
//   - No configured id and exactly one usable provider selects it.
//   - No configured id and several usable providers report
//     CodeProviderAmbiguous, naming them in the message.
//   - No configured id and no usable provider reports CodeProviderUnavailable.
type Web struct {
	search registry[SearchProvider]
	fetch  registry[FetchProvider]

	// Configured ids are immutable after New, so reads need no lock.
	searchProviderID string
	fetchProviderID  string
}

// New returns an empty Web with its configured provider ids resolved.
//
// An empty Config field falls back to an environment variable read here, once:
// DSH_WEB_SEARCH_PROVIDER for SearchProvider and DSH_WEB_FETCH_PROVIDER for
// FetchProvider. Reading it at construction keeps configuration a single
// snapshot for the instance, so later environment changes cannot silently
// reroute calls; the fallback is a default for the same field, not a hidden
// second source of truth. An explicit Config value always wins.
func New(cfg Config) *Web {
	return &Web{
		searchProviderID: firstNonEmpty(cfg.SearchProvider, os.Getenv(envSearchProvider)),
		fetchProviderID:  firstNonEmpty(cfg.FetchProvider, os.Getenv(envFetchProvider)),
	}
}

// firstNonEmpty returns explicit when set, otherwise fallback.
func firstNonEmpty(explicit, fallback string) string {
	if explicit != "" {
		return explicit
	}
	return fallback
}

// RegisterSearchProvider adds a search provider and returns its disposer.
//
// The provider's ID is the registry key. A duplicate id, an empty id, or a nil
// provider is refused rather than replaced: a silent replacement would change
// what an existing call resolves to. The disposer unregisters this registration
// and may be called more than once; later calls do nothing, and it never
// removes a newer registration that reused the same id.
func (w *Web) RegisterSearchProvider(p SearchProvider) (func(), error) {
	if p == nil {
		return nil, fmt.Errorf("web: search provider is nil")
	}
	return w.search.install(p)
}

// RegisterFetchProvider adds a fetch provider and returns its disposer.
//
// It follows the same rules as RegisterSearchProvider, with its own registry:
// ids are unique within a capability kind, not across both.
func (w *Web) RegisterFetchProvider(p FetchProvider) (func(), error) {
	if p == nil {
		return nil, fmt.Errorf("web: fetch provider is nil")
	}
	return w.fetch.install(p)
}

// Search runs one search through the selected provider.
//
// The provider is resolved at call time with the rules on Web. The provider's
// error is returned unchanged so callers can inspect provider-specific codes.
// On success the seam enforces req.MaxResults: if the provider returned more
// sources than allowed, they are truncated and the result is marked truncated.
// A provider's own truncation flag survives the cap.
func (w *Web) Search(ctx context.Context, req SearchRequest) (SearchResult, error) {
	provider, err := w.search.resolve(w.searchProviderID)
	if err != nil {
		return SearchResult{}, err
	}
	result, err := provider.Search(ctx, req)
	if err != nil {
		return SearchResult{}, err
	}
	return capSources(result, req.MaxResults), nil
}

// Fetch retrieves one URL through the selected provider.
//
// The provider is resolved at call time with the rules on Web. The result is
// returned untouched: the seam applies no size cap and rewrites no body
// because representational limits belong to the provider, and a non-2xx
// response is a normal result rather than an error.
func (w *Web) Fetch(ctx context.Context, req FetchRequest) (FetchResult, error) {
	provider, err := w.fetch.resolve(w.fetchProviderID)
	if err != nil {
		return FetchResult{}, err
	}
	return provider.Fetch(ctx, req)
}

// provider is the surface both capability kinds share, so registration and
// selection have one implementation instead of two drifting copies.
type provider interface {
	ID() string
	Available() bool
}

// slot is one installed provider. Its address is the registration's identity,
// which lets a disposer check it is removing its own registration and not a
// later replacement that reused the id.
type slot[P provider] struct {
	provider P
}

// registry holds the providers of one capability kind.
type registry[P provider] struct {
	mu   sync.RWMutex
	byID map[string]*slot[P]
}

// install adds p and returns its idempotent disposer.
func (r *registry[P]) install(p P) (func(), error) {
	id := p.ID()
	if strings.TrimSpace(id) == "" {
		return nil, fmt.Errorf("web: provider id is empty")
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	if r.byID == nil {
		r.byID = make(map[string]*slot[P])
	}
	if _, exists := r.byID[id]; exists {
		return nil, &Error{
			Code:    CodeDuplicateProvider,
			Message: fmt.Sprintf("a web provider with id %q is already registered", id),
		}
	}
	s := &slot[P]{provider: p}
	r.byID[id] = s
	return func() {
		r.mu.Lock()
		defer r.mu.Unlock()
		// Identity, not just id: a stale disposer must not remove a newer
		// registration that took the same id after this one was disposed.
		if r.byID[id] == s {
			delete(r.byID, id)
		}
	}, nil
}

// resolve selects the provider to run a call, following the rules on Web.
func (r *registry[P]) resolve(configured string) (P, error) {
	var zero P

	if configured != "" {
		r.mu.RLock()
		s, ok := r.byID[configured]
		r.mu.RUnlock()
		if !ok {
			return zero, &Error{
				Code:    CodeProviderConfiguredMissing,
				Message: fmt.Sprintf("configured web provider %q is not registered", configured),
			}
		}
		// Available runs unlocked: it is provider code, and the registry must
		// stay usable while a provider checks its own local configuration.
		if !s.provider.Available() {
			return zero, &Error{
				Code:    CodeProviderConfiguredUnavailable,
				Message: fmt.Sprintf("configured web provider %q is registered but unavailable", configured),
			}
		}
		return s.provider, nil
	}

	r.mu.RLock()
	snapshot := make([]P, 0, len(r.byID))
	for _, s := range r.byID {
		snapshot = append(snapshot, s.provider)
	}
	r.mu.RUnlock()

	usable := make([]P, 0, len(snapshot))
	for _, p := range snapshot {
		if p.Available() {
			usable = append(usable, p)
		}
	}

	switch len(usable) {
	case 0:
		return zero, &Error{
			Code:    CodeProviderUnavailable,
			Message: "no usable web provider is registered",
		}
	case 1:
		return usable[0], nil
	default:
		// Sorted so the message is stable across runs; map iteration order is
		// not, and an error message that shuffles between calls is hard to
		// compare in logs and tests.
		ids := make([]string, len(usable))
		for i, p := range usable {
			ids[i] = p.ID()
		}
		sort.Strings(ids)
		return zero, &Error{
			Code:    CodeProviderAmbiguous,
			Message: fmt.Sprintf("multiple usable web providers are registered (%s); configure one explicitly", strings.Join(ids, ", ")),
		}
	}
}

// capSources enforces maxResults on a search result.
//
// A non-positive maxResults means no bound. The truncation flag is sticky: the
// cap sets it, and a provider that already set it keeps it.
func capSources(result SearchResult, maxResults int) SearchResult {
	if maxResults <= 0 || len(result.Sources) <= maxResults {
		return result
	}
	sources := make([]SearchSource, maxResults)
	copy(sources, result.Sources[:maxResults])
	result.Sources = sources
	result.Truncated = true
	return result
}
