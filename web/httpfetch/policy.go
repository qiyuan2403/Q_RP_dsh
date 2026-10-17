package httpfetch

import (
	"net"
	"net/url"
	"regexp"
	"strings"
	"unicode/utf8"
)

// maxURLLength is the longest request URL accepted, in characters. The cap is
// the upstream value: a model-supplied URL is not a document, so an
// unbounded one is a sign of a malformed request rather than a large resource.
const maxURLLength = 2048

// Body kinds this provider produces. They are the FetchBody.Kind values the web
// seam documents ("html" or "text"); the provider keeps its own constants
// because the seam contract is the string value, not a helper symbol in another
// package.
const (
	kindHTML = "html"
	kindText = "text"
)

// validateFetchURL parses and normalizes a request URL and enforces the rules
// that need no network access: http(s) only, no embedded credentials, and a
// bounded length. Public-address policy runs later, once the host resolves.
//
// The returned URL is the form the provider reports and requests: lower-case
// scheme and host, no default port, and a "/" path for an origin-only URL. The
// web URL parser normalizes the same way before the upstream provider reports a
// final URL, and normalizing here keeps a redirect's same-origin comparison
// from failing on spelling alone.
func validateFetchURL(raw string) (*url.URL, error) {
	if utf8.RuneCountInString(raw) > maxURLLength {
		return nil, newError(codeInvalidURL, "URL exceeds the maximum length of %d", maxURLLength)
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" || u.Host == "" {
		// The upstream URL parser rejects a relative or malformed input here.
		// Go's parser accepts "not a url" as a path, so the scheme and host
		// checks stand in for that rejection.
		return nil, newErrorCause(codeInvalidURL, err, "invalid URL: %s", raw)
	}
	u.Scheme = strings.ToLower(u.Scheme)
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, newError(codeInvalidURL, "unsupported URL scheme %q (only http and https are allowed)", u.Scheme)
	}
	if u.User != nil {
		password, hasPassword := u.User.Password()
		// An empty userinfo marker ("http://@host/") carries no credential and
		// the upstream check allows it; any actual user or password is refused.
		if u.User.Username() != "" || (hasPassword && password != "") {
			return nil, newError(codeBlockedURL, "credentials in URLs are not allowed")
		}
		u.User = nil
	}
	normalizeURL(u)
	return u, nil
}

// normalizeURL rewrites scheme, host, port, and an empty path into the form the
// provider reports and requests.
func normalizeURL(u *url.URL) {
	u.Scheme = strings.ToLower(u.Scheme)
	host := strings.ToLower(u.Hostname())
	port := u.Port()
	if (u.Scheme == "http" && port == "80") || (u.Scheme == "https" && port == "443") {
		port = ""
	}
	switch {
	case port != "":
		u.Host = net.JoinHostPort(host, port)
	case strings.Contains(host, ":"):
		u.Host = "[" + host + "]"
	default:
		u.Host = host
	}
	if u.Path == "" {
		u.Path = "/"
	}
}

// sameOrigin reports whether two URLs share scheme, host, and effective port.
//
// The comparison is case-insensitive and default-port-normalized because a
// redirect Location is written by the origin, not by this provider: a spelling
// difference must not turn a same-origin hop into a refusal, and must not turn a
// cross-origin hop into an allowance.
func sameOrigin(a, b *url.URL) bool {
	return strings.EqualFold(a.Scheme, b.Scheme) &&
		strings.EqualFold(a.Hostname(), b.Hostname()) &&
		effectivePort(a) == effectivePort(b)
}

// effectivePort returns the port a request would use, treating an omitted port
// as the scheme's default.
func effectivePort(u *url.URL) string {
	if port := u.Port(); port != "" {
		return port
	}
	switch strings.ToLower(u.Scheme) {
	case "http":
		return "80"
	case "https":
		return "443"
	}
	return ""
}

// originOf renders a URL's origin for diagnostics.
func originOf(u *url.URL) string {
	return strings.ToLower(u.Scheme) + "://" + u.Host
}

// classifyContentType maps a response Content-Type to the body kind this
// provider can represent, or "" when the representation is not text.
//
// The mime type is compared without its parameters and case-insensitively, as
// the upstream provider does. html and XHTML are kept distinct from other text
// because the consumer renders them differently.
func classifyContentType(contentType string) string {
	mime := contentType
	if i := strings.IndexByte(mime, ';'); i >= 0 {
		mime = mime[:i]
	}
	mime = strings.ToLower(strings.TrimSpace(mime))
	switch mime {
	case "text/html", "application/xhtml+xml":
		return kindHTML
	case "application/json", "application/xml":
		return kindText
	}
	if strings.HasPrefix(mime, "text/") || strings.HasSuffix(mime, "+json") || strings.HasSuffix(mime, "+xml") {
		return kindText
	}
	return ""
}

// contentTypeOrUnknown names a missing content type in diagnostics, matching the
// upstream "unknown" placeholder.
func contentTypeOrUnknown(contentType string) string {
	if contentType == "" {
		return "unknown"
	}
	return contentType
}

// charsetPattern extracts the charset parameter from a Content-Type header. It
// is the upstream pattern: the parameter may be quoted or bare, and the capture
// stops at a quote or the next parameter separator.
var charsetPattern = regexp.MustCompile(`;\s*charset\s*=\s*"?([^";]+)"?`)

// parseCharset extracts the lower-cased charset label from a Content-Type, or ""
// when none is declared.
func parseCharset(contentType string) string {
	match := charsetPattern.FindStringSubmatch(contentType)
	if len(match) < 2 {
		return ""
	}
	return strings.ToLower(strings.TrimSpace(match[1]))
}

// truncateChars cuts s to at most max Unicode code points and reports whether
// anything was dropped. A string of exactly max characters is not truncated.
func truncateChars(s string, max int) (string, bool) {
	count := 0
	for i := range s {
		if count == max {
			return s[:i], true
		}
		count++
	}
	return s, false
}
