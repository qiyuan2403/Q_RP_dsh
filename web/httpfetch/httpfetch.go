// Package httpfetch provides the anonymous public HTTP(S) fetch provider for
// the web seam.
//
// A fetch URL is chosen by a model, so the provider treats every destination as
// hostile until checked: only http and https are fetched, only public unicast
// addresses are dialed, no credentials or cookies are sent, and only
// same-origin redirects are followed. The address that passed the check is the
// address that is dialed. Resolving a second time would let a rebinding DNS
// answer point the connection at a private service after the check.
//
// A failure to retrieve or represent a resource is a *web.Error carrying one of
// the upstream provider's codes, so a consumer that routes on codes keeps
// working. A non-2xx response is a result, not a failure: the status code
// describes the fetched resource.
package httpfetch

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/netip"
	"net/url"
	"time"

	"github.com/qiyuan2403/Q_RP_dsh/web"
)

// Defaults for zero-valued Config fields. They are the upstream provider's
// values, not local tuning: a deployment that wants another limit sets it
// explicitly.
const (
	defaultMaxResponseBytes = 5_000_000
	defaultMaxBodyChars     = 100_000
	defaultTimeout          = 30 * time.Second
	defaultMaxRedirects     = 5
	defaultUserAgent        = "deepseek-harness/0.0.1 (+https://github.com/deepseek-ai)"
)

// localFetchProviderID is the registry id the upstream provider registers
// under.
const localFetchProviderID = "http"

// acceptHeader is sent on every request. It is the upstream value verbatim, so
// an origin that content-negotiates keeps returning the same representation.
const acceptHeader = "text/html,application/xhtml+xml,text/*;q=0.9,application/json;q=0.8"

// errorPrefix names this provider inside the seam's shared error namespace.
// The web seam renders an error as "web: CODE: message", so the provider adds
// its own name to say which fetch provider produced the failure.
const errorPrefix = "httpfetch: "

// Error codes carried by web.Error. They are the upstream provider's codes so
// that a consumer already routing on them keeps working. The seam treats the
// code as an open string; these are the ones this package produces.
const (
	codeInvalidURL             = "WEB_INVALID_URL"
	codeBlockedURL             = "WEB_BLOCKED_URL"
	codeRedirectBlocked        = "WEB_REDIRECT_BLOCKED"
	codeFetchTooLarge          = "WEB_FETCH_TOO_LARGE"
	codeFetchTimeout           = "WEB_FETCH_TIMEOUT"
	codeUnsupportedContentType = "WEB_UNSUPPORTED_CONTENT_TYPE"
	codeProviderError          = "WEB_PROVIDER_ERROR"
	codeAborted                = "WEB_ABORTED"
)

// Config sets the provider's transport and response limits.
//
// Every field is optional: a zero value selects the upstream default, except
// MaxRedirects, where a nil pointer does because zero has an upstream meaning
// of its own. A negative limit is refused by New rather than defaulted: it can
// only be a configuration mistake, and running with a silently different limit
// would hide it.
type Config struct {
	// MaxResponseBytes bounds the response body in bytes. Reading stops at the
	// cap and the result is marked truncated; a declared Content-Length above
	// the cap is refused instead, so the bytes are never consumed. Zero uses
	// 5_000_000.
	MaxResponseBytes int64
	// MaxBodyChars bounds the decoded body in Unicode code points. Text past
	// the cap is dropped and the result is marked truncated. Zero uses
	// 100_000.
	MaxBodyChars int
	// Timeout bounds one Fetch call, including every redirect hop and the body
	// read. Zero uses 30s.
	Timeout time.Duration
	// MaxRedirects bounds the number of same-origin redirect hops that may be
	// followed. A nil pointer uses 5; zero follows no redirect, so the first
	// redirect status fails with WEB_REDIRECT_BLOCKED. The pointer is what
	// keeps "follow no redirects" from collapsing into "unset", which the
	// upstream schema also distinguishes.
	MaxRedirects *int
	// UserAgent is sent on every request. Zero uses an explicit harness
	// product string; the provider never disguises itself as a browser.
	UserAgent string
}

// Provider is the anonymous public HTTP(S) fetch provider.
//
// A Provider holds only resolved configuration. Each hop builds its own
// transport, so concurrent calls share no connection or address state.
type Provider struct {
	cfg Config

	// resolve is the test injection point for the checked address set of a
	// hop. It must return only addresses that already passed public-address
	// policy; nil runs resolvePublicAddresses. The field stays unexported
	// because a caller replacing it would own the SSRF policy.
	resolve func(ctx context.Context, hostname string) ([]netip.Addr, error)
}

var _ web.FetchProvider = (*Provider)(nil)

// New resolves cfg defaults and builds the provider.
//
// The error return is for configuration validation only: a negative limit,
// timeout, or redirect cap is rejected at construction. It is not a runtime
// error, so a caller that validates its configuration once gets a Provider that
// cannot fail for configuration reasons later.
func New(cfg Config) (*Provider, error) {
	if cfg.MaxResponseBytes < 0 {
		return nil, fmt.Errorf("httpfetch: MaxResponseBytes must not be negative")
	}
	if cfg.MaxBodyChars < 0 {
		return nil, fmt.Errorf("httpfetch: MaxBodyChars must not be negative")
	}
	if cfg.Timeout < 0 {
		return nil, fmt.Errorf("httpfetch: Timeout must not be negative")
	}
	if cfg.MaxRedirects != nil && *cfg.MaxRedirects < 0 {
		return nil, fmt.Errorf("httpfetch: MaxRedirects must not be negative")
	}
	if cfg.MaxResponseBytes == 0 {
		cfg.MaxResponseBytes = defaultMaxResponseBytes
	}
	if cfg.MaxBodyChars == 0 {
		cfg.MaxBodyChars = defaultMaxBodyChars
	}
	if cfg.Timeout == 0 {
		cfg.Timeout = defaultTimeout
	}
	// The cap is copied so the provider keeps one policy: a caller that holds
	// the pointer must not be able to change redirect limits after New.
	maxRedirects := defaultMaxRedirects
	if cfg.MaxRedirects != nil {
		maxRedirects = *cfg.MaxRedirects
	}
	cfg.MaxRedirects = &maxRedirects
	if cfg.UserAgent == "" {
		cfg.UserAgent = defaultUserAgent
	}
	return &Provider{cfg: cfg}, nil
}

// ID returns the provider's stable registry key.
func (p *Provider) ID() string { return localFetchProviderID }

// Available reports that the provider can run. There are no credentials or
// local services to check: an anonymous public fetcher is always usable.
func (p *Provider) Available() bool { return true }

// Fetch retrieves one URL.
//
// A non-2xx response is returned as a result, not an error. A failure to
// retrieve or represent the resource is a *web.Error whose code distinguishes
// an invalid URL, a blocked address or redirect, a timeout, an oversized
// response, and an unsupported representation.
func (p *Provider) Fetch(ctx context.Context, req web.FetchRequest) (web.FetchResult, error) {
	if err := ctx.Err(); err != nil {
		return web.FetchResult{}, newErrorCause(codeAborted, err, "web fetch aborted")
	}

	// One deadline covers every redirect hop and the body read, so a slow chain
	// cannot outlast the caller's budget.
	timeoutCtx, cancel := context.WithTimeout(ctx, p.cfg.Timeout)
	defer cancel()

	result, err := p.followAndRead(timeoutCtx, req.URL)
	if err != nil {
		return web.FetchResult{}, translateFetchError(ctx, timeoutCtx, err)
	}
	return result, nil
}

// translateFetchError maps a transport failure to the seam's typed error.
//
// The classification reads the deadline state instead of the error value:
// net/http reports cancellation as its own error chain, and the distinction a
// caller needs is whether this provider's timeout fired, not how the transport
// worded the failure.
func translateFetchError(caller, timeoutCtx context.Context, err error) error {
	var webErr *web.Error
	if errors.As(err, &webErr) {
		return err
	}
	// A caller cancellation wins over this provider's own deadline: an outer
	// deadline firing at the same moment is still the caller's budget ending.
	if caller.Err() != nil {
		return newErrorCause(codeAborted, err, "web fetch aborted")
	}
	if timeoutCtx.Err() == context.DeadlineExceeded {
		return newErrorCause(codeFetchTimeout, err, "web fetch timed out")
	}
	return newErrorCause(codeProviderError, err, "web fetch failed")
}

// followAndRead follows same-origin redirects up to the hop cap, then reads the
// final response.
func (p *Provider) followAndRead(ctx context.Context, rawURL string) (web.FetchResult, error) {
	current, err := validateFetchURL(rawURL)
	if err != nil {
		return web.FetchResult{}, err
	}
	followed := 0
	maxRedirects := *p.cfg.MaxRedirects
	for {
		resp, closeResponse, err := p.requestOnce(ctx, current)
		if err != nil {
			return web.FetchResult{}, err
		}
		if !isRedirectStatus(resp.StatusCode) {
			result, err := p.readBody(ctx, resp, current)
			closeResponse()
			return result, err
		}

		// The hop budget is checked before the Location header is read: an
		// over-limit response is refused wherever it points, so the diagnosis is
		// "too many redirects" rather than "cross-origin".
		if followed >= maxRedirects {
			closeResponse()
			return web.FetchResult{}, newError(codeRedirectBlocked, "exceeded the maximum of %d redirects", maxRedirects)
		}
		locations := resp.Header.Values("Location")
		if len(locations) == 0 {
			// A redirect status without a Location is not a usable resource.
			closeResponse()
			return web.FetchResult{}, newError(codeProviderError, "redirect response (HTTP %d) without a Location header", resp.StatusCode)
		}
		target, err := resolveRedirect(locations[0], current)
		if err != nil {
			closeResponse()
			return web.FetchResult{}, err
		}

		// The target is revalidated with the same rules as a direct request:
		// otherwise a hop could smuggle in credentials, another scheme, or an
		// over-long URL that the initial request would have refused.
		validated, err := validateFetchURL(target.String())
		if err != nil {
			closeResponse()
			return web.FetchResult{}, err
		}
		if !sameOrigin(validated, current) {
			closeResponse()
			return web.FetchResult{}, newError(codeRedirectBlocked,
				"cross-origin redirect to %s is not followed automatically; retry against that URL directly", originOf(validated))
		}
		closeResponse()
		current = validated
		followed++
	}
}

// requestOnce performs one GET without following redirects.
//
// The returned close function must be called exactly once. It releases the
// response body and the per-hop transport, whose connection pool belongs to
// this hop because the pinned address set does.
func (p *Provider) requestOnce(ctx context.Context, u *url.URL) (*http.Response, func(), error) {
	addresses, err := p.resolveAddresses(ctx, u.Hostname())
	if err != nil {
		return nil, nil, err
	}
	transport := &http.Transport{
		// No proxy: a proxy would resolve and connect on this provider's
		// behalf, which moves the dial outside the address check.
		Proxy:       nil,
		DialContext: pinnedDial(addresses),
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		transport.CloseIdleConnections()
		return nil, nil, newErrorCause(codeProviderError, err, "web fetch failed")
	}
	req.Header.Set("User-Agent", p.cfg.UserAgent)
	req.Header.Set("Accept", acceptHeader)
	resp, err := transport.RoundTrip(req)
	if err != nil {
		transport.CloseIdleConnections()
		return nil, nil, err
	}
	return resp, func() {
		resp.Body.Close()
		transport.CloseIdleConnections()
	}, nil
}

// readBody reads, byte-caps, classifies, and decodes the final response body.
func (p *Provider) readBody(ctx context.Context, resp *http.Response, finalURL *url.URL) (web.FetchResult, error) {
	contentType := resp.Header.Get("Content-Type")
	kind := classifyContentType(contentType)
	if kind == "" {
		return web.FetchResult{}, newError(codeUnsupportedContentType,
			"unsupported content type %q", contentTypeOrUnknown(contentType))
	}

	// The decoder is resolved before the body is touched so an unsupported
	// charset fails without consuming the stream; the caller closes it.
	charset := parseCharset(contentType)
	decode, ok := charsetDecoder(charset)
	if !ok {
		return web.FetchResult{}, newError(codeUnsupportedContentType, "unsupported charset %q", charset)
	}

	// A declared length above the cap is refused before reading: the point of
	// the cap is to avoid consuming the bytes at all. A body that grows past
	// the cap without declaring its length is truncated instead, so a server
	// that under-reports still yields a bounded usable body.
	if resp.ContentLength > p.cfg.MaxResponseBytes {
		return web.FetchResult{}, newError(codeFetchTooLarge,
			"response exceeds the maximum of %d bytes", p.cfg.MaxResponseBytes)
	}

	raw, truncatedByBytes, err := readCapped(ctx, resp.Body, p.cfg.MaxResponseBytes)
	if err != nil {
		return web.FetchResult{}, err
	}
	content, truncatedByChars := truncateChars(decode(raw), p.cfg.MaxBodyChars)

	return web.FetchResult{
		URL:        finalURL.String(),
		StatusCode: resp.StatusCode,
		Body:       web.FetchBody{Kind: kind, Content: content},
		Truncated:  truncatedByBytes || truncatedByChars,
	}, nil
}

// readCapped reads at most max bytes.
//
// Only bytes that are dropped count as truncation: a body that exactly fills
// the cap is read to EOF and reported as complete, so a caller can tell
// "exactly at the limit" from "cut".
func readCapped(ctx context.Context, r io.Reader, max int64) ([]byte, bool, error) {
	var out bytes.Buffer
	buf := make([]byte, 32*1024)
	var total int64
	for {
		n, err := r.Read(buf)
		if n > 0 {
			remaining := max - total
			if int64(n) > remaining {
				out.Write(buf[:remaining])
				return out.Bytes(), true, nil
			}
			out.Write(buf[:n])
			total += int64(n)
		}
		if err == io.EOF {
			return out.Bytes(), false, nil
		}
		if err != nil {
			return nil, false, err
		}
		// A body reader that ignores cancellation would otherwise hold the
		// call open past the caller's deadline.
		if err := ctx.Err(); err != nil {
			return nil, false, err
		}
	}
}

// isRedirectStatus reports whether a status carries a Location to follow.
func isRedirectStatus(status int) bool {
	switch status {
	case http.StatusMovedPermanently, http.StatusFound, http.StatusSeeOther,
		http.StatusTemporaryRedirect, http.StatusPermanentRedirect:
		return true
	}
	return false
}

// resolveRedirect resolves a possibly relative Location against the current
// URL.
func resolveRedirect(location string, base *url.URL) (*url.URL, error) {
	reference, err := url.Parse(location)
	if err != nil {
		return nil, newErrorCause(codeProviderError, err, "invalid redirect Location %q", location)
	}
	return base.ResolveReference(reference), nil
}

// newError builds a fetch failure with one of the upstream codes.
func newError(code, format string, args ...any) *web.Error {
	return &web.Error{Code: code, Message: errorPrefix + fmt.Sprintf(format, args...)}
}

// newErrorCause keeps the underlying failure available to errors.Is and
// errors.As while the message states what this provider was doing.
func newErrorCause(code string, cause error, format string, args ...any) *web.Error {
	return &web.Error{Code: code, Message: errorPrefix + fmt.Sprintf(format, args...), Err: cause}
}
