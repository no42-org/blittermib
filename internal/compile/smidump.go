package compile

import (
	"bytes"
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"os/exec"
	"strings"

	"github.com/no42-org/blittermib/internal/model"
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

// DumpModule runs `smidump -f xml <module>` and returns the parsed
// result plus any diagnostics smidump emitted on stderr (parsed as
// model.Diagnostic — see ParseSmilintOutput; smidump's stderr format
// matches smilint's). Diagnostics are returned even on success, since
// `-k` lets smidump emit warnings while still exiting 0.
func (s *Smidump) DumpModule(ctx context.Context, moduleName string) (*SMI, []model.Diagnostic, error) {
	return s.run(ctx, moduleName)
}

// DumpFile runs smidump on a MIB file path.
func (s *Smidump) DumpFile(ctx context.Context, path string) (*SMI, []model.Diagnostic, error) {
	return s.run(ctx, path)
}

func (s *Smidump) run(ctx context.Context, target string) (*SMI, []model.Diagnostic, error) {
	bin := s.Path
	if bin == "" {
		bin = "smidump"
	}
	// -k: keep going past non-fatal errors. Without it, smidump
	// bails silently (exit 0, empty stdout) on common production
	// MIB issues like missing REVISION clauses or unconstrained
	// table indexes — failures we want to surface as diagnostics
	// from smilint, not silently drop the whole module.
	// -q: keep stderr free of non-error chatter so cmd.Output()'s
	// captured stderr is meaningful when the exit code IS non-zero.
	args := []string{"-f", "xml", "-k", "-q"}
	for _, p := range s.Paths {
		args = append(args, "-p", p)
	}
	// `--` ends flag parsing — protects against MIB filenames that
	// happen to start with a dash being interpreted as smidump options.
	args = append(args, "--", target)

	// Capture stdout and stderr separately. cmd.Output() only fills
	// ExitError.Stderr on non-zero exit, but with `-k` smidump will
	// happily print warnings to stderr and exit 0 — we want those
	// diagnostics regardless of exit code.
	cmd := exec.CommandContext(ctx, bin, args...)
	var stdoutBuf, stderrBuf bytes.Buffer
	cmd.Stdout = &stdoutBuf
	cmd.Stderr = &stderrBuf
	err := cmd.Run()
	diags := ParseSmilintOutput(strings.NewReader(stderrBuf.String()))
	if err != nil {
		return nil, diags, fmt.Errorf("smidump %s: %w: %s", target, err, stderrBuf.String())
	}

	smi, perr := ParseXML(&stdoutBuf)
	if perr != nil {
		return nil, diags, fmt.Errorf("parse smidump output for %s: %w", target, perr)
	}
	return smi, diags, nil
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
