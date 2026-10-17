package toolweb

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"

	"github.com/qiyuan2403/Q_RP_dsh/tools"
	"github.com/qiyuan2403/Q_RP_dsh/web"
)

// Source is one search result the model may cite. An absent optional field is
// omitted from the serialized output, matching the upstream schema.
type Source struct {
	URL         string `json:"url"`
	Title       string `json:"title,omitempty"`
	Snippet     string `json:"snippet,omitempty"`
	PublishedAt string `json:"publishedAt,omitempty"`
}

// SearchOutput is the structured value `web_search` returns.
//
// `content` is present only when a provider produced an answer; its absence is
// how the model tells "no answer, sources only" from "empty answer".
type SearchOutput struct {
	Content   string   `json:"content,omitempty"`
	Sources   []Source `json:"sources"`
	Truncated bool     `json:"truncated"`
}

// searchParameters builds the `web_search` argument schema. The query bound is
// in the description because the model reads it; the schema cannot express it.
func searchParameters(maxQueries int) string {
	return fmt.Sprintf(searchParametersTemplate,
		fmt.Sprintf("1\u2013%d search queries; their results are merged.", maxQueries))
}

// searchParametersTemplate is the schema document with the query description
// left to the caller, so the model-visible bound and the enforced bound cannot
// disagree.
const searchParametersTemplate = `{
	"type": "object",
	"properties": {
		"queries": {
			"type": "array",
			"items": {"type": "string"},
			"description": %q
		}
	},
	"required": ["queries"],
	"additionalProperties": false
}`

// searchTool builds the `web_search` tool.
func searchTool(w *web.Web, cfg Options) tools.Tool {
	return tools.Tool{
		Name:        SearchToolName,
		Description: "Search the web for current information. Returns an optional summary answer and a list of source URLs.",
		Parameters:  json.RawMessage(searchParameters(cfg.SearchMaxQueries)),
		Run: func(ctx context.Context, raw json.RawMessage) (any, error) {
			var in struct {
				Queries []string `json:"queries"`
			}
			if err := json.Unmarshal(raw, &in); err != nil {
				return nil, fmt.Errorf("toolweb: web_search arguments: %w", err)
			}
			queries, err := parseSearchQueries(in.Queries, cfg.SearchMaxQueries)
			if err != nil {
				return nil, err
			}
			// One budget covers every query in the call, as upstream does: the
			// model asked for one answer, not one budget per query.
			ctx, cancel := context.WithTimeout(ctx, cfg.SearchTimeout)
			defer cancel()
			results := make([]web.SearchResult, 0, len(queries))
			for _, query := range queries {
				result, err := w.Search(ctx, web.SearchRequest{Query: query, MaxResults: cfg.SearchMaxResults})
				if err != nil {
					return nil, fmt.Errorf("toolweb: web_search %q: %w", query, err)
				}
				results = append(results, result)
			}
			// One query is passed through, not merged: upstream adds the
			// `### query` heading only when answers from several queries have to
			// be told apart, and a single-query call has nothing to distinguish.
			if len(queries) == 1 {
				return singleQueryOutput(results[0]), nil
			}
			return mergeSearchResults(queries, results, cfg.SearchMaxResults), nil
		},
		Render: func(value any) string {
			out, ok := decodeOutput[SearchOutput](value)
			if !ok {
				return fmt.Sprint(value)
			}
			return FormatSearchOutput(out)
		},
	}
}

// parseSearchQueries validates the model's query list.
//
// The count is checked before duplicates are collapsed, so a call that names the
// same query five times is still refused: the bound exists to limit work, and
// collapsing first would hide how much the model asked for. Blank queries are
// refused because a provider can only answer nothing useful to them.
func parseSearchQueries(queries []string, maxQueries int) ([]string, error) {
	if len(queries) == 0 {
		return nil, errors.New("toolweb: queries must contain at least one query")
	}
	if len(queries) > maxQueries {
		noun := "queries"
		if maxQueries == 1 {
			noun = "query"
		}
		return nil, fmt.Errorf("toolweb: queries must contain at most %d %s", maxQueries, noun)
	}
	seen := make(map[string]bool, len(queries))
	out := make([]string, 0, len(queries))
	for _, query := range queries {
		if strings.TrimSpace(query) == "" {
			return nil, errors.New("toolweb: each query must be a non-empty string")
		}
		if seen[query] {
			continue
		}
		seen[query] = true
		out = append(out, query)
	}
	return out, nil
}

// singleQueryOutput maps one provider result onto the tool's output.
//
// The seam already capped the sources to the requested bound and set Truncated,
// so this is a projection, not a second policy step: re-capping here would make
// the tool's flag disagree with the seam's decision.
func singleQueryOutput(result web.SearchResult) SearchOutput {
	sources := make([]Source, 0, len(result.Sources))
	for _, source := range result.Sources {
		sources = append(sources, projectSource(source))
	}
	return SearchOutput{
		Content:   result.Content,
		Sources:   sources,
		Truncated: result.Truncated,
	}
}

// projectSource copies the seam's source into the tool's output shape.
func projectSource(source web.SearchSource) Source {
	return Source{
		URL:         source.URL,
		Title:       source.Title,
		Snippet:     source.Snippet,
		PublishedAt: source.PublishedAt,
	}
}

// mergeSearchResults combines per-query results into one answer.
//
// Sources are taken round by round by rank rather than query by query: the first
// source of every query outranks the second source of any one query, which keeps
// a single verbose provider from filling the whole list. URLs are deduplicated
// because two queries commonly find the same page, and the list stops at
// maxResults with `truncated` set so the model knows there were more.
func mergeSearchResults(queries []string, results []web.SearchResult, maxResults int) SearchOutput {
	if len(results) == 0 {
		return SearchOutput{Sources: []Source{}}
	}
	seen := make(map[string]bool)
	sources := make([]Source, 0, maxResults)
	maxRank := 0
	for _, result := range results {
		if len(result.Sources) > maxRank {
			maxRank = len(result.Sources)
		}
	}
	dropped := false
merge:
	for rank := 0; rank < maxRank; rank++ {
		for _, result := range results {
			if rank >= len(result.Sources) {
				continue
			}
			source := result.Sources[rank]
			if seen[source.URL] {
				continue
			}
			seen[source.URL] = true
			if len(sources) == maxResults {
				dropped = true
				break merge
			}
			sources = append(sources, projectSource(source))
		}
	}

	contents := make([]string, 0, len(results))
	for i, result := range results {
		if result.Content == "" {
			continue
		}
		contents = append(contents, "### "+queries[i]+"\n\n"+result.Content)
	}
	truncated := dropped
	for _, result := range results {
		if result.Truncated {
			truncated = true
		}
	}
	return SearchOutput{
		Content:   strings.Join(contents, "\n\n"),
		Sources:   sources,
		Truncated: truncated,
	}
}

// FormatSearchOutput renders a search result as model-facing text.
//
// The answer, when present, comes before the source list because it is the
// provider's synthesis; the sources follow so the model can cite them and check
// the claim. Paragraphs are separated by a blank line so the trust notice is
// never read as part of the answer.
func FormatSearchOutput(out SearchOutput) string {
	parts := []string{externalWebContentNotice}
	if out.Content != "" {
		parts = append(parts, out.Content)
	}
	if len(out.Sources) > 0 {
		lines := make([]string, 0, len(out.Sources))
		for _, source := range out.Sources {
			label := sourceLabel(source.URL, source.Title)
			var meta []string
			if source.Snippet != "" {
				meta = append(meta, source.Snippet)
			}
			if source.PublishedAt != "" {
				meta = append(meta, "("+source.PublishedAt+")")
			}
			suffix := ""
			if len(meta) > 0 {
				suffix = " \u2014 " + strings.Join(meta, " ")
			}
			lines = append(lines, "- ["+label+"]("+source.URL+")"+suffix)
		}
		parts = append(parts, "Sources:\n"+strings.Join(lines, "\n"))
	} else if out.Content == "" {
		parts = append(parts, "No results found.")
	}
	if out.Truncated {
		parts = append(parts, fmt.Sprintf("(Showing the first %d sources. Refine the query for more.)", len(out.Sources)))
	}
	parts = append(parts, "Cite the relevant URLs above as markdown links in your answer.")
	return strings.Join(parts, "\n\n")
}

// sourceLabel is the display name of a source: its title, else its hostname.
//
// A hostname needs an absolute URL, which is what the model would recognize. A
// relative or malformed URL keeps its raw text rather than throwing out of
// formatting: presentation must not turn a provider mistake into a failed call.
func sourceLabel(rawURL, title string) string {
	if title != "" {
		return title
	}
	if parsed, err := url.Parse(rawURL); err == nil && parsed.Hostname() != "" {
		return parsed.Hostname()
	}
	return rawURL
}
