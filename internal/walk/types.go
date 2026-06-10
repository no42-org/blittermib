// Package walk parses snmpwalk / snmpbulkwalk -On (or name-prefixed)
// output captures and resolves the OIDs against a loaded MIB store.
//
// Walks are treated as ephemeral operational telemetry: nothing here
// touches the database or the disk. The parser turns a capture into a
// flat list of Entry values; the resolver decorates each with the
// owning module, decoded index, and — for OIDs no loaded module
// covers — an IANA PEN-derived vendor hint.
package walk

// Entry is one parsed record of an snmpwalk capture.
//
// Ident holds the left-hand side of the `=` exactly as captured,
// normalised only by stripping a leading dot for numeric OIDs. It is
// either a numeric dotted OID (`-On` form) or a name-prefixed
// identifier (`SNMPv2-MIB::sysDescr.0`); the resolver normalises the
// latter to a numeric OID against the store.
type Entry struct {
	Ident      string // numeric OID or MODULE::symbol.suffix, leading dot stripped
	Type       string // SNMP type token, e.g. "STRING", "Counter32"; "" when absent
	Value      string // value as rendered; Hex-STRING bytes are colon-joined
	Raw        string // the original line, verbatim
	LineNumber int    // 1-based source line
	NotPresent bool   // a "No Such Instance/Object" marker rather than a value
}

// Numeric reports whether the entry's identifier is a bare numeric
// OID (as opposed to a name-prefixed one).
func (e Entry) Numeric() bool {
	for i := 0; i < len(e.Ident); i++ {
		if c := e.Ident[i]; (c < '0' || c > '9') && c != '.' {
			return false
		}
	}
	return e.Ident != ""
}

// Walk is the result of parsing a capture.
type Walk struct {
	Entries      []Entry
	SkippedLines int      // lines that looked like data but didn't parse
	ParserNotes  []string // soft warnings surfaced to the user
}
