package iana

import (
	"bufio"
	_ "embed"
	"strconv"
	"strings"
	"sync"
)

//go:embed pen.txt
var penText string

var (
	penOnce  sync.Once
	penTable map[uint32]string
	penErr   error
)

// loadPEN parses the embedded registry on first call and caches the
// resulting map. Idempotent and safe under concurrent access. A
// scanner error during parse is captured and surfaced via penErr but
// does not abort callers — partial maps are still useful.
func loadPEN() map[uint32]string {
	penOnce.Do(func() {
		penTable, penErr = parsePEN(penText)
	})
	return penTable
}

// parsePEN reads the IANA enterprise-numbers text format. Each entry
// is a column-0 decimal followed by one or more indented lines; the
// first non-empty indented line carries the organization name. Lines
// starting with `#` at column 0 are comments. The parser is lenient —
// malformed stanzas are skipped without aborting; if a column-0 PEN
// arrives while the previous PEN is still awaiting its org line, the
// previous PEN is dropped (rather than silently binding the next
// stanza's content to the wrong number).
//
// A leading UTF-8 BOM is stripped before scanning so a refreshed
// upstream snapshot saved with one doesn't drop the first entry.
func parsePEN(s string) (map[uint32]string, error) {
	s = strings.TrimPrefix(s, "\ufeff")
	m := make(map[uint32]string)
	sc := bufio.NewScanner(strings.NewReader(s))
	// Some IANA contact lists are unusually long; cap at 1 MB per line
	// which is well above the largest stanza ever observed.
	sc.Buffer(make([]byte, 64*1024), 1024*1024)

	var (
		pen         uint32
		awaitingOrg bool
	)
	for sc.Scan() {
		line := strings.TrimRight(sc.Text(), "\r")

		// Column 0 = no leading whitespace. Tab and space both count
		// as indentation; everything else (including BOM-stripped
		// non-ASCII) starts a new entry.
		col0 := line == "" || (line[0] != ' ' && line[0] != '\t')

		// Comments are only honoured at column 0 — an indented `#` is
		// part of the org-name slot, not a stanza terminator. (The
		// upstream IANA file does not use either form, but we
		// tolerate column-0 comments for our own annotations.)
		if col0 && strings.HasPrefix(line, "#") {
			continue
		}

		if col0 {
			t := strings.TrimSpace(line)
			if t == "" {
				continue
			}
			if n, err := strconv.ParseUint(t, 10, 32); err == nil {
				// Drop the previous PEN if we never saw its org —
				// otherwise the next indented line would silently
				// attach to the wrong number.
				pen = uint32(n)
				awaitingOrg = true
				continue
			}
			// Junk at column 0 — reset state and resync at the next
			// valid PEN.
			awaitingOrg = false
			continue
		}

		// Indented line. Capture the first non-empty one as the org
		// name, then ignore subsequent indented lines (contact + email
		// in the upstream format).
		if !awaitingOrg {
			continue
		}
		org := strings.TrimSpace(line)
		if org == "" {
			continue
		}
		m[pen] = org
		awaitingOrg = false
	}
	return m, sc.Err()
}

// LookupPEN returns the organization name for a Private Enterprise
// Number. Returns ok=false if the PEN isn't in the embedded snapshot.
func LookupPEN(n uint32) (string, bool) {
	v, ok := loadPEN()[n]
	return v, ok
}

// Slug converts a vendor organization name to a kebab-case directory
// slug suitable for `mibs/vendors/{PEN}-{slug}/`. Rules:
//
//   - lowercase
//   - drop characters outside [a-z 0-9 -] (replaced with whitespace
//     during tokenisation), then split on whitespace
//   - pop trailing tokens that are common corporate suffix words
//     (Inc, Corp, Corporation, Ltd, Limited, LLC, Co, Company, GmbH,
//     AG, plc, Networks, Systems, Technologies)
//   - join with `-`
//   - if all tokens were suffix words, fall back to the un-stripped
//     token list so the slug isn't empty
//   - truncate to 20 runes and trim trailing `-`
//
// A small built-in override map (lowercase keys, lowercase lookup)
// handles abbreviations the rules can't reach (e.g. "Hewlett Packard
// Enterprise" → "hp-enterprise"). Migration-tool-specific overrides
// layer on top in cmd/mib-migrate/slugs.go.
//
// Stripping is trailing-only — a name like "Cisco Systems Routing"
// keeps "Systems" because it isn't the last token, so the slug is
// "cisco-systems-routing" rather than "cisco-routing".
func Slug(name string) string {
	nk := strings.ToLower(strings.TrimSpace(name))
	if s, ok := slugOverrides[nk]; ok {
		return s
	}

	// Build a normalised string containing only [a-z 0-9 - ] — every
	// other rune (punctuation, non-ASCII) becomes a space. This keeps
	// slugs portable across filesystems and avoids byte-truncation
	// landing mid-rune.
	var b strings.Builder
	b.Grow(len(name))
	for _, r := range strings.ToLower(name) {
		switch {
		case r >= 'a' && r <= 'z',
			r >= '0' && r <= '9',
			r == '-':
			b.WriteRune(r)
		default:
			b.WriteByte(' ')
		}
	}

	all := strings.Fields(b.String())
	tokens := append([]string(nil), all...)
	// Pop trailing suffix words.
	for len(tokens) > 0 && isSuffixWord(tokens[len(tokens)-1]) {
		tokens = tokens[:len(tokens)-1]
	}
	// Fallback: if every token was a suffix word, use the un-stripped
	// list so the slug isn't empty (rare, but possible for a name
	// like "Networks Inc").
	if len(tokens) == 0 {
		tokens = all
	}
	if len(tokens) == 0 {
		return ""
	}

	slug := strings.Join(tokens, "-")
	// All characters are now ASCII (filtered above), so byte length
	// equals rune count — but we still phrase the truncation in terms
	// of runes for clarity if the filter is ever loosened.
	if r := []rune(slug); len(r) > 20 {
		slug = strings.TrimRight(string(r[:20]), "-")
	}
	return slug
}

var suffixWords = map[string]struct{}{
	"inc":          {},
	"corp":         {},
	"corporation":  {},
	"ltd":          {},
	"limited":      {},
	"llc":          {},
	"co":           {},
	"company":      {},
	"gmbh":         {},
	"ag":           {},
	"plc":          {},
	"networks":     {},
	"systems":      {},
	"technologies": {},
}

func isSuffixWord(w string) bool {
	_, ok := suffixWords[strings.ToLower(w)]
	return ok
}

// slugOverrides pins kebab-case slugs for organizations whose rule-
// derived form is awkward or ambiguous. Keys are lowercase + trimmed
// to match the case-insensitive lookup. Add sparingly; prefer fixing
// the rules when an addition would generalise.
var slugOverrides = map[string]string{
	"hewlett packard enterprise": "hp-enterprise",
	"hewlett-packard enterprise": "hp-enterprise",
	"hewlett-packard company":    "hp",
	"hewlett-packard":            "hp",
	"hewlett packard":            "hp",
	"no42.org":                   "no42",
}
