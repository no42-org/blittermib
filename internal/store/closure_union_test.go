package store

import (
	"context"
	"testing"

	"github.com/no42-org/blittermib/internal/model"
)

// seedClosureModule writes a bare module that imports a single symbol
// from each named upstream module — enough to exercise the IMPORTS
// graph walk without real MIB content.
func seedClosureModule(t *testing.T, s *Store, name string, imports ...string) {
	t.Helper()
	mod := &model.Module{
		Name:        name,
		OIDRoot:     "1.3.6.1.4.1.99." + name,
		ParseStatus: model.ParseStatusClean,
	}
	for _, from := range imports {
		mod.Imports = append(mod.Imports, model.Import{FromModule: from, Symbol: "x"})
	}
	if err := s.ReplaceModule(context.Background(), mod, nil, nil, nil); err != nil {
		t.Fatalf("seed %s: %v", name, err)
	}
}

func TestListImportClosureUnion(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)

	// Diamond: A -> B -> C and X -> C. The walk from [A, X] must
	// return {A, X, B, C} exactly — C is shared and appears once.
	seedClosureModule(t, s, "C")
	seedClosureModule(t, s, "B", "C")
	seedClosureModule(t, s, "A", "B")
	seedClosureModule(t, s, "X", "C")

	closure, err := s.ListImportClosureUnion(ctx, []string{"A", "X"})
	if err != nil {
		t.Fatalf("ListImportClosureUnion: %v", err)
	}

	// Verify BFS order, not just set membership: roots first in the
	// given order, then their imports. A -> B and X -> C, with C also
	// imported by B but reached first via X.
	gotOrder := make([]string, len(closure))
	importedBy := make(map[string]string, len(closure))
	for i, e := range closure {
		gotOrder[i] = e.Module
		importedBy[e.Module] = e.ImportedBy
	}
	wantOrder := []string{"A", "X", "B", "C"}
	if len(gotOrder) != len(wantOrder) {
		t.Fatalf("closure order = %v, want %v", gotOrder, wantOrder)
	}
	for i := range wantOrder {
		if gotOrder[i] != wantOrder[i] {
			t.Fatalf("closure order = %v, want %v", gotOrder, wantOrder)
		}
	}

	// Roots carry no ImportedBy; shared C is attributed to the first
	// parent that reached it (X, processed before B).
	if importedBy["A"] != "" || importedBy["X"] != "" {
		t.Errorf("roots should have empty ImportedBy, got A=%q X=%q",
			importedBy["A"], importedBy["X"])
	}
	if importedBy["B"] != "A" {
		t.Errorf("B.ImportedBy = %q, want A", importedBy["B"])
	}
	if importedBy["C"] != "X" {
		t.Errorf("C.ImportedBy = %q, want X (first parent to reach it)", importedBy["C"])
	}
}

// The single-root wrapper still behaves exactly as before.
func TestListImportClosureWrapper(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	seedClosureModule(t, s, "C")
	seedClosureModule(t, s, "B", "C")
	seedClosureModule(t, s, "A", "B")

	closure, err := s.ListImportClosure(ctx, "A")
	if err != nil {
		t.Fatalf("ListImportClosure: %v", err)
	}
	if len(closure) != 3 || closure[0].Module != "A" {
		t.Fatalf("single-root closure = %+v", closure)
	}
}
