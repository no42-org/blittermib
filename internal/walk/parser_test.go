package walk

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWalkParserHappy(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("testdata", "sample-walk.txt"))
	if err != nil {
		t.Fatalf("read testdata: %v", err)
	}
	w := Parse(string(raw))

	if w.SkippedLines != 0 {
		t.Errorf("clean capture skipped %d lines, want 0", w.SkippedLines)
	}
	if len(w.Entries) != 8 {
		t.Fatalf("got %d entries, want 8", len(w.Entries))
	}

	// Leading dot stripped, type + value split.
	first := w.Entries[0]
	if first.Ident != "1.3.6.1.2.1.1.1.0" {
		t.Errorf("entry 0 ident = %q", first.Ident)
	}
	if first.Type != "STRING" {
		t.Errorf("entry 0 type = %q, want STRING", first.Type)
	}
	if !first.Numeric() {
		t.Errorf("entry 0 should be numeric")
	}
	if first.LineNumber != 1 {
		t.Errorf("entry 0 line = %d, want 1", first.LineNumber)
	}

	// Hex-STRING is colon-joined.
	var hex *Entry
	for i := range w.Entries {
		if w.Entries[i].Type == "Hex-STRING" {
			hex = &w.Entries[i]
		}
	}
	if hex == nil {
		t.Fatal("no Hex-STRING entry parsed")
	}
	if hex.Value != "00:11:22:33:44:55" {
		t.Errorf("hex value = %q, want 00:11:22:33:44:55", hex.Value)
	}

	// A value containing " = " is not split mid-value (Timeticks form
	// has no '=', but check an OID value kept its dotted RHS intact).
	var oidVal *Entry
	for i := range w.Entries {
		if w.Entries[i].Type == "OID" {
			oidVal = &w.Entries[i]
		}
	}
	if oidVal == nil || oidVal.Value != ".1.3.6.1.4.1.2636.1.1.1.2.82" {
		t.Errorf("OID-typed value not preserved: %+v", oidVal)
	}
}

func TestWalkParserRejectsNonOID(t *testing.T) {
	// Lines that contain " = " but whose left side is neither a
	// numeric OID nor a MODULE::symbol identifier must be skipped, not
	// admitted as entries (otherwise pasted prose inflates the decode
	// and hides the skip count).
	capture := `count = 3
key = value: something
1.3..6 = STRING: "malformed empty segment"
1.3.6. = STRING: "trailing dot"
A::B::sysName.0 = STRING: "double colon"
.1.3.6.1.2.1.1.5.0 = STRING: "the one good line"`
	w := Parse(capture)

	if len(w.Entries) != 1 {
		t.Fatalf("got %d entries, want 1 (only the valid OID line): %+v", len(w.Entries), w.Entries)
	}
	if w.Entries[0].Ident != "1.3.6.1.2.1.1.5.0" {
		t.Errorf("surviving entry = %q", w.Entries[0].Ident)
	}
	if w.SkippedLines != 5 {
		t.Errorf("SkippedLines = %d, want 5", w.SkippedLines)
	}
}

// A single line beyond the scanner's 4 MB token cap aborts the scan;
// the parser must surface that as a note instead of returning a
// clean-looking truncated decode.
func TestWalkParserScannerError(t *testing.T) {
	capture := ".1.3.6.1.2.1.1.5.0 = STRING: ok\n" +
		".1.3.6.1.2.1.1.6.0 = STRING: " + strings.Repeat("x", 5<<20) + "\n" +
		".1.3.6.1.2.1.1.7.0 = INTEGER: 4\n"
	w := Parse(capture)

	if len(w.Entries) != 1 {
		t.Fatalf("got %d entries, want 1 (the line before the oversized one)", len(w.Entries))
	}
	found := false
	for _, n := range w.ParserNotes {
		if strings.Contains(n, "not decoded") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected a truncation note in ParserNotes, got %v", w.ParserNotes)
	}
}

func TestWalkParserMultilineHexString(t *testing.T) {
	// Net-SNMP wraps a long Hex-STRING at 16 bytes per line; only the
	// first line carries the ` = Hex-STRING:` prefix. The continuation
	// lines must fold into the entry's value, not be dropped (which
	// would silently truncate it) nor counted as skipped.
	capture := `.1.3.6.1.4.1.2636.3.48.1.2.1.8.1.2.162 = Hex-STRING: 01 00 07 FC 0F E7 2D A1 48 00 07 00 0A 01 00 05
28 56 5A 2A 4D C3 00 06 00 09 02 00 03 30 BF 01
12 10 02 00 04
.1.3.6.1.2.1.1.5.0 = STRING: srx330
this line is garbage and should be skipped`
	w := Parse(capture)

	if len(w.Entries) != 2 {
		t.Fatalf("got %d entries, want 2: %+v", len(w.Entries), w.Entries)
	}
	if w.SkippedLines != 1 {
		t.Errorf("SkippedLines = %d, want 1 (only the garbage line)", w.SkippedLines)
	}

	hex := w.Entries[0]
	want := "01:00:07:FC:0F:E7:2D:A1:48:00:07:00:0A:01:00:05:" +
		"28:56:5A:2A:4D:C3:00:06:00:09:02:00:03:30:BF:01:" +
		"12:10:02:00:04"
	if hex.Value != want {
		t.Errorf("folded hex value = %q,\n want %q", hex.Value, want)
	}
	// The record after the wrapped value still parses as its own entry.
	if w.Entries[1].Ident != "1.3.6.1.2.1.1.5.0" {
		t.Errorf("entry after wrapped hex = %q, want sysName OID", w.Entries[1].Ident)
	}
}

func TestWalkParserCruft(t *testing.T) {
	capture := `# a pasted walk with comments and junk
.1.3.6.1.2.1.1.1.0 = STRING: "ok with = sign in value"

SNMPv2-MIB::sysName.0 = STRING: srx340.lab
this line is garbage and should be skipped
.1.3.6.1.2.1.1.4.0 = No Such Instance currently exists at this OID
.1.3.6.1.2.1.99 = STRING: trailing value = with equals
End of MIB
`
	w := Parse(capture)

	if w.SkippedLines != 1 {
		t.Errorf("SkippedLines = %d, want 1 (the garbage line)", w.SkippedLines)
	}
	if len(w.Entries) != 4 {
		t.Fatalf("got %d entries, want 4: %+v", len(w.Entries), w.Entries)
	}

	// Value with an embedded " = " is preserved whole.
	if w.Entries[0].Value != `"ok with = sign in value"` {
		t.Errorf("embedded-equals value = %q", w.Entries[0].Value)
	}

	// Name-prefixed identifier is captured verbatim and flagged
	// non-numeric for the resolver to normalise.
	named := w.Entries[1]
	if named.Ident != "SNMPv2-MIB::sysName.0" || named.Numeric() {
		t.Errorf("name-prefixed entry = %+v, Numeric=%v", named, named.Numeric())
	}

	// Not-present marker parsed, not skipped.
	np := w.Entries[2]
	if !np.NotPresent || np.Ident != "1.3.6.1.2.1.1.4.0" {
		t.Errorf("not-present entry = %+v", np)
	}

	if len(w.ParserNotes) == 0 {
		t.Error("expected a parser note about skipped lines")
	}
}
