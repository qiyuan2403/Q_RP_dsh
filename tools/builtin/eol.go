package builtintools

import (
	"strings"
)

// LineEndings is the EOL style detected before LF normalization, in the
// shape of the official fs-local fsio: edit/diff bases work in canonical LF,
// and write-back restores the file's own style.
type LineEndings string

const (
	LF   LineEndings = "LF"
	CRLF LineEndings = "CRLF"
)

// detectLineEndings classifies the file's dominant EOL style from a sample.
// CRLF wins ties so a mixed file that is mostly CRLF is not silently
// rewritten to LF.
func detectLineEndings(raw string) LineEndings {
	sample := raw
	if len(sample) > 4096 {
		sample = sample[:4096]
	}
	crlf := strings.Count(sample, "\r\n")
	lf := strings.Count(sample, "\n") - crlf
	if crlf > lf {
		return CRLF
	}
	return LF
}

// normalizeLineEndings collapses CRLF to LF: the canonical in-memory form
// every edit and comparison basis uses. A lone \r (not followed by \n) is
// left untouched, so a binary-adjacent or old-Mac file is not mangled.
func normalizeLineEndings(content string) string {
	return strings.ReplaceAll(content, "\r\n", "\n")
}

// restoreLineEndings converts LF-normalized content back to the style
// detected at read time. CRLF re-normalizes first, so an already-CRLF
// sequence is never doubled to \r\r\n.
func restoreLineEndings(content string, style LineEndings) string {
	if style == LF {
		return content
	}
	return strings.ReplaceAll(normalizeLineEndings(content), "\n", "\r\n")
}

// forEdit reads the file's text into the canonical LF form, returning the
// detected style for write-back. The two are the read-side contract every
// edit goes through, which is why it lives here rather than in each tool.
func forEdit(data []byte) (content string, style LineEndings) {
	raw := string(data)
	return normalizeLineEndings(raw), detectLineEndings(raw)
}
