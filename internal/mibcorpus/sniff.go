package mibcorpus

import (
	"io"
	"os"
	"regexp"
)

// DefinitionsBeginMarker is the lexical anchor every SMIv2 module
// must contain. Used by the loader, mib-migrate, and mib-ingest as
// a fast gate to filter non-MIB files (LICENSE, README, partial
// downloads) without paying the libsmi parse cost.
//
// SMI grammar allows arbitrary whitespace (spaces, tabs, newlines)
// between `DEFINITIONS`, `::=`, and `BEGIN`. The pattern reflects
// that — a real-world IBM/Microsoft MIB header reads
// `APPC-MIB DEFINITIONS        ::= BEGIN` with multiple spaces, and
// some files break the line between tokens entirely.
var DefinitionsBeginMarker = regexp.MustCompile(`DEFINITIONS\s+::=\s+BEGIN`)

// definitionsBeginMaxSpan caps the byte distance a single match can
// cover, used to size the boundary-overlap read. 64 bytes is well
// above any realistic whitespace run between the three tokens (real
// files use 1–10 spaces or a single newline) but still tight enough
// that the overlap doesn't materially inflate the read.
const definitionsBeginMaxSpan = 64

// HasMIBOpener returns true when the first 32 KB of the file
// contains a `DEFINITIONS ::= BEGIN` marker (whitespace-flexible).
// The 32 KB cap comfortably accommodates real-world Cisco/Juniper
// headers (which can run several KB of copyright/IPR boilerplate
// before the opener) while still keeping the per-file cost cheap on
// a multi-thousand-MIB corpus walk.
//
// Reads `sniffBytes + definitionsBeginMaxSpan-1` bytes to defend
// against the marker straddling the byte-N boundary.
//
// Uses `io.ReadFull` for short-read safety. An empty file or any
// short-read EOF flavour is reported as "no marker" without
// surfacing the EOF — those are non-MIBs, not I/O errors.
func HasMIBOpener(path string) (bool, error) {
	f, err := os.Open(path)
	if err != nil {
		return false, err
	}
	defer func() { _ = f.Close() }()
	const sniffBytes = 32 * 1024
	buf := make([]byte, sniffBytes+definitionsBeginMaxSpan-1)
	n, err := io.ReadFull(f, buf)
	if err == io.EOF || err == io.ErrUnexpectedEOF {
		return DefinitionsBeginMarker.Match(buf[:n]), nil
	}
	if err != nil {
		return false, err
	}
	return DefinitionsBeginMarker.Match(buf[:n]), nil
}
