package main

import (
	"errors"
	"fmt"
	"io/fs"
	"os"

	"gopkg.in/yaml.v3"
)

// Overrides mirrors the on-disk shape of `mibs/_overrides.yaml`:
//
//	licenses:
//	  WEIRD-VENDOR-MIB: vendor-public
//	  ANCIENT-RFC-MIB:  rfc-editor
//
// The map is keyed by module name (case-sensitive; module names are
// canonical SMI uppercase). Empty when the file is missing — a clean
// migration produces no overrides at all.
type Overrides struct {
	Licenses map[string]string `yaml:"licenses"`
}

// LoadOverrides reads _overrides.yaml. A missing file returns an
// empty Overrides without error; a malformed file returns an error
// with the path and YAML position context.
func LoadOverrides(path string) (*Overrides, error) {
	if path == "" {
		return &Overrides{Licenses: map[string]string{}}, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return &Overrides{Licenses: map[string]string{}}, nil
		}
		return nil, err
	}
	var o Overrides
	if err := yaml.Unmarshal(data, &o); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	if o.Licenses == nil {
		o.Licenses = map[string]string{}
	}
	return &o, nil
}

// LicenseFor returns the override license for a module name, or "" if
// no override is configured. Callers fall back to the auto-detected
// tag (or "unknown").
func (o *Overrides) LicenseFor(module string) string {
	if o == nil {
		return ""
	}
	return o.Licenses[module]
}
