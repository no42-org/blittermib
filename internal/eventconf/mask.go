/*
 * Copyright 2026 Ronny Trommer <ronny@no42.org>
 * SPDX-License-Identifier: MIT
 */

package eventconf

import "strings"

// splitTrapOID splits a notification's OID into the SNMPv1-style
// enterprise (`id` mask) and specific-type for the trap mask, porting
// OpenNMS's getTrapEnterprise / getTrapSpecificType.
//
// The dotted input is the store's dot-less OID (e.g. "1.3.6.1...");
// the returned enterprise carries a leading dot to match the `id`
// mevalue format OpenNMS emits.
//
// The last sub-identifier becomes the specific-type; the remainder is
// the enterprise. Per RFC 3584 §3.2, when the next-to-last
// sub-identifier is zero the trailing ".0" is stripped from the
// enterprise (the SNMPv1 enterprise is the v2 snmpTrapOID with the
// last two sub-ids removed).
//
// ok is false when the OID has fewer than two sub-identifiers (no
// specific-type to split off).
func splitTrapOID(oid string) (enterprise, specific string, ok bool) {
	oid = strings.TrimPrefix(oid, ".")
	parts := strings.Split(oid, ".")
	if len(parts) < 2 {
		return "", "", false
	}
	specific = parts[len(parts)-1]
	enterprise = "." + strings.Join(parts[:len(parts)-1], ".")
	// RFC 3584 §3.2: a next-to-last zero sub-identifier is dropped.
	// TrimSuffix is a no-op when the ".0" suffix is absent.
	enterprise = strings.TrimSuffix(enterprise, ".0")
	return enterprise, specific, true
}
