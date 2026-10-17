// Package deepseeksearch implements the DeepSeek search provider.
//
// One search is one Anthropic-compatible Messages request carrying the native
// `web_search_20250305` server tool. The response is read for structured
// `web_search_tool_result` blocks; prose alone is a failure rather than
// something to scrape, because a model turn without result blocks means the
// server tool never ran and the caller gets no citations to show.
//
// The wire format is provider-private and does not go through llm: the request
// is a fixed server-tool probe, and its answer is a source list rather than a
// model reply.
package deepseeksearch

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/qiyuan2403/Q_RP_dsh/web"
)

// ProviderID is the id this provider registers under.
//
// It is the upstream constant, so a configuration written for the upstream
// package selects this provider without translation.
const ProviderID = "deepseek-official"

// Defaults used when the matching Config field is empty.
const (
	// DefaultBaseURL is the Anthropic-compatible endpoint base; `/messages` is
	// appended.
	DefaultBaseURL = "https://api.deepseek.com/anthropic/v1"
	// DefaultModel is the Anthropic-format model name the upstream provider
	// sends.
	DefaultModel = "deepseek-v4-flash"
	// DefaultAPIVersion is the `anthropic-version` header value.
	DefaultAPIVersion = "2023-06-01"
	// DefaultMaxTokens bounds generated tokens for the Messages request.
	DefaultMaxTokens = 4096
	// DefaultMaxUses bounds `web_search` server-tool uses per request.
	DefaultMaxUses = 5
	// DefaultAPIKeyEnv names the environment variable read when Config.APIKey
	// is empty. It is the variable the upstream provider reads.
	DefaultAPIKeyEnv = "DEEPSEEK_API_KEY"
	// DefaultTimeout bounds one search. The upstream provider relies on the
	// caller's abort signal alone; a Go context without a deadline would let a
	// stalled endpoint hold the call open indefinitely, and a search is a model
	// turn plus up to MaxUses server-tool calls, so the bound is generous.
	DefaultTimeout = 2 * time.Minute
)

// Codes reported through *web.Error.
//
// They are the upstream code strings, so a consumer switching on the code keeps
// working. The web seam's own codes are separate; a provider may report its
// own (Code is an open string).
const (
	// CodeProviderError reports an HTTP failure, an unusable response body, a
	// server-tool error, or the provider's own timeout.
	CodeProviderError = "WEB_PROVIDER_ERROR"
	// CodeCredentialMissing reports that no API key is configured.
	CodeCredentialMissing = "WEB_PROVIDER_CREDENTIAL_MISSING"
	// CodeAborted reports caller cancellation.
	CodeAborted = "WEB_ABORTED"
)

// Wire vocabulary. These are the protocol, not tunables: changing one makes the
// provider speak a different API.
const (
	// searchBaseURLEnv overrides the search endpoint independently of any chat
	// endpoint, matching the upstream `DEEPSEEK_SEARCH_BASE_URL`.
	searchBaseURLEnv = "DEEPSEEK_SEARCH_BASE_URL"
	// searchToolType is the versioned Anthropic server-tool type.
	searchToolType = "web_search_20250305"
	// searchToolName is the tool name the server expects.
	searchToolName = "web_search"
	// promptPrefix frames the query for the model turn that triggers the tool.
	promptPrefix = "Perform a web search for the query: "
	// userAgent attributes requests to the harness, as upstream does. It is a
	// protocol header, not a deployment choice.
	userAgent = "deepseek-harness/0.0.1"
)

// endpointGuidance is appended to failures that happen after dispatch begins,
// so a wrong or unsupported endpoint is diagnosable from the error alone.
const endpointGuidance = "Search endpoint configuration is separate from chat; set Config.BaseURL or the " +
	"DEEPSEEK_SEARCH_BASE_URL environment variable to a trusted Anthropic-compatible Messages API base."

// Config describes one DeepSeek search endpoint.
//
// Every field is optional; New fills the upstream defaults. Values are
// snapshotted at construction because the web seam calls Available and Search
// concurrently: a provider that re-read its key and endpoint on every call
// could send a key resolved from one configuration to an endpoint named by
// another.
type Config struct {
	// APIKey is a literal DeepSeek API key. It wins over the environment
	// variable named by APIKeyEnv.
	APIKey string
	// APIKeyEnv names the environment variable read when APIKey is empty.
	// Empty uses DEEPSEEK_API_KEY, the variable the upstream provider reads.
	APIKeyEnv string
	// BaseURL is the Anthropic-compatible endpoint base; `/messages` is
	// appended. Empty reads DEEPSEEK_SEARCH_BASE_URL, then DefaultBaseURL.
	// Search uses this endpoint even when chat talks to a different origin,
	// because upstream configures the two independently.
	BaseURL string
	// Model is the Anthropic-format model name; empty uses DefaultModel.
	Model string
	// APIVersion is the `anthropic-version` header value; empty uses
	// DefaultAPIVersion.
	APIVersion string
	// MaxTokens bounds generated tokens; 0 uses DefaultMaxTokens. A negative
	// value is refused by New, because the Messages API rejects a non-positive
	// limit and 0 already means "unset" here.
	MaxTokens int
	// MaxUses bounds `web_search` server-tool uses per request; 0 uses
	// DefaultMaxUses. A negative value is refused by New.
	MaxUses int
	// Timeout bounds one search, model turn included; 0 uses DefaultTimeout.
	// A negative value is refused by New.
	Timeout time.Duration
	// HTTPClient overrides the transport. A nil client gets a fresh one that
	// refuses redirects: the request carries a credential, and following a
	// redirect would forward that credential to another origin. An injected
	// client keeps an explicitly configured redirect policy, and gets the
	// refusing policy when it sets none.
	HTTPClient *http.Client
}

// Provider is the DeepSeek search backend.
//
// All fields are set by New and never written afterwards, so concurrent
// searches share the provider without locking: a search writes only its own
// request and response values.
type Provider struct {
	apiKey     string
	apiKeyEnv  string
	endpoint   string
	model      string
	apiVersion string
	maxTokens  int
	maxUses    int
	timeout    time.Duration
	client     *http.Client

	// validEndpoint is computed once from the configured base URL so
	// Available stays a field check, as the seam requires.
	validEndpoint bool
}

var _ web.SearchProvider = (*Provider)(nil)

// New builds a provider from cfg.
//
// Only a value that can neither be defaulted nor sent is an error: a negative
// limit or timeout. An unusable endpoint is not an error here, matching the
// upstream provider where a bad endpoint makes the provider unavailable rather
// than breaking construction, and an unavailable provider is what the seam
// reports to the user.
func New(cfg Config) (*Provider, error) {
	if cfg.MaxTokens < 0 {
		return nil, fmt.Errorf("deepseeksearch: max tokens %d must not be negative", cfg.MaxTokens)
	}
	if cfg.MaxUses < 0 {
		return nil, fmt.Errorf("deepseeksearch: max uses %d must not be negative", cfg.MaxUses)
	}
	if cfg.Timeout < 0 {
		return nil, fmt.Errorf("deepseeksearch: timeout %s must not be negative", cfg.Timeout)
	}

	apiKeyEnv := firstNonEmpty(cfg.APIKeyEnv, DefaultAPIKeyEnv)
	baseURL := firstNonEmpty(cfg.BaseURL, os.Getenv(searchBaseURLEnv), DefaultBaseURL)

	p := &Provider{
		apiKey:        firstNonEmpty(cfg.APIKey, os.Getenv(apiKeyEnv)),
		apiKeyEnv:     apiKeyEnv,
		endpoint:      strings.TrimRight(baseURL, "/") + "/messages",
		model:         firstNonEmpty(cfg.Model, DefaultModel),
		apiVersion:    firstNonEmpty(cfg.APIVersion, DefaultAPIVersion),
		maxTokens:     cfg.MaxTokens,
		maxUses:       cfg.MaxUses,
		timeout:       cfg.Timeout,
		client:        redirectSafeClient(cfg.HTTPClient),
		validEndpoint: validBaseURL(baseURL),
	}
	if p.maxTokens == 0 {
		p.maxTokens = DefaultMaxTokens
	}
	if p.maxUses == 0 {
		p.maxUses = DefaultMaxUses
	}
	if p.timeout == 0 {
		p.timeout = DefaultTimeout
	}
	return p, nil
}

// firstNonEmpty returns the first non-empty value, or "" when every value is
// empty.
func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

// validBaseURL reports whether raw names an absolute http(s) endpoint.
//
// The check is stricter than the upstream URL parse because http.Client speaks
// only http(s); a base URL it cannot dispatch to is misconfigured, not merely
// unusual.
func validBaseURL(raw string) bool {
	parsed, err := url.Parse(raw)
	if err != nil {
		return false
	}
	return (parsed.Scheme == "http" || parsed.Scheme == "https") && parsed.Host != ""
}

// redirectSafeClient returns a client that refuses redirects.
//
// The credential in the request must reach only the configured endpoint. An
// injected client's own CheckRedirect is left alone because a caller may have
// installed a stricter policy already; the copy keeps this provider from
// mutating a client it does not own.
func redirectSafeClient(client *http.Client) *http.Client {
	if client == nil {
		client = &http.Client{}
	}
	if client.CheckRedirect != nil {
		return client
	}
	clone := *client
	clone.CheckRedirect = func(*http.Request, []*http.Request) error {
		return errors.New("deepseeksearch: redirects are not followed")
	}
	return &clone
}

// ID returns ProviderID.
func (p *Provider) ID() string { return ProviderID }

// Available reports whether a search can be attempted from local state alone.
//
// It mirrors the upstream check: a key is configured, the endpoint parses as an
// absolute http(s) URL, and the request limits are positive. It performs no
// network I/O because the web seam calls it on every provider selection,
// including auto-selection.
func (p *Provider) Available() bool {
	return p.apiKey != "" && p.validEndpoint && p.maxTokens > 0 && p.maxUses > 0
}

// Search runs one query through the Messages API and maps the response.
//
// MaxResults is deliberately not sent or enforced here. Upstream leaves the
// bound to the web service, which truncates the result and owns the truncated
// flag; applying it here as well would make the provider report a drop it did
// not decide, and the seam caps regardless. The returned Truncated is therefore
// always false.
//
// Cancelling ctx is reported as CodeAborted. The provider's own Timeout is
// reported as CodeProviderError, because a stalled endpoint is a provider
// failure rather than caller cancellation; keeping the two apart is what lets a
// consumer tell "the user stopped this" from "the backend is unhealthy".
func (p *Provider) Search(ctx context.Context, req web.SearchRequest) (web.SearchResult, error) {
	if err := ctx.Err(); err != nil {
		return web.SearchResult{}, abortedError(err)
	}
	if p.apiKey == "" {
		return web.SearchResult{}, &web.Error{
			Code: CodeCredentialMissing,
			Message: fmt.Sprintf(
				"DeepSeek search has no API key for %q; set Config.APIKey or export that environment variable",
				p.apiKeyEnv,
			),
		}
	}

	callCtx := ctx
	if p.timeout > 0 {
		var cancel context.CancelFunc
		callCtx, cancel = context.WithTimeout(ctx, p.timeout)
		defer cancel()
	}

	payload, err := json.Marshal(wireRequest{
		Model:     p.model,
		MaxTokens: p.maxTokens,
		Messages: []wireMessage{{
			Role:    "user",
			Content: []wireContent{{Type: "text", Text: promptPrefix + req.Query}},
		}},
		Tools: []wireTool{{Type: searchToolType, Name: searchToolName, MaxUses: p.maxUses}},
	})
	if err != nil {
		return web.SearchResult{}, endpointError(p.endpoint, "DeepSeek search request could not be encoded: "+err.Error(), err)
	}

	httpReq, err := http.NewRequestWithContext(callCtx, http.MethodPost, p.endpoint, bytes.NewReader(payload))
	if err != nil {
		return web.SearchResult{}, endpointError(p.endpoint, "DeepSeek search request failed: "+err.Error(), err)
	}
	// Official DeepSeek expects `x-api-key`, while an Anthropic-compatible
	// proxy may expect `Authorization: Bearer`; sending both lets either
	// deployment authenticate, as upstream does.
	httpReq.Header.Set("x-api-key", p.apiKey)
	httpReq.Header.Set("authorization", "Bearer "+p.apiKey)
	httpReq.Header.Set("anthropic-version", p.apiVersion)
	httpReq.Header.Set("content-type", "application/json")
	httpReq.Header.Set("accept", "application/json")
	httpReq.Header.Set("user-agent", userAgent)

	response, err := p.client.Do(httpReq)
	if err != nil {
		return web.SearchResult{}, p.transportError(ctx, callCtx, err)
	}
	defer response.Body.Close()

	body, err := io.ReadAll(response.Body)
	if err != nil {
		return web.SearchResult{}, p.transportError(ctx, callCtx, err)
	}

	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		message := fmt.Sprintf("DeepSeek API error (HTTP %d)", response.StatusCode)
		if detail := errorDetail(body); detail != "" {
			message += ": " + detail
		}
		if response.StatusCode == http.StatusUnauthorized || response.StatusCode == http.StatusForbidden {
			return web.SearchResult{}, credentialRejectedError(message)
		}
		return web.SearchResult{}, endpointError(p.endpoint, message, nil)
	}

	var parsed anthropicResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return web.SearchResult{}, endpointError(
			p.endpoint,
			"DeepSeek returned an unprocessable response body: "+err.Error(),
			err,
		)
	}
	result, err := mapResponse(parsed)
	if err != nil {
		message := err.Error()
		var webErr *web.Error
		if errors.As(err, &webErr) {
			message = webErr.Message
		}
		return web.SearchResult{}, endpointError(p.endpoint, message, err)
	}
	return result, nil
}

// transportError classifies a dispatch or body-read failure.
//
// parent is the caller's context and call is the one deadline-bounded by
// Config.Timeout; the order matters because a caller cancellation also cancels
// call, and only the caller's own cancellation is CodeAborted.
func (p *Provider) transportError(parent, call context.Context, err error) *web.Error {
	if parent.Err() != nil {
		return abortedError(parent.Err())
	}
	if call.Err() != nil {
		return &web.Error{
			Code: CodeProviderError,
			Message: fmt.Sprintf("DeepSeek search request timed out after %s", p.timeout) +
				"\n\nThe web search request used endpoint " + strconv.Quote(p.endpoint) + ". " + endpointGuidance,
			Err: err,
		}
	}
	return endpointError(p.endpoint, "DeepSeek search request failed: "+err.Error(), err)
}

// abortedError builds the upstream cancellation error while keeping the
// caller's reason reachable through errors.Is.
func abortedError(cause error) *web.Error {
	return &web.Error{Code: CodeAborted, Message: "DeepSeek search aborted", Err: cause}
}

// credentialRejectedError keeps the provider message and adds the recovery a
// user can act on. Endpoint guidance would be wrong here: the endpoint answered,
// it rejected the credential.
func credentialRejectedError(message string) *web.Error {
	return &web.Error{
		Code: CodeProviderError,
		Message: message + "\n\nDeepSeek rejected the API key used for this web search; " +
			"set Config.APIKey or DEEPSEEK_API_KEY to a valid DeepSeek key.",
	}
}

// endpointError adds endpoint recovery instructions to a failure that happened
// after dispatch began.
func endpointError(endpoint, message string, cause error) *web.Error {
	return &web.Error{
		Code: CodeProviderError,
		Message: message + "\n\nThe web search request used endpoint " + strconv.Quote(endpoint) + ". " +
			endpointGuidance,
		Err: cause,
	}
}

// wireRequest is the exact Messages request body. The field names and the
// `web_search_20250305` tool type are wire vocabulary.
type wireRequest struct {
	Model     string        `json:"model"`
	MaxTokens int           `json:"max_tokens"`
	Messages  []wireMessage `json:"messages"`
	Tools     []wireTool    `json:"tools"`
}

// wireMessage is one Messages turn.
type wireMessage struct {
	Role    string        `json:"role"`
	Content []wireContent `json:"content"`
}

// wireContent is one content block of a turn.
type wireContent struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// wireTool asks the server to run its native web search.
type wireTool struct {
	Type    string `json:"type"`
	Name    string `json:"name"`
	MaxUses int    `json:"max_uses"`
}

// anthropicResponse is the Messages response envelope. Only content blocks are
// read; usage and stop reasons have no meaning for a search.
type anthropicResponse struct {
	Content []contentBlock `json:"content"`
}

// contentBlock covers both consumed block kinds. Content is raw because a
// result block carries either the result array or, when the server tool itself
// fails, one error object.
type contentBlock struct {
	Type      string          `json:"type"`
	Text      string          `json:"text"`
	Citations []citation      `json:"citations"`
	Content   json.RawMessage `json:"content"`
}

// citation is one excerpt attached to prose. It is the snippet source: result
// items carry a URL and title but no excerpt.
type citation struct {
	URL       string `json:"url"`
	CitedText string `json:"cited_text"`
}

// resultItem is one citeable item inside a result block.
type resultItem struct {
	Type    string `json:"type"`
	URL     string `json:"url"`
	Title   string `json:"title"`
	PageAge string `json:"page_age"`
}

// mapResponse maps a Messages response to a normalized search result.
//
// Result items are joined to their citation excerpt by URL and deduplicated by
// URL, first occurrence winning, because one request with MaxUses above one can
// surface the same page across several server-tool calls. Truncated stays false:
// the seam owns MaxResults.
func mapResponse(response anthropicResponse) (web.SearchResult, error) {
	resultBlocks := make([]contentBlock, 0, len(response.Content))
	for _, block := range response.Content {
		if block.Type == "web_search_tool_result" {
			resultBlocks = append(resultBlocks, block)
		}
	}
	if len(resultBlocks) == 0 {
		return web.SearchResult{}, &web.Error{
			Code: CodeProviderError,
			Message: "DeepSeek returned no web_search_tool_result blocks; " +
				"the request may not have triggered native web search",
		}
	}

	snippets := citationSnippets(response.Content)
	seen := make(map[string]struct{})
	sources := make([]web.SearchSource, 0, len(resultBlocks))
	for _, block := range resultBlocks {
		items, err := resultItems(block.Content)
		if err != nil {
			return web.SearchResult{}, err
		}
		for _, item := range items {
			if item.Type != "web_search_result" || item.URL == "" {
				continue
			}
			if _, duplicate := seen[item.URL]; duplicate {
				continue
			}
			seen[item.URL] = struct{}{}
			sources = append(sources, web.SearchSource{
				URL:         item.URL,
				Title:       item.Title,
				Snippet:     snippets[item.URL],
				PublishedAt: item.PageAge,
			})
		}
	}
	return web.SearchResult{Sources: sources, Truncated: false}, nil
}

// resultItems decodes a result block's content.
//
// An absent or null content is an empty block, matching upstream's `?? []`.
// A server-tool failure arrives as one error object instead of an array; it is
// reported with its error code rather than as a JSON decoding failure, because
// `max_uses_exceeded` is the actionable fact and the code stays the same.
func resultItems(raw json.RawMessage) ([]resultItem, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	var items []resultItem
	if err := json.Unmarshal(raw, &items); err == nil {
		return items, nil
	}
	var failure struct {
		ErrorCode string `json:"error_code"`
	}
	if err := json.Unmarshal(raw, &failure); err == nil && failure.ErrorCode != "" {
		return nil, &web.Error{
			Code:    CodeProviderError,
			Message: "DeepSeek web search failed: " + failure.ErrorCode,
		}
	}
	return nil, &web.Error{
		Code:    CodeProviderError,
		Message: "DeepSeek returned an unprocessable web_search_tool_result block: content is not a result array",
	}
}

// citationSnippets builds the url to excerpt map used as snippets.
//
// Only text blocks carry citations. The first excerpt for a URL wins so a
// page cited twice keeps one stable snippet.
func citationSnippets(blocks []contentBlock) map[string]string {
	snippets := make(map[string]string)
	for _, block := range blocks {
		if block.Type != "text" {
			continue
		}
		for _, cite := range block.Citations {
			if cite.URL == "" || cite.CitedText == "" {
				continue
			}
			if _, exists := snippets[cite.URL]; exists {
				continue
			}
			snippets[cite.URL] = cite.CitedText
		}
	}
	return snippets
}

// errorDetail extracts the provider's explanation from an error body.
//
// Error bodies vary: an error object with a message, a bare error string, or a
// top-level message. A body that is not JSON, or that carries no detail, yields
// "", leaving the HTTP status as the whole message; a malformed error body must
// never replace the real status.
func errorDetail(body []byte) string {
	var payload struct {
		Error   json.RawMessage `json:"error"`
		Message string          `json:"message"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return ""
	}
	if len(payload.Error) > 0 {
		var text string
		if err := json.Unmarshal(payload.Error, &text); err == nil && text != "" {
			return text
		}
		var object struct {
			Message string `json:"message"`
		}
		if err := json.Unmarshal(payload.Error, &object); err == nil && object.Message != "" {
			return object.Message
		}
	}
	return payload.Message
}
