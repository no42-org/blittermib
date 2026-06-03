/*
 * Copyright 2026 Ronny Trommer <ronny@no42.org>
 * SPDX-License-Identifier: MIT
 */

package store

import (
	"context"
	"testing"

	"github.com/no42-org/blittermib/internal/model"
)

// TestListNotificationsWithObjects seeds a module whose notification
// references two objects whose OBJECTS-clause order is the reverse of
// their alphabetical order, proving the query honors `position` rather
// than name. It also checks kind/access are carried through.
func TestListNotificationsWithObjects(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()

	mod := &model.Module{Name: "TEST-MIB", OIDRoot: "1.3.6.1.4.1.99", ParseStatus: model.ParseStatusClean}
	syms := []model.Symbol{
		{ModuleName: "TEST-MIB", Name: "alarmRaised", OID: "1.3.6.1.4.1.99.0.1", Kind: model.KindNotificationType},
		{ModuleName: "TEST-MIB", Name: "zStatus", OID: "1.3.6.1.4.1.99.1.1", Kind: model.KindScalar, Access: model.AccessAccessibleNotify},
		{ModuleName: "TEST-MIB", Name: "aValue", OID: "1.3.6.1.4.1.99.2.1.1", Kind: model.KindColumn, Access: model.AccessReadOnly},
	}
	// zStatus is OBJECTS position 0, aValue position 1 — reverse of
	// alphabetical order.
	refs := []model.Reference{
		{SourceModule: "TEST-MIB", SourceName: "alarmRaised", TargetModule: "TEST-MIB", TargetName: "zStatus", Kind: model.RefNotificationObject, Position: 0},
		{SourceModule: "TEST-MIB", SourceName: "alarmRaised", TargetModule: "TEST-MIB", TargetName: "aValue", Kind: model.RefNotificationObject, Position: 1},
	}
	if err := s.ReplaceModule(ctx, mod, syms, refs, nil); err != nil {
		t.Fatalf("ReplaceModule: %v", err)
	}

	notifs, err := s.ListNotificationsWithObjects(ctx, "TEST-MIB")
	if err != nil {
		t.Fatalf("ListNotificationsWithObjects: %v", err)
	}
	if len(notifs) != 1 {
		t.Fatalf("got %d notifications, want 1", len(notifs))
	}
	n := notifs[0]
	if n.Symbol.Name != "alarmRaised" {
		t.Errorf("notification = %q", n.Symbol.Name)
	}
	if len(n.Objects) != 2 {
		t.Fatalf("got %d objects, want 2", len(n.Objects))
	}
	if n.Objects[0].Name != "zStatus" || n.Objects[1].Name != "aValue" {
		t.Errorf("object order = [%s, %s], want [zStatus, aValue]", n.Objects[0].Name, n.Objects[1].Name)
	}
	if n.Objects[0].Kind != model.KindScalar || n.Objects[0].Access != model.AccessAccessibleNotify {
		t.Errorf("zStatus kind/access = %s/%s", n.Objects[0].Kind, n.Objects[0].Access)
	}
	if n.Objects[1].Kind != model.KindColumn {
		t.Errorf("aValue kind = %s, want column", n.Objects[1].Kind)
	}
}
