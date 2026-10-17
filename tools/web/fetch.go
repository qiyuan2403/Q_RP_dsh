package toolweb

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/qiyuan2403/Q_RP_dsh/tools"
	"github.com/qiyuan2403/Q_RP_dsh/web"
)

// FetchOutput is the structured value `web_fetch` returns.
//
// The field names are the upstream tool-output schema: `url`, `statusCode`,
// `body` (`kind` plus `content`), and `truncated`. A caller that serializes the
// value gets the same document the official harness produces.
type FetchOutput struct {
	URL        string    `json:"url"`
	StatusCode int       `json:"statusCode"`
	Body       FetchBody `json:"body"`
	Truncated  bool      `json:"truncated"`
}

// FetchBody is a decoded response body. Kind is "html" or "text"; only "html"
// is converted before it reaches the model.
type FetchBody struct {
	Kind    string `json:"kind"`
	Content string `json:"content"`
}

// truncationFooter tells the model how to get the rest when content was cut.
const truncationFooter = "\n\n(Content truncated. Fetch a more specific URL or section for the full text.)"

// fetchParameters is the `web_fetch` argument schema.
const fetchParameters = `{
	"type": "object",
	"properties": {
		"url": {"type": "string", "description": "The HTTP(S) URL to fetch."}
	},
	"required": ["url"],
	"additionalProperties": false
}`

// fetchTool builds the `web_fetch` tool.
func fetchTool(w *web.Web, cfg Options) tools.Tool {
	return tools.Tool{
		Name:        FetchToolName,
		Description: "Fetch the content of a specific HTTP(S) URL and return it decoded to text.",
		Parameters:  json.RawMessage(fetchParameters),
		Run: func(ctx context.Context, raw json.RawMessage) (any, error) {
			var in struct {
				URL string `json:"url"`
			}
			if err := json.Unmarshal(raw, &in); err != nil {
				return nil, fmt.Errorf("toolweb: web_fetch arguments: %w", err)
			}
			if strings.TrimSpace(in.URL) == "" {
				return nil, errors.New("toolweb: web_fetch url must be a non-empty string")
			}
			// The timeout is deployment policy, not a model argument, so it is
			// applied here rather than exposed in the schema.
			ctx, cancel := context.WithTimeout(ctx, cfg.FetchTimeout)
			defer cancel()
			result, err := w.Fetch(ctx, web.FetchRequest{URL: in.URL})
			if err != nil {
				return nil, fmt.Errorf("toolweb: web_fetch %q: %w", in.URL, err)
			}
			return FetchOutput{
				URL:        result.URL,
				StatusCode: result.StatusCode,
				Body: FetchBody{
					Kind:    result.Body.Kind,
					Content: result.Body.Content,
				},
				Truncated: result.Truncated,
			}, nil
		},
		Render: func(value any) string {
			out, ok := decodeOutput[FetchOutput](value)
			if !ok {
				// Printing an unrepresentable value keeps the call readable
				// rather than panicking inside presentation.
				return fmt.Sprint(value)
			}
			return FormatFetchOutput(out, cfg.FetchMaxOutputChars)
		},
	}
}

// FormatFetchOutput renders a fetch result as bounded model-facing text.
//
// The header states the URL and status before the body, so the model knows what
// it is reading. The cap applies to the COMPLETE text, header, body, and footer
// included: a budget that excluded the footer would overflow by the footer's
// length. When the cap is smaller than the footer the text is cut hard, because
// there is no room for the notice that explains a truncation.
func FormatFetchOutput(out FetchOutput, maxOutputChars int) string {
	if maxOutputChars <= 0 {
		maxOutputChars = DefaultFetchMaxOutputChars
	}
	header := fmt.Sprintf("Fetched %s (HTTP %d)\n\n%s\n\n", out.URL, out.StatusCode, externalWebContentNotice)
	body, sourceTruncated := renderFetchBody(out.Body, maxOutputChars)
	prefix := header + body
	truncated := out.Truncated || sourceTruncated || len(prefix) > maxOutputChars
	full := prefix
	if truncated {
		full += truncationFooter
	}
	if len(full) <= maxOutputChars {
		return full
	}
	if maxOutputChars < len(truncationFooter) {
		return truncateBytes(full, maxOutputChars)
	}
	return truncateBytes(prefix, maxOutputChars-len(truncationFooter)) + truncationFooter
}

// renderFetchBody converts a body to model-facing text and reports whether the
// source was cut before conversion.
//
// The source cap bounds the synchronous conversion, which cannot be interrupted
// by the call timeout, so it runs before the converter sees the input.
func renderFetchBody(body FetchBody, maxInputChars int) (string, bool) {
	content := truncateBytes(body.Content, maxInputChars)
	sourceTruncated := len(content) != len(body.Content)
	if body.Kind != web.KindHTML {
		// "text" passes through verbatim; an unrecognized kind also passes
		// through, so a provider that adds one later still yields its content.
		return content, sourceTruncated
	}
	text, ok := htmlToText(content)
	if !ok {
		return htmlOmissionMarker, sourceTruncated
	}
	return text, sourceTruncated
}

// truncateBytes cuts s to at most max bytes without splitting a UTF-8 rune.
//
// The limit counts bytes, matching Go's len on the returned string, and stops at
// a rune boundary so a non-ASCII page is never cut mid-character. A cut is still
// a cut: the caller detects it by comparing lengths, not by an exact byte count.
func truncateBytes(s string, max int) string {
	if max <= 0 {
		return ""
	}
	if len(s) <= max {
		return s
	}
	cut := max
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		// s[cut] is a continuation byte: back up to the rune start.
		cut--
	}
	return s[:cut]
}
