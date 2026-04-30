package compile

import (
	"context"
	"errors"
	"os"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/no42-org/blittermib/internal/model"
)

// loadFixtureXML reads the captured smidump XML once for tests that
// need it as a string (e.g. seeding fakeDumper).
func loadFixtureXML(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile(fixturePath)
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	return string(b)
}

type fakeDumper struct {
	calls atomic.Int64
	xml   string
	diags []model.Diagnostic
	err   error
}

func (f *fakeDumper) DumpModule(_ context.Context, target string) (*SMI, []model.Diagnostic, error) {
	f.calls.Add(1)
	if f.err != nil {
		return nil, f.diags, f.err
	}
	smi, err := ParseXML(strings.NewReader(f.xml))
	return smi, f.diags, err
}

type fakeLinter struct {
	diags []model.Diagnostic
	err   error
}

func (f *fakeLinter) Lint(_ context.Context, _ string) ([]model.Diagnostic, error) {
	return f.diags, f.err
}

func TestCompiler_OK(t *testing.T) {
	d := &fakeDumper{xml: loadFixtureXML(t)}
	l := &fakeLinter{}
	c := &Compiler{Smidump: d, Smilint: l, Concurrency: 4}
	ctx := context.Background()

	results := c.Compile(ctx, []string{"IF-MIB", "IF-MIB", "IF-MIB"})

	if got, want := len(results), 3; got != want {
		t.Fatalf("results = %d, want %d", got, want)
	}
	if got := d.calls.Load(); got != 3 {
		t.Errorf("DumpModule calls = %d, want 3", got)
	}
	for i, r := range results {
		if r.Err != nil {
			t.Fatalf("result %d: %v", i, r.Err)
		}
		if r.Module == nil || r.Module.Name != "IF-MIB" {
			t.Errorf("result %d: module = %+v", i, r.Module)
		}
		if len(r.Symbols) == 0 {
			t.Errorf("result %d: no symbols", i)
		}
		if r.Module.ParseStatus != model.ParseStatusClean {
			t.Errorf("result %d: parse status = %q", i, r.Module.ParseStatus)
		}
	}
}

func TestCompiler_DumpFailure(t *testing.T) {
	d := &fakeDumper{err: errors.New("boom")}
	c := &Compiler{Smidump: d}
	results := c.Compile(context.Background(), []string{"BAD-MIB"})

	if len(results) != 1 {
		t.Fatalf("results = %d", len(results))
	}
	if results[0].Err == nil {
		t.Error("expected Err to be set on dump failure")
	}
	if results[0].Module != nil {
		t.Error("module should be nil on dump failure")
	}
}

// TestCompiler_MergesSmidumpDiagnostics pins the post-`-k` behaviour:
// smidump exits 0 but emits warnings on stderr. Those warnings must
// land in r.Diagnostics alongside smilint's, and must flip ParseStatus
// to "warnings" — otherwise a `-k`-rescued module would be silently
// labelled "clean" even though smidump complained.
func TestCompiler_MergesSmidumpDiagnostics(t *testing.T) {
	smidumpDiag := model.Diagnostic{
		File:     "/some/IF-MIB",
		Line:     64,
		Severity: model.SeverityWarning,
		Message:  "revision for last update is missing",
	}
	smilintDiag := model.Diagnostic{
		File:     "/some/IF-MIB",
		Line:     128,
		Severity: model.SeverityWarning,
		Code:     "import-unused",
		Message:  "imported symbol unused",
	}

	d := &fakeDumper{xml: loadFixtureXML(t), diags: []model.Diagnostic{smidumpDiag}}
	l := &fakeLinter{diags: []model.Diagnostic{smilintDiag}}
	c := &Compiler{Smidump: d, Smilint: l, Concurrency: 1}

	results := c.Compile(context.Background(), []string{"IF-MIB"})
	if len(results) != 1 || results[0].Err != nil {
		t.Fatalf("compile failed: %+v", results)
	}
	r := results[0]
	if got, want := len(r.Diagnostics), 2; got != want {
		t.Fatalf("diagnostics = %d, want %d (smidump+smilint merged)", got, want)
	}
	// First entry should be smidump's (preserves pipeline order).
	if r.Diagnostics[0].Message != smidumpDiag.Message {
		t.Errorf("first diag = %+v, want smidump's", r.Diagnostics[0])
	}
	if r.Module.ParseStatus != model.ParseStatusWarnings {
		t.Errorf("ParseStatus = %q, want %q (smidump warning should flip clean→warnings)",
			r.Module.ParseStatus, model.ParseStatusWarnings)
	}
}

// TestCompiler_DumpFailureSurfacesSmidumpDiagnostics: when smidump
// exits non-zero, any diagnostics it managed to emit on stderr are
// still attached to the result so the operator can see why it failed.
func TestCompiler_DumpFailureSurfacesSmidumpDiagnostics(t *testing.T) {
	diag := model.Diagnostic{
		File: "/x", Line: 1, Severity: model.SeverityError,
		Message: "fatal parse error",
	}
	d := &fakeDumper{err: errors.New("boom"), diags: []model.Diagnostic{diag}}
	c := &Compiler{Smidump: d}
	results := c.Compile(context.Background(), []string{"BROKEN"})
	if len(results) != 1 {
		t.Fatalf("results = %d", len(results))
	}
	r := results[0]
	if r.Err == nil {
		t.Fatal("expected Err set")
	}
	if got, want := len(r.Diagnostics), 1; got != want {
		t.Errorf("diagnostics = %d, want %d (kept on dump failure)", got, want)
	}
}

func TestParseStatusFor(t *testing.T) {
	cases := []struct {
		name  string
		diags []model.Diagnostic
		want  model.ParseStatus
	}{
		{"empty", nil, model.ParseStatusClean},
		{"warn only", []model.Diagnostic{{Severity: model.SeverityWarning}}, model.ParseStatusWarnings},
		{"err wins", []model.Diagnostic{{Severity: model.SeverityWarning}, {Severity: model.SeverityError}}, model.ParseStatusErrors},
		{"note only", []model.Diagnostic{{Severity: model.SeverityNote}}, model.ParseStatusClean},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := parseStatusFor(c.diags); got != c.want {
				t.Errorf("got %q, want %q", got, c.want)
			}
		})
	}
}
