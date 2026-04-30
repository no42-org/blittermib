package compile

import (
	"os"
	"testing"

	"github.com/no42-org/blittermib/internal/model"
)

// fixturePath points at a real smidump 0.5.0 XML dump captured from
// the IF-MIB shipped with libsmi. Using a real-world fixture instead
// of a hand-written one is load-bearing: an earlier hand-crafted
// fixture diverged from what smidump actually emits, masking a parser
// bug that produced 0 symbols against real input. See commit 4a26781.
//
// To refresh: `smidump -f xml -k <IF-MIB-path> > testdata/if-mib.xml`.
const fixturePath = "testdata/if-mib.xml"

func loadFixture(t *testing.T) *SMI {
	t.Helper()
	f, err := os.Open(fixturePath)
	if err != nil {
		t.Fatalf("open fixture: %v", err)
	}
	defer f.Close()
	smi, err := ParseXML(f)
	if err != nil {
		t.Fatalf("ParseXML: %v", err)
	}
	return smi
}

func TestParseAndConvert(t *testing.T) {
	smi := loadFixture(t)
	mod, syms := ToModel(smi)

	if mod.Name != "IF-MIB" {
		t.Errorf("module name = %q, want IF-MIB", mod.Name)
	}
	if mod.OIDRoot != "1.3.6.1.2.1.31" {
		t.Errorf("OIDRoot = %q, want 1.3.6.1.2.1.31", mod.OIDRoot)
	}
	if len(mod.Imports) == 0 {
		t.Error("imports empty; expected several from SNMPv2-SMI/SNMPv2-TC")
	}
	if len(mod.Revisions) == 0 {
		t.Error("revisions empty; IF-MIB has multiple")
	}

	// Ground-truth shape of real IF-MIB (libsmi 0.5.0). Numbers may
	// drift slightly across libsmi versions; assert lower bounds plus
	// non-zero on each kind we expect to see.
	byKind := map[model.SymbolKind]int{}
	byName := map[string]model.Symbol{}
	for _, s := range syms {
		byKind[s.Kind]++
		byName[s.Name] = s
	}

	if len(syms) < 80 {
		t.Errorf("symbol count = %d, want >= 80 (real IF-MIB has ~94)", len(syms))
	}
	for _, kind := range []model.SymbolKind{
		model.KindTextualConvention,
		model.KindModuleIdentity,
		model.KindObjectIdentity,
		model.KindObjectType,
		model.KindNotificationType,
		model.KindObjectGroup,
		model.KindModuleCompliance,
	} {
		if byKind[kind] == 0 {
			t.Errorf("no symbols of kind %q produced", kind)
		}
	}

	// Spot-check a column that exercises every nested-decode path:
	// inside <table>/<row>/<column>, with <syntax>, <access>, <units>.
	inOctets, ok := byName["ifInOctets"]
	if !ok {
		t.Fatal("ifInOctets symbol missing")
	}
	if inOctets.OID != "1.3.6.1.2.1.2.2.1.10" {
		t.Errorf("ifInOctets OID = %q", inOctets.OID)
	}
	if inOctets.ParentOID != "1.3.6.1.2.1.2.2.1" {
		t.Errorf("ifInOctets ParentOID = %q", inOctets.ParentOID)
	}
	if inOctets.Access != model.AccessReadOnly {
		t.Errorf("ifInOctets Access = %q, want read-only", inOctets.Access)
	}
	if inOctets.Syntax != "Counter32" {
		t.Errorf("ifInOctets Syntax = %q, want Counter32", inOctets.Syntax)
	}
	if got := inOctets.QualifiedName(); got != "IF-MIB::ifInOctets" {
		t.Errorf("qualified name = %q", got)
	}

	// IsTable / IsTableEntry / IndexColumns flow through the table path.
	ifTable, ok := byName["ifTable"]
	if !ok || !ifTable.IsTable {
		t.Error("ifTable should be present and have IsTable=true")
	}
	ifEntry, ok := byName["ifEntry"]
	if !ok {
		t.Fatal("ifEntry missing")
	}
	if !ifEntry.IsTableEntry {
		t.Error("ifEntry should have IsTableEntry=true")
	}
	if got, want := ifEntry.IndexColumns, []string{"ifIndex"}; !equalStrings(got, want) {
		t.Errorf("ifEntry IndexColumns = %v, want %v", got, want)
	}

	// MODULE-IDENTITY resolution.
	ifMIB, ok := byName["ifMIB"]
	if !ok {
		t.Fatal("ifMIB MODULE-IDENTITY symbol missing")
	}
	if ifMIB.Kind != model.KindModuleIdentity {
		t.Errorf("ifMIB Kind = %q, want module-identity", ifMIB.Kind)
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

func TestParentOID(t *testing.T) {
	cases := []struct{ in, want string }{
		{"1.3.6.1.2.1.2.2.1.10", "1.3.6.1.2.1.2.2.1"},
		{"1", ""},
		{"", ""},
	}
	for _, c := range cases {
		if got := parentOID(c.in); got != c.want {
			t.Errorf("parentOID(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestNormalizeAccess(t *testing.T) {
	cases := map[string]model.Access{
		"readonly":     model.AccessReadOnly,
		"readwrite":    model.AccessReadWrite,
		"readcreate":   model.AccessReadCreate,
		"noaccess":     model.AccessNotAccessible,
		"notifyonly":   model.AccessAccessibleNotify,
		"":             model.Access(""),
		"weird-thing":  model.Access("weird-thing"),
	}
	for in, want := range cases {
		if got := normalizeAccess(in); got != want {
			t.Errorf("normalizeAccess(%q) = %q, want %q", in, got, want)
		}
	}
}
