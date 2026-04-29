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

		for _, n := range smi.Module.Nodes {
			if n.Linkage == nil {
				continue
			}
			if n.Linkage.Augments != nil {
				refs = append(refs, model.Reference{
					SourceModule: mod, SourceName: n.Name,
					TargetModule: n.Linkage.Augments.Module,
					TargetName:   n.Linkage.Augments.Name,
					Kind:         model.RefAugments,
				})
			}
			for _, idx := range n.Linkage.Index {
				refs = append(refs, model.Reference{
					SourceModule: mod, SourceName: n.Name,
					TargetModule: idx.Module, TargetName: idx.Name,
					Kind: model.RefIndex,
				})
			}
		}

		for _, nt := range smi.Module.Notifications {
			for _, obj := range nt.Objects {
				refs = append(refs, model.Reference{
					SourceModule: mod, SourceName: nt.Name,
					TargetModule: obj.Module, TargetName: obj.Name,
					Kind: model.RefNotificationObject,
				})
			}
		}

		for _, g := range smi.Module.Groups {
			for _, m := range g.Members {
				refs = append(refs, model.Reference{
					SourceModule: mod, SourceName: g.Name,
					TargetModule: m.Module, TargetName: m.Name,
					Kind: model.RefGroupMember,
				})
			}
		}

		for _, c := range smi.Module.Compliances {
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
