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
	err   error
}

func (f *fakeDumper) DumpModule(_ context.Context, target string) (*SMI, error) {
	f.calls.Add(1)
	if f.err != nil {
		return nil, f.err
	}
	return ParseXML(strings.NewReader(f.xml))
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
