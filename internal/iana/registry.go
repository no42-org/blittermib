// Package iana provides reference data from IANA-maintained registries
// that blittermib uses to interpret OIDs whose modules aren't loaded.
//
// Two pieces:
//
//   - The SMI canonical hierarchy (this file) — well-known OID arcs
//     such as iso/org/dod/internet/mgmt/mib-2 plus a curated set of
//     widely-cited mib-2 children. The walk decoder uses these as a
//     last-resort name when no MIB module covers a prefix.
//   - The Private Enterprise Number registry (pen.go + pen.txt) —
//     a curated snapshot of https://www.iana.org/assignments/enterprise-numbers/
//     that maps PEN integers to organization names. Refreshed via
//     `make refresh-pen`.
package iana

// Canonical maps well-known dotted OID prefixes to their symbolic
// names. Hand-maintained and intentionally small — covers the SMI
// hierarchy and the most widely-referenced mib-2 children. Vendor
// enterprise numbers under .1.3.6.1.4.1.{n} are NOT enumerated here;
// those resolve via LookupPEN against pen.txt.
//
// Keep entries sorted by OID lexicographically when editing.
var Canonical = map[string]string{
	// Top of the OID tree.
	"1":     "iso",
	"1.3":   "org",
	"1.3.6": "dod",

	// internet(1.3.6.1) and its children.
	"1.3.6.1":   "internet",
	"1.3.6.1.1": "directory",
	"1.3.6.1.2": "mgmt",
	"1.3.6.1.3": "experimental",
	"1.3.6.1.4": "private",
	"1.3.6.1.5": "security",
	"1.3.6.1.6": "snmpV2",
	"1.3.6.1.7": "mail",
	"1.3.6.1.8": "features",

	// private(1.3.6.1.4) → enterprises arc; vendor MIBs hang off
	// .1.3.6.1.4.1.{PEN}.
	"1.3.6.1.4.1": "enterprises",

	// snmpV2(1.3.6.1.6) framework children, RFC 3411 / 3412.
	"1.3.6.1.6.1": "snmpDomains",
	"1.3.6.1.6.2": "snmpProxys",
	"1.3.6.1.6.3": "snmpModules",

	// mgmt(1.3.6.1.2) → mib-2 namespace.
	"1.3.6.1.2.1": "mib-2",

	// mib-2 children — RFC 1213 / SNMPv2-MIB and the major
	// follow-on MIBs everyone learns first.
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
	"1.3.6.1.2.1.14": "ospf",
	"1.3.6.1.2.1.15": "bgp",
	"1.3.6.1.2.1.16": "rmon",
	"1.3.6.1.2.1.17": "dot1dBridge",
	"1.3.6.1.2.1.23": "rip2",
	"1.3.6.1.2.1.25": "host",
	"1.3.6.1.2.1.26": "snmpDot3MauMgt",
	"1.3.6.1.2.1.31": "ifMIB",
	"1.3.6.1.2.1.35": "etherMIB",
	"1.3.6.1.2.1.47": "entityMIB",
	"1.3.6.1.2.1.48": "ipMRouteMIB",
	"1.3.6.1.2.1.49": "tcpMIB",
	"1.3.6.1.2.1.50": "udpMIB",
	"1.3.6.1.2.1.55": "ipv6MIB",
	"1.3.6.1.2.1.63": "schedMIB",
	"1.3.6.1.2.1.66": "applicationMIB",
	"1.3.6.1.2.1.67": "radiusMIB",
	"1.3.6.1.2.1.68": "diffServMIB",
	"1.3.6.1.2.1.74": "diameterMIB",
	"1.3.6.1.2.1.80": "pingMIB",
	"1.3.6.1.2.1.83": "snmpNotifyMIB",
	"1.3.6.1.2.1.84": "snmpProxyMIB",
	"1.3.6.1.2.1.88": "dismanEventMIB",
	"1.3.6.1.2.1.90": "dismanExpressionMIB",
	"1.3.6.1.2.1.92": "notificationLogMIB",

	// system(1.3.6.1.2.1.1) — descended scalars are referenced
	// constantly in walks; useful enough to spell out.
	"1.3.6.1.2.1.1.1": "sysDescr",
	"1.3.6.1.2.1.1.2": "sysObjectID",
	"1.3.6.1.2.1.1.3": "sysUpTime",
	"1.3.6.1.2.1.1.4": "sysContact",
	"1.3.6.1.2.1.1.5": "sysName",
	"1.3.6.1.2.1.1.6": "sysLocation",
	"1.3.6.1.2.1.1.7": "sysServices",

	// interfaces(1.3.6.1.2.1.2) — the table everyone walks first.
	"1.3.6.1.2.1.2.1": "ifNumber",
	"1.3.6.1.2.1.2.2": "ifTable",
}

// LookupCanonical returns the symbolic name for a well-known OID
// prefix. The lookup is exact; callers walking up an OID typically
// retry against successively shorter prefixes.
func LookupCanonical(oid string) (string, bool) {
	v, ok := Canonical[oid]
	return v, ok
}
