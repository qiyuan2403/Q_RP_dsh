package httpfetch

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/qiyuan2403/Q_RP_dsh/web"
	"golang.org/x/text/encoding"
	"golang.org/x/text/encoding/simplifiedchinese"
	"golang.org/x/text/encoding/traditionalchinese"
	"golang.org/x/text/encoding/unicode"
)

// intPtr returns a pointer to v, for the optional numeric Config fields.
func intPtr(v int) *int { return &v }

// encodeWith converts UTF-8 text to a fixture encoding; a failure is a test
// bug, not a provider behavior under test.
func encodeWith(t *testing.T, enc encoding.Encoding, text string) []byte {
	t.Helper()
	encoded, err := enc.NewEncoder().Bytes([]byte(text))
	if err != nil {
		t.Fatalf("encode %q: %v", text, err)
	}
	return encoded
}

// loopbackResolver is the test seam for transport tests: the fixture server
// listens on loopback, which the address policy refuses on purpose. Injecting
// the checked address set mirrors the upstream provider's test seam, and the
// address policy itself is covered directly by the tests in network_test.go.
func loopbackResolver(context.Context, string) ([]netip.Addr, error) {
	return []netip.Addr{netip.MustParseAddr("127.0.0.1")}, nil
}

// newTestProvider builds a provider whose hops dial the loopback fixture.
func newTestProvider(t *testing.T, cfg Config) *Provider {
	t.Helper()
	p, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	p.resolve = loopbackResolver
	return p
}

// newServer starts a fixture server and closes it with the test.
func newServer(t *testing.T, handler http.HandlerFunc) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return srv
}

// wantCode asserts that err is a *web.Error carrying want.
func wantCode(t *testing.T, err error, want string) {
	t.Helper()
	if err == nil {
		t.Fatalf("Fetch succeeded, want error code %s", want)
	}
	var webErr *web.Error
	if !errors.As(err, &webErr) {
		t.Fatalf("error is %T, want *web.Error: %v", err, err)
	}
	if webErr.Code != want {
		t.Fatalf("error code = %s, want %s (error: %v)", webErr.Code, want, err)
	}
}

func TestProviderIdentity(t *testing.T) {
	p, err := New(Config{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if got, want := p.ID(), "http"; got != want {
		t.Errorf("ID() = %q, want %q", got, want)
	}
	if !p.Available() {
		t.Error("Available() = false, want true")
	}
}

func TestNewDefaultsAndRejectsNegativeConfig(t *testing.T) {
	p, err := New(Config{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if got, want := p.cfg.MaxResponseBytes, int64(5_000_000); got != want {
		t.Errorf("MaxResponseBytes = %d, want %d", got, want)
	}
	if got, want := p.cfg.MaxBodyChars, 100_000; got != want {
		t.Errorf("MaxBodyChars = %d, want %d", got, want)
	}
	if got, want := p.cfg.Timeout, 30*time.Second; got != want {
		t.Errorf("Timeout = %v, want %v", got, want)
	}
	if got, want := *p.cfg.MaxRedirects, 5; got != want {
		t.Errorf("MaxRedirects = %d, want %d", got, want)
	}
	if got, want := p.cfg.UserAgent, defaultUserAgent; got != want {
		t.Errorf("UserAgent = %q, want %q", got, want)
	}

	// An explicit zero is a policy, not an absent setting: New must keep it
	// rather than replace it with the default.
	zero, err := New(Config{MaxRedirects: intPtr(0)})
	if err != nil {
		t.Fatalf("New(MaxRedirects: 0): %v", err)
	}
	if got := zero.cfg.MaxRedirects; got == nil || *got != 0 {
		t.Errorf("MaxRedirects = %v, want a pointer to 0", got)
	}

	for name, cfg := range map[string]Config{
		"MaxResponseBytes": {MaxResponseBytes: -1},
		"MaxBodyChars":     {MaxBodyChars: -1},
		"Timeout":          {Timeout: -time.Second},
		"MaxRedirects":     {MaxRedirects: intPtr(-1)},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := New(cfg)
			if err == nil {
				t.Fatalf("New(%+v) succeeded, want a configuration error", cfg)
			}
			if !strings.HasPrefix(err.Error(), "httpfetch:") {
				t.Errorf("error %q does not carry the httpfetch prefix", err)
			}
		})
	}
}

func TestFetchKinds(t *testing.T) {
	cases := []struct {
		name        string
		contentType string
		body        string
		wantKind    string
	}{
		{"html", "text/html; charset=utf-8", "<h1>hi</h1>", kindHTML},
		{"xhtml", "application/xhtml+xml", "<html/>", kindHTML},
		{"text", "text/plain", "hello world", kindText},
		{"json", "application/json", `{"a":1}`, kindText},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := newServer(t, func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", tc.contentType)
				w.Write([]byte(tc.body))
			})
			result, err := newTestProvider(t, Config{}).Fetch(t.Context(), web.FetchRequest{URL: srv.URL + "/page"})
			if err != nil {
				t.Fatalf("Fetch: %v", err)
			}
			if result.StatusCode != http.StatusOK {
				t.Errorf("StatusCode = %d, want 200", result.StatusCode)
			}
			if result.Body.Kind != tc.wantKind {
				t.Errorf("Kind = %q, want %q", result.Body.Kind, tc.wantKind)
			}
			if result.Body.Content != tc.body {
				t.Errorf("Content = %q, want %q", result.Body.Content, tc.body)
			}
			if result.Truncated {
				t.Error("Truncated = true, want false")
			}
			if want := srv.URL + "/page"; result.URL != want {
				t.Errorf("URL = %q, want %q", result.URL, want)
			}
		})
	}
}

func TestFetchReportsNormalizedURL(t *testing.T) {
	srv := newServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		w.Write([]byte("ok"))
	})
	raw := "HTTP://" + srv.Listener.Addr().String()
	result, err := newTestProvider(t, Config{}).Fetch(t.Context(), web.FetchRequest{URL: raw})
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if want := "http://" + srv.Listener.Addr().String() + "/"; result.URL != want {
		t.Errorf("URL = %q, want %q", result.URL, want)
	}
}

func TestFetchNon2xxIsResult(t *testing.T) {
	srv := newServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		w.WriteHeader(http.StatusNotFound)
		w.Write([]byte("nope"))
	})
	result, err := newTestProvider(t, Config{}).Fetch(t.Context(), web.FetchRequest{URL: srv.URL})
	if err != nil {
		t.Fatalf("Fetch returned an error for a 404: %v", err)
	}
	if result.StatusCode != http.StatusNotFound {
		t.Errorf("StatusCode = %d, want 404", result.StatusCode)
	}
	if result.Body.Kind != kindText || result.Body.Content != "nope" {
		t.Errorf("Body = %+v, want text \"nope\"", result.Body)
	}
}

func TestFetchSameOriginRedirect(t *testing.T) {
	srv := newServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/start" {
			// Relative Location exercises resolution against the current URL.
			http.Redirect(w, r, "/end", http.StatusFound)
			return
		}
		w.Header().Set("Content-Type", "text/plain")
		w.Write([]byte("arrived"))
	})
	result, err := newTestProvider(t, Config{}).Fetch(t.Context(), web.FetchRequest{URL: srv.URL + "/start"})
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if result.Body.Content != "arrived" {
		t.Errorf("Content = %q, want %q", result.Body.Content, "arrived")
	}
	if want := srv.URL + "/end"; result.URL != want {
		t.Errorf("URL = %q, want %q", result.URL, want)
	}
}

func TestFetchCrossOriginRedirectBlocked(t *testing.T) {
	var requests atomic.Int64
	srv := newServer(t, func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.Header().Set("Location", "https://example.com/")
		w.WriteHeader(http.StatusFound)
	})
	_, err := newTestProvider(t, Config{}).Fetch(t.Context(), web.FetchRequest{URL: srv.URL})
	wantCode(t, err, codeRedirectBlocked)
	if !strings.Contains(err.Error(), "https://example.com") {
		t.Errorf("error %q does not name the blocked origin", err)
	}
	if got := requests.Load(); got != 1 {
		t.Errorf("origin received %d requests, want 1 (the redirect target must not be contacted)", got)
	}
}

func TestFetchRedirectRevalidatesTarget(t *testing.T) {
	srv := newServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Location", "http://user:pass@"+r.Host+"/")
		w.WriteHeader(http.StatusFound)
	})
	_, err := newTestProvider(t, Config{}).Fetch(t.Context(), web.FetchRequest{URL: srv.URL})
	wantCode(t, err, codeBlockedURL)
}

func TestFetchRedirectWithoutLocation(t *testing.T) {
	srv := newServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusFound)
	})
	_, err := newTestProvider(t, Config{}).Fetch(t.Context(), web.FetchRequest{URL: srv.URL})
	wantCode(t, err, codeProviderError)
}

func TestFetchRedirectBudget(t *testing.T) {
	t.Run("lands on the last allowed hop", func(t *testing.T) {
		var requests atomic.Int64
		srv := newServer(t, func(w http.ResponseWriter, r *http.Request) {
			requests.Add(1)
			if r.URL.Query().Get("n") == "2" {
				w.Header().Set("Content-Type", "text/plain")
				w.Write([]byte("landed"))
				return
			}
			next := "2"
			if r.URL.Query().Get("n") == "0" {
				next = "1"
			}
			w.Header().Set("Location", "/?n="+next)
			w.WriteHeader(http.StatusFound)
		})
		result, err := newTestProvider(t, Config{MaxRedirects: intPtr(2)}).Fetch(t.Context(), web.FetchRequest{URL: srv.URL + "/?n=0"})
		if err != nil {
			t.Fatalf("Fetch: %v", err)
		}
		if result.Body.Content != "landed" {
			t.Errorf("Content = %q, want %q", result.Body.Content, "landed")
		}
		if got := requests.Load(); got != 3 {
			t.Errorf("requests = %d, want 3", got)
		}
	})

	t.Run("refuses an over-long chain", func(t *testing.T) {
		var requests atomic.Int64
		srv := newServer(t, func(w http.ResponseWriter, r *http.Request) {
			requests.Add(1)
			w.Header().Set("Location", "/next")
			w.WriteHeader(http.StatusFound)
		})
		_, err := newTestProvider(t, Config{MaxRedirects: intPtr(2)}).Fetch(t.Context(), web.FetchRequest{URL: srv.URL})
		wantCode(t, err, codeRedirectBlocked)
		if !strings.Contains(err.Error(), "exceeded the maximum of 2 redirects") {
			t.Errorf("error %q does not report the hop cap", err)
		}
		if got := requests.Load(); got != 3 {
			t.Errorf("requests = %d, want 3", got)
		}
	})

	t.Run("reports the hop cap before the origin check", func(t *testing.T) {
		srv := newServer(t, func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Query().Get("n") == "0" {
				w.Header().Set("Location", "/?n=1")
				w.WriteHeader(http.StatusFound)
				return
			}
			w.Header().Set("Location", "https://example.com/")
			w.WriteHeader(http.StatusFound)
		})
		_, err := newTestProvider(t, Config{MaxRedirects: intPtr(1)}).Fetch(t.Context(), web.FetchRequest{URL: srv.URL + "/?n=0"})
		wantCode(t, err, codeRedirectBlocked)
		if !strings.Contains(err.Error(), "exceeded the maximum of 1 redirects") {
			t.Errorf("error %q reports the wrong redirect diagnosis", err)
		}
	})
}

// TestFetchMaxRedirectsZero covers the configuration the int-valued field could
// not express: an explicit cap of zero refuses the first redirect instead of
// falling back to the default hop count.
func TestFetchMaxRedirectsZero(t *testing.T) {
	var startRequests, targetRequests atomic.Int64
	srv := newServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/start" {
			startRequests.Add(1)
			http.Redirect(w, r, "/end", http.StatusFound)
			return
		}
		targetRequests.Add(1)
		w.Header().Set("Content-Type", "text/plain")
		w.Write([]byte("arrived"))
	})

	_, err := newTestProvider(t, Config{MaxRedirects: intPtr(0)}).Fetch(t.Context(), web.FetchRequest{URL: srv.URL + "/start"})
	wantCode(t, err, codeRedirectBlocked)
	if !strings.Contains(err.Error(), "exceeded the maximum of 0 redirects") {
		t.Errorf("error %q does not report the zero redirect cap", err)
	}
	if got := startRequests.Load(); got != 1 {
		t.Errorf("start endpoint received %d requests, want 1", got)
	}
	if got := targetRequests.Load(); got != 0 {
		t.Errorf("redirect target received %d requests, want 0", got)
	}
}

// streamingServer writes body without a Content-Length so the client sees a
// chunked response and has to enforce the byte cap while reading.
func streamingServer(t *testing.T, body []byte) *httptest.Server {
	t.Helper()
	return newServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		w.Write(body)
	})
}

func TestFetchResponseByteCap(t *testing.T) {
	srv := streamingServer(t, bytes.Repeat([]byte("a"), 4096))
	result, err := newTestProvider(t, Config{MaxResponseBytes: 100}).Fetch(t.Context(), web.FetchRequest{URL: srv.URL})
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if len(result.Body.Content) != 100 {
		t.Errorf("content length = %d, want 100", len(result.Body.Content))
	}
	if !result.Truncated {
		t.Error("Truncated = false, want true")
	}
}

func TestFetchResponseByteCapExactIsNotTruncated(t *testing.T) {
	srv := newServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		w.Write([]byte("abcd"))
	})
	result, err := newTestProvider(t, Config{MaxResponseBytes: 4}).Fetch(t.Context(), web.FetchRequest{URL: srv.URL})
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if result.Body.Content != "abcd" {
		t.Errorf("Content = %q, want %q", result.Body.Content, "abcd")
	}
	if result.Truncated {
		t.Error("Truncated = true, want false for a body exactly at the cap")
	}
}

func TestFetchDeclaredContentLengthTooLarge(t *testing.T) {
	const size = 999_999
	srv := newServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		w.Header().Set("Content-Length", "999999")
		w.Write(bytes.Repeat([]byte("x"), size))
	})
	_, err := newTestProvider(t, Config{MaxResponseBytes: 10}).Fetch(t.Context(), web.FetchRequest{URL: srv.URL})
	wantCode(t, err, codeFetchTooLarge)
}

func TestFetchBodyCharCap(t *testing.T) {
	srv := newServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		w.Write([]byte("abcdefghij"))
	})
	result, err := newTestProvider(t, Config{MaxBodyChars: 3}).Fetch(t.Context(), web.FetchRequest{URL: srv.URL})
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if result.Body.Content != "abc" {
		t.Errorf("Content = %q, want %q", result.Body.Content, "abc")
	}
	if !result.Truncated {
		t.Error("Truncated = false, want true")
	}
}

func TestFetchTimeout(t *testing.T) {
	srv := newServer(t, func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(5 * time.Second):
		}
	})
	_, err := newTestProvider(t, Config{Timeout: 50 * time.Millisecond}).Fetch(t.Context(), web.FetchRequest{URL: srv.URL})
	wantCode(t, err, codeFetchTimeout)
}

func TestFetchTimeoutDuringBodyRead(t *testing.T) {
	srv := newServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		w.Header().Set("Content-Length", "100")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("partial"))
		w.(http.Flusher).Flush()
		select {
		case <-r.Context().Done():
		case <-time.After(5 * time.Second):
		}
	})
	_, err := newTestProvider(t, Config{Timeout: 80 * time.Millisecond}).Fetch(t.Context(), web.FetchRequest{URL: srv.URL})
	wantCode(t, err, codeFetchTimeout)
}

func TestFetchInvalidURL(t *testing.T) {
	p := newTestProvider(t, Config{})
	cases := []struct {
		name string
		url  string
		code string
	}{
		{"scheme", "ftp://example.com/", codeInvalidURL},
		{"no scheme", "not a url", codeInvalidURL},
		{"relative", "/just/a/path", codeInvalidURL},
		{"credentials", "http://user:pass@example.com/", codeBlockedURL},
		{"too long", "https://example.com/" + strings.Repeat("a", maxURLLength), codeInvalidURL},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := p.Fetch(t.Context(), web.FetchRequest{URL: tc.url})
			wantCode(t, err, tc.code)
		})
	}
}

func TestFetchRedirectRechecksAddresses(t *testing.T) {
	// The address set is resolved per hop, so a hostname re-resolved after a
	// redirect is checked again rather than trusted from the first request.
	var calls atomic.Int64
	srv := newServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/start" {
			http.Redirect(w, r, "/end", http.StatusFound)
			return
		}
		w.Header().Set("Content-Type", "text/plain")
		w.Write([]byte("arrived"))
	})
	p, err := New(Config{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	p.resolve = func(context.Context, string) ([]netip.Addr, error) {
		calls.Add(1)
		return []netip.Addr{netip.MustParseAddr("127.0.0.1")}, nil
	}
	if _, err := p.Fetch(t.Context(), web.FetchRequest{URL: srv.URL + "/start"}); err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if got := calls.Load(); got != 2 {
		t.Errorf("address resolution ran %d times, want 2 (one per hop)", got)
	}
}

func TestFetchResolverFailureIsProviderError(t *testing.T) {
	p := newTestProvider(t, Config{})
	p.resolve = func(context.Context, string) ([]netip.Addr, error) {
		return nil, errors.New("dns failed")
	}
	_, err := p.Fetch(t.Context(), web.FetchRequest{URL: "http://example.test/"})
	wantCode(t, err, codeProviderError)
}

func TestFetchInvalidRedirectLocation(t *testing.T) {
	srv := newServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Location", "http://[bad")
		w.WriteHeader(http.StatusFound)
	})
	_, err := newTestProvider(t, Config{}).Fetch(t.Context(), web.FetchRequest{URL: srv.URL})
	wantCode(t, err, codeProviderError)
}

func TestFetchConnectionFailureIsProviderError(t *testing.T) {
	// Port 1 on loopback is not listening: a real connection failure, not an
	// abort or a timeout.
	_, err := newTestProvider(t, Config{}).Fetch(t.Context(), web.FetchRequest{URL: "http://127.0.0.1:1/"})
	wantCode(t, err, codeProviderError)
}

func TestFetchPrivateAddressBlocked(t *testing.T) {
	p, err := New(Config{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	for _, tc := range []struct{ name, url string }{
		{"ipv4 loopback", "http://127.0.0.1:1/"},
		{"ipv6 loopback", "http://[::1]:1/"},
		{"link local", "http://169.254.169.254/latest/"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := p.Fetch(t.Context(), web.FetchRequest{URL: tc.url})
			wantCode(t, err, codeBlockedURL)
		})
	}
}

func TestFetchUnsupportedContentType(t *testing.T) {
	cases := []struct {
		name        string
		contentType string
		omitHeader  bool
	}{
		{"binary", "image/png", false},
		{"missing", "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := newServer(t, func(w http.ResponseWriter, r *http.Request) {
				if tc.omitHeader {
					// A present but nil key suppresses Go's content sniffing.
					w.Header()["Content-Type"] = nil
				} else {
					w.Header().Set("Content-Type", tc.contentType)
				}
				w.Write([]byte("body"))
			})
			_, err := newTestProvider(t, Config{}).Fetch(t.Context(), web.FetchRequest{URL: srv.URL})
			wantCode(t, err, codeUnsupportedContentType)
		})
	}
}

func TestFetchCharset(t *testing.T) {
	t.Run("decodes latin1", func(t *testing.T) {
		srv := newServer(t, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/plain; charset=iso-8859-1")
			w.Write([]byte{0x63, 0x61, 0x66, 0xe9})
		})
		result, err := newTestProvider(t, Config{}).Fetch(t.Context(), web.FetchRequest{URL: srv.URL})
		if err != nil {
			t.Fatalf("Fetch: %v", err)
		}
		if result.Body.Content != "café" {
			t.Errorf("Content = %q, want %q", result.Body.Content, "café")
		}
	})

	t.Run("decodes a GBK page", func(t *testing.T) {
		const body = "<html><title>中文页面</title></html>"
		srv := newServer(t, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/html; charset=gbk")
			w.Write(encodeWith(t, simplifiedchinese.GBK, body))
		})
		result, err := newTestProvider(t, Config{}).Fetch(t.Context(), web.FetchRequest{URL: srv.URL})
		if err != nil {
			t.Fatalf("Fetch: %v", err)
		}
		if result.Body.Kind != kindHTML {
			t.Errorf("Kind = %q, want %q", result.Body.Kind, kindHTML)
		}
		if result.Body.Content != body {
			t.Errorf("Content = %q, want %q", result.Body.Content, body)
		}
	})

	t.Run("decodes a Big5 page", func(t *testing.T) {
		const body = "繁體中文"
		srv := newServer(t, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/plain; charset=big5")
			w.Write(encodeWith(t, traditionalchinese.Big5, body))
		})
		result, err := newTestProvider(t, Config{}).Fetch(t.Context(), web.FetchRequest{URL: srv.URL})
		if err != nil {
			t.Fatalf("Fetch: %v", err)
		}
		if result.Body.Content != body {
			t.Errorf("Content = %q, want %q", result.Body.Content, body)
		}
	})

	t.Run("strips a UTF-16LE BOM", func(t *testing.T) {
		const body = "你好世界"
		srv := newServer(t, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/plain; charset=utf-16le")
			w.Write(encodeWith(t, unicode.UTF16(unicode.LittleEndian, unicode.UseBOM), body))
		})
		result, err := newTestProvider(t, Config{}).Fetch(t.Context(), web.FetchRequest{URL: srv.URL})
		if err != nil {
			t.Fatalf("Fetch: %v", err)
		}
		if result.Body.Content != body {
			t.Errorf("Content = %q, want %q", result.Body.Content, body)
		}
	})

	t.Run("strips a UTF-8 BOM", func(t *testing.T) {
		for _, contentType := range []string{"text/plain; charset=utf-8", "text/plain"} {
			srv := newServer(t, func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", contentType)
				w.Write(append([]byte{0xef, 0xbb, 0xbf}, []byte("hello")...))
			})
			result, err := newTestProvider(t, Config{}).Fetch(t.Context(), web.FetchRequest{URL: srv.URL})
			if err != nil {
				t.Fatalf("%s: Fetch: %v", contentType, err)
			}
			if result.Body.Content != "hello" {
				t.Errorf("%s: Content = %q, want %q", contentType, result.Body.Content, "hello")
			}
		}
	})

	t.Run("counts the character cap after decoding", func(t *testing.T) {
		srv := newServer(t, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/plain; charset=gbk")
			w.Write(encodeWith(t, simplifiedchinese.GBK, "中文页面"))
		})
		result, err := newTestProvider(t, Config{MaxBodyChars: 2}).Fetch(t.Context(), web.FetchRequest{URL: srv.URL})
		if err != nil {
			t.Fatalf("Fetch: %v", err)
		}
		if result.Body.Content != "中文" {
			t.Errorf("Content = %q, want %q", result.Body.Content, "中文")
		}
		if !result.Truncated {
			t.Error("Truncated = false, want true")
		}
	})

	t.Run("refuses an unknown charset", func(t *testing.T) {
		srv := newServer(t, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/plain; charset=no-such-charset")
			w.Write([]byte("x"))
		})
		_, err := newTestProvider(t, Config{}).Fetch(t.Context(), web.FetchRequest{URL: srv.URL})
		wantCode(t, err, codeUnsupportedContentType)
		if !strings.Contains(err.Error(), "no-such-charset") {
			t.Errorf("error %q does not name the rejected label", err)
		}
	})
}

func TestFetchHeaders(t *testing.T) {
	var userAgent, accept string
	srv := newServer(t, func(w http.ResponseWriter, r *http.Request) {
		userAgent = r.Header.Get("User-Agent")
		accept = r.Header.Get("Accept")
		w.Header().Set("Content-Type", "text/plain")
		w.Write([]byte("ok"))
	})

	t.Run("configured user agent", func(t *testing.T) {
		if _, err := newTestProvider(t, Config{UserAgent: "test-agent/1.0"}).Fetch(t.Context(), web.FetchRequest{URL: srv.URL}); err != nil {
			t.Fatalf("Fetch: %v", err)
		}
		if userAgent != "test-agent/1.0" {
			t.Errorf("User-Agent = %q, want %q", userAgent, "test-agent/1.0")
		}
		if accept != acceptHeader {
			t.Errorf("Accept = %q, want %q", accept, acceptHeader)
		}
	})

	t.Run("default user agent", func(t *testing.T) {
		if _, err := newTestProvider(t, Config{}).Fetch(t.Context(), web.FetchRequest{URL: srv.URL}); err != nil {
			t.Fatalf("Fetch: %v", err)
		}
		if userAgent != defaultUserAgent {
			t.Errorf("User-Agent = %q, want %q", userAgent, defaultUserAgent)
		}
	})
}

func TestFetchPreCanceledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err := newTestProvider(t, Config{}).Fetch(ctx, web.FetchRequest{URL: "http://127.0.0.1:1/"})
	wantCode(t, err, codeAborted)
}

func TestFetchAbortDuringRequest(t *testing.T) {
	started := make(chan struct{}, 1)
	srv := newServer(t, func(w http.ResponseWriter, r *http.Request) {
		started <- struct{}{}
		select {
		case <-r.Context().Done():
		case <-time.After(5 * time.Second):
		}
	})
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	resultCh := make(chan error, 1)
	go func() {
		_, err := newTestProvider(t, Config{}).Fetch(ctx, web.FetchRequest{URL: srv.URL})
		resultCh <- err
	}()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("fixture request never started")
	}
	cancel()
	wantCode(t, <-resultCh, codeAborted)
}
