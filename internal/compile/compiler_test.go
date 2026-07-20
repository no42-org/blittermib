package compile

import (
	"context"
	"errors"
	"os"
	"path/filepath"
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

// TestCompiler_BackfillsSourcePathFromTarget pins the production
// path's SourcePath population: smidump 0.5.0's XML output omits the
// `path=` attribute on `<module>`, so `ToModel` produces an empty
// SourcePath. The compile pipeline must back-fill from `target`
// when `target` resolves to a real file on disk, otherwise the
// download endpoints (`/m/{name}/download`, `download.zip`) and the
// module-info bar's download affordances are dead code: every
// module ships with empty SourcePath, the handler's `SourcePath != ""`
// gate fires 404, and `ModuleDownloadable` stays false in the UI.
func TestCompiler_BackfillsSourcePathFromTarget(t *testing.T) {
	// Real file on disk — this is what the loader passes to
	// compileOne via walkMIBFiles. fakeDumper still parses the
	// canned IF-MIB XML fixture; we only care about SourcePath
	// back-fill here.
	dir := t.TempDir()
	target := filepath.Join(dir, "IF-MIB.txt")
	if err := os.WriteFile(target, []byte("placeholder"), 0o644); err != nil {
		t.Fatalf("seed target: %v", err)
	}

	d := &fakeDumper{xml: loadFixtureXML(t)}
	c := &Compiler{Smidump: d}
	results := c.Compile(context.Background(), []string{target})
	if len(results) != 1 {
		t.Fatalf("results = %d, want 1", len(results))
	}
	r := results[0]
	if r.Err != nil {
		t.Fatalf("compile: %v", r.Err)
	}
	if r.Module == nil {
		t.Fatalf("module nil")
	}
	// Absolute form so the path-traversal guard's filepath.Rel
	// computation works regardless of how the loader phrased it.
	if !strings.HasSuffix(r.Module.SourcePath, "IF-MIB.txt") {
		t.Errorf("SourcePath = %q, want suffix %q", r.Module.SourcePath, "IF-MIB.txt")
	}
	if !filepath.IsAbs(r.Module.SourcePath) {
		t.Errorf("SourcePath = %q, want absolute path", r.Module.SourcePath)
	}
}

// TestCompiler_LeavesSourcePathEmptyForNameTargets verifies the
// back-fill is gated on `os.Stat` success — bare module names that
// don't resolve to a file (the form used by other unit tests in
// this package) leave SourcePath empty rather than synthesising a
// bogus path.
func TestCompiler_LeavesSourcePathEmptyForNameTargets(t *testing.T) {
	d := &fakeDumper{xml: loadFixtureXML(t)}
	c := &Compiler{Smidump: d}
	results := c.Compile(context.Background(), []string{"IF-MIB"})
	if len(results) != 1 {
		t.Fatalf("results = %d", len(results))
	}
	if got := results[0].Module.SourcePath; got != "" {
		t.Errorf("SourcePath = %q, want empty for non-file target", got)
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

// TestCompileReportsDuplicateDescriptor pins the graceful degradation
// for a MIB that defines the same descriptor twice: the module still
// compiles, the duplicate is dropped rather than failing the whole
// file on the store's UNIQUE (module_name, name), and the operator
// gets a diagnostic saying so.
func TestCompileReportsDuplicateDescriptor(t *testing.T) {
	// Two <node> entries sharing a name — what smidump emits for a MIB
	// that assigns the same descriptor twice.
	const dupXML = `<?xml version="1.0"?>
<smi version="1.0">
  <module name="DUP-MIB" language="SMIv2">
    <organization>test</organization>
    <contact>test</contact>
    <description>dup</description>
  </module>
  <imports/>
  <typedefs/>
  <nodes>
    <node name="system" oid="1.3.6.1.2.1.1" status="current" line="10">
      <description>first</description>
    </node>
    <node name="unique" oid="1.3.6.1.2.1.2" status="current" line="20">
      <description>only</description>
    </node>
    <node name="system" oid="1.3.6.1.2.1.3" status="current" line="30">
      <description>second</description>
    </node>
  </nodes>
</smi>`

	c := &Compiler{Smidump: &fakeDumper{xml: dupXML}, Concurrency: 1}
	results := c.Compile(context.Background(), []string{"DUP-MIB"})
	if len(results) != 1 {
		t.Fatalf("results = %d, want 1", len(results))
	}
	r := results[0]
	if r.Err != nil {
		t.Fatalf("compile failed on a duplicate descriptor: %v", r.Err)
	}

	if len(r.Symbols) != 2 {
		t.Errorf("symbols = %d, want 2 (the duplicate dropped)", len(r.Symbols))
	}
	seen := map[string]int{}
	for _, s := range r.Symbols {
		seen[s.Name]++
	}
	if seen["system"] != 1 {
		t.Errorf("`system` appears %d times, want exactly 1", seen["system"])
	}

	var found *model.Diagnostic
	for i := range r.Diagnostics {
		if r.Diagnostics[i].Code == "duplicate-descriptor" {
			found = &r.Diagnostics[i]
			break
		}
	}
	if found == nil {
		t.Fatalf("no duplicate-descriptor diagnostic in %+v", r.Diagnostics)
	}
	if !strings.Contains(found.Message, "system") {
		t.Errorf("diagnostic should name the descriptor, got %q", found.Message)
	}
	if found.Line != 30 {
		t.Errorf("diagnostic line = %d, want 30 (the dropped definition)", found.Line)
	}
	// The message anchors on the dropped definition and must point at
	// where the kept one lives.
	if !strings.Contains(found.Message, "line 10") {
		t.Errorf("diagnostic should name the kept definition's line, got %q", found.Message)
	}
	if r.Module.ParseStatus != model.ParseStatusWarnings {
		t.Errorf("ParseStatus = %q, want %q", r.Module.ParseStatus, model.ParseStatusWarnings)
	}
}

// TestCompileDuplicateAcrossKindsKeepsSourceOrder pins the cross-kind
// duplicate (the A10 ACOS pattern): the same descriptor as a scalar and
// a table column. ToModel appends scalars before columns, so slice
// order would keep the scalar — but the column appears FIRST in the
// source file and must win, with the diagnostic anchored on the dropped
// scalar and naming the kept column's line.
func TestCompileDuplicateAcrossKindsKeepsSourceOrder(t *testing.T) {
	const dupXML = `<?xml version="1.0"?>
<smi version="1.0">
  <module name="DUP2-MIB" language="SMIv2">
    <organization>test</organization>
    <contact>test</contact>
    <description>dup</description>
  </module>
  <imports/>
  <typedefs/>
  <nodes>
    <table name="fooTable" oid="1.3.6.1.9.1" status="current" line="15">
      <row name="fooEntry" oid="1.3.6.1.9.1.1" status="current" line="17">
        <column name="foo" oid="1.3.6.1.9.1.1.1" status="current" line="20">
          <description>the real foo, defined first in the file</description>
        </column>
      </row>
    </table>
    <scalar name="foo" oid="1.3.6.1.9.2" status="current" line="50">
      <description>illegal redefinition, later in the file</description>
    </scalar>
  </nodes>
</smi>`

	c := &Compiler{Smidump: &fakeDumper{xml: dupXML}, Concurrency: 1}
	results := c.Compile(context.Background(), []string{"DUP2-MIB"})
	if len(results) != 1 || results[0].Err != nil {
		t.Fatalf("compile: %+v", results)
	}
	r := results[0]

	var foo *model.Symbol
	for i := range r.Symbols {
		if r.Symbols[i].Name == "foo" {
			if foo != nil {
				t.Fatal("`foo` survived twice")
			}
			foo = &r.Symbols[i]
		}
	}
	if foo == nil {
		t.Fatal("`foo` missing entirely")
	}
	if foo.Kind != model.KindColumn || foo.SourceLine != 20 {
		t.Errorf("kept foo = kind %q line %d, want the line-20 column (first in source)",
			foo.Kind, foo.SourceLine)
	}

	var diag *model.Diagnostic
	for i := range r.Diagnostics {
		if r.Diagnostics[i].Code == "duplicate-descriptor" {
			diag = &r.Diagnostics[i]
			break
		}
	}
	if diag == nil {
		t.Fatal("no duplicate-descriptor diagnostic")
	}
	if diag.Line != 50 {
		t.Errorf("diagnostic line = %d, want 50 (the dropped scalar)", diag.Line)
	}
	if !strings.Contains(diag.Message, "line 20") {
		t.Errorf("diagnostic should point at the kept line-20 column, got %q", diag.Message)
	}
}
