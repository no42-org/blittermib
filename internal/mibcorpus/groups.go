package mibcorpus

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strings"

	"gopkg.in/yaml.v3"
)

// GroupMap is the inverted form of mibs/_groups.yaml: module name →
// IETF function group ("core", "transport", "interfaces", …). Empty
// when the file is missing or hasn't been seeded yet — callers fall
// back to ietf/other in that case.
type GroupMap struct {
	byModule map[string]string
}

// NewGroupMap builds a GroupMap from a module-name → group map.
// Used by tests that want to inject a fixture without touching disk.
func NewGroupMap(byModule map[string]string) GroupMap {
	if byModule == nil {
		byModule = map[string]string{}
	}
	return GroupMap{byModule: byModule}
}

// LoadGroups reads a _groups.yaml file. The file format is:
//
//	core:        [SNMPv2-SMI, SNMPv2-TC, SNMP-MIB]
//	transport:   [TCP-MIB, UDP-MIB, IP-MIB]
//	interfaces:  [IF-MIB, EtherLike-MIB]
//	...
//	other:       []  # default for unclassified IETF MIBs
//
// A missing file returns an empty (non-nil) map and no error so the
// caller can run before the corpus has been seeded. A duplicate
// module across two groups is rejected — Go's map iteration order
// would otherwise produce non-deterministic group assignment.
func LoadGroups(path string) (GroupMap, error) {
	if path == "" {
		return GroupMap{byModule: map[string]string{}}, nil
	}
	// #nosec G304 -- path is the operator-supplied --groups flag value, offline CLI input.
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return GroupMap{byModule: map[string]string{}}, nil
		}
		return GroupMap{}, err
	}
	var raw map[string][]string
	if err := yaml.Unmarshal(data, &raw); err != nil {
		return GroupMap{}, fmt.Errorf("parse %s: %w", path, err)
	}
	by := make(map[string]string, len(raw))
	for group, modules := range raw {
		for _, m := range modules {
			m = strings.TrimSpace(m)
			if m == "" {
				continue
			}
			if existing, dup := by[m]; dup && existing != group {
				return GroupMap{}, fmt.Errorf(
					"%s: module %q listed in both %q and %q groups",
					path, m, existing, group)
			}
			by[m] = group
		}
	}
	return GroupMap{byModule: by}, nil
}

// GroupOf returns the configured IETF group for a module name, or "" if
// the module isn't listed (callers fall back to "other").
func (g GroupMap) GroupOf(module string) string {
	return g.byModule[module]
}
