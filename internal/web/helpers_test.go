package web

import (
	"testing"

	"github.com/no42-org/blittermib/internal/model"
)

func TestOIDUnderPrefix(t *testing.T) {
	cases := []struct {
		name   string
		oid    string
		prefix string
		want   bool
	}{
		{"empty prefix matches anything", "1.3.6.1", "", true},
		{"empty oid matches empty prefix", "", "", true},
		{"empty oid does not match non-empty prefix", "", "1.3", false},
		{"exact match counts as under", "1.3", "1.3", true},
		{"strict prefix with dot boundary", "1.3.6", "1.3", true},
		{"deep prefix match", "1.3.6.1.2.1.2.2.1.10", "1.3.6.1.2.1.2.2.1", true},
		// The classic substring-of-different-OID trap. `1.3` must
		// not match `1.30.6` even though it's a byte-prefix.
		{"numeric substring trap rejected", "1.30.6", "1.3", false},
		{"numeric substring trap rejected (single segment)", "10.20", "1", false},
		// Prefix longer than oid is never a match.
		{"prefix longer than oid", "1", "1.3", false},
		// First-segment differences.
		{"different first segment", "2.3.6", "1.3.6", false},
		// Prefix with trailing dot is malformed but should not match.
		{"trailing-dot prefix does not match", "1.3.6", "1.3.", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := OIDUnderPrefix(c.oid, c.prefix)
			if got != c.want {
				t.Errorf("OIDUnderPrefix(%q, %q) = %v, want %v", c.oid, c.prefix, got, c.want)
			}
		})
	}
}

func TestIsEmptyCounts(t *testing.T) {
	if !isEmptyCounts(nil) {
		t.Error("nil FamilyCounts should be empty")
	}
	if !isEmptyCounts(&model.FamilyCounts{}) {
		t.Error("zero-value FamilyCounts should be empty")
	}
	if isEmptyCounts(&model.FamilyCounts{Counters: 1}) {
		t.Error("FamilyCounts with one Counter should not be empty")
	}
	if isEmptyCounts(&model.FamilyCounts{Structs: 1}) {
		t.Error("FamilyCounts with one Struct should not be empty")
	}
}