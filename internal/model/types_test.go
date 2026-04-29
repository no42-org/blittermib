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
