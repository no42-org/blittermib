package compile

import "encoding/xml"

// SMI is the root of smidump's XML output.
//
// libsmi 0.5.0 emits a `<smi>` document where `<module>` carries
// the metadata, and the symbol containers (`<imports>`, `<typedefs>`,
// `<nodes>`, …) are siblings of `<module>` — not children.
type SMI struct {
	XMLName       xml.Name          `xml:"smi"`
	Version       string            `xml:"version,attr"`
	Module        XMLModule         `xml:"module"`
	Imports       XMLImports        `xml:"imports"`
	Typedefs      []XMLTypedef      `xml:"typedefs>typedef"`
	Nodes         XMLNodes          `xml:"nodes"`
	Notifications []XMLNotification `xml:"notifications>notification"`
	Groups        []XMLGroup        `xml:"groups>group"`
	Compliances   []XMLCompliance   `xml:"compliances>compliance"`
}

// XMLModule mirrors a single `<module>` element.
type XMLModule struct {
	Name         string        `xml:"name,attr"`
	Language     string        `xml:"language,attr"`
	Path         string        `xml:"path,attr,omitempty"`
	Organization string        `xml:"organization"`
	Contact      string        `xml:"contact"`
	Description  string        `xml:"description"`
	Reference    string        `xml:"reference,omitempty"`
	Revisions    []XMLRevision `xml:"revision"`
	Identity     *XMLIdentity  `xml:"identity,omitempty"`
}

// XMLRevision is a MODULE-IDENTITY REVISION entry.
type XMLRevision struct {
	Date        string `xml:"date,attr"`
	Description string `xml:"description"`
}

// XMLIdentity is the MODULE-IDENTITY pointer; it names the node whose
// OID becomes the module's identity OID.
type XMLIdentity struct {
	Node string `xml:"node,attr"`
}

// XMLImports is the list of imported symbols from other modules.
type XMLImports struct {
	Imports []XMLImport `xml:"import"`
}

// XMLImport names a symbol pulled in from another module.
type XMLImport struct {
	Module string `xml:"module,attr"`
	Name   string `xml:"name,attr"`
}

// XMLTypedef is a TEXTUAL-CONVENTION (or plain typedef in SMIng).
//
// In smidump's emitted XML, typedef bodies are flat — `<range>`,
// `<format>`, `<description>` are direct children of `<typedef>`.
// There is no `<syntax>` wrapper here (unlike for nodes/columns).
type XMLTypedef struct {
	Name        string     `xml:"name,attr"`
	BaseType    string     `xml:"basetype,attr"`
	Status      string     `xml:"status,attr"`
	Default     string     `xml:"default,omitempty"`
	Format      string     `xml:"format,omitempty"`
	Range       []XMLRange `xml:"range"`
	Description string     `xml:"description"`
	Reference   string     `xml:"reference,omitempty"`
	Line        int        `xml:"line,attr,omitempty"`
}

// XMLNodes is the heterogeneous container under `<nodes>`. smidump
// emits four element tags here; the kind of symbol is encoded in the
// tag name rather than a `nodetype` attribute. `<table>` wraps a
// nested `<row>`, which in turn wraps `<column>` children.
type XMLNodes struct {
	Plain   []XMLNode  `xml:"node"`
	Scalars []XMLNode  `xml:"scalar"`
	Tables  []XMLTable `xml:"table"`
}

// XMLNode is the shared shape for plain `<node>`, `<scalar>`, and
// `<column>` elements. They all carry the same attribute and child
// vocabulary in smidump's output.
type XMLNode struct {
	Name        string      `xml:"name,attr"`
	OID         string      `xml:"oid,attr"`
	Status      string      `xml:"status,attr,omitempty"`
	Line        int         `xml:"line,attr,omitempty"`
	Syntax      *XMLSyntax  `xml:"syntax,omitempty"`
	Access      string      `xml:"access,omitempty"`
	Default     string      `xml:"default,omitempty"`
	Format      string      `xml:"format,omitempty"`
	Units       string      `xml:"units,omitempty"`
	Description string      `xml:"description,omitempty"`
	Reference   string      `xml:"reference,omitempty"`
	Linkage     *XMLLinkage `xml:"linkage,omitempty"`
}

// XMLTable is an SMIv2 conceptual table. The single `<row>` child
// holds the conceptual-row entry plus its column definitions.
type XMLTable struct {
	Name        string  `xml:"name,attr"`
	OID         string  `xml:"oid,attr"`
	Status      string  `xml:"status,attr,omitempty"`
	Line        int     `xml:"line,attr,omitempty"`
	Description string  `xml:"description,omitempty"`
	Reference   string  `xml:"reference,omitempty"`
	Row         *XMLRow `xml:"row,omitempty"`
}

// XMLRow is a conceptual-row entry. INDEX/AUGMENTS metadata lives in
// `<linkage>`; the row's columns are nested as `<column>` children.
type XMLRow struct {
	Name        string      `xml:"name,attr"`
	OID         string      `xml:"oid,attr"`
	Status      string      `xml:"status,attr,omitempty"`
	Line        int         `xml:"line,attr,omitempty"`
	Linkage     *XMLLinkage `xml:"linkage,omitempty"`
	Description string      `xml:"description,omitempty"`
	Reference   string      `xml:"reference,omitempty"`
	Columns     []XMLNode   `xml:"column"`
}

// XMLLinkage carries INDEX, AUGMENTS, IMPLIED for SMIv2 table rows.
type XMLLinkage struct {
	Implied  bool     `xml:"implied,attr,omitempty"`
	Index    []XMLRef `xml:"index"`
	Augments *XMLRef  `xml:"augments,omitempty"`
}

// XMLRef refers to another symbol by module + name.
type XMLRef struct {
	Module string `xml:"module,attr"`
	Name   string `xml:"name,attr"`
}

// XMLSyntax holds the parsed SYNTAX clause for a node/scalar/column.
type XMLSyntax struct {
	Type         *XMLRef          `xml:"type,omitempty"`
	Typedef      *XMLTypedef      `xml:"typedef,omitempty"`
	Range        []XMLRange       `xml:"parent>range,omitempty"`
	NamedNumbers []XMLNamedNumber `xml:"namednumber,omitempty"`
}

// XMLRange is a numeric range constraint.
type XMLRange struct {
	Min string `xml:"min,attr"`
	Max string `xml:"max,attr"`
}

// XMLNamedNumber is one entry in an INTEGER {name(value), …} enumeration.
type XMLNamedNumber struct {
	Name   string `xml:"name,attr"`
	Number string `xml:"number,attr"`
}

// XMLNotification is a NOTIFICATION-TYPE (SMIv2) or TRAP-TYPE (SMIv1).
type XMLNotification struct {
	Name        string   `xml:"name,attr"`
	OID         string   `xml:"oid,attr"`
	Status      string   `xml:"status,attr,omitempty"`
	Line        int      `xml:"line,attr,omitempty"`
	Objects     []XMLRef `xml:"objects>object"`
	Description string   `xml:"description"`
	Reference   string   `xml:"reference,omitempty"`
}

// XMLGroup is OBJECT-GROUP or NOTIFICATION-GROUP.
type XMLGroup struct {
	Name        string   `xml:"name,attr"`
	OID         string   `xml:"oid,attr"`
	Status      string   `xml:"status,attr,omitempty"`
	GroupType   string   `xml:"type,attr,omitempty"` // object|notification
	Line        int      `xml:"line,attr,omitempty"`
	Members     []XMLRef `xml:"members>member"`
	Description string   `xml:"description"`
	Reference   string   `xml:"reference,omitempty"`
}

// XMLCompliance is MODULE-COMPLIANCE.
type XMLCompliance struct {
	Name        string          `xml:"name,attr"`
	OID         string          `xml:"oid,attr"`
	Status      string          `xml:"status,attr,omitempty"`
	Line        int             `xml:"line,attr,omitempty"`
	Description string          `xml:"description"`
	Reference   string          `xml:"reference,omitempty"`
	Mandatory   []XMLRef        `xml:"requires>mandatory"`
	Options     []XMLOption     `xml:"requires>option"`
	Refinements []XMLRefinement `xml:"requires>refine"`
}

// XMLOption is an OPTIONAL group inside a compliance.
type XMLOption struct {
	Module      string `xml:"module,attr"`
	Name        string `xml:"name,attr"`
	Description string `xml:"description"`
}

// XMLRefinement narrows SYNTAX/ACCESS for a specific compliance.
type XMLRefinement struct {
	Module      string     `xml:"module,attr"`
	Name        string     `xml:"name,attr"`
	Syntax      *XMLSyntax `xml:"syntax,omitempty"`
	Access      string     `xml:"access,omitempty"`
	Description string     `xml:"description"`
}
