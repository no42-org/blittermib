package model

import "strings"

// ParseStatus indicates how cleanly a module parsed.
type ParseStatus string

const (
	ParseStatusClean    ParseStatus = "clean"
	ParseStatusWarnings ParseStatus = "warnings"
	ParseStatusErrors   ParseStatus = "errors"
)

// SymbolKind enumerates the SMI definitions blittermib recognizes.
//
// OBJECT-TYPE-derived symbols are split into four narrow kinds —
// scalar, table, table-entry, column — so render-time consumers can
// branch on Kind alone, without auxiliary boolean flags.
type SymbolKind string

const (
	KindScalar            SymbolKind = "scalar"
	KindTable             SymbolKind = "table"
	KindTableEntry        SymbolKind = "table-entry"
	KindColumn            SymbolKind = "column"
	KindTextualConvention SymbolKind = "textual-convention"
	KindObjectIdentity    SymbolKind = "object-identity"
	KindNotificationType  SymbolKind = "notification-type"
	KindTrapType          SymbolKind = "trap-type"
	KindModuleIdentity    SymbolKind = "module-identity"
	KindObjectGroup       SymbolKind = "object-group"
	KindNotificationGroup SymbolKind = "notification-group"
	KindModuleCompliance  SymbolKind = "module-compliance"
)

// Status maps to the SMI STATUS clause.
type Status string

const (
	StatusCurrent    Status = "current"
	StatusDeprecated Status = "deprecated"
	StatusObsolete   Status = "obsolete"
	StatusMandatory  Status = "mandatory" // SMIv1
	StatusOptional   Status = "optional"  // SMIv1
)

// Access maps to MAX-ACCESS.
type Access string

const (
	AccessNotAccessible    Access = "not-accessible"
	AccessAccessibleNotify Access = "accessible-for-notify"
	AccessReadOnly         Access = "read-only"
	AccessReadWrite        Access = "read-write"
	AccessReadCreate       Access = "read-create"
)

// ReferenceKind classifies a relationship between two symbols.
type ReferenceKind string

const (
	RefIndex              ReferenceKind = "index"
	RefAugments           ReferenceKind = "augments"
	RefSyntax             ReferenceKind = "syntax"
	RefSequenceMember     ReferenceKind = "sequence-member"
	RefGroupMember        ReferenceKind = "group-member"
	RefComplianceObject   ReferenceKind = "compliance-object"
	RefNotificationObject ReferenceKind = "notification-object"
)

// DiagnosticSeverity ranks parse issues.
type DiagnosticSeverity string

const (
	SeverityError   DiagnosticSeverity = "error"
	SeverityWarning DiagnosticSeverity = "warning"
	SeverityNote    DiagnosticSeverity = "note"
)

// Module is a parsed MIB module.
type Module struct {
	Name         string
	OIDRoot      string
	Organization string
	ContactInfo  string
	Description  string
	LastUpdated  string
	SourcePath   string
	ParseStatus  ParseStatus
	Imports      []Import
	Revisions    []Revision
}

// Import names a symbol pulled in from another module.
type Import struct {
	FromModule string
	Symbol     string
}

// Revision records one MODULE-IDENTITY REVISION entry.
type Revision struct {
	When        string
	Description string
}

// EnumValue is one entry in an `INTEGER { name(value), … }`
// enumeration. Number is a 64-bit int because SMI permits values
// outside the int32 range (rare but legal).
type EnumValue struct {
	Name   string `json:"name"`
	Number int64  `json:"number"`
}

// FamilyCounts holds the per-type-family symbol totals for one
// module. The fields mirror the type-family taxonomy defined in
// `docs/design/handoff/` (ten families) so the status bar can show
// each chip via the matching `--c-…` CSS variable. Counts are
// independent metrics — they do NOT sum to the module's symbol
// total. The Structs count is also surfaced as "objects" in the
// status bar (Reading-3 semantics from the locked redesign
// decisions: structural-kind total = module-identity +
// object-identity + table + table-entry).
type FamilyCounts struct {
	Counters int
	Gauges   int
	Ints     int
	Texts    int
	Indexes  int
	Times    int
	Addrs    int
	Bools    int
	Notifs   int
	Structs  int
}

// OIDStep is one segment of a decoded OID path. Canonical is true
// when the step's name comes from the hardcoded canonical-OID
// fallback table (iso, org, dod, …) rather than a loaded MIB.
type OIDStep struct {
	Prefix    string
	Name      string
	Module    string
	Kind      SymbolKind
	Canonical bool
}

// TypeFamily classifies an SMI symbol into one of ten visual
// families used by the workspace status bar, tree rows, and list
// rows. The taxonomy mirrors `docs/design/handoff/helpers.js`'s
// `typeFamily` function so the front-end and back-end agree without
// a translation layer.
//
// Returns one of: "t-counter", "t-gauge", "t-int", "t-text",
// "t-index", "t-time", "t-addr", "t-bool", "t-notif", "t-struct".
func TypeFamily(kind SymbolKind, syntax string, isIndex bool) string {
	if kind == KindNotificationType {
		return "t-notif"
	}
	switch kind {
	case KindTable, KindTableEntry, KindObjectIdentity, KindModuleIdentity:
		return "t-struct"
	}
	if isIndex {
		return "t-index"
	}
	if syntax == "" {
		return "t-struct"
	}
	t := strings.ToLower(syntax)
	switch {
	case strings.HasPrefix(t, "counter"):
		return "t-counter"
	case strings.HasPrefix(t, "gauge"), strings.HasPrefix(t, "unsigned"):
		return "t-gauge"
	case strings.HasPrefix(t, "integer"):
		return "t-int"
	case t == "displaystring", strings.Contains(t, "string"), strings.Contains(t, "octet"):
		return "t-text"
	case t == "timeticks":
		return "t-time"
	case strings.Contains(t, "address"), t == "ipaddress", t == "macaddress", t == "physaddress":
		return "t-addr"
	case t == "truthvalue", t == "boolean":
		return "t-bool"
	case strings.Contains(t, "index"), strings.Contains(t, "rowpointer"):
		return "t-index"
	}
	return "t-struct"
}

// Symbol is any named SMI definition.
type Symbol struct {
	ID           int64
	ModuleName   string
	Name         string
	OID          string
	ParentOID    string
	Kind         SymbolKind
	Syntax       string
	Access       Access
	Status       Status
	Units        string
	Reference    string
	Description  string
	DefaultValue string
	Augments     string
	IndexColumns []string
	EnumValues   []EnumValue
	SourceLine   int
}

// QualifiedName returns the canonical `Module::Symbol` identifier.
//
// SMI module and symbol names follow the grammar `letter (letter|digit|'-')*`
// (RFC 1212 §4.1.6, RFC 2578 §3.1), which excludes ':' — so `::` is
// unambiguous as a separator and round-trips to a unique (module, name) pair.
func (s Symbol) QualifiedName() string {
	return s.ModuleName + "::" + s.Name
}

// OIDNode is a node in the OID tree.
type OIDNode struct {
	OID        string
	ParentOID  string
	SymbolID   int64
	ChildCount int
}

// Reference relates one symbol to another by qualified name.
//
// Qualified-name keys (rather than database IDs) make hot-reload simpler:
// reloading a module invalidates only its own outgoing references; the
// references pointing INTO the reloaded module remain valid because
// they were always keyed by name, not row id.
type Reference struct {
	SourceModule string
	SourceName   string
	TargetModule string
	TargetName   string
	Kind         ReferenceKind
}

// SourceQualifiedName returns the canonical Module::Symbol identifier
// of the reference's source.
func (r Reference) SourceQualifiedName() string {
	return r.SourceModule + "::" + r.SourceName
}

// TargetQualifiedName returns the canonical Module::Symbol identifier
// of the reference's target.
func (r Reference) TargetQualifiedName() string {
	return r.TargetModule + "::" + r.TargetName
}

// Diagnostic is a single parse warning or error from smilint.
type Diagnostic struct {
	File     string
	Line     int
	Severity DiagnosticSeverity
	Code     string
	Message  string
	Module   string
}
