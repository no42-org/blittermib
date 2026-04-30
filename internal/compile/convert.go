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

	identityName := ""
	if smi.Module.Identity != nil {
		identityName = smi.Module.Identity.Node
	}

	// MODULE-IDENTITY usually appears as a plain <node> in <nodes>;
	// resolve OIDRoot by name match across the heterogeneous node tags.
	if identityName != "" {
		mod.OIDRoot = findOIDByName(smi, identityName)
	}

	for _, imp := range smi.Imports.Imports {
		mod.Imports = append(mod.Imports, model.Import{
			FromModule: imp.Module,
			Symbol:     imp.Name,
		})
	}

	var syms []model.Symbol

	for _, t := range smi.Typedefs {
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

	for _, n := range smi.Nodes.Plain {
		kind := model.KindObjectIdentity
		if n.Name == identityName {
			kind = model.KindModuleIdentity
		}
		syms = append(syms, nodeToSymbol(smi.Module.Name, n, kind, false, false))
	}

	for _, n := range smi.Nodes.Scalars {
		kind := model.KindObjectType
		if n.Name == identityName {
			kind = model.KindModuleIdentity
		}
		syms = append(syms, nodeToSymbol(smi.Module.Name, n, kind, false, false))
	}

	for _, tbl := range smi.Nodes.Tables {
		syms = append(syms, model.Symbol{
			ModuleName:  smi.Module.Name,
			Name:        tbl.Name,
			OID:         tbl.OID,
			ParentOID:   parentOID(tbl.OID),
			Kind:        model.KindObjectType,
			Status:      model.Status(tbl.Status),
			Description: strings.TrimSpace(tbl.Description),
			Reference:   strings.TrimSpace(tbl.Reference),
			SourceLine:  tbl.Line,
			IsTable:     true,
		})
		if tbl.Row == nil {
			continue
		}
		row := tbl.Row
		rowSym := model.Symbol{
			ModuleName:   smi.Module.Name,
			Name:         row.Name,
			OID:          row.OID,
			ParentOID:    parentOID(row.OID),
			Kind:         model.KindObjectType,
			Status:       model.Status(row.Status),
			Description:  strings.TrimSpace(row.Description),
			Reference:    strings.TrimSpace(row.Reference),
			SourceLine:   row.Line,
			IsTableEntry: true,
		}
		if row.Linkage != nil {
			if row.Linkage.Augments != nil {
				rowSym.Augments = qualified(row.Linkage.Augments.Module, row.Linkage.Augments.Name)
			}
			for _, idx := range row.Linkage.Index {
				rowSym.IndexColumns = append(rowSym.IndexColumns, idx.Name)
			}
		}
		syms = append(syms, rowSym)
		for _, c := range row.Columns {
			syms = append(syms, nodeToSymbol(smi.Module.Name, c, model.KindObjectType, false, false))
		}
	}

	for _, nt := range smi.Notifications {
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

	for _, g := range smi.Groups {
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

	for _, c := range smi.Compliances {
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

func nodeToSymbol(moduleName string, n XMLNode, kind model.SymbolKind, isTable, isTableEntry bool) model.Symbol {
	sym := model.Symbol{
		ModuleName:   moduleName,
		Name:         n.Name,
		OID:          n.OID,
		ParentOID:    parentOID(n.OID),
		Kind:         kind,
		Access:       normalizeAccess(n.Access),
		Status:       model.Status(n.Status),
		Units:        n.Units,
		Reference:    strings.TrimSpace(n.Reference),
		Description:  strings.TrimSpace(n.Description),
		DefaultValue: n.Default,
		SourceLine:   n.Line,
		IsTable:      isTable,
		IsTableEntry: isTableEntry,
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
	return sym
}

func findOIDByName(smi *SMI, name string) string {
	for _, n := range smi.Nodes.Plain {
		if n.Name == name {
			return n.OID
		}
	}
	for _, n := range smi.Nodes.Scalars {
		if n.Name == name {
			return n.OID
		}
	}
	for _, t := range smi.Nodes.Tables {
		if t.Name == name {
			return t.OID
		}
	}
	return ""
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
		if s.Typedef.Name != "" {
			return s.Typedef.Name
		}
		return s.Typedef.BaseType
	}
	return ""
}

func renderTypedefSyntax(t XMLTypedef) string {
	return t.BaseType
}

// normalizeAccess maps smidump's compact MAX-ACCESS tokens to the
// hyphenated SMIv2 spelling that the model.Access constants use.
// smidump emits readonly/readwrite/readcreate/noaccess/notifyonly;
// SMIv2 surfaces them as read-only/read-write/read-create/not-accessible/
// accessible-for-notify. Unknown values pass through unchanged.
func normalizeAccess(s string) model.Access {
	switch s {
	case "readonly":
		return model.AccessReadOnly
	case "readwrite":
		return model.AccessReadWrite
	case "readcreate":
		return model.AccessReadCreate
	case "noaccess":
		return model.AccessNotAccessible
	case "notifyonly":
		return model.AccessAccessibleNotify
	default:
		return model.Access(s)
	}
}
