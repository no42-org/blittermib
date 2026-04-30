package compile

import (
	"testing"

	"github.com/no42-org/blittermib/internal/model"
)

func TestBuildReferences(t *testing.T) {
	smi := loadFixture(t)
	refs := BuildReferences([]*SMI{smi})

	want := map[string]model.ReferenceKind{
		// INDEX from ifEntry's <linkage>.
		"IF-MIB::ifEntry|IF-MIB::ifIndex": model.RefIndex,
		// linkUp NOTIFICATION-TYPE OBJECTS list.
		"IF-MIB::linkUp|IF-MIB::ifIndex": model.RefNotificationObject,
		// ifPacketGroup OBJECT-GROUP membership.
		"IF-MIB::ifPacketGroup|IF-MIB::ifInOctets": model.RefGroupMember,
		// ifCompliance3 references ifPacketGroup as an OPTIONAL group;
		// our parser emits both <mandatory> and <option> entries as
		// RefComplianceObject.
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
