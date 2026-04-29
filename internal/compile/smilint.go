package compile

import (
	"bufio"
	"context"
	"io"
	"os/exec"
	"regexp"
	"strconv"
	"strings"

	"github.com/no42-org/blittermib/internal/model"
)

// Smilint invokes libsmi's smilint and parses its diagnostics.
type Smilint struct {
	// Path to the smilint binary. Defaults to "smilint".
	Path string

	// Paths to prepend to libsmi's MIB search path (`-p` flag).
	Paths []string

	// Level controls smilint's severity threshold (`-l`); -1 leaves it
	// at smilint's default (typically 3).
	Level int
}

// NewSmilint returns a Smilint at default severity, resolving the binary
// from PATH.
func NewSmilint() *Smilint {
	return &Smilint{Path: "smilint", Level: -1}
}

// Lint runs `smilint <target>` and returns the parsed diagnostics.
//
// smilint exits non-zero whenever it emits any diagnostic, so a non-zero
// exit is not treated as a tool failure — only an unexpected absence of
// output along with an error is.
func (s *Smilint) Lint(ctx context.Context, target string) ([]model.Diagnostic, error) {
	bin := s.Path
	if bin == "" {
		bin = "smilint"
	}
	var args []string
	if s.Level >= 0 {
		args = append(args, "-l", strconv.Itoa(s.Level))
	}
	for _, p := range s.Paths {
		args = append(args, "-p", p)
	}
	// `--` ends flag parsing so a MIB filename starting with `-`
	// won't be mistaken for a flag.
	args = append(args, "--", target)

	cmd := exec.CommandContext(ctx, bin, args...)
	combined, err := cmd.CombinedOutput()
	if len(combined) == 0 && err != nil {
		return nil, err
	}
	return ParseSmilintOutput(strings.NewReader(string(combined))), nil
}

// ParseSmilintOutput parses lines from a smilint output stream.
//
// Lines are expected to follow `<file>:<line>: <rest>`, where <rest> may
// begin with a severity word (`error:`, `warning:`, …) and optionally
// carry a `{check-code}` token from smilint's check-name catalog.
func ParseSmilintOutput(r io.Reader) []model.Diagnostic {
	var diags []model.Diagnostic
	scanner := bufio.NewScanner(r)
	for scanner.Scan() {
		if d, ok := parseSmilintLine(scanner.Text()); ok {
			diags = append(diags, d)
		}
	}
	return diags
}

var smilintCodeRe = regexp.MustCompile(`\{([A-Za-z0-9_-]+)\}`)

func parseSmilintLine(line string) (model.Diagnostic, bool) {
	line = strings.TrimSpace(line)
	if line == "" {
		return model.Diagnostic{}, false
	}

	parts := strings.SplitN(line, ":", 3)
	if len(parts) < 3 {
		return model.Diagnostic{}, false
	}
	lineNum, err := strconv.Atoi(strings.TrimSpace(parts[1]))
	if err != nil {
		return model.Diagnostic{}, false
	}

	rest := strings.TrimSpace(parts[2])
	sev, msg := splitSeverity(rest)

	code := ""
	if m := smilintCodeRe.FindStringSubmatch(msg); m != nil {
		code = m[1]
		msg = strings.TrimSpace(strings.Replace(msg, m[0], "", 1))
	}

	return model.Diagnostic{
		File:     parts[0],
		Line:     lineNum,
		Severity: sev,
		Code:     code,
		Message:  msg,
	}, true
}

func splitSeverity(s string) (model.DiagnosticSeverity, string) {
	lower := strings.ToLower(s)
	switch {
	case strings.HasPrefix(lower, "error:"),
		strings.HasPrefix(lower, "fatal:"):
		return model.SeverityError, strings.TrimSpace(s[strings.Index(s, ":")+1:])
	case strings.HasPrefix(lower, "warning:"),
		strings.HasPrefix(lower, "warn:"):
		return model.SeverityWarning, strings.TrimSpace(s[strings.Index(s, ":")+1:])
	case strings.HasPrefix(lower, "note:"),
		strings.HasPrefix(lower, "info:"):
		return model.SeverityNote, strings.TrimSpace(s[strings.Index(s, ":")+1:])
	}
	return model.SeverityWarning, s
}
