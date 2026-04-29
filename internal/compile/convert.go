package compile

import (
	"strings"

	"github.com/no42-org/blittermib/internal/model"
)

// ToModel converts a parsed smidump SMI document into a normalized
// blittermib model.Module + the symbols it contains.
//
// Cross-references between symbols (INDEX, AUGMENTS, OBJECT-GROUP
// members, MODULE-COMPLIANCE objects, etc.) are extracted in a
// separate pass once all modules have been parsed; see BuildReferences.
func ToModel(smi *SMI) (*model.Module, []model.Symbol) {
	mod := &model.Module{
		Name:         smi.Module.Name,
		Organization: strings.TrimSpace(smi.Module.Organization),
		ContactInfo:  strings.TrimSpace(smi.Module.Contact),
		Description:  strings.TrimSpace(smi.Module.Description),
		SourcePath:   smi.Module.Path,
	}

	if len(smi.Module.Revisions) > 0 {
		mod.LastUpdated = smi.Module.Revisions[0].Date
		mod.Revisions = make([]model.Revision, len(smi.Module.Revisions))
		for i, r := range smi.Module.Revisions {
			mod.Revisions[i] = model.Revision{
				When:        r.Date,
				Description: strings.TrimSpace(r.Description),
			}
		}
	}

	if smi.Module.Identity != nil {
		for _, n := range smi.Module.Nodes {
			if n.Name == smi.Module.Identity.Name {
				mod.OIDRoot = n.OID
				break
			}
		}
	}

	for _, imp := range smi.Module.Imports.Imports {
		mod.Imports = append(mod.Imports, model.Import{
			FromModule: imp.Module,
			Symbol:     imp.Name,
		})
	}

	var syms []model.Symbol

	for _, t := range smi.Module.Typedefs {
		syms = append(syms, model.Symbol{
			ModuleName:   smi.Module.Name,
			Name:         t.Name,
			Kind:         model.KindTextualConvention,
			Syntax:       renderTypedefSyntax(t),
			Status:       model.Status(t.Status),
			Description:  strings.TrimSpace(t.Description),
			Reference:    strings.TrimSpace(t.Reference),
			DefaultValue: t.Default,
			SourceLine:   t.Line,
		})
	}

	for _, n := range smi.Module.Nodes {
		kind := nodeKind(n.NodeType)
		if smi.Module.Identity != nil && n.Name == smi.Module.Identity.Name {
			kind = model.KindModuleIdentity
		}
		sym := model.Symbol{
			ModuleName:   smi.Module.Name,
			Name:         n.Name,
			OID:          n.OID,
			ParentOID:    parentOID(n.OID),
			Kind:         kind,
			Access:       model.Access(n.Access),
			Status:       model.Status(n.Status),
			Units:        n.Units,
			Reference:    strings.TrimSpace(n.Reference),
			Description:  strings.TrimSpace(n.Description),
			DefaultValue: n.Default,
			SourceLine:   n.Line,
			IsTable:      n.NodeType == "table",
			IsTableEntry: n.NodeType == "row",
		}
		if n.Syntax != nil {
			sym.Syntax = renderSyntax(n.Syntax)
		}
		if n.Linkage != nil {
			if n.Linkage.Augments != nil {
				sym.Augments = qualified(n.Linkage.Augments.Module, n.Linkage.Augments.Name)
			}
			for _, idx := range n.Linkage.Index {
				sym.IndexColumns = append(sym.IndexColumns, idx.Name)
			}
		}
		syms = append(syms, sym)
	}

	for _, nt := range smi.Module.Notifications {
		syms = append(syms, model.Symbol{
			ModuleName:  smi.Module.Name,
			Name:        nt.Name,
			OID:         nt.OID,
			ParentOID:   parentOID(nt.OID),
			Kind:        model.KindNotificationType,
			Status:      model.Status(nt.Status),
			Description: strings.TrimSpace(nt.Description),
			Reference:   strings.TrimSpace(nt.Reference),
			SourceLine:  nt.Line,
		})
	}

	for _, g := range smi.Module.Groups {
		kind := model.KindObjectGroup
		if g.GroupType == "notification" {
			kind = model.KindNotificationGroup
		}
		syms = append(syms, model.Symbol{
			ModuleName:  smi.Module.Name,
			Name:        g.Name,
			OID:         g.OID,
			ParentOID:   parentOID(g.OID),
			Kind:        kind,
			Status:      model.Status(g.Status),
			Description: strings.TrimSpace(g.Description),
			Reference:   strings.TrimSpace(g.Reference),
			SourceLine:  g.Line,
		})
	}

	for _, c := range smi.Module.Compliances {
		syms = append(syms, model.Symbol{
			ModuleName:  smi.Module.Name,
			Name:        c.Name,
			OID:         c.OID,
			ParentOID:   parentOID(c.OID),
			Kind:        model.KindModuleCompliance,
			Status:      model.Status(c.Status),
			Description: strings.TrimSpace(c.Description),
			Reference:   strings.TrimSpace(c.Reference),
			SourceLine:  c.Line,
		})
	}

	return mod, syms
}

func nodeKind(nodeType string) model.SymbolKind {
	switch nodeType {
	case "node":
		return model.KindObjectIdentity
	case "scalar", "column", "table", "row":
		return model.KindObjectType
	default:
		return model.KindObjectType
	}
}

func parentOID(oid string) string {
	if oid == "" {
		return ""
	}
	if i := strings.LastIndex(oid, "."); i > 0 {
		return oid[:i]
	}
	return ""
}

func qualified(module, name string) string {
	if module == "" {
		return name
	}
	return module + "::" + name
}

// renderSyntax produces a short, human-readable rendering of an SMI SYNTAX
// clause. Constraints and enumerations are not yet expanded — the type
// name (or referenced TC) is sufficient for the symbol-table view; the
// detail page will fetch the full SYNTAX from the source.
func renderSyntax(s *XMLSyntax) string {
	if s == nil {
		return ""
	}
	if s.Type != nil {
		return s.Type.Name
	}
	if s.Typedef != nil {
		return s.Typedef.Name
	}
	return ""
}

func renderTypedefSyntax(t XMLTypedef) string {
	if t.Syntax.Type != nil {
		return t.Syntax.Type.Name
	}
	return t.BaseType
}
