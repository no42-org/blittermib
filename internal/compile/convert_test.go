package compile

import (
	"strings"
	"testing"

	"github.com/no42-org/blittermib/internal/model"
)

// fixtureXML mirrors the structure smidump 0.5.0 actually emits:
// `<imports>`, `<typedefs>`, `<nodes>`, `<notifications>`, `<groups>`,
// `<compliances>` are siblings of `<module>` (under `<smi>`), the
// module-identity is `<identity node="X"/>`, and `<nodes>` is a
// heterogeneous container of `<node>`/`<scalar>`/`<table>` (with
// `<row>`/`<column>` nested in tables).
const fixtureXML = `<?xml version="1.0"?>
<smi version="2.0">
  <module name="IF-MIB" language="SMIv2">
    <organization>IETF Interfaces MIB Working Group</organization>
    <contact>WG email: ietfmibs@ops.ietf.org</contact>
    <description>The MIB module to describe generic objects for network interface sub-layers.</description>
    <revision date="2007-09-29 00:00">
      <description>Updated to incorporate clarifications.</description>
    </revision>
    <identity node="ifMIB"/>
  </module>

  <imports>
    <import module="SNMPv2-SMI" name="MODULE-IDENTITY"/>
    <import module="SNMPv2-SMI" name="OBJECT-TYPE"/>
    <import module="SNMPv2-SMI" name="Counter32"/>
  </imports>

  <typedefs>
    <typedef name="OwnerString" basetype="OctetString" status="current">
      <range min="0" max="255"/>
      <description>An owner string.</description>
    </typedef>
  </typedefs>

  <nodes>
    <node name="ifMIB" oid="1.3.6.1.2.1.31" status="current"/>
    <table name="ifTable" oid="1.3.6.1.2.1.2.2" status="current">
      <description>A list of interface entries.</description>
      <row name="ifEntry" oid="1.3.6.1.2.1.2.2.1" status="current">
        <linkage>
          <index module="IF-MIB" name="ifIndex"/>
        </linkage>
        <description>An entry containing interface information.</description>
        <column name="ifIndex" oid="1.3.6.1.2.1.2.2.1.1" status="current">
          <syntax><type module="IF-MIB" name="InterfaceIndex"/></syntax>
          <access>readonly</access>
          <description>A unique value for each interface.</description>
        </column>
        <column name="ifInOctets" oid="1.3.6.1.2.1.2.2.1.10" status="current">
          <syntax><type module="SNMPv2-SMI" name="Counter32"/></syntax>
          <access>readonly</access>
          <units>octets</units>
          <description>The total number of octets received on the interface.</description>
        </column>
      </row>
    </table>
  </nodes>

  <notifications>
    <notification name="linkUp" oid="1.3.6.1.6.3.1.1.5.4" status="current">
      <objects>
        <object module="IF-MIB" name="ifIndex"/>
      </objects>
      <description>A linkUp trap signifies that the SNMP entity has detected an interface coming up.</description>
    </notification>
  </notifications>

  <groups>
    <group name="ifPacketGroup" oid="1.3.6.1.2.1.31.3" status="current" type="object">
      <members>
        <member module="IF-MIB" name="ifInOctets"/>
      </members>
      <description>The collection of objects providing packet statistics.</description>
    </group>
  </groups>

  <compliances>
    <compliance name="ifCompliance3" oid="1.3.6.1.2.1.31.4" status="current">
      <description>The compliance statement for SNMP entities supporting IF-MIB.</description>
      <requires>
        <mandatory module="IF-MIB" name="ifPacketGroup"/>
      </requires>
    </compliance>
  </compliances>
</smi>`

func TestParseAndConvert(t *testing.T) {
	smi, err := ParseXML(strings.NewReader(fixtureXML))
	if err != nil {
		t.Fatalf("ParseXML: %v", err)
	}

	mod, syms := ToModel(smi)

	if mod.Name != "IF-MIB" {
		t.Errorf("module name = %q, want IF-MIB", mod.Name)
	}
	if mod.OIDRoot != "1.3.6.1.2.1.31" {
		t.Errorf("OIDRoot = %q, want 1.3.6.1.2.1.31", mod.OIDRoot)
	}
	if got, want := len(mod.Imports), 3; got != want {
		t.Errorf("imports = %d, want %d", got, want)
	}
	if got, want := len(mod.Revisions), 1; got != want {
		t.Errorf("revisions = %d, want %d", got, want)
	}

	want := map[string]model.SymbolKind{
		"OwnerString":   model.KindTextualConvention,
		"ifMIB":         model.KindModuleIdentity,
		"ifTable":       model.KindObjectType,
		"ifEntry":       model.KindObjectType,
		"ifInOctets":    model.KindObjectType,
		"linkUp":        model.KindNotificationType,
		"ifPacketGroup": model.KindObjectGroup,
		"ifCompliance3": model.KindModuleCompliance,
	}

	got := map[string]model.SymbolKind{}
	for _, s := range syms {
		got[s.Name] = s.Kind
	}

	for name, kind := range want {
		if got[name] != kind {
			t.Errorf("symbol %q kind = %q, want %q", name, got[name], kind)
		}
	}

	var inOctets *model.Symbol
	var ifTable *model.Symbol
	var ifEntry *model.Symbol
	for i := range syms {
		switch syms[i].Name {
		case "ifInOctets":
			inOctets = &syms[i]
		case "ifTable":
			ifTable = &syms[i]
		case "ifEntry":
			ifEntry = &syms[i]
		}
	}

	if inOctets == nil {
		t.Fatal("ifInOctets symbol missing")
	}
	if inOctets.OID != "1.3.6.1.2.1.2.2.1.10" {
		t.Errorf("ifInOctets OID = %q", inOctets.OID)
	}
	if inOctets.ParentOID != "1.3.6.1.2.1.2.2.1" {
		t.Errorf("ifInOctets ParentOID = %q", inOctets.ParentOID)
	}
	if inOctets.Access != model.AccessReadOnly {
		t.Errorf("ifInOctets Access = %q", inOctets.Access)
	}
	if inOctets.Units != "octets" {
		t.Errorf("ifInOctets Units = %q", inOctets.Units)
	}
	if inOctets.Syntax != "Counter32" {
		t.Errorf("ifInOctets Syntax = %q", inOctets.Syntax)
	}

	if ifTable == nil || !ifTable.IsTable {
		t.Error("ifTable should have IsTable=true")
	}
	if ifEntry == nil {
		t.Fatal("ifEntry missing")
	}
	if !ifEntry.IsTableEntry {
		t.Error("ifEntry should have IsTableEntry=true")
	}
	if got, want := ifEntry.IndexColumns, []string{"ifIndex"}; !equalStrings(got, want) {
		t.Errorf("ifEntry IndexColumns = %v, want %v", got, want)
	}

	if got := inOctets.QualifiedName(); got != "IF-MIB::ifInOctets" {
		t.Errorf("qualified name = %q", got)
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
