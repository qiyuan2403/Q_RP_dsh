package toolweb

import (
	"encoding/json"
	"testing"
)

// logRoundTrip is the shape a session-log reader hands to Render: the value is
// serialized by the loop and decoded by the reader, so objects become
// map[string]any and numbers become float64.
func logRoundTrip(t *testing.T, value any) any {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var decoded any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	return decoded
}

// TestFetchRenderMatchesTheLoggedShape pins the replay invariant: the live loop
// renders the FetchOutput that Run returned, while a resumed session renders the
// same document decoded from the log. Both must produce identical text, or a
// resumed conversation shows the model something the live turn never did.
func TestFetchRenderMatchesTheLoggedShape(t *testing.T) {
	tool := fetchTool(nil, Options{})
	out := FetchOutput{
		URL:        "https://example.com/a",
		StatusCode: 200,
		Body:       FetchBody{Kind: "html", Content: "<p>hello <b>world</b></p>"},
		Truncated:  false,
	}
	live := tool.Render(out)
	logged := tool.Render(logRoundTrip(t, out))
	if live != logged {
		t.Fatalf("live render != logged render:\nlive: %q\nlogged: %q", live, logged)
	}
	if got := tool.Render(logRoundTrip(t, out)); got == "" {
		t.Fatal("the logged shape rendered to empty text")
	}
}

// TestSearchRenderMatchesTheLoggedShape is the same invariant for web_search,
// including optional fields omitted by their JSON tags.
func TestSearchRenderMatchesTheLoggedShape(t *testing.T) {
	tool := searchTool(nil, Options{})
	out := SearchOutput{
		Content: "answer",
		Sources: []Source{
			{URL: "https://example.com/a", Title: "A", Snippet: "snippet"},
			{URL: "https://example.com/b"},
		},
		Truncated: true,
	}
	live := tool.Render(out)
	logged := tool.Render(logRoundTrip(t, out))
	if live != logged {
		t.Fatalf("live render != logged render:\nlive: %q\nlogged: %q", live, logged)
	}
}

// TestRenderFallsBackForUnrepresentableValues pins that a value that is neither
// the output type nor its JSON shape degrades to readable text instead of
// panicking inside presentation.
func TestRenderFallsBackForUnrepresentableValues(t *testing.T) {
	fetch := fetchTool(nil, Options{})
	if got := fetch.Render(make(chan int)); got == "" {
		t.Fatal("an unrepresentable fetch value rendered to empty text")
	}
	search := searchTool(nil, Options{})
	if got := search.Render(func() {}); got == "" {
		t.Fatal("an unrepresentable search value rendered to empty text")
	}
}
