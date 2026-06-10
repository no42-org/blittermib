package walk

import (
	"bufio"
	"fmt"
	"strings"
)

// notPresentMarkers are the agent responses that report a gap rather
// than a value. They are parsed (not skipped) so the user sees the
// hole in the walk.
var notPresentMarkers = []string{
	"No Such Instance currently exists at this OID",
	"No Such Object available on this agent at this OID",
	"No more variables left in this MIB View",
}

// Parse reads an snmpwalk / snmpbulkwalk capture in -On numeric or
// name-prefixed form. It tolerates blank lines, column-0 `#`
// comments, and "End of MIB" markers (all ignored), and counts any
// other line it cannot make sense of as a skipped line rather than
// aborting — a pasted walk with a few mangled rows still decodes.
func Parse(text string) Walk {
	var w Walk
	sc := bufio.NewScanner(strings.NewReader(text))
	// snmpwalk values (long DESCRIPTIONs, base64 blobs) can be wide;
	// lift the line cap well above the default 64 KB.
	sc.Buffer(make([]byte, 64*1024), 4*1024*1024)

	line := 0
	for sc.Scan() {
		line++
		raw := sc.Text()
		t := strings.TrimSpace(raw)

		// Ignored cruft: blank lines, comments, end-of-walk markers.
		if t == "" || strings.HasPrefix(t, "#") || strings.HasPrefix(t, "End of MIB") {
			continue
		}

		e, ok := parseLine(t)
		if !ok {
			// A long Hex-STRING wraps its bytes across continuation
			// lines that carry no ` = `; Net-SNMP prints 16 bytes per
			// line. Fold such a line into the preceding Hex-STRING entry
			// rather than dropping it — dropping silently truncates the
			// value to its first line.
			if n := len(w.Entries); n > 0 && isHexContinuation(t) &&
				w.Entries[n-1].Type == "Hex-STRING" {
				prev := &w.Entries[n-1]
				if prev.Value == "" {
					prev.Value = hexColons(t)
				} else {
					prev.Value += ":" + hexColons(t)
				}
				prev.Raw += "\n" + raw
				continue
			}
			w.SkippedLines++
			continue
		}
		e.Raw = raw
		e.LineNumber = line
		w.Entries = append(w.Entries, e)
	}

	// A scanner error (a single line beyond the 4 MB token cap — e.g. a
	// binary paste or a capture with no newlines) aborts the loop early.
	// Surface it: silently returning a clean-looking partial decode
	// would contradict this parser's tolerant-but-honest contract.
	if err := sc.Err(); err != nil {
		w.ParserNotes = append(w.ParserNotes, fmt.Sprintf(
			"reading stopped after line %d (%v) — the rest of the capture was not decoded",
			line, err))
	}

	if w.SkippedLines > 0 {
		w.ParserNotes = append(w.ParserNotes,
			"some lines did not look like snmpwalk records and were skipped")
	}
	return w
}

// parseLine parses a single `<ident> = <type>: <value>` record (or a
// not-present marker). Returns ok=false for anything that doesn't fit
// the OID-equals shape.
func parseLine(t string) (Entry, bool) {
	// Split on the FIRST " = " — the identifier never contains a
	// space, but values routinely do (and may even contain " = ").
	eq := strings.Index(t, " = ")
	if eq < 0 {
		return Entry{}, false
	}
	ident := strings.TrimPrefix(strings.TrimSpace(t[:eq]), ".")
	rhs := strings.TrimSpace(t[eq+3:])
	if !validIdent(ident) {
		// The left side isn't a numeric OID or a MODULE::symbol
		// identifier — a pasted prose line (`count = 3`) or a
		// malformed OID (`1.3..6`). Count it as skipped rather than
		// admitting junk as a decoded entry.
		return Entry{}, false
	}

	// Not-present markers carry no type/value.
	for _, m := range notPresentMarkers {
		if strings.HasPrefix(rhs, m) {
			return Entry{Ident: ident, NotPresent: true}, true
		}
	}

	// `<type>: <value>` — the type token is everything up to the
	// first colon. A value-only RHS (no colon) is kept with an empty
	// type rather than rejected.
	typ, val := "", rhs
	if c := strings.Index(rhs, ":"); c >= 0 {
		typ = strings.TrimSpace(rhs[:c])
		val = strings.TrimSpace(rhs[c+1:])
	}
	if typ == "Hex-STRING" {
		val = hexColons(val)
	}
	return Entry{Ident: ident, Type: typ, Value: val}, true
}

// validIdent reports whether the left-hand side of a record is a
// shape the resolver can act on: a numeric OID (dot-separated,
// non-empty all-digit segments) or a MODULE::symbol[.suffix]
// identifier with a non-empty module and symbol name. Anything else
// is treated as a skipped line.
func validIdent(ident string) bool {
	if i := strings.Index(ident, "::"); i >= 0 {
		module, rest := ident[:i], ident[i+2:]
		if module == "" || rest == "" || strings.Contains(rest, "::") {
			return false
		}
		name, _ := splitNameSuffix(rest)
		return name != ""
	}
	return validNumericOID(ident)
}

// validNumericOID reports whether oid is a dotted sequence of
// non-empty, all-digit segments. Rejects empty segments produced by
// leading/trailing/double dots.
func validNumericOID(oid string) bool {
	if oid == "" {
		return false
	}
	for _, seg := range strings.Split(oid, ".") {
		if seg == "" {
			return false
		}
		for i := 0; i < len(seg); i++ {
			if seg[i] < '0' || seg[i] > '9' {
				return false
			}
		}
	}
	return true
}

// isHexContinuation reports whether a line is the wrapped remainder of
// a Hex-STRING value: a non-empty run of space-separated two-digit hex
// byte tokens and nothing else. Prose junk ("this line is garbage")
// fails this and is still counted as a skipped line.
func isHexContinuation(t string) bool {
	fields := strings.Fields(t)
	if len(fields) == 0 {
		return false
	}
	for _, f := range fields {
		if len(f) != 2 || !isHexDigit(f[0]) || !isHexDigit(f[1]) {
			return false
		}
	}
	return true
}

func isHexDigit(c byte) bool {
	return (c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')
}

// hexColons rewrites a Net-SNMP Hex-STRING body ("00 11 22 33") into
// the colon-separated MAC convention ("00:11:22:33"). Trailing
// whitespace the format sometimes carries is dropped.
func hexColons(v string) string {
	return strings.Join(strings.Fields(v), ":")
}
