package main

import (
	"bufio"
	"fmt"
	"io"
	"regexp"
)

// licensePattern pairs a regex against the canonical tag emitted in
// INDEX.yaml. Order matters in two ways:
//
//   - Vendor patterns run BEFORE rfc-editor: real Cisco/Juniper/etc.
//     MIBs frequently quote IETF boilerplate ("Copyright ... IETF
//     Trust") near the top alongside their own copyright. Putting
//     rfc-editor last ensures vendor attribution wins.
//   - Among vendors, more-specific patterns appear before more-general
//     ones (e.g. "Hewlett-Packard Enterprise" before "Hewlett-Packard")
//     so a multi-tier vendor doesn't get downgraded.
type licensePattern struct {
	tag string
	re  *regexp.Regexp
}

// licensePatterns is the v1.0 starter set per design.md Decision 4.
// Each pattern requires `\bCopyright\b` (so "Copyrighted" doesn't
// match) and a word-bounded vendor anchor on the same line. New tags
// should be added with care — and accompanied by a
// `mibs/LICENSES/<tag>.txt` file.
var licensePatterns = []licensePattern{
	{tag: "cisco", re: regexp.MustCompile(`(?i)\bCopyright\b[^\n]*\bCisco Systems\b`)},
	{tag: "juniper", re: regexp.MustCompile(`(?i)\bCopyright\b[^\n]*\bJuniper Networks\b`)},
	{tag: "hpe", re: regexp.MustCompile(`(?i)\bCopyright\b[^\n]*\bHewlett[- ]Packard Enterprise\b`)},
	{tag: "hp", re: regexp.MustCompile(`(?i)\bCopyright\b[^\n]*\bHewlett[- ]Packard\b`)},
	{tag: "aruba", re: regexp.MustCompile(`(?i)\bCopyright\b[^\n]*\bAruba Networks\b`)},
	{tag: "huawei", re: regexp.MustCompile(`(?i)\bCopyright\b[^\n]*\bHuawei Technologies\b`)},
	{tag: "a10", re: regexp.MustCompile(`(?i)\bCopyright\b[^\n]*\bA10 Networks\b`)},
	{tag: "mellanox", re: regexp.MustCompile(`(?i)\bCopyright\b[^\n]*\bMellanox\b`)},
	{tag: "brocade", re: regexp.MustCompile(`(?i)\bCopyright\b[^\n]*\bBrocade\b`)},
	{tag: "extreme", re: regexp.MustCompile(`(?i)\bCopyright\b[^\n]*\bExtreme Networks\b`)},
	{tag: "rfc-editor", re: regexp.MustCompile(`(?i)\bCopyright\b[^\n]*(?:The Internet Society|IETF Trust)\b`)},
}

// licenseScanLines bounds how much of each MIB the detector reads —
// MIB headers reliably carry the copyright notice in the first ~200
// lines, and reading further wastes I/O on a 5000-file corpus.
const licenseScanLines = 200

// detectLicense returns the matching tag for the first ~200 lines of
// the supplied reader, or "unknown" when no pattern matches. A short
// reader is fine; the scanner exits at EOF. A scanner I/O error is
// returned alongside whatever partial classification we managed.
func detectLicense(r io.Reader) string {
	tag, _ := detectLicenseE(r)
	return tag
}

// detectLicenseE is the error-returning form. Used internally so
// callers that care about I/O errors during scanning can surface
// them.
func detectLicenseE(r io.Reader) (string, error) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)

	var head []byte
	for n := 0; n < licenseScanLines && sc.Scan(); n++ {
		head = append(head, sc.Bytes()...)
		head = append(head, '\n')
	}
	if err := sc.Err(); err != nil {
		return "unknown", fmt.Errorf("license scan: %w", err)
	}

	for _, p := range licensePatterns {
		if p.re.Match(head) {
			return p.tag, nil
		}
	}
	return "unknown", nil
}
