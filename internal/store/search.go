package store

import (
	"context"
	"fmt"
	"regexp"
	"strings"
)

// oidPrefixPattern restricts SearchByOIDPrefix input to digits and dots.
// Any other character — particularly LIKE wildcards like `%` and `_` —
// would otherwise leak into the LIKE pattern and match unintended rows.
var oidPrefixPattern = regexp.MustCompile(`^[0-9]+(\.[0-9]+)*$`)

// SearchHit is one ranked match from the FTS5 search index.
type SearchHit struct {
	SymbolID int64
	Module   string
	Name     string
	OID      string
	Kind     string
	Snippet  string
	Rank     float64
}

// Search runs a full-text query against the symbol index.
//
// The query is passed through to FTS5 after light sanitization. Use
// SearchPrefix for prefix matches; for exact symbol or OID lookup
// callers should prefer GetSymbol / GetSymbolByOID.
func (s *Store) Search(ctx context.Context, query string, limit int) ([]SearchHit, error) {
	if limit <= 0 {
		limit = 25
	}
	q := sanitizeFTS(query)
	if q == "" {
		return nil, nil
	}

	rows, err := s.db.QueryContext(ctx, `
		SELECT s.id, s.module_name, s.name, s.oid, s.kind,
		       snippet(symbol_fts, 2, '<mark>', '</mark>', '…', 12) AS snip,
		       bm25(symbol_fts) AS rank
		FROM symbol_fts
		JOIN symbol s ON s.id = symbol_fts.rowid
		WHERE symbol_fts MATCH ?
		ORDER BY rank
		LIMIT ?`, q, limit)
	if err != nil {
		return nil, fmt.Errorf("fts search: %w", err)
	}
	defer rows.Close()

	var out []SearchHit
	for rows.Next() {
		var h SearchHit
		if err := rows.Scan(&h.SymbolID, &h.Module, &h.Name, &h.OID, &h.Kind, &h.Snippet, &h.Rank); err != nil {
			return nil, err
		}
		out = append(out, h)
	}
	return out, rows.Err()
}

// SearchByOIDPrefix returns symbols whose OID is at or under the given prefix.
//
// The prefix MUST consist of digits and dots only; any other character
// is rejected to prevent LIKE-wildcard injection (e.g. `%` or `_` would
// otherwise match across the entire OID space).
func (s *Store) SearchByOIDPrefix(ctx context.Context, prefix string, limit int) ([]SearchHit, error) {
	if limit <= 0 {
		limit = 25
	}
	if !oidPrefixPattern.MatchString(prefix) {
		return nil, fmt.Errorf("invalid OID prefix %q: must be digits and dots only", prefix)
	}
	pattern := prefix + ".%"
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, module_name, name, oid, kind, '', 0
		FROM symbol
		WHERE oid = ? OR oid LIKE ?
		ORDER BY oid
		LIMIT ?`, prefix, pattern, limit)
	if err != nil {
		return nil, fmt.Errorf("oid prefix search: %w", err)
	}
	defer rows.Close()

	var out []SearchHit
	for rows.Next() {
		var h SearchHit
		if err := rows.Scan(&h.SymbolID, &h.Module, &h.Name, &h.OID, &h.Kind, &h.Snippet, &h.Rank); err != nil {
			return nil, err
		}
		out = append(out, h)
	}
	return out, rows.Err()
}

// sanitizeFTS strips characters that confuse FTS5's MATCH parser.
//
// FTS5 reserves: " ' ( ) : * ^ + - and bare keyword tokens (NEAR, AND, …).
// For the cmd-K palette use case we want plain prefix-style search,
// so we drop everything but word characters and dots, then add a `*`
// suffix so each token becomes a prefix match.
func sanitizeFTS(q string) string {
	q = strings.TrimSpace(q)
	if q == "" {
		return ""
	}
	var b strings.Builder
	var word strings.Builder
	flush := func() {
		if word.Len() > 0 {
			if b.Len() > 0 {
				b.WriteByte(' ')
			}
			b.WriteString(word.String())
			b.WriteByte('*')
			word.Reset()
		}
	}
	for _, r := range q {
		switch {
		case r == '_' || r == '.' ||
			(r >= '0' && r <= '9') ||
			(r >= 'A' && r <= 'Z') ||
			(r >= 'a' && r <= 'z'):
			word.WriteRune(r)
		default:
			// Anything else (including hyphen, which FTS5 interprets
			// as the NOT operator) is treated as a token boundary.
			flush()
		}
	}
	flush()
	return b.String()
}
