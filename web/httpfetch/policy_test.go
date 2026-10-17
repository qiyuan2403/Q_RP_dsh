package httpfetch

import (
	"net/url"
	"strings"
	"testing"
)

// mustURL parses a URL that the test itself declares valid.
func mustURL(t *testing.T, raw string) *url.URL {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("url.Parse(%q): %v", raw, err)
	}
	return u
}

func TestValidateFetchURL(t *testing.T) {
	t.Run("accepts http and https", func(t *testing.T) {
		for _, raw := range []string{"http://example.com/", "https://example.com/a?b=c"} {
			if _, err := validateFetchURL(raw); err != nil {
				t.Errorf("validateFetchURL(%q) = %v, want nil", raw, err)
			}
		}
	})

	t.Run("normalizes scheme, host, default port, and empty path", func(t *testing.T) {
		cases := map[string]string{
			"HTTP://EXAMPLE.COM:80":         "http://example.com/",
			"https://Example.Com:443/a":     "https://example.com/a",
			"http://example.com":            "http://example.com/",
			"http://example.com:8080":       "http://example.com:8080/",
			"https://example.com:80/a":      "https://example.com:80/a",
			"http://example.com/a?b=c#frag": "http://example.com/a?b=c#frag",
			"http://[::1]":                  "http://[::1]/",
			"http://[::1]:8080/x":           "http://[::1]:8080/x",
			// An empty userinfo marker carries no credential.
			"http://@example.com/": "http://example.com/",
		}
		for raw, want := range cases {
			u, err := validateFetchURL(raw)
			if err != nil {
				t.Errorf("validateFetchURL(%q): %v", raw, err)
				continue
			}
			if got := u.String(); got != want {
				t.Errorf("validateFetchURL(%q) = %q, want %q", raw, got, want)
			}
		}
	})

	t.Run("accepts a URL exactly at the length cap", func(t *testing.T) {
		prefix := "https://example.com/"
		exact := prefix + strings.Repeat("a", maxURLLength-len(prefix))
		if _, err := validateFetchURL(exact); err != nil {
			t.Fatalf("validateFetchURL(exact cap) = %v, want nil", err)
		}
		_, err := validateFetchURL(exact + "a")
		wantCode(t, err, codeInvalidURL)
	})

	t.Run("refuses invalid input", func(t *testing.T) {
		cases := []struct {
			name string
			url  string
			code string
		}{
			{"scheme", "ftp://example.com/", codeInvalidURL},
			{"file scheme", "file:///etc/passwd", codeInvalidURL},
			{"no scheme", "not a url", codeInvalidURL},
			{"relative path", "/just/a/path", codeInvalidURL},
			{"credentials", "http://user:pass@example.com/", codeBlockedURL},
			{"empty", "", codeInvalidURL},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				_, err := validateFetchURL(tc.url)
				wantCode(t, err, tc.code)
			})
		}
	})
}

func TestClassifyContentType(t *testing.T) {
	cases := []struct {
		contentType string
		want        string
	}{
		{"text/html; charset=utf-8", kindHTML},
		{"text/html", kindHTML},
		{"TEXT/HTML", kindHTML},
		{" application/xhtml+xml ", kindHTML},
		{"text/plain", kindText},
		{"text/*", kindText},
		{"application/json", kindText},
		{"application/ld+json", kindText},
		{"application/xml", kindText},
		{"application/rss+xml", kindText},
		{"image/png", ""},
		{"application/octet-stream", ""},
		{"", ""},
	}
	for _, tc := range cases {
		if got := classifyContentType(tc.contentType); got != tc.want {
			t.Errorf("classifyContentType(%q) = %q, want %q", tc.contentType, got, tc.want)
		}
	}
}

func TestParseCharset(t *testing.T) {
	cases := map[string]string{
		"text/html; charset=UTF-8":           "utf-8",
		"text/plain; charset=\"iso-8859-1\"": "iso-8859-1",
		"text/plain;charset= utf-8":          "utf-8",
		"text/plain":                         "",
		"":                                   "",
	}
	for contentType, want := range cases {
		if got := parseCharset(contentType); got != want {
			t.Errorf("parseCharset(%q) = %q, want %q", contentType, got, want)
		}
	}
}

func TestSameOrigin(t *testing.T) {
	cases := []struct {
		name string
		a    string
		b    string
		want bool
	}{
		{"same origin", "https://example.com/a", "https://example.com/b", true},
		{"different path and query", "https://example.com/a?x=1", "https://example.com/b?y=2", true},
		{"host case", "https://Example.COM/", "https://example.com/", true},
		{"default port", "https://example.com:443/", "https://example.com/", true},
		{"different host", "https://example.com/", "https://example.net/", false},
		{"different scheme", "http://example.com/", "https://example.com/", false},
		{"different port", "https://example.com:8443/", "https://example.com/", false},
		{"http default port", "http://example.com:80/", "http://example.com/", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := sameOrigin(mustURL(t, tc.a), mustURL(t, tc.b)); got != tc.want {
				t.Errorf("sameOrigin(%q, %q) = %v, want %v", tc.a, tc.b, got, tc.want)
			}
		})
	}
}

func TestOriginOf(t *testing.T) {
	u, err := validateFetchURL("HTTPS://Example.com:8443/x")
	if err != nil {
		t.Fatalf("validateFetchURL: %v", err)
	}
	if got, want := originOf(u), "https://example.com:8443"; got != want {
		t.Errorf("originOf = %q, want %q", got, want)
	}
}

func TestCharsetDecoder(t *testing.T) {
	t.Run("utf-8 replaces malformed bytes", func(t *testing.T) {
		decode, ok := charsetDecoder("utf-8")
		if !ok {
			t.Fatal("charsetDecoder(utf-8) = false")
		}
		if got, want := decode([]byte("caf\xe9")), "caf\uFFFD"; got != want {
			t.Errorf("decoded = %q, want %q", got, want)
		}
	})

	t.Run("utf-8 strips a leading BOM", func(t *testing.T) {
		decode, ok := charsetDecoder("utf-8")
		if !ok {
			t.Fatal("charsetDecoder(utf-8) = false")
		}
		if got, want := decode([]byte{0xef, 0xbb, 0xbf, 'h', 'i'}), "hi"; got != want {
			t.Errorf("decoded = %q, want %q", got, want)
		}
	})

	t.Run("an absent label decodes as UTF-8", func(t *testing.T) {
		decode, ok := charsetDecoder("")
		if !ok {
			t.Fatal("charsetDecoder(\"\") = false")
		}
		if got, want := decode([]byte{0xef, 0xbb, 0xbf, 'h', 'i'}), "hi"; got != want {
			t.Errorf("decoded = %q, want %q", got, want)
		}
	})

	t.Run("utf-16 strips the BOM", func(t *testing.T) {
		for _, tc := range []struct {
			label string
			body  []byte
		}{
			{"utf-16le", []byte{0xff, 0xfe, 0x60, 0x4f, 0x7d, 0x59}},
			{"utf-16be", []byte{0xfe, 0xff, 0x4f, 0x60, 0x59, 0x7d}},
		} {
			decode, ok := charsetDecoder(tc.label)
			if !ok {
				t.Fatalf("charsetDecoder(%q) = false", tc.label)
			}
			if got, want := decode(tc.body), "你好"; got != want {
				t.Errorf("%s decoded = %q, want %q", tc.label, got, want)
			}
		}
	})

	t.Run("windows-1252 family decodes high bytes", func(t *testing.T) {
		for _, label := range []string{"windows-1252", "iso-8859-1", "latin1", "us-ascii", "ascii"} {
			decode, ok := charsetDecoder(label)
			if !ok {
				t.Fatalf("charsetDecoder(%q) = false, want true", label)
			}
			if got, want := decode([]byte{0x63, 0x61, 0x66, 0xe9}), "café"; got != want {
				t.Errorf("%s decoded = %q, want %q", label, got, want)
			}
		}
	})

	t.Run("windows-1252 special range", func(t *testing.T) {
		decode, _ := charsetDecoder("windows-1252")
		// 0x80 is the euro sign, 0x93 the left double quote; neither is Latin-1.
		if got, want := decode([]byte{0x80, 0x93}), "€“"; got != want {
			t.Errorf("decoded = %q, want %q", got, want)
		}
	})

	t.Run("gbk decodes Chinese and replaces malformed bytes", func(t *testing.T) {
		decode, ok := charsetDecoder("gbk")
		if !ok {
			t.Fatal("charsetDecoder(gbk) = false")
		}
		if got, want := decode([]byte{0xd6, 0xd0, 0xce, 0xc4}), "中文"; got != want {
			t.Errorf("decoded = %q, want %q", got, want)
		}
		if got, want := decode([]byte{0xff}), "\uFFFD"; got != want {
			t.Errorf("malformed decoded = %q, want %q", got, want)
		}
	})

	t.Run("multi-byte labels are supported", func(t *testing.T) {
		for _, label := range []string{"shift_jis", "big5", "gb18030", "euc-jp", "euc-kr"} {
			if _, ok := charsetDecoder(label); !ok {
				t.Errorf("charsetDecoder(%q) = false, want true", label)
			}
		}
	})

	t.Run("unknown label is refused", func(t *testing.T) {
		if _, ok := charsetDecoder("no-such-charset"); ok {
			t.Error("charsetDecoder(no-such-charset) = true, want false")
		}
	})
}

func TestTruncateChars(t *testing.T) {
	cases := []struct {
		name      string
		in        string
		max       int
		want      string
		truncated bool
	}{
		{"under cap", "abc", 5, "abc", false},
		{"exactly at cap", "abc", 3, "abc", false},
		{"over cap", "abcdef", 3, "abc", true},
		{"empty", "", 3, "", false},
		{"multibyte", "héllo", 2, "hé", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, truncated := truncateChars(tc.in, tc.max)
			if got != tc.want || truncated != tc.truncated {
				t.Errorf("truncateChars(%q, %d) = (%q, %v), want (%q, %v)",
					tc.in, tc.max, got, truncated, tc.want, tc.truncated)
			}
		})
	}
}
