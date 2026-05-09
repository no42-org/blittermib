package compile

import (
	"errors"
	"strings"
	"unicode/utf8"
)

// xmlNeedsSanitize reports whether err looks like an encoding-class
// XML decode failure that the sanitize-and-retry path can address.
//
// We match against error message substrings rather than concrete error
// types because Go's encoding/xml does not export sentinels for the
// "invalid UTF-8" or "illegal character code" failure modes — both
// arrive as *xml.SyntaxError values whose only encoding-distinguishing
// field is Msg. The match walks the wrap chain so it works whether the
// caller hands us the raw decoder error or one wrapped by
// fmt.Errorf("...: %w", ...).
func xmlNeedsSanitize(err error) bool {
	for e := err; e != nil; e = errors.Unwrap(e) {
		msg := e.Error()
		if strings.Contains(msg, "invalid UTF-8") ||
			strings.Contains(msg, "illegal character code") {
			return true
		}
	}
	return false
}

// sanitizeXMLBytes returns a copy of in with two transformations applied
// in a single linear pass:
//
//  1. Bytes 0x80–0xFF that are NOT part of a valid UTF-8 multi-byte
//     sequence are interpreted as Latin-1 and emitted as the two-byte
//     UTF-8 encoding of the corresponding rune. Valid UTF-8 sequences
//     pass through verbatim — this preserves genuine UTF-8 content in
//     mixed-encoding source where one stray Latin-1 byte coexists with
//     correctly-encoded multi-byte characters.
//  2. ASCII C0 control bytes that are forbidden in XML 1.0 element
//     content (0x00–0x08, 0x0B–0x0C, 0x0E–0x1F) are dropped. Tab
//     (0x09), LF (0x0A), and CR (0x0D) are preserved.
//
// Bytes 0x20–0x7F (printable ASCII plus DEL) pass through verbatim.
//
// The output slice is preallocated with capacity len(in)*2, the upper
// bound for an all-Latin-1 input; this avoids mid-pass growth on the
// recovery path. The function is pure — it does not modify in.
func sanitizeXMLBytes(in []byte) []byte {
	out := make([]byte, 0, len(in)*2)
	for i := 0; i < len(in); {
		b := in[i]
		if b < 0x80 {
			switch {
			case b == 0x09 || b == 0x0A || b == 0x0D:
				out = append(out, b)
			case b < 0x20:
				// XML-forbidden C0 control: drop.
			default:
				out = append(out, b)
			}
			i++
			continue
		}
		// Non-ASCII: try to decode a valid UTF-8 sequence first.
		if r, size := utf8.DecodeRune(in[i:]); r != utf8.RuneError {
			out = append(out, in[i:i+size]...)
			i += size
			continue
		}
		// Invalid UTF-8 byte → interpret as Latin-1, emit two-byte
		// UTF-8 of rune(b) (which lies in U+0080..U+00FF).
		out = append(out, 0xC0|(b>>6), 0x80|(b&0x3F))
		i++
	}
	return out
}
