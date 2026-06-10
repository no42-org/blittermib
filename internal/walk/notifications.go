package walk

import (
	"context"
	"sort"

	"github.com/no42-org/blittermib/internal/store"
)

// NotifModule names a module the walk proves relevant to the device
// and the number of NOTIFICATION-TYPE / TRAP-TYPE symbols it defines.
type NotifModule struct {
	Module string
	Count  int
}

// NotificationSummary lists which of the walk's matched modules — and
// the union of their import closures — define notifications or traps.
//
// This is derived from the module set, never from walk content: an
// snmpwalk captures only accessible objects and never carries a
// notification definition, so the summary answers "which MIBs would
// you load to decode traps this device might send", not "which traps
// were observed". It reuses Store.ListNotificationsWithObjects (the
// same data behind the eventconf export) and introduces no new
// walk-derived state.
func NotificationSummary(ctx context.Context, s *store.Store, modules []string) ([]NotifModule, error) {
	if len(modules) == 0 {
		return nil, nil
	}

	closure, err := s.ListImportClosureUnion(ctx, modules)
	if err != nil {
		return nil, err
	}

	var out []NotifModule
	for _, e := range closure {
		if !e.Loaded {
			continue
		}
		notifs, err := s.ListNotificationsWithObjects(ctx, e.Module)
		if err != nil {
			return nil, err
		}
		if len(notifs) > 0 {
			out = append(out, NotifModule{Module: e.Module, Count: len(notifs)})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Module < out[j].Module })
	return out, nil
}
