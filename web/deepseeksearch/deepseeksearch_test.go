package deepseeksearch

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/qiyuan2403/Q_RP_dsh/web"
)

// searchResponse is a realistic Messages response: one prose block carrying the
// citation excerpt and one result block carrying the citeable items.
const searchResponse = `{
  "content": [
    {
      "type": "text",
      "text": "Here is what I found.",
      "citations": [
        {"type": "web_search_result_location", "url": "https://a.test", "cited_text": "excerpt for A"},
        {"type": "web_search_result_location", "url": "https://a.test", "cited_text": "second excerpt for A"}
      ]
    },
    {
      "type": "web_search_tool_result",
      "content": [
        {"type": "web_search_result", "url": "https://a.test", "title": "A", "page_age": "2026-02-02"},
        {"type": "web_search_result", "url": "https://b.test", "title": "B"},
        {"type": "web_search_result", "url": "", "title": "no url"},
        {"type": "web_search_result_error", "url": "https://skipped.test"}
      ]
    }
  ]
}`

// recordedRequest is one request a test server captured. Tests receive it over a
// channel rather than reading shared variables, so the handler goroutine and
// the test need no mutex.
type recordedRequest struct {
	Method string
	Path   string
	Header http.Header
	Body   []byte
}

// newServer starts an httptest server returning status and body, and reports
// the single request it captured. The channel is buffered so the handler never
// blocks a test that does not read it.
func newServer(t *testing.T, status int, body string) (*httptest.Server, <-chan recordedRequest) {
	t.Helper()
	recorded := make(chan recordedRequest, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		payload, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read request body: %v", err)
			return
		}
		recorded <- recordedRequest{
			Method: r.Method,
			Path:   r.URL.Path,
			Header: r.Header.Clone(),
			Body:   payload,
		}
		w.Header().Set("content-type", "application/json")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(srv.Close)
	return srv, recorded
}

// clearEnv isolates a test from the developer's real DeepSeek configuration, so
// a machine with an exported key does not make an "unavailable" test pass for
// the wrong reason.
func clearEnv(t *testing.T) {
	t.Helper()
	t.Setenv(DefaultAPIKeyEnv, "")
	t.Setenv(searchBaseURLEnv, "")
}

// mustNew builds a provider or fails the test.
func mustNew(t *testing.T, cfg Config) *Provider {
	t.Helper()
	provider, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return provider
}

// requireWebError asserts err is a *web.Error carrying code and returns it.
func requireWebError(t *testing.T, err error, code string) *web.Error {
	t.Helper()
	var webErr *web.Error
	if !errors.As(err, &webErr) {
		t.Fatalf("error = %v, want *web.Error", err)
	}
	if webErr.Code != code {
		t.Fatalf("code = %q, want %q (message: %s)", webErr.Code, code, webErr.Message)
	}
	return webErr
}

// searchWithBody runs one search against an httptest server that answers with
// status and body.
func searchWithBody(t *testing.T, status int, body string) (web.SearchResult, error) {
	t.Helper()
	srv, _ := newServer(t, status, body)
	provider := mustNew(t, Config{APIKey: "test-key", BaseURL: srv.URL, Timeout: 5 * time.Second})
	return provider.Search(context.Background(), web.SearchRequest{Query: "q"})
}

func TestNewResolvesUpstreamDefaults(t *testing.T) {
	clearEnv(t)

	provider := mustNew(t, Config{APIKey: "k"})
	if provider.ID() != ProviderID {
		t.Fatalf("ID = %q, want %q", provider.ID(), ProviderID)
	}
	if provider.endpoint != DefaultBaseURL+"/messages" {
		t.Fatalf("endpoint = %q, want %q", provider.endpoint, DefaultBaseURL+"/messages")
	}
	// Pin the literal upstream endpoint: a default that drifts silently would
	// send user traffic somewhere else.
	if provider.endpoint != "https://api.deepseek.com/anthropic/v1/messages" {
		t.Fatalf("endpoint = %q, want the upstream default", provider.endpoint)
	}
	if provider.model != "deepseek-v4-flash" {
		t.Fatalf("model = %q, want deepseek-v4-flash", provider.model)
	}
	if provider.apiVersion != "2023-06-01" {
		t.Fatalf("apiVersion = %q, want 2023-06-01", provider.apiVersion)
	}
	if provider.maxTokens != 4096 || provider.maxUses != 5 {
		t.Fatalf("limits = %d/%d, want 4096/5", provider.maxTokens, provider.maxUses)
	}
	if provider.timeout != DefaultTimeout {
		t.Fatalf("timeout = %s, want %s", provider.timeout, DefaultTimeout)
	}
	if !provider.Available() {
		t.Fatal("Available() = false with a key and the default endpoint")
	}
}

func TestNewReadsEnvironmentFallbacks(t *testing.T) {
	clearEnv(t)
	t.Setenv(DefaultAPIKeyEnv, "env-key")
	t.Setenv(searchBaseURLEnv, "https://search.test/v2")

	provider := mustNew(t, Config{})
	if provider.apiKey != "env-key" {
		t.Fatalf("apiKey = %q, want the environment key", provider.apiKey)
	}
	if provider.endpoint != "https://search.test/v2/messages" {
		t.Fatalf("endpoint = %q, want the DEEPSEEK_SEARCH_BASE_URL endpoint", provider.endpoint)
	}

	// Explicit configuration wins over the environment.
	explicit := mustNew(t, Config{APIKey: "literal", BaseURL: "https://explicit.test/v1"})
	if explicit.apiKey != "literal" {
		t.Fatalf("apiKey = %q, want the literal key", explicit.apiKey)
	}
	if explicit.endpoint != "https://explicit.test/v1/messages" {
		t.Fatalf("endpoint = %q, want the explicit endpoint", explicit.endpoint)
	}
}

func TestAvailable(t *testing.T) {
	clearEnv(t)

	if mustNew(t, Config{BaseURL: "https://api.deepseek.test/v1"}).Available() {
		t.Fatal("Available() = true without an API key")
	}
	if !mustNew(t, Config{APIKey: "k", BaseURL: "https://api.deepseek.test/v1"}).Available() {
		t.Fatal("Available() = false with a key")
	}
	if mustNew(t, Config{APIKey: "k", BaseURL: "not a url"}).Available() {
		t.Fatal("Available() = true with an unparseable endpoint")
	}
	if mustNew(t, Config{APIKey: "k", BaseURL: "/relative"}).Available() {
		t.Fatal("Available() = true with a relative endpoint")
	}
	if mustNew(t, Config{APIKey: "k", BaseURL: "http://exa mple.com"}).Available() {
		t.Fatal("Available() = true with a malformed endpoint")
	}

	provider := mustNew(t, Config{APIKey: "k", BaseURL: "https://api.deepseek.test/v1"})
	if !provider.Available() || !provider.Available() {
		t.Fatal("Available() is not stable across calls")
	}
}

func TestNewRejectsNegativeLimits(t *testing.T) {
	clearEnv(t)

	cases := []Config{
		{MaxTokens: -1},
		{MaxUses: -1},
		{Timeout: -time.Second},
	}
	for _, cfg := range cases {
		if _, err := New(cfg); err == nil || !strings.Contains(err.Error(), "deepseeksearch:") {
			t.Fatalf("New(%+v) error = %v, want a deepseeksearch: error", cfg, err)
		}
	}
}

func TestSearchSendsUpstreamRequestAndMapsResponse(t *testing.T) {
	clearEnv(t)
	srv, recorded := newServer(t, http.StatusOK, searchResponse)
	provider := mustNew(t, Config{APIKey: "test-key", BaseURL: srv.URL + "/anthropic/v1", Timeout: 5 * time.Second})

	result, err := provider.Search(context.Background(), web.SearchRequest{Query: "golang"})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}

	want := []web.SearchSource{
		{URL: "https://a.test", Title: "A", Snippet: "excerpt for A", PublishedAt: "2026-02-02"},
		{URL: "https://b.test", Title: "B"},
	}
	if len(result.Sources) != len(want) {
		t.Fatalf("sources = %+v, want %+v", result.Sources, want)
	}
	for i, source := range result.Sources {
		if source != want[i] {
			t.Fatalf("source[%d] = %+v, want %+v", i, source, want[i])
		}
	}
	if result.Content != "" {
		t.Fatalf("Content = %q, want empty: the upstream provider returns sources only", result.Content)
	}
	if result.Truncated {
		t.Fatal("Truncated = true, want false: the seam owns maxResults")
	}

	got := <-recorded
	if got.Method != http.MethodPost {
		t.Fatalf("method = %s, want POST", got.Method)
	}
	if got.Path != "/anthropic/v1/messages" {
		t.Fatalf("path = %s, want /anthropic/v1/messages", got.Path)
	}
	for header, want := range map[string]string{
		"x-api-key":         "test-key",
		"authorization":     "Bearer test-key",
		"anthropic-version": "2023-06-01",
		"content-type":      "application/json",
		"accept":            "application/json",
		"user-agent":        userAgent,
	} {
		if value := got.Header.Get(header); value != want {
			t.Fatalf("header %s = %q, want %q", header, value, want)
		}
	}

	// A body with extra fields would mean the port carries configuration the
	// upstream provider does not send.
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(got.Body, &fields); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if len(fields) != 4 {
		t.Fatalf("body fields = %v, want exactly model, max_tokens, messages, tools", fields)
	}
	for _, field := range []string{"model", "max_tokens", "messages", "tools"} {
		if _, ok := fields[field]; !ok {
			t.Fatalf("body is missing %q: %s", field, got.Body)
		}
	}

	var body struct {
		Model     string `json:"model"`
		MaxTokens int    `json:"max_tokens"`
		Messages  []struct {
			Role    string `json:"role"`
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		} `json:"messages"`
		Tools []struct {
			Type    string `json:"type"`
			Name    string `json:"name"`
			MaxUses int    `json:"max_uses"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(got.Body, &body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if body.Model != "deepseek-v4-flash" || body.MaxTokens != 4096 {
		t.Fatalf("model/max_tokens = %q/%d", body.Model, body.MaxTokens)
	}
	if len(body.Messages) != 1 || body.Messages[0].Role != "user" {
		t.Fatalf("messages = %+v, want one user message", body.Messages)
	}
	if len(body.Messages[0].Content) != 1 ||
		body.Messages[0].Content[0].Type != "text" ||
		body.Messages[0].Content[0].Text != "Perform a web search for the query: golang" {
		t.Fatalf("message content = %+v", body.Messages[0].Content)
	}
	if len(body.Tools) != 1 ||
		body.Tools[0].Type != "web_search_20250305" ||
		body.Tools[0].Name != "web_search" ||
		body.Tools[0].MaxUses != 5 {
		t.Fatalf("tools = %+v", body.Tools)
	}
}

func TestSearchIgnoresMaxResults(t *testing.T) {
	clearEnv(t)
	srv, _ := newServer(t, http.StatusOK, searchResponse)
	provider := mustNew(t, Config{APIKey: "test-key", BaseURL: srv.URL})

	result, err := provider.Search(context.Background(), web.SearchRequest{Query: "q", MaxResults: 1})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(result.Sources) != 2 {
		t.Fatalf("sources = %d, want 2: the provider does not truncate", len(result.Sources))
	}
	if result.Truncated {
		t.Fatal("Truncated = true, want false: truncation belongs to the seam")
	}
}

func TestSearchResponseMapping(t *testing.T) {
	clearEnv(t)

	cases := []struct {
		name  string
		body  string
		want  []web.SearchSource
		code  string
		match string
	}{
		{
			name: "empty result block has no sources",
			body: `{"content":[{"type":"web_search_tool_result"}]}`,
		},
		{
			name: "non-result items are skipped",
			body: `{"content":[{"type":"web_search_tool_result","content":[
				{"type":"web_search_result_error","url":"https://err.test"},
				{"type":"web_search_result","url":""},
				{"type":"web_search_result","url":"https://ok.test"}
			]}]}`,
			want: []web.SearchSource{{URL: "https://ok.test"}},
		},
		{
			name: "duplicate urls keep the first item",
			body: `{"content":[
				{"type":"web_search_tool_result","content":[{"type":"web_search_result","url":"https://a.test","title":"first"}]},
				{"type":"web_search_tool_result","content":[{"type":"web_search_result","url":"https://a.test","title":"second"}]}
			]}`,
			want: []web.SearchSource{{URL: "https://a.test", Title: "first"}},
		},
		{
			name: "empty optional fields stay empty",
			body: `{"content":[{"type":"web_search_tool_result","content":[
				{"type":"web_search_result","url":"https://a.test","title":"","page_age":""}
			]}]}`,
			want: []web.SearchSource{{URL: "https://a.test"}},
		},
		{
			name: "citation without a matching result is dropped",
			body: `{"content":[
				{"type":"text","citations":[{"url":"https://orphan.test","cited_text":"orphan"}]},
				{"type":"web_search_tool_result","content":[{"type":"web_search_result","url":"https://a.test"}]}
			]}`,
			want: []web.SearchSource{{URL: "https://a.test"}},
		},
		{
			name: "citation without an excerpt is ignored",
			body: `{"content":[
				{"type":"text","citations":[{"url":"https://a.test","cited_text":""},{"url":"","cited_text":"no url"}]},
				{"type":"web_search_tool_result","content":[{"type":"web_search_result","url":"https://a.test"}]}
			]}`,
			want: []web.SearchSource{{URL: "https://a.test"}},
		},
		{
			name:  "prose only is a provider error",
			body:  `{"content":[{"type":"text","text":"no search happened"}]}`,
			code:  CodeProviderError,
			match: "web_search_tool_result",
		},
		{
			name:  "absent content is a provider error",
			body:  `{}`,
			code:  CodeProviderError,
			match: "web_search_tool_result",
		},
		{
			name:  "server tool error names its code",
			body:  `{"content":[{"type":"web_search_tool_result","content":{"type":"web_search_tool_result_error","error_code":"max_uses_exceeded"}}]}`,
			code:  CodeProviderError,
			match: "max_uses_exceeded",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			result, err := searchWithBody(t, http.StatusOK, tc.body)
			if tc.code != "" {
				webErr := requireWebError(t, err, tc.code)
				if !strings.Contains(webErr.Message, tc.match) {
					t.Fatalf("message = %q, want it to contain %q", webErr.Message, tc.match)
				}
				return
			}
			if err != nil {
				t.Fatalf("Search: %v", err)
			}
			if len(result.Sources) != len(tc.want) {
				t.Fatalf("sources = %+v, want %+v", result.Sources, tc.want)
			}
			for i, source := range result.Sources {
				if source != tc.want[i] {
					t.Fatalf("source[%d] = %+v, want %+v", i, source, tc.want[i])
				}
			}
		})
	}
}

func TestSearchHTTPError(t *testing.T) {
	clearEnv(t)
	srv, _ := newServer(t, http.StatusTooManyRequests, `{"error":{"message":"rate limited"}}`)
	provider := mustNew(t, Config{APIKey: "test-key", BaseURL: srv.URL + "/anthropic/v1"})

	_, err := provider.Search(context.Background(), web.SearchRequest{Query: "q"})
	webErr := requireWebError(t, err, CodeProviderError)
	if !strings.Contains(webErr.Message, "DeepSeek API error (HTTP 429): rate limited") {
		t.Fatalf("message = %q", webErr.Message)
	}
	if !strings.Contains(webErr.Message, `"`+srv.URL+`/anthropic/v1/messages"`) {
		t.Fatalf("message = %q, want the endpoint named", webErr.Message)
	}
	if !strings.Contains(webErr.Message, "Search endpoint configuration is separate from chat") {
		t.Fatalf("message = %q, want endpoint guidance", webErr.Message)
	}
}

func TestSearchCredentialRejected(t *testing.T) {
	clearEnv(t)
	srv, _ := newServer(t, http.StatusUnauthorized, `{"error":{"message":"invalid key"}}`)
	provider := mustNew(t, Config{APIKey: "test-key", BaseURL: srv.URL})

	_, err := provider.Search(context.Background(), web.SearchRequest{Query: "q"})
	webErr := requireWebError(t, err, CodeProviderError)
	if !strings.Contains(webErr.Message, "DeepSeek API error (HTTP 401): invalid key") {
		t.Fatalf("message = %q", webErr.Message)
	}
	if !strings.Contains(webErr.Message, "DeepSeek rejected the API key") {
		t.Fatalf("message = %q, want API-key guidance", webErr.Message)
	}
	if strings.Contains(webErr.Message, "Search endpoint configuration is separate from chat") {
		t.Fatalf("message = %q, want no endpoint guidance for an auth failure", webErr.Message)
	}
}

func TestSearchErrorBodyVariants(t *testing.T) {
	clearEnv(t)

	cases := []struct {
		name   string
		status int
		body   string
		match  string
	}{
		{"string error body", http.StatusBadRequest, `{"error":"bad request"}`, "DeepSeek API error (HTTP 400): bad request"},
		{"top-level message", http.StatusInternalServerError, `{"message":"server exploded"}`, "DeepSeek API error (HTTP 500): server exploded"},
		{"non-JSON body", http.StatusServiceUnavailable, `upstream error`, "DeepSeek API error (HTTP 503)"},
		{"no detail", http.StatusInternalServerError, `{}`, "DeepSeek API error (HTTP 500)"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := searchWithBody(t, tc.status, tc.body)
			webErr := requireWebError(t, err, CodeProviderError)
			if !strings.Contains(webErr.Message, tc.match) {
				t.Fatalf("message = %q, want it to contain %q", webErr.Message, tc.match)
			}
		})
	}
}

func TestSearchMalformedSuccessBody(t *testing.T) {
	clearEnv(t)

	for _, body := range []string{`not json`, `{"content":{}}`, `{"content":[{"type":"web_search_tool_result","content":"nope"}]}`} {
		_, err := searchWithBody(t, http.StatusOK, body)
		webErr := requireWebError(t, err, CodeProviderError)
		if !strings.Contains(webErr.Message, "unprocessable") {
			t.Fatalf("body %q: message = %q, want an unprocessable-response report", body, webErr.Message)
		}
	}
}

func TestSearchMissingCredential(t *testing.T) {
	clearEnv(t)
	srv, _ := newServer(t, http.StatusOK, searchResponse)
	provider := mustNew(t, Config{BaseURL: srv.URL})

	_, err := provider.Search(context.Background(), web.SearchRequest{Query: "q"})
	webErr := requireWebError(t, err, CodeCredentialMissing)
	if !strings.Contains(webErr.Message, DefaultAPIKeyEnv) {
		t.Fatalf("message = %q, want the credential reference named", webErr.Message)
	}
}

func TestSearchTimeout(t *testing.T) {
	clearEnv(t)
	started := make(chan struct{}, 1)
	// The handler parks until the test releases it. Waiting on the request
	// context instead would leave httptest.Server.Close waiting for a handler
	// the server never observed as disconnected, because the handler does not
	// read the request body and so never sees the closed connection.
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started <- struct{}{}
		<-release
	}))
	t.Cleanup(srv.Close)
	defer close(release)

	provider := mustNew(t, Config{APIKey: "test-key", BaseURL: srv.URL, Timeout: 20 * time.Millisecond})
	start := time.Now()
	errs := make(chan error, 1)
	go func() {
		_, err := provider.Search(context.Background(), web.SearchRequest{Query: "q"})
		errs <- err
	}()

	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("search never reached the server")
	}
	var err error
	select {
	case err = <-errs:
	case <-time.After(5 * time.Second):
		t.Fatal("search did not end at its timeout")
	}

	webErr := requireWebError(t, err, CodeProviderError)
	if !strings.Contains(webErr.Message, "timed out") {
		t.Fatalf("message = %q, want a timeout report", webErr.Message)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("search took %s, want the configured timeout to end it", elapsed)
	}
}

func TestSearchContextCancelledBeforeDispatch(t *testing.T) {
	clearEnv(t)
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
	}))
	t.Cleanup(srv.Close)

	provider := mustNew(t, Config{APIKey: "test-key", BaseURL: srv.URL})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := provider.Search(ctx, web.SearchRequest{Query: "q"})
	requireWebError(t, err, CodeAborted)
	if hits.Load() != 0 {
		t.Fatalf("requests = %d, want none for a cancelled call", hits.Load())
	}
}

func TestSearchContextCancelledInFlight(t *testing.T) {
	clearEnv(t)
	started := make(chan struct{}, 1)
	// See TestSearchTimeout for why the handler waits on the test's channel
	// rather than on the request context.
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started <- struct{}{}
		<-release
	}))
	t.Cleanup(srv.Close)
	defer close(release)

	provider := mustNew(t, Config{APIKey: "test-key", BaseURL: srv.URL, Timeout: time.Minute})
	ctx, cancel := context.WithCancel(context.Background())
	errs := make(chan error, 1)
	go func() {
		_, err := provider.Search(ctx, web.SearchRequest{Query: "q"})
		errs <- err
	}()

	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("search never reached the server")
	}
	cancel()

	select {
	case err := <-errs:
		requireWebError(t, err, CodeAborted)
	case <-time.After(5 * time.Second):
		t.Fatal("search did not return after cancellation")
	}
}

func TestSearchRejectsRedirect(t *testing.T) {
	clearEnv(t)
	var targetHits atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		targetHits.Add(1)
		_, _ = io.WriteString(w, searchResponse)
	}))
	t.Cleanup(target.Close)

	redirector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusFound)
	}))
	t.Cleanup(redirector.Close)

	provider := mustNew(t, Config{APIKey: "test-key", BaseURL: redirector.URL, Timeout: 5 * time.Second})
	_, err := provider.Search(context.Background(), web.SearchRequest{Query: "q"})

	webErr := requireWebError(t, err, CodeProviderError)
	if !strings.Contains(webErr.Message, "redirect") {
		t.Fatalf("message = %q, want the redirect refusal reported", webErr.Message)
	}
	if targetHits.Load() != 0 {
		t.Fatalf("redirect target received %d requests, want 0: the credential must not be forwarded", targetHits.Load())
	}
}

// roundTripFunc adapts a function to http.RoundTripper, so a test can answer a
// request without a network listener.
type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestSearchUsesInjectedClient(t *testing.T) {
	clearEnv(t)
	var calls atomic.Int32
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		calls.Add(1)
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body: io.NopCloser(strings.NewReader(
				`{"content":[{"type":"web_search_tool_result","content":[{"type":"web_search_result","url":"https://a.test"}]}]}`,
			)),
			Request: r,
		}, nil
	})}

	provider := mustNew(t, Config{
		APIKey:     "test-key",
		BaseURL:    "https://api.deepseek.test/anthropic/v1",
		HTTPClient: client,
	})
	result, err := provider.Search(context.Background(), web.SearchRequest{Query: "q"})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if calls.Load() != 1 {
		t.Fatalf("injected client calls = %d, want 1", calls.Load())
	}
	if len(result.Sources) != 1 || result.Sources[0].URL != "https://a.test" {
		t.Fatalf("sources = %+v", result.Sources)
	}
}

func TestAvailableDoesNotTouchNetwork(t *testing.T) {
	clearEnv(t)
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
	}))
	t.Cleanup(srv.Close)

	provider := mustNew(t, Config{APIKey: "test-key", BaseURL: srv.URL})
	for i := 0; i < 3; i++ {
		provider.Available()
	}
	if hits.Load() != 0 {
		t.Fatalf("Available made %d network requests, want 0", hits.Load())
	}
}
