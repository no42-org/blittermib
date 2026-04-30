package compile

import "github.com/no42-org/blittermib/internal/model"

// BuildReferences walks parsed SMI modules and extracts every
// cross-reference the UI surfaces ("Used by", "Augmented by",
// "Indexed by", "Compliance", "Notification objects").
//
// References are returned with qualified Module + Name keys, which
// the store persists directly without resolving to integer IDs —
// keeping hot-reload semantics simple.
//
// SYNTAX → TC references are intentionally not yet emitted: doing so
// requires distinguishing built-in SMI primitives (Counter32, OCTET
// STRING, …) from user TCs, which lives in a future capability.
func BuildReferences(parsed []*SMI) []model.Reference {
	var refs []model.Reference
	for _, smi := range parsed {
		mod := smi.Module.Name

		// INDEX / AUGMENTS only appear on table rows in smidump's output.
		for _, tbl := range smi.Nodes.Tables {
			if tbl.Row == nil || tbl.Row.Linkage == nil {
				continue
			}
			row := tbl.Row
			if row.Linkage.Augments != nil {
				refs = append(refs, model.Reference{
					SourceModule: mod, SourceName: row.Name,
					TargetModule: row.Linkage.Augments.Module,
					TargetName:   row.Linkage.Augments.Name,
					Kind:         model.RefAugments,
				})
			}
			for _, idx := range row.Linkage.Index {
				refs = append(refs, model.Reference{
					SourceModule: mod, SourceName: row.Name,
					TargetModule: idx.Module, TargetName: idx.Name,
					Kind: model.RefIndex,
				})
			}
		}

		for _, nt := range smi.Notifications {
			for _, obj := range nt.Objects {
				refs = append(refs, model.Reference{
					SourceModule: mod, SourceName: nt.Name,
					TargetModule: obj.Module, TargetName: obj.Name,
					Kind: model.RefNotificationObject,
				})
			}
		}

		for _, g := range smi.Groups {
			for _, m := range g.Members {
				refs = append(refs, model.Reference{
					SourceModule: mod, SourceName: g.Name,
					TargetModule: m.Module, TargetName: m.Name,
					Kind: model.RefGroupMember,
				})
			}
		}

		for _, c := range smi.Compliances {
			for _, m := range c.Mandatory {
				refs = append(refs, model.Reference{
					SourceModule: mod, SourceName: c.Name,
					TargetModule: m.Module, TargetName: m.Name,
					Kind: model.RefComplianceObject,
				})
			}
			for _, o := range c.Options {
				refs = append(refs, model.Reference{
					SourceModule: mod, SourceName: c.Name,
					TargetModule: o.Module, TargetName: o.Name,
					Kind: model.RefComplianceObject,
				})
			}
		}
	}
	return refs
}
