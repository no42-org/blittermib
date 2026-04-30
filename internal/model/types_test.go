package model

import "testing"

func TestQualifiedName(t *testing.T) {
	s := Symbol{ModuleName: "IF-MIB", Name: "ifInOctets"}
	got := s.QualifiedName()
	want := "IF-MIB::ifInOctets"
	if got != want {
		t.Errorf("QualifiedName() = %q, want %q", got, want)
	}
}

func TestTypeFamily(t *testing.T) {
	cases := []struct {
		name    string
		kind    SymbolKind
		syntax  string
		isIndex bool
		want    string
	}{
		{"notification beats syntax", KindNotificationType, "Counter32", false, "t-notif"},
		{"table is struct", KindTable, "SEQUENCE OF IfEntry", false, "t-struct"},
		{"entry is struct", KindTableEntry, "IfEntry", false, "t-struct"},
		{"object-identity is struct", KindObjectIdentity, "", false, "t-struct"},
		{"module-identity is struct", KindModuleIdentity, "", false, "t-struct"},
		{"index column overrides syntax", KindColumn, "InterfaceIndex", true, "t-index"},
		{"counter32", KindColumn, "Counter32", false, "t-counter"},
		{"counter64", KindScalar, "Counter64", false, "t-counter"},
		{"gauge32", KindScalar, "Gauge32", false, "t-gauge"},
		{"unsigned32", KindColumn, "Unsigned32", false, "t-gauge"},
		{"integer32", KindScalar, "Integer32", false, "t-int"},
		{"INTEGER bare", KindScalar, "INTEGER", false, "t-int"},
		{"DisplayString", KindColumn, "DisplayString", false, "t-text"},
		{"OCTET STRING", KindScalar, "OCTET STRING", false, "t-text"},
		{"TimeTicks", KindScalar, "TimeTicks", false, "t-time"},
		{"IpAddress", KindScalar, "IpAddress", false, "t-addr"},
		{"MacAddress", KindColumn, "MacAddress", false, "t-addr"},
		{"PhysAddress", KindColumn, "PhysAddress", false, "t-addr"},
		{"TruthValue", KindScalar, "TruthValue", false, "t-bool"},
		{"InterfaceIndex without flag", KindColumn, "InterfaceIndex", false, "t-index"},
		{"RowPointer", KindColumn, "RowPointer", false, "t-index"},
		{"unknown syntax falls back to struct", KindScalar, "WeirdType", false, "t-struct"},
		{"empty syntax", KindScalar, "", false, "t-struct"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := TypeFamily(c.kind, c.syntax, c.isIndex)
			if got != c.want {
				t.Errorf("TypeFamily(%q, %q, %v) = %q, want %q",
					c.kind, c.syntax, c.isIndex, got, c.want)
			}
		})
	}
}
