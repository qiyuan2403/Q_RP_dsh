package toolweb

import (
	"html"
	"strings"
)

// htmlOmissionMarker is what the model sees when HTML cannot be converted.
//
// It deliberately omits the source: a body whose structure defeated the
// converter may contain active markup, and passing that through as text could
// still be read as instructions.
const htmlOmissionMarker = "[HTML content omitted: unable to convert safely.]"

// maxHTMLDepth is the nesting ceiling above which a document is left
// unconverted.
//
// Upstream measures the same ceiling before handing the document to turndown,
// because parsing an unclosed-tag bomb is superlinear and runs synchronously.
// This converter is a single linear scan, so the ceiling is no longer a
// performance guard; it stays because a pathological document is a sign the
// body is not article text, and refusing it is cheaper than emitting 200,000
// characters of attribute noise.
const maxHTMLDepth = 512

// voidElements never take a closing tag, so they do not grow the depth.
var voidElements = map[string]bool{
	"area": true, "base": true, "br": true, "col": true, "embed": true,
	"hr": true, "img": true, "input": true, "link": true, "meta": true,
	"param": true, "source": true, "track": true, "wbr": true,
}

// droppedElements contribute nothing: their contents are script, styling, or
// fallback copies that would otherwise be read as body text. Embedding elements
// are dropped for the same reason: their contents are another document.
var droppedElements = map[string]bool{
	"script": true, "style": true, "noscript": true, "template": true,
	"iframe": true, "object": true, "embed": true,
}

// blockElements separate paragraphs, so their boundaries become blank lines.
var blockElements = map[string]bool{
	"address": true, "article": true, "aside": true, "blockquote": true,
	"body": true, "caption": true, "dd": true, "div": true, "dl": true,
	"dt": true, "fieldset": true, "figcaption": true, "figure": true,
	"footer": true, "form": true, "h1": true, "h2": true, "h3": true,
	"h4": true, "h5": true, "h6": true, "head": true, "header": true,
	"hr": true, "html": true, "legend": true, "main": true, "nav": true,
	"ol": true, "p": true, "pre": true, "section": true, "summary": true,
	"table": true, "tbody": true, "tfoot": true, "thead": true,
	"title": true, "tr": true, "ul": true,
}

// htmlToText converts an HTML body to plain text and reports whether the
// conversion ran.
//
// WHY NOT MARKDOWN. The Go harness has no turndown equivalent, and adding a
// converter dependency was out of scope for this port, so this is a simpler
// text conversion: drop non-content elements, break at block boundaries, decode
// entities, and collapse whitespace. It loses headings, link targets, and table
// structure, which the upstream Markdown conversion preserves. That loss is the
// known deviation, and it is bounded by design: nothing here is active markup,
// so a failed or partial conversion cannot execute in the conversation.
//
// The scanner never recurses, so malformed and deeply nested input cannot panic.
// An unclosed tag or comment at the end of a truncated source is treated as the
// end of the document, not as a failure; only crossing maxHTMLDepth reports a
// failure, and the caller emits htmlOmissionMarker for it.
func htmlToText(src string) (string, bool) {
	var text textBuilder
	depth := 0

	for i := 0; i < len(src); {
		if src[i] != '<' {
			next := strings.IndexByte(src[i:], '<')
			if next < 0 {
				next = len(src)
			} else {
				next += i
			}
			text.writeString(html.UnescapeString(src[i:next]))
			i = next
			continue
		}

		rest := src[i:]
		if strings.HasPrefix(rest, "<!--") {
			// A comment is dropped whole. An unterminated one runs to the end of
			// the source, which is what a truncated body looks like.
			end := strings.Index(rest[4:], "-->")
			if end < 0 {
				break
			}
			i += 4 + end + 3
			continue
		}
		if strings.HasPrefix(rest, "<!") || strings.HasPrefix(rest, "<?") {
			end := strings.IndexByte(rest, '>')
			if end < 0 {
				break
			}
			i += end + 1
			continue
		}

		cursor := i + 1
		closing := false
		if cursor < len(src) && src[cursor] == '/' {
			closing = true
			cursor++
		}
		if cursor >= len(src) || !isTagNameStart(src[cursor]) {
			// A `<` that does not start a tag is text ("a < b"), not markup.
			text.writeByte('<')
			i++
			continue
		}
		nameStart := cursor
		for cursor < len(src) && isTagNameByte(src[cursor]) {
			cursor++
		}
		name := strings.ToLower(src[nameStart:cursor])
		tagEnd, ok := findTagEnd(src, cursor)
		if !ok {
			// An unterminated tag is preserved as literal text: dropping the
			// rest of the body would lose content the model could still use.
			text.writeString(html.UnescapeString(src[i:]))
			break
		}
		selfClosing := strings.HasSuffix(strings.TrimSpace(src[cursor:tagEnd]), "/")
		i = tagEnd + 1

		if closing {
			if depth > 0 {
				depth--
			}
			switch {
			case blockElements[name]:
				text.blockBreak()
			case name == "li":
				text.newline()
			}
			continue
		}
		if voidElements[name] {
			if name == "br" || name == "hr" {
				text.newline()
			}
			continue
		}
		if droppedElements[name] {
			if selfClosing {
				continue
			}
			// Skip the element's body without interpreting it as markup.
			after, found := findRawTextEnd(src, name, i)
			if !found {
				break
			}
			i = after
			continue
		}
		if !selfClosing {
			depth++
			if depth > maxHTMLDepth {
				return "", false
			}
		}
		switch {
		case blockElements[name]:
			text.blockBreak()
		case name == "li":
			text.newline()
			text.writeString("- ")
		case name == "td" || name == "th":
			text.space()
		}
	}
	return normalizeText(text.String()), true
}

// isTagNameStart reports whether c can begin an element name.
func isTagNameStart(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z'
}

// isTagNameByte reports whether c continues an element name.
func isTagNameByte(c byte) bool {
	return isTagNameStart(c) || c >= '0' && c <= '9' || c == '-' || c == ':'
}

// findTagEnd returns the index of the `>` that closes a tag, skipping quoted
// attribute values so `<a title="a > b">` does not end at the quoted `>`.
func findTagEnd(src string, from int) (int, bool) {
	var quote byte
	for i := from; i < len(src); i++ {
		c := src[i]
		if quote != 0 {
			if c == quote {
				quote = 0
			}
			continue
		}
		switch c {
		case '"', '\'':
			quote = c
		case '>':
			return i, true
		}
	}
	return 0, false
}

// findRawTextEnd returns the index just past the closing tag of a raw-text
// element, or false when the document has no such closing tag.
//
// The search is case-insensitive and requires a tag boundary after the name, so
// text such as `</scripted` is not mistaken for the end of a script element.
func findRawTextEnd(src, name string, from int) (int, bool) {
	prefix := "</" + name
	for from < len(src) {
		offset := strings.IndexByte(src[from:], '<')
		if offset < 0 {
			return 0, false
		}
		start := from + offset
		if len(src)-start >= len(prefix) && strings.EqualFold(src[start:start+len(prefix)], prefix) {
			after := start + len(prefix)
			if after >= len(src) || isTagBoundary(src[after]) {
				end, ok := findTagEnd(src, after)
				if !ok {
					return 0, false
				}
				return end + 1, true
			}
		}
		from = start + 1
	}
	return 0, false
}

// isTagBoundary reports whether c may follow a raw-text element's end-tag name.
func isTagBoundary(c byte) bool {
	return c == '>' || c == '/' || c == ' ' || c == '\t' || c == '\n' || c == '\r' || c == '\f'
}

// textBuilder writes converted text while collapsing repeated boundary breaks.
//
// Block boundaries arrive in pairs (one closing tag, then one opening tag), so
// writing a break for each without looking at the current tail produces blank
// lines between every pair. Tracking the last two bytes keeps a paragraph
// boundary a paragraph boundary and nothing more.
type textBuilder struct {
	body strings.Builder
	prev byte
	last byte
}

// writeString appends already-decoded text.
func (b *textBuilder) writeString(s string) {
	if s == "" {
		return
	}
	b.body.WriteString(s)
	if len(s) >= 2 {
		b.prev = s[len(s)-2]
	} else {
		b.prev = b.last
	}
	b.last = s[len(s)-1]
}

// writeByte appends one ASCII byte.
func (b *textBuilder) writeByte(c byte) {
	b.body.WriteByte(c)
	b.prev = b.last
	b.last = c
}

// newline ends the current line.
func (b *textBuilder) newline() {
	if b.last == 0 || b.last == '\n' {
		return
	}
	b.writeByte('\n')
}

// blockBreak ends the current paragraph, ensuring one blank line.
func (b *textBuilder) blockBreak() {
	if b.last == 0 {
		return
	}
	if b.last != '\n' {
		b.writeByte('\n')
	}
	if b.prev != '\n' {
		b.writeByte('\n')
	}
}

// space separates adjacent inline content such as table cells.
func (b *textBuilder) space() {
	if b.last == 0 || b.last == ' ' || b.last == '\n' {
		return
	}
	b.writeByte(' ')
}

// String returns the converted text.
func (b *textBuilder) String() string { return b.body.String() }

// normalizeText trims each line, collapses runs of whitespace inside it, and
// reduces blank-line runs to one.
//
// Collapsing happens line by line because a newline is the only structure this
// converter produces; folding everything into one paragraph would make a long
// page unreadable in the conversation.
func normalizeText(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	wroteLine := false
	pendingBlank := false
	for _, line := range strings.Split(s, "\n") {
		line = strings.Join(strings.Fields(line), " ")
		if line == "" {
			if wroteLine {
				pendingBlank = true
			}
			continue
		}
		if wroteLine {
			if pendingBlank {
				b.WriteString("\n\n")
			} else {
				b.WriteByte('\n')
			}
		}
		b.WriteString(line)
		wroteLine = true
		pendingBlank = false
	}
	return b.String()
}
