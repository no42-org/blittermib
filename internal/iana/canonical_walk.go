package iana

import "strings"

// Step is one named arc along an OID's canonical breadcrumb: the
// dotted prefix and the well-known name it carries.
type Step struct {
	OID  string // dotted prefix, e.g. "1.3.6.1.2.1"
	Name string // canonical name, e.g. "mib-2"
}

// ResolveCanonical walks the dotted OID against the Canonical
// hierarchy and returns each prefix that has a well-known name,
// shortest first. A leading dot is tolerated. Because Canonical is
// contiguous from the root, the breadcrumb naturally stops at the
// deepest well-known arc: a vendor OID such as
// .1.3.6.1.4.1.9.2.1.58.0 yields steps up to enterprises
// (1.3.6.1.4.1) and no further — the caller extracts the next
// segment as a PEN and resolves it via LookupPEN. Returns nil for
// empty input.
func ResolveCanonical(oid string) []Step {
	oid = strings.TrimPrefix(strings.TrimSpace(oid), ".")
	if oid == "" {
		return nil
	}
	var steps []Step
	for i := 0; i <= len(oid); i++ {
		if i == len(oid) || oid[i] == '.' {
			prefix := oid[:i]
			if name, ok := Canonical[prefix]; ok {
				steps = append(steps, Step{OID: prefix, Name: name})
			}
		}
	}
	return steps
}
