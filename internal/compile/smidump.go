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
	// `--` ends flag parsing — protects against MIB filenames that
	// happen to start with a dash being interpreted as smidump options.
	args = append(args, "--", target)

	// Capture stdout and stderr separately. cmd.Output() only fills
	// ExitError.Stderr on non-zero exit, but with `-k` smidump will
	// happily print warnings to stderr and exit 0 — we want those
	// diagnostics regardless of exit code.
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Env = smiEnv(s.Paths)
	var stdoutBuf, stderrBuf bytes.Buffer
	cmd.Stdout = &stdoutBuf
	cmd.Stderr = &stderrBuf
	err := cmd.Run()
	diags := ParseSmilintOutput(strings.NewReader(stderrBuf.String()))
	if err != nil {
		return nil, diags, fmt.Errorf("smidump %s: %w: %s", target, err, stderrBuf.String())
	}

	// Clone stdoutBuf.Bytes() rather than aliasing it. Bytes() returns
	// a slice referencing internal storage; the clone makes the
	// no-write-after-this-point invariant explicit and immune to
	// surrounding-code edits.
	stdoutBytes := append([]byte(nil), stdoutBuf.Bytes()...)
	smi, recovery, perr := parseSmidumpXMLWithRecovery(target, stdoutBytes)
	if recovery != nil {
		diags = append(diags, *recovery)
	}
	if perr != nil {
		return nil, diags, perr
	}
	return smi, diags, nil
}

// parseSmidumpXMLWithRecovery decodes smidump's XML output and, on a
// specific class of decoder failures (invalid UTF-8 / illegal character
// code), runs sanitizeXMLBytes once and retries the decode.
//
// On clean inputs the function takes the fast path: exactly one
// ParseXML call, no scan of the buffer, no recovery diagnostic.
//
// The recovery path exists because vendor MIBs in the wild commonly
// source-encode in Latin-1 / Windows-1252 or carry stray ASCII C0
// controls inside DESCRIPTION strings; smidump passes those bytes
// through verbatim into its XML output and Go's encoding/xml correctly
// refuses them. Sanitize-and-retry recovers the module and attaches a
// warning Diagnostic so the operator can see at /diagnostics which
// modules were fuzzed and re-encode the source if desired.
//
// On second-parse success, the returned recovery Diagnostic is non-nil
// and the error is nil. On second-parse failure, the recovery
// Diagnostic is nil and the error is the second-parse error wrapped
// with the same prefix as the first-parse failure path.
func parseSmidumpXMLWithRecovery(target string, raw []byte) (*SMI, *model.Diagnostic, error) {
	smi, perr := ParseXML(bytes.NewReader(raw))
	if perr == nil {
		return smi, nil, nil
	}
	if !xmlNeedsSanitize(perr) {
		return nil, nil, fmt.Errorf("parse smidump output for %s: %w", target, perr)
	}
	sanitized := sanitizeXMLBytes(raw)
	smi, perr2 := ParseXML(bytes.NewReader(sanitized))
	if perr2 != nil {
		return nil, nil, fmt.Errorf("parse smidump output for %s: %w", target, perr2)
	}
	recovery := &model.Diagnostic{
		File:     target,
		Severity: model.SeverityWarning,
		Code:     "non-utf8-source",
		Message:  "MIB source contains bytes that are not valid UTF-8. Valid UTF-8 sequences were preserved; isolated invalid bytes were interpreted as Latin-1, which may render slightly off in the Windows-1252-only band U+0080..U+009F. Re-encode the source as UTF-8 to silence this diagnostic.",
	}
	return smi, recovery, nil
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
