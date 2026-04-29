package compile

import (
	"context"
	"log/slog"
	"runtime"
	"sync"
	"time"

	"github.com/no42-org/blittermib/internal/model"
)

// Smidumper produces structured SMI documents from a MIB target
// (module name or file path).
type Smidumper interface {
	DumpModule(ctx context.Context, target string) (*SMI, error)
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

	smi, err := c.Smidump.DumpModule(ctx, target)
	if err != nil {
		r.Err = err
		r.Duration = time.Since(start)
		slog.Warn("smidump failed", "target", target, "err", err, "duration", r.Duration)
		return r
	}
	r.SMI = smi

	if c.Smilint != nil {
		diags, lerr := c.Smilint.Lint(ctx, target)
		if lerr != nil {
			slog.Debug("smilint error", "target", target, "err", lerr)
		}
		r.Diagnostics = diags
	}

	mod, syms := ToModel(smi)
	mod.ParseStatus = parseStatusFor(r.Diagnostics)

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
