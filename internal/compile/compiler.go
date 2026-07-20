package compile

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"time"

	"github.com/no42-org/blittermib/internal/model"
)

// Smidumper produces structured SMI documents from a MIB target
// (module name or file path), along with any diagnostics smidump
// emitted on stderr (the `-k` flag lets smidump emit warnings while
// still exiting 0, so success-path diagnostics are routine).
type Smidumper interface {
	DumpModule(ctx context.Context, target string) (*SMI, []model.Diagnostic, error)
}

// Smilinter produces diagnostics for a MIB target.
type Smilinter interface {
	Lint(ctx context.Context, target string) ([]model.Diagnostic, error)
}

// Result is the parsed output for a single MIB target.
type Result struct {
	Target      string
	Module      *model.Module
	Symbols     []model.Symbol
	Diagnostics []model.Diagnostic
	SMI         *SMI
	Err         error
	Duration    time.Duration
}

// Compiler orchestrates parallel parsing of many MIB targets.
//
// Concurrency defaults to GOMAXPROCS when set to 0 or below.
// Smilint diagnostics are best-effort: smilint failures do not abort
// the result, only smidump failures (no structured output) do.
type Compiler struct {
	Smidump     Smidumper
	Smilint     Smilinter
	Concurrency int
}

// Compile runs the compile pipeline over targets in parallel and
// returns the results in input order.
func (c *Compiler) Compile(ctx context.Context, targets []string) []Result {
	n := c.Concurrency
	if n <= 0 {
		n = runtime.GOMAXPROCS(0)
	}
	if n < 1 {
		n = 1
	}

	sem := make(chan struct{}, n)
	var wg sync.WaitGroup
	results := make([]Result, len(targets))

	for i, t := range targets {
		i, t := i, t
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			results[i] = c.compileOne(ctx, t)
		}()
	}
	wg.Wait()
	return results
}

func (c *Compiler) compileOne(ctx context.Context, target string) Result {
	start := time.Now()
	r := Result{Target: target}

	smi, smiDiags, err := c.Smidump.DumpModule(ctx, target)
	if err != nil {
		r.Err = err
		r.Diagnostics = smiDiags
		r.Duration = time.Since(start)
		slog.Warn("smidump failed", "target", target, "err", err, "duration", r.Duration)
		return r
	}
	r.SMI = smi
	r.Diagnostics = smiDiags

	if c.Smilint != nil {
		diags, lerr := c.Smilint.Lint(ctx, target)
		if lerr != nil {
			slog.Debug("smilint error", "target", target, "err", lerr)
		}
		r.Diagnostics = append(r.Diagnostics, diags...)
	}

	mod, syms := ToModel(smi)

	// A duplicate descriptor is a fault in the MIB, not in the module's
	// other 800 symbols — report it and keep going. This runs BEFORE
	// parseStatusFor so the added warnings are reflected in the stored
	// parse status. The diagnostic anchors on the DROPPED definition
	// and names where the kept one lives, so the operator reading it at
	// the dropped line isn't told the opposite of what happened.
	syms, dropped := DedupeSymbols(syms)
	if len(dropped) > 0 {
		keptByName := make(map[string]*model.Symbol, len(dropped))
		for i := range syms {
			keptByName[syms[i].Name] = &syms[i]
		}
		for _, d := range dropped {
			kept := keptByName[d.Name]
			var where string
			switch {
			case kept.SourceLine > 0:
				where = fmt.Sprintf("the definition at line %d", kept.SourceLine)
			case kept.OID != "":
				where = fmt.Sprintf("the definition at OID %s", kept.OID)
			default:
				where = "another definition"
			}
			r.Diagnostics = append(r.Diagnostics, model.Diagnostic{
				File:     target,
				Line:     d.SourceLine,
				Severity: model.SeverityWarning,
				Code:     "duplicate-descriptor",
				Message: fmt.Sprintf(
					"%s is defined more than once in this module; a descriptor must be unique, so this definition was dropped in favor of %s",
					d.Name, where),
			})
		}
	}

	mod.ParseStatus = parseStatusFor(r.Diagnostics)

	// smidump 0.5.0 does not emit a `path=` attribute on the
	// `<module>` element of its XML output, so `ToModel` produces an
	// empty SourcePath. The compile target is the file path we just
	// fed smidump (the loader passes file paths from `walkMIBFiles`),
	// so back-fill SourcePath here when target resolves to a real
	// file. Targets that are bare module names (used in tests) leave
	// SourcePath empty — `os.Stat` distinguishes the cases.
	if mod.SourcePath == "" {
		if abs, err := filepath.Abs(target); err == nil {
			if info, err := os.Stat(abs); err == nil && !info.IsDir() {
				mod.SourcePath = abs
			}
		}
	}

	r.Module = mod
	r.Symbols = syms
	r.Duration = time.Since(start)

	slog.Info("compiled",
		"module", mod.Name,
		"symbols", len(syms),
		"diagnostics", len(r.Diagnostics),
		"status", string(mod.ParseStatus),
		"duration", r.Duration,
	)
	return r
}

func parseStatusFor(diags []model.Diagnostic) model.ParseStatus {
	hasErr, hasWarn := false, false
	for _, d := range diags {
		switch d.Severity {
		case model.SeverityError:
			hasErr = true
		case model.SeverityWarning:
			hasWarn = true
		}
	}
	switch {
	case hasErr:
		return model.ParseStatusErrors
	case hasWarn:
		return model.ParseStatusWarnings
	default:
		return model.ParseStatusClean
	}
}
