package store

// canonicalOIDs maps the well-known IETF OID prefixes to their
// short symbolic names. Used by Store.OIDPath as a fallback for
// prefixes that no loaded MIB module covers — most commonly the
// uppermost levels (1, 1.3, 1.3.6, …) which sit above the SMIv2
// `mgmt(2)` subtree where module identities anchor.
//
// The table is intentionally minimal: only RFC 1155-rooted nodes
// that every SNMP implementation agrees on. Vendor enterprise
// numbers (1.3.6.1.4.1.<n>) are NOT enumerated here — those are
// resolved from loaded MIB modules or rendered as bare numerals.
var canonicalOIDs = map[string]string{
	"1":           "iso",
	"1.3":         "org",
	"1.3.6":       "dod",
	"1.3.6.1":     "internet",
	"1.3.6.1.2":   "mgmt",
	"1.3.6.1.2.1": "mib-2",
	"1.3.6.1.3":   "experimental",
	"1.3.6.1.4":   "private",
	"1.3.6.1.4.1": "enterprises",
	"1.3.6.1.6":   "snmpV2",

	// mib-2 well-known children — RFC 1213 / SNMPv2-MIB. Listed here
	// as a fallback so OID-decode breadcrumbs stay readable even when
	// the loaded MIB set doesn't include the module that declares
	// these names. (Loaded MIBs override via Store.OIDPath's row
	// match; this table is only consulted for unmatched prefixes.)
	"1.3.6.1.2.1.1":  "system",
	"1.3.6.1.2.1.2":  "interfaces",
	"1.3.6.1.2.1.3":  "at",
	"1.3.6.1.2.1.4":  "ip",
	"1.3.6.1.2.1.5":  "icmp",
	"1.3.6.1.2.1.6":  "tcp",
	"1.3.6.1.2.1.7":  "udp",
	"1.3.6.1.2.1.8":  "egp",
	"1.3.6.1.2.1.10": "transmission",
	"1.3.6.1.2.1.11": "snmp",
}

// maxOIDDepth caps the number of prefix segments OIDPath will
// expand into IN-clause placeholders. Real-world OIDs are well
// under this; the cap keeps a pathological URL (e.g. `1.2.3.…`
// with thousands of dots) from tripping SQLite's
// SQLITE_MAX_VARIABLE_NUMBER limit.
const maxOIDDepth = 64

// canonicalName returns (name, true) if prefix is a well-known IETF
// node; otherwise ("", false).
func canonicalName(prefix string) (string, bool) {
	n, ok := canonicalOIDs[prefix]
	return n, ok
}

// oidPrefixes splits "1.3.6.1.2.1.2.2" into the slice
// ["1", "1.3", "1.3.6", "1.3.6.1", "1.3.6.1.2", …, "1.3.6.1.2.1.2.2"].
// Returns nil for empty input.
func oidPrefixes(oid string) []string {
	if oid == "" {
		return nil
	}
	out := make([]string, 0, 16)
	for i := 0; i < len(oid); i++ {
		if oid[i] == '.' {
			out = append(out, oid[:i])
		}
	}
	out = append(out, oid)
	return out
}
