package store

// maxOIDDepth caps the number of prefix segments OIDPath will
// expand into IN-clause placeholders. Real-world OIDs are well
// under this; the cap keeps a pathological URL (e.g. `1.2.3.…`
// with thousands of dots) from tripping SQLite's
// SQLITE_MAX_VARIABLE_NUMBER limit.
const maxOIDDepth = 64

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
