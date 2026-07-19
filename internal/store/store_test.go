package store

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/no42-org/blittermib/internal/model"
)

// TestMigrateAddOIDNodeFamilyFlagsPartial pins the self-heal for a
// partially-applied family-flag migration: the three columns are added by
// separate autocommit ALTERs, so a crash after has_scalar but before
// has_notif must be recovered on the next boot (each column checked
// independently), NOT skipped on a single sentinel — which would leave
// RebuildOIDTree's INSERT failing forever on the missing column.
func TestMigrateAddOIDNodeFamilyFlagsPartial(t *testing.T) {
	ctx := context.Background()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer func() { _ = db.Close() }()

	// A trie whose prior migration got only as far as has_scalar.
	if _, err := db.ExecContext(ctx,
		`CREATE TABLE oid_node (oid TEXT PRIMARY KEY, has_scalar INTEGER NOT NULL DEFAULT 0)`); err != nil {
		t.Fatalf("seed partial oid_node: %v", err)
	}

	if err := migrateAddOIDNodeFamilyFlags(ctx, db); err != nil {
		t.Fatalf("recovery migration: %v", err)
	}
	for _, col := range []string{"has_scalar", "has_table", "has_notif"} {
		has, err := tableHasColumn(ctx, db, "oid_node", col)
		if err != nil {
			t.Fatal(err)
		}
		if !has {
			t.Errorf("column %s missing after recovery migration", col)
		}
	}

	// Idempotent: a second run is a no-op (no duplicate-column error).
	if err := migrateAddOIDNodeFamilyFlags(ctx, db); err != nil {
		t.Fatalf("re-run should be a no-op: %v", err)
	}
}

func newStore(t *testing.T) *Store {
	t.Helper()
	s, err := OpenInMemory(context.Background())
	if err != nil {
		t.Fatalf("OpenInMemory: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func sampleModule() *model.Module {
	return &model.Module{
		Name:         "IF-MIB",
		OIDRoot:      "1.3.6.1.2.1.31",
		Organization: "IETF",
		ContactInfo:  "ietfmibs@ops.ietf.org",
		Description:  "Interfaces MIB.",
		LastUpdated:  "2007-09-29 00:00",
		ParseStatus:  model.ParseStatusClean,
	}
}

func sampleSymbols() []model.Symbol {
	return []model.Symbol{
		{
			ModuleName: "IF-MIB", Name: "ifTable",
			OID: "1.3.6.1.2.1.2.2", ParentOID: "1.3.6.1.2.1.2",
			Kind: model.KindTable, Syntax: "SEQUENCE OF IfEntry",
			Access: model.AccessNotAccessible, Status: model.StatusCurrent,
			Description: "A list of interface entries.",
		},
		{
			ModuleName: "IF-MIB", Name: "ifEntry",
			OID: "1.3.6.1.2.1.2.2.1", ParentOID: "1.3.6.1.2.1.2.2",
			Kind: model.KindTableEntry, Syntax: "IfEntry",
			Access: model.AccessNotAccessible, Status: model.StatusCurrent,
			IndexColumns: []string{"ifIndex"},
		},
		{
			ModuleName: "IF-MIB", Name: "ifInOctets",
			OID: "1.3.6.1.2.1.2.2.1.10", ParentOID: "1.3.6.1.2.1.2.2.1",
			Kind: model.KindColumn, Syntax: "Counter32",
			Access: model.AccessReadOnly, Status: model.StatusCurrent,
			Units:       "octets",
			Description: "The total number of octets received on the interface.",
			EnumValues: []model.EnumValue{
				{Name: "ok", Number: 1},
				{Name: "fault", Number: 2},
			},
		},
	}
}

func sampleRefs() []model.Reference {
	return []model.Reference{
		{
			SourceModule: "IF-MIB", SourceName: "ifEntry",
			TargetModule: "IF-MIB", TargetName: "ifIndex",
			Kind: model.RefIndex,
		},
	}
}

func sampleDiags() []model.Diagnostic {
	return []model.Diagnostic{
		{File: "IF-MIB.txt", Line: 142, Severity: model.SeverityWarning,
			Code: "compliance-non-current", Message: "stale compliance"},
	}
}

func TestOpenAndSchemaApplied(t *testing.T) {
	s := newStore(t)
	n, err := s.CountModules(context.Background())
	if err != nil {
		t.Fatalf("CountModules: %v", err)
	}
	if n != 0 {
		t.Errorf("empty store should have 0 modules, got %d", n)
	}
}

func TestReplaceAndQuery(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)

	if err := s.ReplaceModule(ctx, sampleModule(), sampleSymbols(), sampleRefs(), sampleDiags()); err != nil {
		t.Fatalf("ReplaceModule: %v", err)
	}

	mod, err := s.GetModule(ctx, "IF-MIB")
	if err != nil {
		t.Fatalf("GetModule: %v", err)
	}
	if mod.OIDRoot != "1.3.6.1.2.1.31" {
		t.Errorf("OIDRoot = %q", mod.OIDRoot)
	}

	syms, err := s.ListSymbolsByModule(ctx, "IF-MIB")
	if err != nil {
		t.Fatalf("ListSymbolsByModule: %v", err)
	}
	if len(syms) != 3 {
		t.Errorf("symbols = %d, want 3", len(syms))
	}

	in, err := s.GetSymbol(ctx, "IF-MIB", "ifInOctets")
	if err != nil {
		t.Fatalf("GetSymbol: %v", err)
	}
	if in.Access != model.AccessReadOnly || in.Units != "octets" {
		t.Errorf("ifInOctets fields wrong: %+v", in)
	}

	byOID, err := s.GetSymbolByOID(ctx, "1.3.6.1.2.1.2.2.1.10")
	if err != nil {
		t.Fatalf("GetSymbolByOID: %v", err)
	}
	if byOID.Name != "ifInOctets" {
		t.Errorf("got %q by OID, want ifInOctets", byOID.Name)
	}

	entry, err := s.GetSymbol(ctx, "IF-MIB", "ifEntry")
	if err != nil {
		t.Fatalf("GetSymbol(ifEntry): %v", err)
	}
	if got, want := entry.IndexColumns, []string{"ifIndex"}; !equalStrings(got, want) {
		t.Errorf("IndexColumns = %v, want %v", got, want)
	}
	if entry.Kind != model.KindTableEntry {
		t.Errorf("ifEntry Kind = %q, want %q", entry.Kind, model.KindTableEntry)
	}

	// Enum values round-trip through JSON column.
	in2, err := s.GetSymbol(ctx, "IF-MIB", "ifInOctets")
	if err != nil {
		t.Fatalf("GetSymbol(ifInOctets): %v", err)
	}
	wantEnum := []model.EnumValue{
		{Name: "ok", Number: 1},
		{Name: "fault", Number: 2},
	}
	if got := in2.EnumValues; len(got) != len(wantEnum) {
		t.Errorf("EnumValues length = %d, want %d", len(got), len(wantEnum))
	} else {
		for i := range got {
			if got[i] != wantEnum[i] {
				t.Errorf("EnumValues[%d] = %+v, want %+v", i, got[i], wantEnum[i])
			}
		}
	}

	children, err := s.ListChildren(ctx, "1.3.6.1.2.1.2.2.1")
	if err != nil {
		t.Fatalf("ListChildren: %v", err)
	}
	if len(children) != 1 || children[0].Name != "ifInOctets" {
		t.Errorf("children = %+v", children)
	}

	refsFrom, err := s.ListReferencesFrom(ctx, "IF-MIB", "ifEntry")
	if err != nil {
		t.Fatalf("ListReferencesFrom: %v", err)
	}
	if len(refsFrom) != 1 || refsFrom[0].Kind != model.RefIndex {
		t.Errorf("refsFrom = %+v", refsFrom)
	}

	diags, err := s.ListDiagnosticsByModule(ctx, "IF-MIB")
	if err != nil {
		t.Fatalf("ListDiagnosticsByModule: %v", err)
	}
	if len(diags) != 1 || diags[0].Code != "compliance-non-current" {
		t.Errorf("diags = %+v", diags)
	}
}

func TestHotReloadReplacesAtomically(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)

	if err := s.ReplaceModule(ctx, sampleModule(), sampleSymbols(), sampleRefs(), sampleDiags()); err != nil {
		t.Fatalf("first ReplaceModule: %v", err)
	}

	// New version of IF-MIB with one fewer symbol and a different description.
	mod := sampleModule()
	mod.Description = "updated"
	syms := sampleSymbols()[:2] // drop ifInOctets

	if err := s.ReplaceModule(ctx, mod, syms, nil, nil); err != nil {
		t.Fatalf("second ReplaceModule: %v", err)
	}

	got, err := s.GetModule(ctx, "IF-MIB")
	if err != nil {
		t.Fatalf("GetModule after reload: %v", err)
	}
	if got.Description != "updated" {
		t.Errorf("description not updated: %q", got.Description)
	}

	if _, err := s.GetSymbol(ctx, "IF-MIB", "ifInOctets"); !errors.Is(err, ErrNotFound) {
		t.Errorf("ifInOctets should be gone, got err=%v", err)
	}

	// Old refs from this module should be gone.
	refs, err := s.ListReferencesFrom(ctx, "IF-MIB", "ifEntry")
	if err != nil {
		t.Fatalf("ListReferencesFrom: %v", err)
	}
	if len(refs) != 0 {
		t.Errorf("old refs not cleared: %+v", refs)
	}

	// Old diagnostics should be gone.
	diags, err := s.ListDiagnosticsByModule(ctx, "IF-MIB")
	if err != nil {
		t.Fatalf("ListDiagnosticsByModule: %v", err)
	}
	if len(diags) != 0 {
		t.Errorf("old diagnostics not cleared: %+v", diags)
	}
}

func TestSearchFTS(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	if err := s.ReplaceModule(ctx, sampleModule(), sampleSymbols(), nil, nil); err != nil {
		t.Fatalf("ReplaceModule: %v", err)
	}

	hits, err := s.Search(ctx, "octets", 10)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(hits) == 0 {
		t.Fatal("expected at least one hit for 'octets'")
	}
	found := false
	for _, h := range hits {
		if h.Name == "ifInOctets" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("ifInOctets not in search results: %+v", hits)
	}
}

func TestSearchQueryTooShort(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	if err := s.ReplaceModule(ctx, sampleModule(), sampleSymbols(), nil, nil); err != nil {
		t.Fatalf("ReplaceModule: %v", err)
	}

	// Queries whose every token is under the floor are declined with a
	// distinguishable sentinel, not treated as "no results".
	for _, q := range []string{"i", "p-", "a b"} {
		if _, err := s.Search(ctx, q, 10); !errors.Is(err, ErrQueryTooShort) {
			t.Errorf("Search(%q) err = %v, want ErrQueryTooShort", q, err)
		}
	}

	// A blank query stays a silent no-op (nil, nil) — nothing was asked.
	if hits, err := s.Search(ctx, "   ", 10); err != nil || hits != nil {
		t.Errorf("Search(blank) = (%v, %v), want (nil, nil)", hits, err)
	}

	// Mixed queries survive: the sub-floor token is dropped and the
	// remaining token still searches.
	hits, err := s.Search(ctx, "octets x", 10)
	if err != nil {
		t.Fatalf("Search(mixed): %v", err)
	}
	if len(hits) == 0 {
		t.Error("Search(\"octets x\") returned no hits; want octets* matches")
	}
}

func TestSearchByOIDPrefix(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	if err := s.ReplaceModule(ctx, sampleModule(), sampleSymbols(), nil, nil); err != nil {
		t.Fatalf("ReplaceModule: %v", err)
	}

	hits, err := s.SearchByOIDPrefix(ctx, "1.3.6.1.2.1.2.2", 10)
	if err != nil {
		t.Fatalf("SearchByOIDPrefix: %v", err)
	}
	names := map[string]bool{}
	for _, h := range hits {
		names[h.Name] = true
	}
	for _, want := range []string{"ifTable", "ifEntry", "ifInOctets"} {
		if !names[want] {
			t.Errorf("OID prefix didn't return %s; got %v", want, names)
		}
	}
}

func TestDidYouMean(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	if err := s.ReplaceModule(ctx, sampleModule(), sampleSymbols(), nil, nil); err != nil {
		t.Fatalf("ReplaceModule: %v", err)
	}

	// "ifInOctts" — typo (distance 1) of "ifInOctets"
	got, err := s.DidYouMean(ctx, "ifInOctts", 5)
	if err != nil {
		t.Fatalf("DidYouMean: %v", err)
	}
	if len(got) == 0 {
		t.Fatal("expected at least one suggestion for typo 'ifInOctts'")
	}
	if got[0].Name != "ifInOctets" {
		t.Errorf("top suggestion = %q, want ifInOctets", got[0].Name)
	}
}

func TestDidYouMeanFarMissReturnsNothing(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	if err := s.ReplaceModule(ctx, sampleModule(), sampleSymbols(), nil, nil); err != nil {
		t.Fatalf("ReplaceModule: %v", err)
	}
	// Distance >> 3 from any seeded name.
	got, err := s.DidYouMean(ctx, "totallyUnrelated", 5)
	if err != nil {
		t.Fatalf("DidYouMean: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("expected no suggestions, got %v", got)
	}
}

func TestLevenshtein(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"", "", 0},
		{"abc", "abc", 0},
		{"abc", "", 3},
		{"", "abc", 3},
		{"kitten", "sitting", 3},
		{"flaw", "lawn", 2},
		{"ifInOctets", "ifInOctts", 1},
	}
	for _, c := range cases {
		if got := levenshtein(c.a, c.b); got != c.want {
			t.Errorf("levenshtein(%q,%q) = %d, want %d", c.a, c.b, got, c.want)
		}
	}
}

func TestSanitizeFTS(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"", ""},
		{"  ", ""},
		{"ifInOctets", "ifInOctets*"},
		{"if in oct", "if* in* oct*"},
		{"foo:bar", "foo* bar*"},
		{"\"injection\"--stuff", "injection* stuff*"},
		// Sub-floor tokens are dropped, not just short whole queries —
		// punctuation splits tokens, so "p-" would otherwise still
		// compile to the pathological one-rune prefix `p*`.
		{"p", ""},
		{"p-", ""},
		{"p q", ""},
		{"palo a", "palo*"},
		// A '.' is a token boundary — it must never survive into a token,
		// because an unquoted dot in an FTS5 MATCH term is a syntax error
		// (e.g. `1.3*` → `fts5: syntax error near "."`). These are the
		// inputs that reach the FTS path with an embedded dot: an OID with
		// a trailing dot typed into the palette, or a dotted word.
		{"1.3.6.", ""},
		{"ifDescr.1", "ifDescr*"},
		{"node.js", "node* js*"},
		{"a.b", ""},
	}
	for _, c := range cases {
		if got := sanitizeFTS(c.in); got != c.want {
			t.Errorf("sanitize(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestOpenFile(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	path := filepath.Join(dir, "blittermib.db")
	s, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = s.Close() }()

	if err := s.ReplaceModule(ctx, sampleModule(), sampleSymbols(), nil, nil); err != nil {
		t.Fatalf("ReplaceModule: %v", err)
	}

	// Reopen — schema should already exist; data should persist.
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	s2, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer func() { _ = s2.Close() }()

	got, err := s2.GetModule(ctx, "IF-MIB")
	if err != nil {
		t.Fatalf("GetModule on reopen: %v", err)
	}
	if got.Name != "IF-MIB" {
		t.Errorf("module not persisted: %+v", got)
	}
}

func TestReplaceModuleRejectsNil(t *testing.T) {
	s := newStore(t)
	if err := s.ReplaceModule(context.Background(), nil, nil, nil, nil); err == nil {
		t.Error("expected error for nil module")
	}
}

func TestReplaceModuleRejectsEmptyName(t *testing.T) {
	s := newStore(t)
	if err := s.ReplaceModule(context.Background(), &model.Module{}, nil, nil, nil); err == nil {
		t.Error("expected error for module with empty name")
	}
}

func TestSearchByOIDPrefixRejectsBadInput(t *testing.T) {
	s := newStore(t)
	cases := []string{
		"",
		"1.3.6.%",
		"1.3.6._",
		"1.3.6.abc",
		"' OR 1=1 --",
	}
	for _, in := range cases {
		if _, err := s.SearchByOIDPrefix(context.Background(), in, 10); err == nil {
			t.Errorf("expected error for %q", in)
		}
	}
}

func TestSearchByOIDPrefixAcceptsValidInput(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	if err := s.ReplaceModule(ctx, sampleModule(), sampleSymbols(), nil, nil); err != nil {
		t.Fatalf("ReplaceModule: %v", err)
	}
	if _, err := s.SearchByOIDPrefix(ctx, "1.3.6.1.2.1.2.2", 10); err != nil {
		t.Errorf("valid prefix rejected: %v", err)
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// sameStringsAnyOrder reports whether a and b are the same multiset
// of strings — used by closure-walk tests where the iteration order
// of imports inside a single module is not part of the contract.
func sameStringsAnyOrder(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	counts := make(map[string]int, len(a))
	for _, s := range a {
		counts[s]++
	}
	for _, s := range b {
		counts[s]--
		if counts[s] < 0 {
			return false
		}
	}
	return true
}

func TestCountByFamily(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)

	// Seed a fixture covering several families so the helper has
	// something to classify: 3 counters, 2 gauges, 1 table, 1
	// table-entry, 2 columns (one Counter32 → t-counter, one
	// DisplayString → t-text), 1 NOTIFICATION-TYPE.
	syms := []model.Symbol{
		{ModuleName: "X", Name: "scalar1", OID: "1.1", Kind: model.KindScalar, Syntax: "Counter32", Status: model.StatusCurrent},
		{ModuleName: "X", Name: "scalar2", OID: "1.2", Kind: model.KindScalar, Syntax: "Counter64", Status: model.StatusCurrent},
		{ModuleName: "X", Name: "scalar3", OID: "1.3", Kind: model.KindScalar, Syntax: "Gauge32", Status: model.StatusCurrent},
		{ModuleName: "X", Name: "scalar4", OID: "1.4", Kind: model.KindScalar, Syntax: "Unsigned32", Status: model.StatusCurrent},
		{ModuleName: "X", Name: "tbl", OID: "1.5", Kind: model.KindTable, Syntax: "SEQUENCE OF Y", Status: model.StatusCurrent},
		{ModuleName: "X", Name: "row", OID: "1.5.1", Kind: model.KindTableEntry, Syntax: "Y", Status: model.StatusCurrent},
		{ModuleName: "X", Name: "col1", OID: "1.5.1.1", Kind: model.KindColumn, Syntax: "Counter32", Status: model.StatusCurrent},
		{ModuleName: "X", Name: "col2", OID: "1.5.1.2", Kind: model.KindColumn, Syntax: "DisplayString", Status: model.StatusCurrent},
		{ModuleName: "X", Name: "alert", OID: "1.6", Kind: model.KindNotificationType, Status: model.StatusCurrent},
	}
	if err := s.ReplaceModule(ctx,
		&model.Module{Name: "X", ParseStatus: model.ParseStatusClean},
		syms, nil, nil); err != nil {
		t.Fatalf("ReplaceModule: %v", err)
	}

	fc, err := s.CountByFamily(ctx, "X")
	if err != nil {
		t.Fatalf("CountByFamily: %v", err)
	}
	if fc.Counters != 3 {
		t.Errorf("Counters = %d, want 3", fc.Counters)
	}
	if fc.Gauges != 2 {
		t.Errorf("Gauges = %d, want 2", fc.Gauges)
	}
	if fc.Texts != 1 {
		t.Errorf("Texts = %d, want 1", fc.Texts)
	}
	if fc.Notifs != 1 {
		t.Errorf("Notifs = %d, want 1", fc.Notifs)
	}
	// Structs = table + table-entry (the locked Reading-3 set).
	if fc.Structs != 2 {
		t.Errorf("Structs = %d, want 2", fc.Structs)
	}
}

func TestOIDPath(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)

	if err := s.ReplaceModule(ctx, sampleModule(), sampleSymbols(), nil, nil); err != nil {
		t.Fatalf("ReplaceModule: %v", err)
	}

	// IF-MIB anchored under 1.3.6.1.2.1.2.2.1.10 (ifInOctets):
	// 10 prefixes total. The first six (1, 1.3, 1.3.6, 1.3.6.1,
	// 1.3.6.1.2, 1.3.6.1.2.1) come from the canonical table; the
	// rest from the fixture (1.3.6.1.2.1.2 → bare; 1.3.6.1.2.1.2.2
	// → ifTable; 1.3.6.1.2.1.2.2.1 → ifEntry; 1.3.6.1.2.1.2.2.1.10
	// → ifInOctets).
	steps, err := s.OIDPath(ctx, "1.3.6.1.2.1.2.2.1.10")
	if err != nil {
		t.Fatalf("OIDPath: %v", err)
	}
	if len(steps) != 10 {
		t.Fatalf("step count = %d, want 10", len(steps))
	}
	wantNames := []string{
		"iso", "org", "dod", "internet", "mgmt", "mib-2",
		"interfaces", "ifTable", "ifEntry", "ifInOctets",
	}
	for i, want := range wantNames {
		if steps[i].Name != want {
			t.Errorf("step[%d].Name = %q, want %q (prefix %q)",
				i, steps[i].Name, want, steps[i].Prefix)
		}
	}
	if !steps[0].Canonical {
		t.Error("step 0 (iso) should be Canonical")
	}
	if steps[7].Canonical {
		t.Error("step 7 (ifTable) should not be Canonical")
	}
	if steps[7].Module != "IF-MIB" {
		t.Errorf("step 7 module = %q, want IF-MIB", steps[7].Module)
	}

	// Empty input is allowed, returns empty slice.
	if steps, err := s.OIDPath(ctx, ""); err != nil || len(steps) != 0 {
		t.Errorf("OIDPath(\"\") = %v, %v", steps, err)
	}
}

// TestOIDPathIANAOnlyArc covers an arc that only the IANA canonical
// registry names: no module is loaded, and `bgp(1.3.6.1.2.1.15)` was
// absent from the old in-store fallback table. Breadcrumbs gain these
// names by delegating to iana.LookupCanonical.
func TestOIDPathIANAOnlyArc(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)

	steps, err := s.OIDPath(ctx, "1.3.6.1.2.1.15")
	if err != nil {
		t.Fatalf("OIDPath: %v", err)
	}
	if len(steps) != 7 {
		t.Fatalf("step count = %d, want 7", len(steps))
	}
	last := steps[len(steps)-1]
	if last.Name != "bgp" || !last.Canonical {
		t.Errorf("step %q = (%q, canonical=%v), want (\"bgp\", true)",
			last.Prefix, last.Name, last.Canonical)
	}
}

func TestOIDPathDeterministicOrdering(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)

	// Two modules both export a symbol at the same OID. OIDPath
	// MUST pick one deterministically — alphabetical by module
	// name, then by symbol name.
	for _, m := range []string{"Z-MIB", "A-MIB"} {
		if err := s.ReplaceModule(ctx,
			&model.Module{Name: m, ParseStatus: model.ParseStatusClean},
			[]model.Symbol{{ModuleName: m, Name: "shared",
				OID: "1.99", Kind: model.KindScalar, Status: model.StatusCurrent}},
			nil, nil); err != nil {
			t.Fatalf("ReplaceModule(%s): %v", m, err)
		}
	}
	steps, err := s.OIDPath(ctx, "1.99")
	if err != nil {
		t.Fatalf("OIDPath: %v", err)
	}
	// Last step is the contended one.
	last := steps[len(steps)-1]
	if last.Module != "A-MIB" {
		t.Errorf("multi-match resolved to %q, want A-MIB (alphabetical)", last.Module)
	}
}

// TestListImportClosure seeds a small graph A → B → C and an
// unloaded D imported by B; closure walk from A should return
// four entries (A, B, C, D), with D marked Loaded=false and
// carrying the symbols B imported from it.
func TestListImportClosure(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)

	// C — leaf, no imports
	if err := s.ReplaceModule(ctx,
		&model.Module{Name: "C-MIB", SourcePath: "/m/C-MIB", ParseStatus: model.ParseStatusClean},
		nil, nil, nil,
	); err != nil {
		t.Fatalf("seed C: %v", err)
	}

	// B — imports from C and from unloaded D
	if err := s.ReplaceModule(ctx,
		&model.Module{
			Name:        "B-MIB",
			SourcePath:  "/m/B-MIB",
			ParseStatus: model.ParseStatusClean,
			Imports: []model.Import{
				{FromModule: "C-MIB", Symbol: "Counter32"},
				{FromModule: "D-MIB", Symbol: "TimeTicks"},
				{FromModule: "D-MIB", Symbol: "Gauge32"},
			},
		},
		nil, nil, nil,
	); err != nil {
		t.Fatalf("seed B: %v", err)
	}

	// A — imports from B only
	if err := s.ReplaceModule(ctx,
		&model.Module{
			Name:        "A-MIB",
			SourcePath:  "/m/A-MIB",
			ParseStatus: model.ParseStatusClean,
			Imports: []model.Import{
				{FromModule: "B-MIB", Symbol: "ifIndex"},
			},
		},
		nil, nil, nil,
	); err != nil {
		t.Fatalf("seed A: %v", err)
	}

	closure, err := s.ListImportClosure(ctx, "A-MIB")
	if err != nil {
		t.Fatalf("ListImportClosure: %v", err)
	}

	// Closure has 4 entries: A (root), B (direct), and B's imports
	// (C and D). Internal ordering depends on `listImportsByModule`'s
	// SQL `ORDER BY` which is not part of the Store contract — match
	// by module name into a map so the test stays stable across
	// future query refinements.
	if len(closure) != 4 {
		t.Fatalf("closure size = %d, want 4: %+v", len(closure), closure)
	}
	byModule := make(map[string]ClosureEntry, len(closure))
	for _, e := range closure {
		byModule[e.Module] = e
	}

	// Root must be the first entry — that's a contract guarantee
	// (handlers depend on it for the bundle root-traversal check).
	if closure[0].Module != "A-MIB" {
		t.Errorf("closure[0] = %q, want A-MIB (root must be first)", closure[0].Module)
	}

	want := []struct {
		Module     string
		Loaded     bool
		ImportedBy string
		Symbols    []string
	}{
		{"A-MIB", true, "", nil},
		{"B-MIB", true, "A-MIB", []string{"ifIndex"}},
		{"C-MIB", true, "B-MIB", []string{"Counter32"}},
		{"D-MIB", false, "B-MIB", []string{"TimeTicks", "Gauge32"}},
	}
	for _, w := range want {
		got, ok := byModule[w.Module]
		if !ok {
			t.Errorf("closure missing module %q", w.Module)
			continue
		}
		if got.Loaded != w.Loaded {
			t.Errorf("%s: Loaded = %v, want %v", w.Module, got.Loaded, w.Loaded)
		}
		if got.ImportedBy != w.ImportedBy {
			t.Errorf("%s: ImportedBy = %q, want %q", w.Module, got.ImportedBy, w.ImportedBy)
		}
		if !sameStringsAnyOrder(got.Symbols, w.Symbols) {
			t.Errorf("%s: Symbols = %+v, want %+v (any order)", w.Module, got.Symbols, w.Symbols)
		}
	}

	// Loaded entries should carry SourcePath; unloaded should not.
	if a := byModule["A-MIB"]; a.SourcePath != "/m/A-MIB" {
		t.Errorf("A-MIB SourcePath = %q, want /m/A-MIB", a.SourcePath)
	}
	if d := byModule["D-MIB"]; d.SourcePath != "" {
		t.Errorf("D-MIB SourcePath = %q, want empty (unloaded)", d.SourcePath)
	}
}

// TestListImportClosureCycle defends against a malformed input
// where two modules import each other (forbidden by SMI but
// possible if the parser ever lets one through). Must not loop.
func TestListImportClosureCycle(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)

	if err := s.ReplaceModule(ctx,
		&model.Module{
			Name:        "P-MIB",
			SourcePath:  "/m/P-MIB",
			ParseStatus: model.ParseStatusClean,
			Imports:     []model.Import{{FromModule: "Q-MIB", Symbol: "x"}},
		},
		nil, nil, nil,
	); err != nil {
		t.Fatalf("seed P: %v", err)
	}
	if err := s.ReplaceModule(ctx,
		&model.Module{
			Name:        "Q-MIB",
			SourcePath:  "/m/Q-MIB",
			ParseStatus: model.ParseStatusClean,
			Imports:     []model.Import{{FromModule: "P-MIB", Symbol: "y"}},
		},
		nil, nil, nil,
	); err != nil {
		t.Fatalf("seed Q: %v", err)
	}

	closure, err := s.ListImportClosure(ctx, "P-MIB")
	if err != nil {
		t.Fatalf("ListImportClosure: %v", err)
	}
	if len(closure) != 2 {
		t.Errorf("cycle should still produce 2 entries (P, Q), got %d: %+v", len(closure), closure)
	}
}

// TestIndexImpliedRoundtrip pins the new `index_implied` field's
// model → SQLite → model contract. Two row-entry symbols are
// inserted with opposite IMPLIED bits; both must scan back with
// the same value the model went in with.
//
// The variable-length OCTET STRING / OID composers in the trap
// simulator depend on this round-tripping correctly — a regression
// where IsImplied is silently flipped (or dropped) would produce
// length-prefixed OIDs where bare-bytes were intended, or vice
// versa, and the bug would only surface when a user simulated a
// trap rooted at an IMPLIED-indexed table.
func TestIndexImpliedRoundtrip(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)

	mod := &model.Module{
		Name: "TEST-MIB", OIDRoot: "1.3.6.1.4.1.99999",
		ParseStatus: model.ParseStatusClean,
	}
	syms := []model.Symbol{
		{
			ModuleName: "TEST-MIB", Name: "impliedEntry",
			OID: "1.3.6.1.4.1.99999.1.1", ParentOID: "1.3.6.1.4.1.99999.1",
			Kind: model.KindTableEntry, Syntax: "ImpliedEntry",
			IndexColumns: []string{"impliedKey"},
			IndexImplied: true,
		},
		{
			ModuleName: "TEST-MIB", Name: "regularEntry",
			OID: "1.3.6.1.4.1.99999.2.1", ParentOID: "1.3.6.1.4.1.99999.2",
			Kind: model.KindTableEntry, Syntax: "RegularEntry",
			IndexColumns: []string{"regularKey"},
			IndexImplied: false,
		},
	}
	if err := s.ReplaceModule(ctx, mod, syms, nil, nil); err != nil {
		t.Fatalf("ReplaceModule: %v", err)
	}

	implied, err := s.GetSymbol(ctx, "TEST-MIB", "impliedEntry")
	if err != nil {
		t.Fatalf("GetSymbol(impliedEntry): %v", err)
	}
	if !implied.IndexImplied {
		t.Errorf("impliedEntry.IndexImplied = false, want true")
	}

	regular, err := s.GetSymbol(ctx, "TEST-MIB", "regularEntry")
	if err != nil {
		t.Fatalf("GetSymbol(regularEntry): %v", err)
	}
	if regular.IndexImplied {
		t.Errorf("regularEntry.IndexImplied = true, want false")
	}
}

// TestMigrateAddIndexImpliedAlters covers the in-place ALTER TABLE
// migration path: a SQLite database whose `symbol` table predates
// the `index_implied` column should boot through `Open` cleanly,
// pick up the new column, and start round-tripping the field
// without losing prior data.
//
// We exercise the migration end-to-end on a file-backed DB by:
//  1. Opening at a temp path so PRAGMA table_info can be inspected.
//  2. Dropping and recreating the symbol table without the new
//     column (simulating a pre-migration shape).
//  3. Closing and reopening — `migrateAddIndexImplied` must run on
//     the second open and add the column.
//  4. Inserting a row through the model layer and reading it back.
func TestMigrateAddIndexImpliedAlters(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	path := dir + "/store.db"

	s, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("Open #1: %v", err)
	}
	// Drop and recreate the symbol table without the new column,
	// simulating the pre-migration shape. The FTS shadow + triggers
	// drop with it so the recreate doesn't double-up.
	for _, stmt := range []string{
		`DROP TRIGGER IF EXISTS symbol_ai`,
		`DROP TRIGGER IF EXISTS symbol_ad`,
		`DROP TRIGGER IF EXISTS symbol_au`,
		`DROP TABLE IF EXISTS symbol_fts`,
		`DROP TABLE IF EXISTS symbol`,
		`CREATE TABLE symbol (
			id             INTEGER PRIMARY KEY,
			module_name    TEXT    NOT NULL REFERENCES module(name) ON DELETE CASCADE,
			name           TEXT    NOT NULL,
			oid            TEXT    NOT NULL DEFAULT '',
			parent_oid     TEXT    NOT NULL DEFAULT '',
			kind           TEXT    NOT NULL,
			syntax         TEXT    NOT NULL DEFAULT '',
			access         TEXT    NOT NULL DEFAULT '',
			status         TEXT    NOT NULL DEFAULT '',
			units          TEXT    NOT NULL DEFAULT '',
			reference_text TEXT    NOT NULL DEFAULT '',
			description    TEXT    NOT NULL DEFAULT '',
			default_value  TEXT    NOT NULL DEFAULT '',
			augments       TEXT    NOT NULL DEFAULT '',
			index_columns  TEXT    NOT NULL DEFAULT '',
			enum_values    TEXT    NOT NULL DEFAULT '[]',
			source_line    INTEGER NOT NULL DEFAULT 0,
			UNIQUE (module_name, name)
		)`,
	} {
		if _, err := s.db.ExecContext(ctx, stmt); err != nil {
			t.Fatalf("simulate pre-migration shape: %s: %v", stmt, err)
		}
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close #1: %v", err)
	}

	// Reopen — migration must add the column non-destructively.
	s, err = Open(ctx, path)
	if err != nil {
		t.Fatalf("Open #2 (post-migration): %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	mod := &model.Module{
		Name: "TEST-MIB", OIDRoot: "1.3.6.1.4.1.99999",
		ParseStatus: model.ParseStatusClean,
	}
	syms := []model.Symbol{
		{
			ModuleName: "TEST-MIB", Name: "impliedEntry",
			OID: "1.3.6.1.4.1.99999.1.1", ParentOID: "1.3.6.1.4.1.99999.1",
			Kind: model.KindTableEntry, Syntax: "ImpliedEntry",
			IndexColumns: []string{"impliedKey"},
			IndexImplied: true,
		},
	}
	if err := s.ReplaceModule(ctx, mod, syms, nil, nil); err != nil {
		t.Fatalf("ReplaceModule after migration: %v", err)
	}
	got, err := s.GetSymbol(ctx, "TEST-MIB", "impliedEntry")
	if err != nil {
		t.Fatalf("GetSymbol post-migration: %v", err)
	}
	if !got.IndexImplied {
		t.Errorf("post-migration round-trip: IndexImplied = false, want true")
	}
}

// TestSymbolsAtOID pins the collision-lookup query: it returns every
// symbol a module defines at an OID (module-scoped, name-ordered), which
// the detail pane uses to warn about a MIB that assigns one OID twice.
func TestSymbolsAtOID(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	const oid = "1.3.6.1.4.1.5189.1.1"
	clean := func(name string, syms []model.Symbol) {
		if err := s.ReplaceModule(ctx,
			&model.Module{Name: name, ParseStatus: model.ParseStatusClean}, syms, nil, nil); err != nil {
			t.Fatalf("ReplaceModule(%s): %v", name, err)
		}
	}
	clean("ZBX", []model.Symbol{
		{ModuleName: "ZBX", Name: "zabbixEventTable", OID: oid, Kind: model.KindTable, Status: model.StatusCurrent},
		{ModuleName: "ZBX", Name: "zabbixAlertEvent", OID: oid, Kind: model.KindNotificationType, Status: model.StatusCurrent},
		{ModuleName: "ZBX", Name: "solo", OID: "1.3.6.1.4.1.5189.2", Kind: model.KindScalar, Status: model.StatusCurrent},
	})

	both, err := s.SymbolsAtOID(ctx, "ZBX", oid)
	if err != nil {
		t.Fatal(err)
	}
	if len(both) != 2 || both[0].Name != "zabbixAlertEvent" || both[1].Name != "zabbixEventTable" {
		t.Fatalf("SymbolsAtOID = %+v, want [zabbixAlertEvent, zabbixEventTable] name-sorted", both)
	}
	if one, _ := s.SymbolsAtOID(ctx, "ZBX", "1.3.6.1.4.1.5189.2"); len(one) != 1 {
		t.Errorf("unique OID = %d rows, want 1", len(one))
	}
	if none, _ := s.SymbolsAtOID(ctx, "ZBX", ""); none != nil {
		t.Errorf("empty OID should return nil, got %+v", none)
	}

	// Module-scoped: another module at the SAME OID does not leak in.
	clean("OTHER", []model.Symbol{
		{ModuleName: "OTHER", Name: "elsewhere", OID: oid, Kind: model.KindScalar, Status: model.StatusCurrent},
	})
	if got, _ := s.SymbolsAtOID(ctx, "ZBX", oid); len(got) != 2 {
		t.Errorf("ZBX at OID after OTHER added = %d, want 2 (module-scoped)", len(got))
	}
}

// TestForeignKeysSurviveConnectionLoss pins the DSN-vs-PRAGMA fix.
//
// database/sql discards a pooled connection whenever the driver marks
// it bad, which a cancelled request context routinely does. When the
// FK enforcement rode on a one-off `PRAGMA foreign_keys = ON`, the
// replacement connection came back with cascades DISABLED, so
// ReplaceModule's `DELETE FROM module` stopped taking the module's
// symbols with it — and the next re-import of that module died on
// UNIQUE (module_name, name). Carrying the pragma in the DSN makes
// every connection, original or replacement, enforce the cascade.
func TestForeignKeysSurviveConnectionLoss(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, filepath.Join(t.TempDir(), "fk.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = s.Close() }()

	mod := sampleModule()
	if err := s.ReplaceModule(ctx, mod, sampleSymbols(), nil, nil); err != nil {
		t.Fatalf("ReplaceModule #1: %v", err)
	}

	// A TEMP table lives in the connection's own temp schema, so its
	// survival is a direct probe of connection identity: if it is still
	// there afterwards, the pool handed back the SAME connection and the
	// test never exercised the regression.
	if _, err := s.db.ExecContext(ctx, `CREATE TEMP TABLE conn_sentinel(x)`); err != nil {
		t.Fatalf("create sentinel: %v", err)
	}

	// Kill the pooled connection the way a cancelled HTTP request does:
	// start a query too long to finish, then cancel it out from under
	// the driver. Retry until the sentinel is gone so a deadline that
	// expires before the query is dispatched cannot make this test pass
	// vacuously.
	replaced := false
	for i := 0; i < 50 && !replaced; i++ {
		cctx, cancel := context.WithTimeout(ctx, 20*time.Millisecond)
		//nolint:rowserrcheck // the query is cancelled on purpose; the rows are never consumed.
		_, _ = s.db.QueryContext(cctx, `WITH RECURSIVE c(x) AS (
			SELECT 1 UNION ALL SELECT x+1 FROM c WHERE x < 50000000)
			SELECT count(*) FROM c`)
		<-cctx.Done()
		cancel()
		err := s.db.QueryRowContext(ctx, `SELECT 1 FROM temp.conn_sentinel LIMIT 1`).Scan(new(int))
		replaced = err != nil && !errors.Is(err, sql.ErrNoRows)
	}
	if !replaced {
		t.Fatal("pooled connection was never replaced — test would pass vacuously")
	}

	var fk int
	if err := s.db.QueryRowContext(ctx, `PRAGMA foreign_keys`).Scan(&fk); err != nil {
		t.Fatalf("read foreign_keys: %v", err)
	}
	if fk != 1 {
		t.Errorf("foreign_keys = %d after connection loss, want 1", fk)
	}

	// The re-import that used to fail: the cascade must have cleared
	// the previous symbols before these are inserted.
	if err := s.ReplaceModule(ctx, mod, sampleSymbols(), nil, nil); err != nil {
		t.Fatalf("ReplaceModule #2 after connection loss: %v", err)
	}
}

// TestSweepOrphanedModuleRows covers the repair half: a database that
// already ran with cascades disabled carries symbol rows whose module
// is gone. Open must clear them, so the module re-imports cleanly.
func TestSweepOrphanedModuleRows(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "orphan.db")
	s, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("Open #1: %v", err)
	}
	if err := s.ReplaceModule(ctx, sampleModule(), sampleSymbols(), nil, nil); err != nil {
		t.Fatalf("ReplaceModule: %v", err)
	}
	// Simulate the damage: drop the module row with cascades off, the
	// way a connection that lost the pragma would have.
	if _, err := s.db.ExecContext(ctx, `PRAGMA foreign_keys = OFF`); err != nil {
		t.Fatalf("disable foreign_keys: %v", err)
	}
	if _, err := s.db.ExecContext(ctx, `DELETE FROM module WHERE name = 'IF-MIB'`); err != nil {
		t.Fatalf("delete module: %v", err)
	}
	var orphans int
	if err := s.db.QueryRowContext(ctx,
		`SELECT count(*) FROM symbol WHERE module_name = 'IF-MIB'`).Scan(&orphans); err != nil {
		t.Fatal(err)
	}
	if orphans == 0 {
		t.Fatal("setup did not produce orphaned symbol rows")
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// Reopen — the sweep runs and the orphans are gone.
	s2, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("Open #2: %v", err)
	}
	defer func() { _ = s2.Close() }()
	if err := s2.db.QueryRowContext(ctx,
		`SELECT count(*) FROM symbol WHERE module_name = 'IF-MIB'`).Scan(&orphans); err != nil {
		t.Fatal(err)
	}
	if orphans != 0 {
		t.Errorf("orphaned symbol rows after sweep = %d, want 0", orphans)
	}
	// The OID trie is a projection of the rows just deleted, so the
	// sweep must mark it stale — otherwise the swept symbols keep
	// showing up in the browser until an unrelated import rebuilds it.
	stale, err := s2.OIDTreeStale(ctx)
	if err != nil {
		t.Fatalf("OIDTreeStale: %v", err)
	}
	if !stale {
		t.Error("OID trie not marked stale after a sweep removed symbol rows")
	}
	if err := s2.ReplaceModule(ctx, sampleModule(), sampleSymbols(), nil, nil); err != nil {
		t.Errorf("re-import after sweep: %v", err)
	}
}

// TestOpenRejectsPathWithQuestionMark pins the DSN-splitting hazard:
// the driver cuts the DSN at the first '?', so such a path would open a
// DIFFERENT database with every pragma dropped — cascades off, silently.
func TestOpenRejectsPathWithQuestionMark(t *testing.T) {
	s, err := Open(context.Background(), filepath.Join(t.TempDir(), "q?mark.db"))
	if err == nil {
		_ = s.Close()
		t.Fatal("Open accepted a path containing '?'; want an error")
	}
	if !strings.Contains(err.Error(), "?") {
		t.Errorf("error should name the offending character, got: %v", err)
	}
}
