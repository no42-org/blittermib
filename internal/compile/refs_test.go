package compile

import (
	"strings"
	"testing"

	"github.com/no42-org/blittermib/internal/model"
)

func TestBuildReferences(t *testing.T) {
	smi, err := ParseXML(strings.NewReader(fixtureXML))
	if err != nil {
		t.Fatalf("ParseXML: %v", err)
	}
	refs := BuildReferences([]*SMI{smi})

	want := map[string]model.ReferenceKind{
		"IF-MIB::ifEntry|IF-MIB::ifIndex":             model.RefIndex,
		"IF-MIB::linkUp|IF-MIB::ifIndex":              model.RefNotificationObject,
		"IF-MIB::ifPacketGroup|IF-MIB::ifInOctets":    model.RefGroupMember,
		"IF-MIB::ifCompliance3|IF-MIB::ifPacketGroup": model.RefComplianceObject,
	}

	got := map[string]model.ReferenceKind{}
	for _, r := range refs {
		got[r.SourceQualifiedName()+"|"+r.TargetQualifiedName()] = r.Kind
	}

	for k, kind := range want {
		if got[k] != kind {
			t.Errorf("ref %s: got %q, want %q", k, got[k], kind)
		}
	}
}
