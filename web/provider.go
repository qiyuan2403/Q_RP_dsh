package web

import (
	"context"
)

// SearchRequest is one search to run.
//
// One request carries one query; a consumer that needs several issues several
// requests. Keeping the request this small lets search and fetch share one
// seam without one capability's controls leaking into the other.
type SearchRequest struct {
	// Query is the search text.
	Query string
	// MaxResults bounds the returned sources; a non-positive value means no
	// bound, the Go stand-in for upstream's absent bound (see the package
	// documentation). The seam enforces the bound on the result, so a provider
	// cannot over-run it. A provider whose API takes a result count should pass
	// it through as a cost and latency optimization; the seam caps regardless.
	MaxResults int
}

// SearchSource is one citeable search result.
//
// Only URL is required. The other fields are optional because providers do not
// all return them, and forcing adapters to invent a title or snippet would make
// the seam report data the provider never supplied.
type SearchSource struct {
	// URL is the source's address.
	URL string
	// Title is the source's display title, empty when the provider gave none.
	Title string
	// Snippet is the provider's excerpt, empty when the provider gave none.
	Snippet string
	// PublishedAt is the provider-supplied publication or crawl time as an
	// ISO-8601 string, empty when unknown.
	PublishedAt string
}

// SearchResult is a normalized search outcome.
type SearchResult struct {
	// Content is provider-generated answer or summary text; it may be empty.
	// Some backends return only sources, others return a generated answer.
	Content string
	// Sources are the citeable results. The seam fills this already capped to
	// the request's MaxResults, so consumers do not need to re-check the bound.
	Sources []SearchSource
	// Truncated reports that sources were dropped, either by the provider or by
	// the seam enforcing MaxResults. A provider's true survives a cap.
	Truncated bool
}

// FetchRequest is one URL retrieval.
//
// The request omits timeout, format, and extraction controls on purpose:
// cancellation is the context's job, and presentation belongs to the consumer.
type FetchRequest struct {
	// URL is the resource to retrieve.
	URL string
}

// FetchBody is the decoded body of a fetched resource.
//
// Kind is a closed set: KindHTML or KindText. Consumers switch on it, so adding
// a kind is a coordinated change across the seam and every renderer rather than
// a provider extension point.
type FetchBody struct {
	// Kind classifies Content; one of KindHTML or KindText.
	Kind string
	// Content is the decoded body text.
	Content string
}

// Values for FetchBody.Kind.
const (
	// KindHTML marks an HTML body.
	KindHTML = "html"
	// KindText marks a plain-text body.
	KindText = "text"
)

// FetchResult is a normalized fetch outcome.
//
// A successful retrieval of a non-2xx response is a result, not an error: the
// status code describes the fetched resource. Error is reserved for failing to
// retrieve or represent the resource at all. The seam passes the result through
// untouched, including the body and any provider-applied truncation.
type FetchResult struct {
	// URL is the final URL after redirects, not necessarily the requested one.
	URL string
	// StatusCode is the HTTP status code of the fetched response.
	StatusCode int
	// Body is the decoded response body.
	Body FetchBody
	// Truncated reports that the provider capped the decoded body.
	Truncated bool
}

// SearchProvider is one registered search backend.
//
// ID is the registry key and must be stable and unique within search. A
// provider registered for search and one registered for fetch may share an id;
// the two capabilities have separate registries.
type SearchProvider interface {
	// ID returns the provider's stable registry key.
	ID() string
	// Available performs a cheap local usability check. It runs on every
	// selection, including auto-selection, so it must not make network calls.
	Available() bool
	// Search runs one search and honors ctx for cancellation.
	Search(ctx context.Context, req SearchRequest) (SearchResult, error)
}

// FetchProvider is one registered fetch backend.
type FetchProvider interface {
	// ID returns the provider's stable registry key.
	ID() string
	// Available performs a cheap local usability check. It runs on every
	// selection, including auto-selection, so it must not make network calls.
	Available() bool
	// Fetch retrieves one URL and honors ctx for cancellation.
	Fetch(ctx context.Context, req FetchRequest) (FetchResult, error)
}

// Codes carried by Error.
//
// Code is an open string rather than a closed enum: providers may report their
// own codes, and consumers must tolerate codes they do not recognize. These are
// the codes the seam itself produces.
const (
	// CodeDuplicateProvider reports that a provider id is already registered for
	// that capability kind.
	CodeDuplicateProvider = "WEB_DUPLICATE_PROVIDER"
	// CodeProviderConfiguredMissing reports that a configured provider id is not
	// registered for that capability kind.
	CodeProviderConfiguredMissing = "WEB_PROVIDER_CONFIGURED_MISSING"
	// CodeProviderConfiguredUnavailable reports that a configured provider is
	// registered but its Available check failed.
	CodeProviderConfiguredUnavailable = "WEB_PROVIDER_CONFIGURED_UNAVAILABLE"
	// CodeProviderUnavailable reports that no configured id was given and no
	// registered provider is usable.
	CodeProviderUnavailable = "WEB_PROVIDER_UNAVAILABLE"
	// CodeProviderAmbiguous reports that no configured id was given and more than
	// one registered provider is usable.
	CodeProviderAmbiguous = "WEB_PROVIDER_AMBIGUOUS"
)

// Error is a web failure with a machine-routable code and an optional cause.
//
// Consumers select on Code with errors.As:
//
//	var webErr *web.Error
//	if errors.As(err, &webErr) && webErr.Code == web.CodeProviderAmbiguous { ... }
//
// Error's message always starts with "web:" and includes the code, so a log
// line stays diagnosable without re-deriving the failure. Err chains the
// underlying failure for errors.Is and errors.As.
type Error struct {
	// Code is the machine-routable failure code.
	Code string
	// Message is the human-readable explanation.
	Message string
	// Err is the underlying cause, if any.
	Err error
}

// Error implements error.
func (e *Error) Error() string {
	msg := "web: " + e.Code
	if e.Message != "" {
		msg += ": " + e.Message
	}
	if e.Err != nil {
		msg += ": " + e.Err.Error()
	}
	return msg
}

// Unwrap returns the underlying cause, if any.
func (e *Error) Unwrap() error { return e.Err }
