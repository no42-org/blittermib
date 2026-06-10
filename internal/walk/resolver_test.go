package walk

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/no42-org/blittermib/internal/model"
	"github.com/no42-org/blittermib/internal/store"
)

func ifMIBStore(t *testing.T) *store.Store {
	t.Helper()
	ctx := context.Background()
	s, err := store.OpenInMemory(ctx)
	if err != nil {
		t.Fatalf("OpenInMemory: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	ifmib := &model.Module{
		Name: "IF-MIB", OIDRoot: "1.3.6.1.2.1.2",
		ParseStatus: model.ParseStatusClean,
	}
	ifSyms := []model.Symbol{
		{ModuleName: "IF-MIB", Name: "ifTable", OID: "1.3.6.1.2.1.2.2",
			ParentOID: "1.3.6.1.2.1.2", Kind: model.KindTable},
		{ModuleName: "IF-MIB", Name: "ifEntry", OID: "1.3.6.1.2.1.2.2.1",
			ParentOID: "1.3.6.1.2.1.2.2", Kind: model.KindTableEntry,
			IndexColumns: []string{"ifIndex"}},
		{ModuleName: "IF-MIB", Name: "ifIndex", OID: "1.3.6.1.2.1.2.2.1.1",
			ParentOID: "1.3.6.1.2.1.2.2.1", Kind: model.KindColumn, Syntax: "Integer32"},
		{ModuleName: "IF-MIB", Name: "ifInOctets", OID: "1.3.6.1.2.1.2.2.1.10",
			ParentOID: "1.3.6.1.2.1.2.2.1", Kind: model.KindColumn, Syntax: "Counter32"},
	}
	if err := s.ReplaceModule(ctx, ifmib, ifSyms, nil, nil); err != nil {
		t.Fatalf("seed IF-MIB: %v", err)
	}

	v2 := &model.Module{Name: "SNMPv2-MIB", OIDRoot: "1.3.6.1.2.1.1", ParseStatus: model.ParseStatusClean}
	v2Syms := []model.Symbol{
		{ModuleName: "SNMPv2-MIB", Name: "sysDescr", OID: "1.3.6.1.2.1.1.1",
			ParentOID: "1.3.6.1.2.1.1", Kind: model.KindScalar, Syntax: "DisplayString"},
	}
	if err := s.ReplaceModule(ctx, v2, v2Syms, nil, nil); err != nil {
		t.Fatalf("seed SNMPv2-MIB: %v", err)
	}
	return s
}

func TestWalkResolveAgainstStore(t *testing.T) {
	s := ifMIBStore(t)
	capture := `.1.3.6.1.2.1.1.1.0 = STRING: "Juniper SRX340"
.1.3.6.1.2.1.2.2.1.10.1 = Counter32: 84572301`

	rw, err := Resolve(context.Background(), Parse(capture), s)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if len(rw.Entries) != 2 {
		t.Fatalf("got %d resolved entries, want 2", len(rw.Entries))
	}

	// sysDescr scalar — resolves, suffix is the .0 instance, no index.
	sys := rw.Entries[0]
	if !sys.Resolved || sys.Module != "SNMPv2-MIB" || sys.Symbol != "sysDescr" {
		t.Errorf("sysDescr resolution = %+v", sys)
	}
	if sys.Suffix != "0" || sys.IndexDecode != "" {
		t.Errorf("sysDescr suffix/index = %q/%q, want 0/<empty>", sys.Suffix, sys.IndexDecode)
	}

	// ifInOctets column — single-INTEGER index decodes to ifIndex=1.
	col := rw.Entries[1]
	if !col.Resolved || col.Module != "IF-MIB" || col.Symbol != "ifInOctets" {
		t.Errorf("ifInOctets resolution = %+v", col)
	}
	if col.IndexDecode != "integer" || col.IndexName != "ifIndex" || col.IndexValue != "1" {
		t.Errorf("index decode = %q %s=%s, want integer ifIndex=1",
			col.IndexDecode, col.IndexName, col.IndexValue)
	}

	if len(rw.Modules) != 2 || rw.Modules[0] != "IF-MIB" || rw.Modules[1] != "SNMPv2-MIB" {
		t.Errorf("matched modules = %v, want [IF-MIB SNMPv2-MIB]", rw.Modules)
	}
}

// TestWalkResolveVendorExcerpt runs the resolver over a sanitized
// excerpt modelled on a real Juniper SRX capture, against a store that
// mirrors the live corpus's relevant shape: the SMI scaffolding
// (enterprises) plus a couple of standard mib-2 symbols, but no
// JUNIPER MIB. Standard OIDs must resolve; every vendor OID under PEN
// 2636 must fall through to the PEN hint rather than the enterprises
// scaffolding node.
func TestWalkResolveVendorExcerpt(t *testing.T) {
	ctx := context.Background()
	s, err := store.OpenInMemory(ctx)
	if err != nil {
		t.Fatalf("OpenInMemory: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	seed := func(mod *model.Module, syms []model.Symbol) {
		if err := s.ReplaceModule(ctx, mod, syms, nil, nil); err != nil {
			t.Fatalf("seed %s: %v", mod.Name, err)
		}
	}
	seed(&model.Module{Name: "RFC1155-SMI", OIDRoot: "1.3.6.1", ParseStatus: model.ParseStatusClean},
		[]model.Symbol{
			{ModuleName: "RFC1155-SMI", Name: "enterprises", OID: "1.3.6.1.4.1", ParentOID: "1.3.6.1.4", Kind: model.KindObjectIdentity},
			{ModuleName: "RFC1155-SMI", Name: "private", OID: "1.3.6.1.4", ParentOID: "1.3.6.1", Kind: model.KindObjectIdentity},
		})
	seed(&model.Module{Name: "SNMPv2-MIB", OIDRoot: "1.3.6.1.2.1.1", ParseStatus: model.ParseStatusClean},
		[]model.Symbol{
			{ModuleName: "SNMPv2-MIB", Name: "sysDescr", OID: "1.3.6.1.2.1.1.1", ParentOID: "1.3.6.1.2.1.1", Kind: model.KindScalar, Syntax: "DisplayString"},
			{ModuleName: "SNMPv2-MIB", Name: "sysName", OID: "1.3.6.1.2.1.1.5", ParentOID: "1.3.6.1.2.1.1", Kind: model.KindScalar, Syntax: "DisplayString"},
		})
	seed(&model.Module{Name: "IF-MIB", OIDRoot: "1.3.6.1.2.1.2", ParseStatus: model.ParseStatusClean},
		[]model.Symbol{
			{ModuleName: "IF-MIB", Name: "ifInOctets", OID: "1.3.6.1.2.1.2.2.1.10", ParentOID: "1.3.6.1.2.1.2.2.1", Kind: model.KindColumn, Syntax: "Counter32"},
		})

	raw, err := os.ReadFile(filepath.Join("testdata", "srx-excerpt.txt"))
	if err != nil {
		t.Fatalf("read excerpt: %v", err)
	}
	rw, err := Resolve(ctx, Parse(string(raw)), s)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	// At least the standard OIDs we seeded resolve.
	if len(rw.Modules) == 0 {
		t.Fatal("no modules resolved from the excerpt")
	}
	var sawResolved bool
	for _, re := range rw.Entries {
		if re.Resolved && (re.Module == "SNMPv2-MIB" || re.Module == "IF-MIB") {
			sawResolved = true
		}
		// Every vendor OID must be unresolved with the PEN 2636 hint —
		// never "resolved" to the enterprises scaffolding node.
		if strings.HasPrefix(re.Entry.Ident, "1.3.6.1.4.1.2636") {
			if re.Resolved {
				t.Errorf("vendor OID %s resolved to %s::%s, want PEN fallback",
					re.Entry.Ident, re.Module, re.Symbol)
			}
			if re.Unresolved == nil || re.Unresolved.EnterpriseID != 2636 {
				t.Errorf("vendor OID %s missing PEN 2636 hint: %+v", re.Entry.Ident, re.Unresolved)
			}
		}
	}
	if !sawResolved {
		t.Error("expected at least one standard mib-2 OID to resolve")
	}
}

// Regression for the smoke-test finding: the real corpus loads
// RFC1155-SMI / SNMPv2-SMI, which define `enterprises` (1.3.6.1.4.1)
// as a real symbol. A vendor OID whose own MIB isn't loaded must still
// fall through to the PEN hint rather than "resolving" to the bare
// enterprises scaffolding node.
func TestWalkResolveSkipsStructuralEnterprises(t *testing.T) {
	ctx := context.Background()
	s, err := store.OpenInMemory(ctx)
	if err != nil {
		t.Fatalf("OpenInMemory: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	smi := &model.Module{Name: "RFC1155-SMI", OIDRoot: "1.3.6.1", ParseStatus: model.ParseStatusClean}
	smiSyms := []model.Symbol{
		{ModuleName: "RFC1155-SMI", Name: "enterprises", OID: "1.3.6.1.4.1",
			ParentOID: "1.3.6.1.4", Kind: model.KindObjectIdentity},
		{ModuleName: "RFC1155-SMI", Name: "private", OID: "1.3.6.1.4",
			ParentOID: "1.3.6.1", Kind: model.KindObjectIdentity},
	}
	if err := s.ReplaceModule(ctx, smi, smiSyms, nil, nil); err != nil {
		t.Fatalf("seed RFC1155-SMI: %v", err)
	}

	rw, err := Resolve(ctx, Parse(".1.3.6.1.4.1.2636.3.1.2.0 = STRING: \"Juniper SRX340\""), s)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	e := rw.Entries[0]
	if e.Resolved {
		t.Fatalf("vendor OID resolved to %s::%s — should fall through to PEN", e.Module, e.Symbol)
	}
	if e.Unresolved == nil || e.Unresolved.EnterpriseID != 2636 {
		t.Fatalf("expected PEN 2636 hint, got %+v", e.Unresolved)
	}
}

// TestWalkResolveSkipsStructuralStandardArcs is the non-enterprise
// counterpart of the structural-enterprises regression: the live corpus
// loads SNMPv2-SMI, which defines mgmt/mib-2 as real symbols. An OID
// under an UNLOADED standard MIB must not "resolve" to the mib-2
// scaffolding node — it falls through to the canonical-arc guidance.
// A real (7-arc) scalar's `.0` instance, by contrast, still resolves:
// the structural rule requires the walked OID to extend the match by
// two or more arcs.
func TestWalkResolveSkipsStructuralStandardArcs(t *testing.T) {
	ctx := context.Background()
	s, err := store.OpenInMemory(ctx)
	if err != nil {
		t.Fatalf("OpenInMemory: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	smi := &model.Module{Name: "SNMPv2-SMI", OIDRoot: "1.3.6.1", ParseStatus: model.ParseStatusClean}
	smiSyms := []model.Symbol{
		{ModuleName: "SNMPv2-SMI", Name: "mgmt", OID: "1.3.6.1.2",
			ParentOID: "1.3.6.1", Kind: model.KindObjectIdentity},
		{ModuleName: "SNMPv2-SMI", Name: "mib-2", OID: "1.3.6.1.2.1",
			ParentOID: "1.3.6.1.2", Kind: model.KindObjectIdentity},
		// A walkable object at exactly 7 arcs — the structural cutoff.
		{ModuleName: "SNMPv2-SMI", Name: "shallowScalar", OID: "1.3.6.1.2.99",
			ParentOID: "1.3.6.1.2", Kind: model.KindScalar, Syntax: "Integer32"},
	}
	if err := s.ReplaceModule(ctx, smi, smiSyms, nil, nil); err != nil {
		t.Fatalf("seed SNMPv2-SMI: %v", err)
	}

	capture := `.1.3.6.1.2.1.131.1.1.0 = INTEGER: 1
.1.3.6.1.2.99.0 = INTEGER: 5`
	rw, err := Resolve(ctx, Parse(capture), s)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	// OID under an unloaded standard MIB: matching mib-2 is scaffolding,
	// not a resolution.
	deep := rw.Entries[0]
	if deep.Resolved {
		t.Fatalf("OID under unloaded MIB resolved to %s::%s — want canonical fallback",
			deep.Module, deep.Symbol)
	}
	if deep.Unresolved == nil || deep.Unresolved.CanonicalName != "mib-2" {
		t.Fatalf("expected canonical mib-2 hint, got %+v", deep.Unresolved)
	}

	// A shallow (≤7-arc) object's own .0 instance extends it by one arc
	// and must still resolve.
	shallow := rw.Entries[1]
	if !shallow.Resolved || shallow.Symbol != "shallowScalar" {
		t.Fatalf("shallow scalar instance = %+v, want resolved shallowScalar", shallow)
	}
}

// Name-prefixed records (default snmpwalk output without -On) resolve
// through the store and carry the numeric symbol OID + suffix, so
// downstream consumers (results summary, workspace overlay payload) can
// reconstruct the numeric instance OID.
func TestWalkResolveNamePrefixed(t *testing.T) {
	s := ifMIBStore(t)
	rw, err := Resolve(context.Background(),
		Parse("IF-MIB::ifInOctets.1 = Counter32: 84572301"), s)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	e := rw.Entries[0]
	if !e.Resolved || e.Module != "IF-MIB" || e.Symbol != "ifInOctets" {
		t.Fatalf("name-prefixed resolution = %+v", e)
	}
	if e.SymbolOID != "1.3.6.1.2.1.2.2.1.10" || e.Suffix != "1" {
		t.Errorf("SymbolOID/Suffix = %q/%q, want 1.3.6.1.2.1.2.2.1.10/1", e.SymbolOID, e.Suffix)
	}
}

func TestWalkResolveFallbackToPEN(t *testing.T) {
	// IF-MIB store has no Cisco MIB; a Cisco enterprise OID must fall
	// back to the PEN registry rather than resolving.
	s := ifMIBStore(t)
	rw, err := Resolve(context.Background(), Parse(".1.3.6.1.4.1.9.2.1.58.0 = INTEGER: 42"), s)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if len(rw.Entries) != 1 {
		t.Fatalf("got %d entries, want 1", len(rw.Entries))
	}
	e := rw.Entries[0]
	if e.Resolved {
		t.Fatalf("Cisco OID should not resolve against an IF-MIB-only store")
	}
	if e.Unresolved == nil {
		t.Fatal("expected Unresolved guidance")
	}
	if e.Unresolved.EnterpriseID != 9 || e.Unresolved.EnterpriseName != "ciscoSystems" {
		t.Errorf("PEN hint = %d/%q, want 9/ciscoSystems",
			e.Unresolved.EnterpriseID, e.Unresolved.EnterpriseName)
	}
	if e.Unresolved.CanonicalName != "enterprises" {
		t.Errorf("canonical = %q, want enterprises", e.Unresolved.CanonicalName)
	}
}
