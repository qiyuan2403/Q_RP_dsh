package web

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// fakeSearchProvider is a search provider whose behavior each test sets.
type fakeSearchProvider struct {
	id        string
	available bool
	result    SearchResult
	err       error
	calls     atomic.Int32
}

func (p *fakeSearchProvider) ID() string      { return p.id }
func (p *fakeSearchProvider) Available() bool { return p.available }
func (p *fakeSearchProvider) Search(context.Context, SearchRequest) (SearchResult, error) {
	p.calls.Add(1)
	return p.result, p.err
}

// fakeFetchProvider is a fetch provider whose behavior each test sets.
type fakeFetchProvider struct {
	id        string
	available bool
	result    FetchResult
	err       error
	calls     atomic.Int32
}

func (p *fakeFetchProvider) ID() string      { return p.id }
func (p *fakeFetchProvider) Available() bool { return p.available }
func (p *fakeFetchProvider) Fetch(context.Context, FetchRequest) (FetchResult, error) {
	p.calls.Add(1)
	return p.result, p.err
}

// blockingSearchProvider blocks in Available until release is closed, so a test
// can observe whether selection keeps the registry usable while provider code
// runs. entered is closed on the first Available call.
type blockingSearchProvider struct {
	id      string
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (p *blockingSearchProvider) ID() string { return p.id }

func (p *blockingSearchProvider) Available() bool {
	p.once.Do(func() { close(p.entered) })
	<-p.release
	return true
}

func (p *blockingSearchProvider) Search(context.Context, SearchRequest) (SearchResult, error) {
	return SearchResult{Content: "slow"}, nil
}

// clearProviderEnv makes the env fallback of New deterministic in tests.
//
// The variable names are literals here and in TestEnvVarNameLiterals: reading
// them through the package constants would let a typo in those constants pass
// both the clearing and the fallback.
func clearProviderEnv(t *testing.T) {
	t.Helper()
	t.Setenv("DSH_WEB_SEARCH_PROVIDER", "")
	t.Setenv("DSH_WEB_FETCH_PROVIDER", "")
}

// requireCode asserts that err is a *Error carrying want.
func requireCode(t *testing.T, err error, want string) *Error {
	t.Helper()
	if err == nil {
		t.Fatalf("got nil error, want code %s", want)
	}
	var e *Error
	if !errors.As(err, &e) {
		t.Fatalf("error %v is not a *Error", err)
	}
	if e.Code != want {
		t.Fatalf("got code %q, want %q (message: %q)", e.Code, want, e.Message)
	}
	if !strings.HasPrefix(e.Error(), "web:") {
		t.Fatalf("error %q does not start with web:", e.Error())
	}
	return e
}

func TestSearchSelection(t *testing.T) {
	query := SearchRequest{Query: "q"}

	t.Run("configured provider wins", func(t *testing.T) {
		clearProviderEnv(t)
		w := New(Config{SearchProvider: "chosen"})
		chosen := &fakeSearchProvider{id: "chosen", available: true, result: SearchResult{Content: "chosen"}}
		other := &fakeSearchProvider{id: "other", available: true, result: SearchResult{Content: "other"}}
		mustRegisterSearch(t, w, chosen)
		mustRegisterSearch(t, w, other)

		got, err := w.Search(context.Background(), query)
		if err != nil {
			t.Fatalf("Search: %v", err)
		}
		if got.Content != "chosen" {
			t.Fatalf("got content %q, want chosen", got.Content)
		}
		if chosen.calls.Load() != 1 || other.calls.Load() != 0 {
			t.Fatalf("calls chosen=%d other=%d, want 1 and 0", chosen.calls.Load(), other.calls.Load())
		}
	})

	t.Run("configured id missing", func(t *testing.T) {
		clearProviderEnv(t)
		w := New(Config{SearchProvider: "absent"})
		present := &fakeSearchProvider{id: "present", available: true}
		mustRegisterSearch(t, w, present)

		_, err := w.Search(context.Background(), query)
		e := requireCode(t, err, CodeProviderConfiguredMissing)
		if !strings.Contains(e.Error(), "absent") {
			t.Fatalf("error %q does not name the configured id", e.Error())
		}
		if present.calls.Load() != 0 {
			t.Fatal("a search ran despite the configured provider being missing")
		}
	})

	t.Run("configured id unavailable", func(t *testing.T) {
		clearProviderEnv(t)
		w := New(Config{SearchProvider: "off"})
		off := &fakeSearchProvider{id: "off", available: false}
		fallback := &fakeSearchProvider{id: "fallback", available: true}
		mustRegisterSearch(t, w, off)
		mustRegisterSearch(t, w, fallback)

		_, err := w.Search(context.Background(), query)
		e := requireCode(t, err, CodeProviderConfiguredUnavailable)
		if !strings.Contains(e.Error(), "off") {
			t.Fatalf("error %q does not name the configured id", e.Error())
		}
		if off.calls.Load() != 0 || fallback.calls.Load() != 0 {
			t.Fatal("a search ran despite the configured provider being unavailable")
		}
	})

	t.Run("auto selects the single usable provider", func(t *testing.T) {
		clearProviderEnv(t)
		w := New(Config{})
		on := &fakeSearchProvider{id: "on", available: true, result: SearchResult{Content: "on"}}
		off := &fakeSearchProvider{id: "off", available: false}
		mustRegisterSearch(t, w, on)
		mustRegisterSearch(t, w, off)

		got, err := w.Search(context.Background(), query)
		if err != nil {
			t.Fatalf("Search: %v", err)
		}
		if got.Content != "on" {
			t.Fatalf("got content %q, want on", got.Content)
		}
	})

	t.Run("auto rejects ambiguity", func(t *testing.T) {
		clearProviderEnv(t)
		w := New(Config{})
		alpha := &fakeSearchProvider{id: "alpha", available: true}
		beta := &fakeSearchProvider{id: "beta", available: true}
		off := &fakeSearchProvider{id: "off", available: false}
		mustRegisterSearch(t, w, alpha)
		mustRegisterSearch(t, w, beta)
		mustRegisterSearch(t, w, off)

		_, err := w.Search(context.Background(), query)
		e := requireCode(t, err, CodeProviderAmbiguous)
		for _, id := range []string{"alpha", "beta"} {
			if !strings.Contains(e.Error(), id) {
				t.Fatalf("error %q does not list usable provider %q", e.Error(), id)
			}
		}
		if strings.Contains(e.Error(), "off") {
			t.Fatalf("error %q lists an unavailable provider", e.Error())
		}
		if alpha.calls.Load() != 0 || beta.calls.Load() != 0 {
			t.Fatal("a search ran despite the ambiguous selection")
		}
	})

	t.Run("auto with no registered provider", func(t *testing.T) {
		clearProviderEnv(t)
		w := New(Config{})

		_, err := w.Search(context.Background(), query)
		requireCode(t, err, CodeProviderUnavailable)
	})

	t.Run("auto with only unavailable providers", func(t *testing.T) {
		clearProviderEnv(t)
		w := New(Config{})
		mustRegisterSearch(t, w, &fakeSearchProvider{id: "off", available: false})

		_, err := w.Search(context.Background(), query)
		requireCode(t, err, CodeProviderUnavailable)
	})
}

func TestFetchSelection(t *testing.T) {
	req := FetchRequest{URL: "https://example.com/"}

	t.Run("configured provider wins", func(t *testing.T) {
		clearProviderEnv(t)
		w := New(Config{FetchProvider: "chosen"})
		chosen := &fakeFetchProvider{id: "chosen", available: true, result: FetchResult{StatusCode: 200}}
		other := &fakeFetchProvider{id: "other", available: true, result: FetchResult{StatusCode: 200}}
		mustRegisterFetch(t, w, chosen)
		mustRegisterFetch(t, w, other)

		if _, err := w.Fetch(context.Background(), req); err != nil {
			t.Fatalf("Fetch: %v", err)
		}
		if chosen.calls.Load() != 1 || other.calls.Load() != 0 {
			t.Fatalf("calls chosen=%d other=%d, want 1 and 0", chosen.calls.Load(), other.calls.Load())
		}
	})

	t.Run("configured id missing", func(t *testing.T) {
		clearProviderEnv(t)
		w := New(Config{FetchProvider: "absent"})

		_, err := w.Fetch(context.Background(), req)
		requireCode(t, err, CodeProviderConfiguredMissing)
	})

	t.Run("configured id unavailable", func(t *testing.T) {
		clearProviderEnv(t)
		w := New(Config{FetchProvider: "off"})
		off := &fakeFetchProvider{id: "off", available: false}
		fallback := &fakeFetchProvider{id: "fallback", available: true}
		mustRegisterFetch(t, w, off)
		mustRegisterFetch(t, w, fallback)

		_, err := w.Fetch(context.Background(), req)
		e := requireCode(t, err, CodeProviderConfiguredUnavailable)
		if !strings.Contains(e.Error(), "off") {
			t.Fatalf("error %q does not name the configured id", e.Error())
		}
		if off.calls.Load() != 0 || fallback.calls.Load() != 0 {
			t.Fatal("a fetch ran despite the configured provider being unavailable")
		}
	})

	t.Run("auto rejects ambiguity", func(t *testing.T) {
		clearProviderEnv(t)
		w := New(Config{})
		mustRegisterFetch(t, w, &fakeFetchProvider{id: "alpha", available: true})
		mustRegisterFetch(t, w, &fakeFetchProvider{id: "beta", available: true})

		_, err := w.Fetch(context.Background(), req)
		e := requireCode(t, err, CodeProviderAmbiguous)
		if !strings.Contains(e.Error(), "alpha") || !strings.Contains(e.Error(), "beta") {
			t.Fatalf("error %q does not list both providers", e.Error())
		}
	})

	t.Run("search registry does not satisfy fetch", func(t *testing.T) {
		clearProviderEnv(t)
		w := New(Config{})
		mustRegisterSearch(t, w, &fakeSearchProvider{id: "searcher", available: true})

		_, err := w.Fetch(context.Background(), req)
		requireCode(t, err, CodeProviderUnavailable)
	})
}

func TestSearchMaxResults(t *testing.T) {
	sources := func(n int) []SearchSource {
		out := make([]SearchSource, n)
		for i := range out {
			out[i] = SearchSource{URL: fmt.Sprintf("https://example.com/%d", i), Title: fmt.Sprintf("t%d", i)}
		}
		return out
	}

	tests := []struct {
		name              string
		maxResults        int
		providerSources   int
		providerTruncated bool
		wantSources       int
		wantTruncated     bool
	}{
		{"no bound", 0, 3, false, 3, false},
		{"negative means no bound", -1, 3, false, 3, false},
		{"caps overhead", 2, 3, false, 2, true},
		// Equality must not truncate. This row fails if capSources compares
		// len(result.Sources) < maxResults instead of <=: with two sources and
		// a bound of two, a strict comparison would drop one and set Truncated.
		{"exact bound without provider flag", 2, 2, false, 2, false},
		{"exact bound keeps provider flag", 2, 2, true, 2, true},
		{"provider truncation survives no bound", 0, 2, true, 2, true},
		{"cap merges with provider flag", 1, 3, true, 1, true},
		{"fewer than bound", 5, 1, false, 1, false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			clearProviderEnv(t)
			w := New(Config{})
			p := &fakeSearchProvider{
				id:        "p",
				available: true,
				result: SearchResult{
					Content:   "answer",
					Sources:   sources(tc.providerSources),
					Truncated: tc.providerTruncated,
				},
			}
			mustRegisterSearch(t, w, p)

			got, err := w.Search(context.Background(), SearchRequest{Query: "q", MaxResults: tc.maxResults})
			if err != nil {
				t.Fatalf("Search: %v", err)
			}
			if len(got.Sources) != tc.wantSources {
				t.Fatalf("got %d sources, want %d", len(got.Sources), tc.wantSources)
			}
			if got.Truncated != tc.wantTruncated {
				t.Fatalf("got truncated=%v, want %v", got.Truncated, tc.wantTruncated)
			}
			if got.Content != "answer" {
				t.Fatalf("got content %q, want answer", got.Content)
			}
			for i, src := range got.Sources {
				want := fmt.Sprintf("https://example.com/%d", i)
				if src.URL != want {
					t.Fatalf("source %d URL = %q, want %q", i, src.URL, want)
				}
			}
		})
	}
}

func TestProviderErrorPassesThrough(t *testing.T) {
	clearProviderEnv(t)
	w := New(Config{SearchProvider: "search", FetchProvider: "fetch"})
	searchErr := &Error{Code: "PROVIDER_SPECIFIC", Message: "quota exceeded"}
	fetchErr := errors.New("dial failed")
	mustRegisterSearch(t, w, &fakeSearchProvider{id: "search", available: true, err: searchErr})
	mustRegisterFetch(t, w, &fakeFetchProvider{id: "fetch", available: true, err: fetchErr})

	if _, err := w.Search(context.Background(), SearchRequest{Query: "q"}); !errors.Is(err, searchErr) {
		t.Fatalf("Search error = %v, want the provider's error unchanged", err)
	}
	if _, err := w.Fetch(context.Background(), FetchRequest{URL: "https://example.com/"}); !errors.Is(err, fetchErr) {
		t.Fatalf("Fetch error = %v, want the provider's error unchanged", err)
	}
}

func TestFetchPassesNon2xxThrough(t *testing.T) {
	clearProviderEnv(t)
	w := New(Config{})
	want := FetchResult{
		URL:        "https://example.com/final",
		StatusCode: 404,
		Body:       FetchBody{Kind: KindHTML, Content: "<h1>gone</h1>"},
		Truncated:  true,
	}
	p := &fakeFetchProvider{id: "http", available: true, result: want}
	mustRegisterFetch(t, w, p)

	got, err := w.Fetch(context.Background(), FetchRequest{URL: "https://example.com/start"})
	if err != nil {
		t.Fatalf("Fetch returned an error for a non-2xx response: %v", err)
	}
	if got.URL != want.URL || got.StatusCode != want.StatusCode || got.Body != want.Body || got.Truncated != want.Truncated {
		t.Fatalf("got %+v, want %+v", got, want)
	}
	if p.calls.Load() != 1 {
		t.Fatalf("provider calls = %d, want 1", p.calls.Load())
	}
}

func TestRegistrationValidation(t *testing.T) {
	clearProviderEnv(t)
	w := New(Config{})

	tests := []struct {
		name     string
		register func() (func(), error)
	}{
		{"search nil provider", func() (func(), error) {
			return w.RegisterSearchProvider(nil)
		}},
		{"fetch nil provider", func() (func(), error) {
			return w.RegisterFetchProvider(nil)
		}},
		{"search empty id", func() (func(), error) {
			return w.RegisterSearchProvider(&fakeSearchProvider{id: ""})
		}},
		{"search blank id", func() (func(), error) {
			return w.RegisterSearchProvider(&fakeSearchProvider{id: "   "})
		}},
		{"fetch empty id", func() (func(), error) {
			return w.RegisterFetchProvider(&fakeFetchProvider{id: ""})
		}},
		{"fetch blank id", func() (func(), error) {
			return w.RegisterFetchProvider(&fakeFetchProvider{id: "   "})
		}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dispose, err := tc.register()
			if err == nil {
				t.Fatal("registration succeeded")
			}
			if dispose != nil {
				t.Fatal("registration returned a non-nil disposer with the error")
			}
			// Validation failures are plain errors today: a nil provider or a
			// blank id is a programming mistake, not a condition a call can
			// route on, so they carry no web code. Lock that shape so changing
			// it to *Error would be a deliberate edit.
			var e *Error
			if errors.As(err, &e) {
				t.Fatalf("validation error %v is a *Error; want an uncoded error", err)
			}
		})
	}

	dispose, err := w.RegisterSearchProvider(&fakeSearchProvider{id: "p", available: true})
	if err != nil {
		t.Fatalf("RegisterSearchProvider: %v", err)
	}
	if dispose == nil {
		t.Fatal("RegisterSearchProvider returned a nil disposer")
	}
}

func TestDuplicateRegistration(t *testing.T) {
	clearProviderEnv(t)
	w := New(Config{SearchProvider: "p"})
	first := &fakeSearchProvider{id: "p", available: true, result: SearchResult{Content: "first"}}
	mustRegisterSearch(t, w, first)

	_, err := w.RegisterSearchProvider(&fakeSearchProvider{id: "p", available: true, result: SearchResult{Content: "second"}})
	e := requireCode(t, err, CodeDuplicateProvider)
	if !strings.Contains(e.Error(), "p") {
		t.Fatalf("error %q does not name the duplicate id", e.Error())
	}

	got, err := w.Search(context.Background(), SearchRequest{Query: "q"})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if got.Content != "first" {
		t.Fatalf("got content %q after duplicate attempt, want first", got.Content)
	}

	// The two capabilities have separate registries, so one id may exist once in
	// each without being a duplicate.
	if _, err := w.RegisterFetchProvider(&fakeFetchProvider{id: "p", available: true}); err != nil {
		t.Fatalf("RegisterFetchProvider with an id used for search: %v", err)
	}

	// Duplicate detection is per capability kind, so a second fetch provider
	// with the same id is still refused.
	_, err = w.RegisterFetchProvider(&fakeFetchProvider{id: "p", available: true})
	requireCode(t, err, CodeDuplicateProvider)
}

func TestDisposer(t *testing.T) {
	t.Run("is idempotent", func(t *testing.T) {
		clearProviderEnv(t)
		w := New(Config{SearchProvider: "p"})
		dispose, err := w.RegisterSearchProvider(&fakeSearchProvider{id: "p", available: true})
		if err != nil {
			t.Fatalf("RegisterSearchProvider: %v", err)
		}
		dispose()
		dispose()

		_, err = w.Search(context.Background(), SearchRequest{Query: "q"})
		requireCode(t, err, CodeProviderConfiguredMissing)
	})

	t.Run("stale disposer leaves a newer registration alone", func(t *testing.T) {
		clearProviderEnv(t)
		w := New(Config{SearchProvider: "p"})
		firstDispose, err := w.RegisterSearchProvider(&fakeSearchProvider{id: "p", available: true, result: SearchResult{Content: "first"}})
		if err != nil {
			t.Fatalf("RegisterSearchProvider: %v", err)
		}
		firstDispose()

		secondDispose, err := w.RegisterSearchProvider(&fakeSearchProvider{id: "p", available: true, result: SearchResult{Content: "second"}})
		if err != nil {
			t.Fatalf("re-register: %v", err)
		}
		firstDispose()

		got, err := w.Search(context.Background(), SearchRequest{Query: "q"})
		if err != nil {
			t.Fatalf("Search: %v", err)
		}
		if got.Content != "second" {
			t.Fatalf("stale disposer removed the newer provider: got content %q", got.Content)
		}
		secondDispose()
	})

	t.Run("auto-selected provider disappears on dispose", func(t *testing.T) {
		clearProviderEnv(t)
		w := New(Config{})
		dispose, err := w.RegisterSearchProvider(&fakeSearchProvider{id: "only", available: true})
		if err != nil {
			t.Fatalf("RegisterSearchProvider: %v", err)
		}
		dispose()
		dispose()

		_, err = w.Search(context.Background(), SearchRequest{Query: "q"})
		requireCode(t, err, CodeProviderUnavailable)
	})

	t.Run("fetch disposer is idempotent", func(t *testing.T) {
		clearProviderEnv(t)
		w := New(Config{FetchProvider: "p"})
		dispose, err := w.RegisterFetchProvider(&fakeFetchProvider{id: "p", available: true})
		if err != nil {
			t.Fatalf("RegisterFetchProvider: %v", err)
		}
		dispose()
		dispose()

		_, err = w.Fetch(context.Background(), FetchRequest{URL: "https://example.com/"})
		requireCode(t, err, CodeProviderConfiguredMissing)
	})

	t.Run("stale fetch disposer leaves a newer registration alone", func(t *testing.T) {
		clearProviderEnv(t)
		w := New(Config{FetchProvider: "p"})
		firstDispose, err := w.RegisterFetchProvider(&fakeFetchProvider{id: "p", available: true, result: FetchResult{StatusCode: 200}})
		if err != nil {
			t.Fatalf("RegisterFetchProvider: %v", err)
		}
		firstDispose()

		secondDispose, err := w.RegisterFetchProvider(&fakeFetchProvider{id: "p", available: true, result: FetchResult{StatusCode: 201}})
		if err != nil {
			t.Fatalf("re-register: %v", err)
		}
		firstDispose()

		got, err := w.Fetch(context.Background(), FetchRequest{URL: "https://example.com/"})
		if err != nil {
			t.Fatalf("Fetch: %v", err)
		}
		if got.StatusCode != 201 {
			t.Fatalf("stale fetch disposer removed the newer provider: got status %d, want 201", got.StatusCode)
		}
		secondDispose()
	})
}

func TestEnvFallback(t *testing.T) {
	t.Run("empty config reads both env vars", func(t *testing.T) {
		clearProviderEnv(t)
		t.Setenv(envSearchProvider, "env-search")
		t.Setenv(envFetchProvider, "env-fetch")
		w := New(Config{})

		mustRegisterSearch(t, w, &fakeSearchProvider{id: "env-search", available: true, result: SearchResult{Content: "env"}})
		// A second usable provider proves the env id pins the selection instead
		// of falling through to the ambiguity rule.
		mustRegisterSearch(t, w, &fakeSearchProvider{id: "other", available: true, result: SearchResult{Content: "other"}})
		mustRegisterFetch(t, w, &fakeFetchProvider{id: "env-fetch", available: true, result: FetchResult{StatusCode: 204}})

		got, err := w.Search(context.Background(), SearchRequest{Query: "q"})
		if err != nil {
			t.Fatalf("Search: %v", err)
		}
		if got.Content != "env" {
			t.Fatalf("got content %q, want env", got.Content)
		}
		res, err := w.Fetch(context.Background(), FetchRequest{URL: "https://example.com/"})
		if err != nil {
			t.Fatalf("Fetch: %v", err)
		}
		if res.StatusCode != 204 {
			t.Fatalf("got status %d, want 204", res.StatusCode)
		}
	})

	t.Run("explicit config beats env", func(t *testing.T) {
		clearProviderEnv(t)
		t.Setenv(envSearchProvider, "env-search")
		w := New(Config{SearchProvider: "explicit"})
		mustRegisterSearch(t, w, &fakeSearchProvider{id: "env-search", available: true, result: SearchResult{Content: "env"}})
		mustRegisterSearch(t, w, &fakeSearchProvider{id: "explicit", available: true, result: SearchResult{Content: "explicit"}})

		got, err := w.Search(context.Background(), SearchRequest{Query: "q"})
		if err != nil {
			t.Fatalf("Search: %v", err)
		}
		if got.Content != "explicit" {
			t.Fatalf("got content %q, want explicit", got.Content)
		}
	})

	t.Run("env is read once at construction", func(t *testing.T) {
		clearProviderEnv(t)
		w := New(Config{})
		t.Setenv(envSearchProvider, "late")
		mustRegisterSearch(t, w, &fakeSearchProvider{id: "late", available: true})
		mustRegisterSearch(t, w, &fakeSearchProvider{id: "other", available: true})

		_, err := w.Search(context.Background(), SearchRequest{Query: "q"})
		requireCode(t, err, CodeProviderAmbiguous)
	})
}

func TestEnvVarNameLiterals(t *testing.T) {
	// The names are written out by hand instead of through the package
	// constants: a test that sets and reads the same misspelled constant would
	// still pass, so only the literal proves New reads the documented names.
	t.Setenv("DSH_WEB_SEARCH_PROVIDER", "env-search")
	t.Setenv("DSH_WEB_FETCH_PROVIDER", "env-fetch")

	w := New(Config{})
	mustRegisterSearch(t, w, &fakeSearchProvider{id: "env-search", available: true, result: SearchResult{Content: "env"}})
	// A second usable provider proves the env id pins the selection instead of
	// falling through to the ambiguity rule.
	mustRegisterSearch(t, w, &fakeSearchProvider{id: "other", available: true, result: SearchResult{Content: "other"}})
	mustRegisterFetch(t, w, &fakeFetchProvider{id: "env-fetch", available: true, result: FetchResult{StatusCode: 204}})
	mustRegisterFetch(t, w, &fakeFetchProvider{id: "other-fetch", available: true, result: FetchResult{StatusCode: 500}})

	got, err := w.Search(context.Background(), SearchRequest{Query: "q"})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if got.Content != "env" {
		t.Fatalf("got content %q, want env", got.Content)
	}

	res, err := w.Fetch(context.Background(), FetchRequest{URL: "https://example.com/"})
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if res.StatusCode != 204 {
		t.Fatalf("got status %d, want 204", res.StatusCode)
	}
}

func TestErrorFormattingAndUnwrap(t *testing.T) {
	cause := errors.New("connection refused")
	e := &Error{Code: CodeProviderConfiguredUnavailable, Message: "provider is off", Err: cause}
	if got, want := e.Error(), "web: WEB_PROVIDER_CONFIGURED_UNAVAILABLE: provider is off: connection refused"; got != want {
		t.Fatalf("Error() = %q, want %q", got, want)
	}
	if !errors.Is(e, cause) {
		t.Fatal("errors.Is did not reach the cause")
	}

	wrapped := fmt.Errorf("caller context: %w", e)
	var target *Error
	if !errors.As(wrapped, &target) || target.Code != CodeProviderConfiguredUnavailable {
		t.Fatalf("errors.As through wrapping did not find the *Error: %v", wrapped)
	}
}

// TestErrorCodeLiterals pins the five seam codes to the upstream strings and
// checks that a real failure of each kind is extractable as *Error with the code
// visible in its message.
func TestErrorCodeLiterals(t *testing.T) {
	tests := []struct {
		name     string
		constant string
		upstream string
		trigger  func(t *testing.T) error
	}{
		{
			name:     "duplicate provider",
			constant: CodeDuplicateProvider,
			upstream: "WEB_DUPLICATE_PROVIDER",
			trigger: func(t *testing.T) error {
				w := New(Config{})
				mustRegisterSearch(t, w, &fakeSearchProvider{id: "dup", available: true})
				_, err := w.RegisterSearchProvider(&fakeSearchProvider{id: "dup", available: true})
				return err
			},
		},
		{
			name:     "configured provider missing",
			constant: CodeProviderConfiguredMissing,
			upstream: "WEB_PROVIDER_CONFIGURED_MISSING",
			trigger: func(t *testing.T) error {
				w := New(Config{SearchProvider: "absent"})
				_, err := w.Search(context.Background(), SearchRequest{Query: "q"})
				return err
			},
		},
		{
			name:     "configured provider unavailable",
			constant: CodeProviderConfiguredUnavailable,
			upstream: "WEB_PROVIDER_CONFIGURED_UNAVAILABLE",
			trigger: func(t *testing.T) error {
				w := New(Config{SearchProvider: "off"})
				mustRegisterSearch(t, w, &fakeSearchProvider{id: "off", available: false})
				_, err := w.Search(context.Background(), SearchRequest{Query: "q"})
				return err
			},
		},
		{
			name:     "provider unavailable",
			constant: CodeProviderUnavailable,
			upstream: "WEB_PROVIDER_UNAVAILABLE",
			trigger: func(t *testing.T) error {
				w := New(Config{})
				_, err := w.Search(context.Background(), SearchRequest{Query: "q"})
				return err
			},
		},
		{
			name:     "provider ambiguous",
			constant: CodeProviderAmbiguous,
			upstream: "WEB_PROVIDER_AMBIGUOUS",
			trigger: func(t *testing.T) error {
				w := New(Config{})
				mustRegisterSearch(t, w, &fakeSearchProvider{id: "alpha", available: true})
				mustRegisterSearch(t, w, &fakeSearchProvider{id: "beta", available: true})
				_, err := w.Search(context.Background(), SearchRequest{Query: "q"})
				return err
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			clearProviderEnv(t)
			if tc.constant != tc.upstream {
				t.Fatalf("code constant = %q, want upstream literal %q", tc.constant, tc.upstream)
			}

			err := tc.trigger(t)
			var e *Error
			if !errors.As(err, &e) {
				t.Fatalf("error %v is not extractable as *Error", err)
			}
			if e.Code != tc.upstream {
				t.Fatalf("error code = %q, want upstream literal %q", e.Code, tc.upstream)
			}
			if !strings.Contains(e.Error(), tc.constant) {
				t.Fatalf("Error() = %q does not contain code %q", e.Error(), tc.constant)
			}
		})
	}
}

func TestConcurrentRegistrationAndSearch(t *testing.T) {
	clearProviderEnv(t)
	w := New(Config{})
	base := &fakeSearchProvider{id: "base", available: true, result: SearchResult{Content: "ok"}}
	mustRegisterSearch(t, w, base)

	const workers = 8
	const iterations = 25

	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(2)
		go func(worker int) {
			defer wg.Done()
			for j := 0; j < iterations; j++ {
				p := &fakeSearchProvider{id: fmt.Sprintf("temp-%d-%d", worker, j), available: false}
				dispose, err := w.RegisterSearchProvider(p)
				if err != nil {
					t.Errorf("register temp provider: %v", err)
					return
				}
				dispose()
				dispose()
			}
		}(i)
		go func() {
			defer wg.Done()
			for j := 0; j < iterations; j++ {
				got, err := w.Search(context.Background(), SearchRequest{Query: "q"})
				if err != nil {
					t.Errorf("Search: %v", err)
					return
				}
				if got.Content != "ok" {
					t.Errorf("got content %q, want ok", got.Content)
					return
				}
			}
		}()
	}
	wg.Wait()

	if got, want := base.calls.Load(), int32(workers*iterations); got != want {
		t.Fatalf("base provider calls = %d, want %d", got, want)
	}

	w.search.mu.RLock()
	remaining := len(w.search.byID)
	w.search.mu.RUnlock()
	if remaining != 1 {
		t.Fatalf("%d providers remain registered, want only base", remaining)
	}
}

// TestAvailableRunsWithoutRegistryLock fails if selection holds the registry
// lock while calling provider.Available: registration in another goroutine
// would then wait for the provider to return.
func TestAvailableRunsWithoutRegistryLock(t *testing.T) {
	clearProviderEnv(t)
	w := New(Config{SearchProvider: "slow"})
	entered := make(chan struct{})
	release := make(chan struct{})
	mustRegisterSearch(t, w, &blockingSearchProvider{id: "slow", entered: entered, release: release})

	searchDone := make(chan error, 1)
	go func() {
		_, err := w.Search(context.Background(), SearchRequest{Query: "q"})
		searchDone <- err
	}()

	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		close(release)
		t.Fatal("Search never called provider.Available")
	}

	registerDone := make(chan error, 1)
	go func() {
		_, err := w.RegisterSearchProvider(&fakeSearchProvider{id: "concurrent", available: true})
		registerDone <- err
	}()

	select {
	case err := <-registerDone:
		if err != nil {
			close(release)
			t.Fatalf("RegisterSearchProvider while Available blocked: %v", err)
		}
	case <-time.After(5 * time.Second):
		close(release)
		t.Fatal("registration blocked while provider.Available was running")
	}
	close(release)

	select {
	case err := <-searchDone:
		if err != nil {
			t.Fatalf("Search: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Search did not finish after Available was released")
	}
}

// mustRegisterSearch registers p or fails the test.
func mustRegisterSearch(t *testing.T, w *Web, p SearchProvider) {
	t.Helper()
	if _, err := w.RegisterSearchProvider(p); err != nil {
		t.Fatalf("RegisterSearchProvider(%q): %v", p.ID(), err)
	}
}

// mustRegisterFetch registers p or fails the test.
func mustRegisterFetch(t *testing.T, w *Web, p FetchProvider) {
	t.Helper()
	if _, err := w.RegisterFetchProvider(p); err != nil {
		t.Fatalf("RegisterFetchProvider(%q): %v", p.ID(), err)
	}
}
