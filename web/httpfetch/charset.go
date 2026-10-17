package httpfetch

import (
	"bytes"
	"strings"
	"unicode/utf8"

	"golang.org/x/text/encoding"
	"golang.org/x/text/encoding/htmlindex"
	"golang.org/x/text/transform"
)

// charsetDecoder maps a declared charset label to a decoder, and reports false
// when the label is not one the WHATWG encoding index knows.
//
// The index is the table the upstream TextDecoder uses, so the accepted labels
// and the encoding each selects match the upstream provider: GBK, GB18030,
// Big5, Shift_JIS, EUC-JP, UTF-16, and the single-byte families all decode
// instead of being refused. A label present but unknown is still refused rather
// than decoded as UTF-8: returning mojibake as if it were text hides the
// encoding mismatch, while an unsupported content-type failure names it. An
// absent label keeps the TextDecoder default, UTF-8.
func charsetDecoder(charset string) (func([]byte) string, bool) {
	label := charset
	if label == "" {
		label = "utf-8"
	}
	enc, err := htmlindex.Get(label)
	if err != nil || enc == nil {
		return nil, false
	}
	bom := bomFor(enc)
	return func(b []byte) string {
		// TextDecoder drops one leading byte order mark for the UTF-8 and
		// UTF-16 decoders; x/text keeps it as U+FEFF, so the provider removes
		// it to produce the same text. It is not removed for any other
		// encoding, because TextDecoder does not treat those bytes as a BOM.
		if len(bom) > 0 {
			b = bytes.TrimPrefix(b, bom)
		}
		return decodeLossy(enc, b)
	}, true
}

// bomFor returns the leading byte order mark that the TextDecoder equivalent
// removes, or nil for an encoding where every byte is decoded as content.
func bomFor(enc encoding.Encoding) []byte {
	name, err := htmlindex.Name(enc)
	if err != nil {
		return nil
	}
	switch name {
	case "utf-8":
		return []byte{0xef, 0xbb, 0xbf}
	case "utf-16le":
		return []byte{0xff, 0xfe}
	case "utf-16be":
		return []byte{0xfe, 0xff}
	}
	return nil
}

// decodeLossy decodes b into UTF-8 text.
//
// Malformed input becomes U+FFFD, which is what TextDecoder does by default
// and what x/text decoders do on their own; a bad byte must not shorten the
// text or fail the fetch. The error branch only fires for a decoder that
// rejects input outright: the decoded prefix is kept, the rejected byte is
// replaced, and decoding resumes after it.
func decodeLossy(enc encoding.Encoding, b []byte) string {
	var out strings.Builder
	out.Grow(len(b))
	for len(b) > 0 {
		decoded, n, err := transform.Bytes(enc.NewDecoder(), b)
		out.Write(decoded)
		if err == nil {
			break
		}
		out.WriteRune(utf8.RuneError)
		if n >= len(b) {
			break
		}
		b = b[n+1:]
	}
	return out.String()
}
