package toolweb

import (
	"strings"
	"testing"
)

// TestHTMLToTextDropsNonContent pins that script, style, comments, and embedded
// documents contribute nothing: their text would otherwise read as body copy.
func TestHTMLToTextDropsNonContent(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"script", "<p>keep</p><script>drop()</script>", "keep"},
		{"style", "<style>.x{color:red}</style><p>keep</p>", "keep"},
		{"comment", "a<!-- hidden -->b", "ab"},
		{"noscript", "<noscript>enable js</noscript>ok", "ok"},
		{"iframe", `<iframe src="x"><p>inner</p></iframe>ok`, "ok"},
		{"template", "<template><p>inner</p></template>ok", "ok"},
		{"doctype", "<!DOCTYPE html><p>ok</p>", "ok"},
		{"processing instruction", "<?xml version=\"1.0\"?><p>ok</p>", "ok"},
		{"self closing script", "<script/>ok", "ok"},
		{"unterminated script close", "a<script>b</script", "a"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := htmlToText(tc.in)
			if !ok {
				t.Fatalf("conversion failed for %q", tc.in)
			}
			if got != tc.want {
				t.Fatalf("output = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestHTMLToTextBlocksAndWhitespace pins the line structure: paragraphs get a
// blank line, list items one line each, and runs of spaces collapse.
func TestHTMLToTextBlocksAndWhitespace(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"paragraphs", "<p>one</p><p>two</p>", "one\n\ntwo"},
		{"divs", "<div>one</div><div>two</div>", "one\n\ntwo"},
		{"headings", "<h1>Title</h1><p>body</p>", "Title\n\nbody"},
		{"line break", "a<br>b", "a\nb"},
		{"list", "<ul><li>a</li><li>b</li></ul>", "- a\n- b"},
		{"cells", "<table><tr><td>a</td><td>b</td></tr></table>", "a b"},
		{"nested blocks", "<div><div><p>x</p></div></div>", "x"},
		{"inline markup", "<p>a <strong>b</strong> c</p>", "a b c"},
		{"collapsed spaces", "<p>  a   b  </p>", "a b"},
		{"blank runs", "<div></div><div></div><p>x</p>", "x"},
		{"uppercase tags", "<P>Hi</P><SCRIPT>x</SCRIPT>", "Hi"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := htmlToText(tc.in)
			if !ok {
				t.Fatalf("conversion failed for %q", tc.in)
			}
			if got != tc.want {
				t.Fatalf("output = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestHTMLToTextDecodesEntities pins entity decoding, including the non-breaking
// space that collapses like any other whitespace.
func TestHTMLToTextDecodesEntities(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"named", "<p>&amp; &lt;tag&gt; &quot;d&quot;</p>", "& <tag> \"d\""},
		{"numeric", "<p>&#39;q&#39; &#65;</p>", "'q' A"},
		{"nbsp", "<p>a&nbsp;b</p>", "a b"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := htmlToText(tc.in)
			if !ok {
				t.Fatalf("conversion failed for %q", tc.in)
			}
			if got != tc.want {
				t.Fatalf("output = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestHTMLToTextKeepsLiteralText pins that a `<` which does not begin a tag and a
// `>` inside a quoted attribute stay text.
func TestHTMLToTextKeepsLiteralText(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"stray less than", "a < b and 3<4", "a < b and 3<4"},
		{"quoted tag end", `<a title="a > b">link</a>`, "link"},
		{"unterminated tag tail", `<p>keep</p><div class="x`, "keep\n\n<div class=\"x"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := htmlToText(tc.in)
			if !ok {
				t.Fatalf("conversion failed for %q", tc.in)
			}
			if got != tc.want {
				t.Fatalf("output = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestHTMLToTextTruncatedDocument pins that a source cut in the middle of a
// comment or an element still converts what came before it.
func TestHTMLToTextTruncatedDocument(t *testing.T) {
	got, ok := htmlToText("keep<!-- unterminated")
	if !ok {
		t.Fatal("an unterminated comment failed the conversion")
	}
	if got != "keep" {
		t.Fatalf("output = %q, want %q", got, "keep")
	}
	got, ok = htmlToText("a<!unterminated")
	if !ok {
		t.Fatal("an unterminated declaration failed the conversion")
	}
	if got != "a" {
		t.Fatalf("output = %q, want %q", got, "a")
	}
	got, ok = htmlToText("a<script>unterminated")
	if !ok {
		t.Fatal("an unterminated script failed the conversion")
	}
	if got != "a" {
		t.Fatalf("output = %q, want %q", got, "a")
	}
}

// TestHTMLToTextDepthLimit pins the ceiling: exactly the ceiling converts, one
// level more is refused so the caller can emit the omission marker.
func TestHTMLToTextDepthLimit(t *testing.T) {
	allowed := strings.Repeat("<div>", maxHTMLDepth) + "deep" + strings.Repeat("</div>", maxHTMLDepth)
	got, ok := htmlToText(allowed)
	if !ok {
		t.Fatal("a document at the depth ceiling was refused")
	}
	if got != "deep" {
		t.Fatalf("output = %q, want %q", got, "deep")
	}

	tooDeep := strings.Repeat("<div>", maxHTMLDepth+1)
	if _, ok := htmlToText(tooDeep); ok {
		t.Fatal("a document past the depth ceiling was converted")
	}
}

// TestHTMLToTextMalformedInputDoesNotPanic pins that the scanner is total over
// arbitrary bytes. A panic here would abort the turn, so the property matters
// more than the exact output for these inputs.
func TestHTMLToTextMalformedInputDoesNotPanic(t *testing.T) {
	inputs := []string{
		"",
		"<",
		"<div",
		"<div class=\"unterminated",
		"<!-- unterminated",
		"<script>unterminated",
		"<p>a</p><p>b",
		"<td>x",
		"<a href='>'>x</a>",
		"\x00<\xff>",
		strings.Repeat("<div>", maxHTMLDepth+1) + strings.Repeat("</div>", maxHTMLDepth+1),
	}
	for _, in := range inputs {
		func() {
			defer func() {
				if recovered := recover(); recovered != nil {
					t.Fatalf("htmlToText(%q) panicked: %v", in, recovered)
				}
			}()
			htmlToText(in)
		}()
	}
}

// TestHTMLToTextEmptyDocument pins that an empty body converts to empty text
// rather than the omission marker: nothing went wrong.
func TestHTMLToTextEmptyDocument(t *testing.T) {
	got, ok := htmlToText("")
	if !ok {
		t.Fatal("an empty body was treated as a conversion failure")
	}
	if got != "" {
		t.Fatalf("output = %q, want empty", got)
	}
	if got, ok := htmlToText("<br>"); !ok || got != "" {
		t.Fatalf("output = %q ok=%v, want empty and converted", got, ok)
	}
}
