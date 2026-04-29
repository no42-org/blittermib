package compile

import (
	"strings"
	"testing"

	"github.com/no42-org/blittermib/internal/model"
)

func TestParseSmilintOutput(t *testing.T) {
	output := `
IF-MIB.txt:142: warning: {compliance-non-current} compliance does not include all mandatory groups
CISCO-WEIRD-MIB.txt:198: error: identifier "FooBar" not found
some-other.txt:24: note: typedef lacks DISPLAY-HINT
malformed line that should be skipped
`
	diags := ParseSmilintOutput(strings.NewReader(output))

	if got, want := len(diags), 3; got != want {
		t.Fatalf("got %d diagnostics, want %d", got, want)
	}

	d := diags[0]
	if d.File != "IF-MIB.txt" || d.Line != 142 || d.Severity != model.SeverityWarning {
		t.Errorf("first diag: %+v", d)
	}
	if d.Code != "compliance-non-current" {
		t.Errorf("code = %q", d.Code)
	}
	if !strings.Contains(d.Message, "compliance does not include") {
		t.Errorf("message = %q", d.Message)
	}

	if diags[1].Severity != model.SeverityError {
		t.Errorf("second severity = %q, want error", diags[1].Severity)
	}
	if diags[2].Severity != model.SeverityNote {
		t.Errorf("third severity = %q, want note", diags[2].Severity)
	}
}

func TestParseSmilintLineSkipsNonsense(t *testing.T) {
	cases := []string{
		"",
		"no colons here",
		"only:two",
		"file.mib:notanumber: warning: foo",
	}
	for _, c := range cases {
		if _, ok := parseSmilintLine(c); ok {
			t.Errorf("expected to skip: %q", c)
		}
	}
}
