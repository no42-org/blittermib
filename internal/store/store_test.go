package store

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/no42-org/blittermib/internal/model"
)

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
	defer s.Close()

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
	defer s2.Close()

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
