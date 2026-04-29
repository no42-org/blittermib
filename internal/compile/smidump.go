package compile

import (
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"os/exec"
)

// Smidump invokes libsmi's smidump tool and parses its XML output.
//
// Construct with NewSmidump for default settings; override Path or Paths
// directly for tests or non-standard installs.
type Smidump struct {
	// Path to the smidump binary. Defaults to "smidump" (resolved via PATH).
	Path string

	// Paths to prepend to libsmi's MIB search path (`-p` flag, repeated).
	Paths []string
}

// NewSmidump returns a Smidump that resolves the smidump binary from PATH.
func NewSmidump() *Smidump {
	return &Smidump{Path: "smidump"}
}

// DumpModule runs `smidump -f xml <module>` and returns the parsed result.
func (s *Smidump) DumpModule(ctx context.Context, moduleName string) (*SMI, error) {
	return s.run(ctx, moduleName)
}

// DumpFile runs smidump on a MIB file path.
func (s *Smidump) DumpFile(ctx context.Context, path string) (*SMI, error) {
	return s.run(ctx, path)
}

func (s *Smidump) run(ctx context.Context, target string) (*SMI, error) {
	bin := s.Path
	if bin == "" {
		bin = "smidump"
	}
	args := []string{"-f", "xml", "-q"}
	for _, p := range s.Paths {
		args = append(args, "-p", p)
	}
	// `--` ends flag parsing — protects against MIB filenames that
	// happen to start with a dash being interpreted as smidump options.
	args = append(args, "--", target)

	cmd := exec.CommandContext(ctx, bin, args...)
	stdout, err := cmd.Output()
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			return nil, fmt.Errorf("smidump %s: %w: %s", target, err, string(ee.Stderr))
		}
		return nil, fmt.Errorf("smidump %s: %w", target, err)
	}

	smi, err := ParseXML(bytes.NewReader(stdout))
	if err != nil {
		return nil, fmt.Errorf("parse smidump output for %s: %w", target, err)
	}
	return smi, nil
}

// ParseXML decodes smidump's XML output from r.
//
// Strict mode is disabled because real-world libsmi output occasionally
// contains XML quirks (entities, unbalanced whitespace) that strict
// parsing would reject — this is consistent with how downstream tools
// in the libsmi ecosystem treat the output.
func ParseXML(r io.Reader) (*SMI, error) {
	dec := xml.NewDecoder(r)
	dec.Strict = false
	dec.Entity = xml.HTMLEntity

	var smi SMI
	if err := dec.Decode(&smi); err != nil {
		return nil, err
	}
	return &smi, nil
}
