package store

import (
	"context"
	"fmt"
	"regexp"
	"sort"
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

// DidYouMean returns up to limit symbols whose names are close to
// query by Levenshtein distance — suggestions for an empty
// FTS5 result set.
//
// Candidate space is filtered by name length (query length ±3) and
// hard-capped at 5000 rows, so the worst case is bounded
// regardless of corpus size. Distance is computed over lowercase
// names; matches up to distance 3 are kept and ranked.
func (s *Store) DidYouMean(ctx context.Context, query string, limit int) ([]SearchHit, error) {
	q := strings.TrimSpace(query)
	if q == "" {
		return nil, nil
	}
	if limit <= 0 {
		limit = 5
	}

	qLower := strings.ToLower(q)
	qLen := len([]rune(q))
	minLen := qLen - 3
	if minLen < 1 {
		minLen = 1
	}
	maxLen := qLen + 3

	rows, err := s.db.QueryContext(ctx, `
		SELECT id, module_name, name, oid, kind
		FROM symbol
		WHERE LENGTH(name) BETWEEN ? AND ?
		LIMIT 5000`, minLen, maxLen)
	if err != nil {
		return nil, fmt.Errorf("did-you-mean: %w", err)
	}
	defer rows.Close()

	type cand struct {
		hit  SearchHit
		dist int
	}
	var cands []cand
	for rows.Next() {
		var h SearchHit
		if err := rows.Scan(&h.SymbolID, &h.Module, &h.Name, &h.OID, &h.Kind); err != nil {
			return nil, err
		}
		d := levenshtein(qLower, strings.ToLower(h.Name))
		if d <= 3 {
			cands = append(cands, cand{h, d})
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	sort.Slice(cands, func(i, j int) bool {
		if cands[i].dist != cands[j].dist {
			return cands[i].dist < cands[j].dist
		}
		// Stable secondary sort by name so output is deterministic.
		return cands[i].hit.Name < cands[j].hit.Name
	})
	if len(cands) > limit {
		cands = cands[:limit]
	}
	out := make([]SearchHit, len(cands))
	for i, c := range cands {
		out[i] = c.hit
	}
	return out, nil
}

// levenshtein returns the edit distance between a and b. Two-row
// dynamic-programming variant — O(len(a)*len(b)) time, O(min(...))
// space.
func levenshtein(a, b string) int {
	ar := []rune(a)
	br := []rune(b)
	if len(ar) == 0 {
		return len(br)
	}
	if len(br) == 0 {
		return len(ar)
	}

	prev := make([]int, len(br)+1)
	curr := make([]int, len(br)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(ar); i++ {
		curr[0] = i
		for j := 1; j <= len(br); j++ {
			cost := 1
			if ar[i-1] == br[j-1] {
				cost = 0
			}
			ins := curr[j-1] + 1
			del := prev[j] + 1
			sub := prev[j-1] + cost
			m := ins
			if del < m {
				m = del
			}
			if sub < m {
				m = sub
			}
			curr[j] = m
		}
		prev, curr = curr, prev
	}
	return prev[len(br)]
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
