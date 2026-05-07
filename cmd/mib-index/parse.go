package main

import (
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// moduleNameRE matches the `<NAME> DEFINITIONS ::= BEGIN` opener that
// every SMIv2 module begins with. The capture group is the module
// name.
//
// The leading `(?m)^[ \t]*` allows leading whitespace before the name
// (rare but legal). Comments preceding the line are ignored because
// SMI comments use `--` to end-of-line and don't span the BEGIN
// opener.
var moduleNameRE = regexp.MustCompile(`(?m)^[ \t]*([A-Za-z][A-Za-z0-9-]*)[ \t]+DEFINITIONS[ \t]*::=[ \t]*BEGIN`)

// importsBlockRE captures everything between the `IMPORTS` keyword
// and the trailing `;` that terminates the clause. Multiline by
// design — the IMPORTS block typically spans many lines.
var importsBlockRE = regexp.MustCompile(`(?s)\bIMPORTS\b(.*?);`)

// importsFromRE captures each `FROM <ModuleName>` inside the IMPORTS
// block; we collect the unique module names.
var importsFromRE = regexp.MustCompile(`\bFROM[ \t\r\n]+([A-Za-z][A-Za-z0-9-]*)`)

// extractModuleName returns the SMIv2 module name from the source
// header. Returns "" if no `... DEFINITIONS ::= BEGIN` opener is
// found in the supplied buffer (which should cover the file's first
// ~50 lines — the opener is invariably near the top).
func extractModuleName(src []byte) string {
	m := moduleNameRE.FindSubmatch(src)
	if len(m) < 2 {
		return ""
	}
	return string(m[1])
}

// extractImports returns the unique module names referenced by the
// MIB's IMPORTS clause, sorted alphabetically. Returns nil for a
// module without an IMPORTS clause (e.g. SNMPv2-SMI itself).
func extractImports(src []byte) []string {
	block := importsBlockRE.Find(src)
	if block == nil {
		return nil
	}
	matches := importsFromRE.FindAllSubmatch(block, -1)
	if len(matches) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(matches))
	out := make([]string, 0, len(matches))
	for _, m := range matches {
		name := string(m[1])
		if _, dup := seen[name]; dup {
			continue
		}
		seen[name] = struct{}{}
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// penFromPath extracts the PEN integer and vendor slug from the
// canonical vendor-directory layout `vendors/{PEN}-{slug}/...`.
// Returns (0, "", false) for non-vendor paths or malformed segments.
func penFromPath(rel string) (uint32, string, bool) {
	const prefix = "vendors/"
	if !strings.HasPrefix(rel, prefix) {
		return 0, "", false
	}
	rest := rel[len(prefix):]
	slash := strings.Index(rest, "/")
	if slash <= 0 {
		return 0, "", false
	}
	bucket := rest[:slash]
	dash := strings.Index(bucket, "-")
	if dash <= 0 || dash == len(bucket)-1 {
		return 0, "", false
	}
	penStr, slug := bucket[:dash], bucket[dash+1:]
	n, err := strconv.ParseUint(penStr, 10, 32)
	if err != nil || n == 0 || penStr != strconv.FormatUint(n, 10) {
		return 0, "", false
	}
	return uint32(n), slug, true
}
