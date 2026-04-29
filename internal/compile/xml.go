package compile

import "encoding/xml"

// SMI is the root of smidump's XML output.
//
// libsmi emits a `<smi>` document containing exactly one `<module>` per
// invocation, with nested type definitions, OID nodes, notifications,
// groups, and compliance statements. Field tags follow libsmi's smi.dtd.
type SMI struct {
	XMLName xml.Name  `xml:"smi"`
	Version string    `xml:"version,attr"`
	Module  XMLModule `xml:"module"`
}

// XMLModule mirrors a single `<module>` element.
type XMLModule struct {
	Name          string            `xml:"name,attr"`
	Language      string            `xml:"language,attr"`
	Path          string            `xml:"path,attr,omitempty"`
	Organization  string            `xml:"organization"`
	Contact       string            `xml:"contact"`
	Description   string            `xml:"description"`
	Reference     string            `xml:"reference,omitempty"`
	Revisions     []XMLRevision     `xml:"revision"`
	Identity      *XMLIdentityNode  `xml:"identity-node,omitempty"`
	Imports       XMLImports        `xml:"imports"`
	Typedefs      []XMLTypedef      `xml:"typedefs>typedef"`
	Nodes         []XMLNode         `xml:"nodes>node"`
	Notifications []XMLNotification `xml:"notifications>notification"`
	Groups        []XMLGroup        `xml:"groups>group"`
	Compliances   []XMLCompliance   `xml:"compliances>compliance"`
}

// XMLRevision is a MODULE-IDENTITY REVISION entry.
type XMLRevision struct {
	Date        string `xml:"date,attr"`
	Description string `xml:"description"`
}

// XMLIdentityNode names the OID that the MODULE-IDENTITY resolves to.
type XMLIdentityNode struct {
	Name string `xml:"name,attr"`
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
type XMLTypedef struct {
	Name        string    `xml:"name,attr"`
	BaseType    string    `xml:"basetype,attr"`
	Status      string    `xml:"status,attr"`
	Format      string    `xml:"format,attr,omitempty"`
	Default     string    `xml:"default,attr,omitempty"`
	Line        int       `xml:"line,attr,omitempty"`
	Syntax      XMLSyntax `xml:"syntax"`
	Description string    `xml:"description"`
	Reference   string    `xml:"reference,omitempty"`
}

// XMLNode is an OBJECT-TYPE, OBJECT-IDENTITY, or internal OID node.
type XMLNode struct {
	Name        string      `xml:"name,attr"`
	OID         string      `xml:"oid,attr"`
	Status      string      `xml:"status,attr,omitempty"`
	NodeType    string      `xml:"nodetype,attr,omitempty"` // node|scalar|table|row|column|notification
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

// XMLSyntax holds the parsed SYNTAX clause.
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
