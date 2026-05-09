package compile

import (
	"strings"
	"testing"

	"github.com/no42-org/blittermib/internal/model"
)

// minimumSmidumpXML returns a stripped-down but structurally valid
// smidump-style XML document with the supplied bytes embedded inside a
// <description> element. The host XML is ASCII-only so any encoding
// failure during parse is unambiguously caused by descBytes.
func minimumSmidumpXML(descBytes []byte) []byte {
	const head = `<?xml version="1.0"?>
<smi version="0.5">
  <module name="TEST-MIB" language="SMIv2">
    <organization>test</organization>
    <contact>test</contact>
    <description>`
	const tail = `</description>
  </module>
  <imports/>
  <nodes/>
</smi>`
	out := make([]byte, 0, len(head)+len(descBytes)+len(tail))
	out = append(out, head...)
	out = append(out, descBytes...)
	out = append(out, tail...)
	return out
}

func TestParseSmidumpXMLWithRecovery_LatinOneDegreeSign(t *testing.T) {
	// 0xB0 in a description triggers "invalid UTF-8" on the first
	// parse; the sanitize pass expands it to two-byte UTF-8 and the
	// re-parse succeeds.
	raw := minimumSmidumpXML([]byte{'2', '5', 0xB0, 'C'})

	smi, recovery, err := parseSmidumpXMLWithRecovery("TEST-MIB", raw)
	if err != nil {
		t.Fatalf("expected recovery to succeed, got error: %v", err)
	}
	if smi == nil {
		t.Fatal("expected non-nil SMI from recovery")
	}
	if smi.Module.Name != "TEST-MIB" {
		t.Errorf("Module.Name = %q, want TEST-MIB", smi.Module.Name)
	}
	if recovery == nil {
		t.Fatal("expected non-nil recovery diagnostic")
	}
	if recovery.Severity != model.SeverityWarning {
		t.Errorf("Severity = %q, want %q", recovery.Severity, model.SeverityWarning)
	}
	if recovery.Code != "non-utf8-source" {
		t.Errorf("Code = %q, want non-utf8-source", recovery.Code)
	}
	if recovery.File != "TEST-MIB" {
		t.Errorf("File = %q, want TEST-MIB", recovery.File)
	}
	if recovery.Line != 0 {
		t.Errorf("Line = %d, want 0", recovery.Line)
	}
	if !strings.Contains(recovery.Message, "UTF-8") {
		t.Errorf("Message = %q, want one that mentions UTF-8", recovery.Message)
	}
	// Description should now contain U+00B0 (two-byte UTF-8: 0xC2 0xB0).
	if !strings.Contains(smi.Module.Description, "°") {
		t.Errorf("recovered description missing U+00B0; got %q", smi.Module.Description)
	}
}

func TestParseSmidumpXMLWithRecovery_BackspaceControl(t *testing.T) {
	// 0x08 (backspace) is XML-illegal regardless of encoding; the
	// sanitize pass strips it.
	raw := minimumSmidumpXML([]byte{'A', 0x08, 'B'})

	smi, recovery, err := parseSmidumpXMLWithRecovery("TEST-MIB", raw)
	if err != nil {
		t.Fatalf("expected recovery to succeed, got error: %v", err)
	}
	if smi == nil {
		t.Fatal("expected non-nil SMI")
	}
	if recovery == nil || recovery.Code != "non-utf8-source" {
		t.Fatalf("expected non-utf8-source recovery diagnostic, got %+v", recovery)
	}
	// Description should contain "AB" with the 0x08 stripped.
	if !strings.Contains(smi.Module.Description, "AB") {
		t.Errorf("expected description to contain 'AB' after stripping backspace, got %q", smi.Module.Description)
	}
}

func TestParseSmidumpXMLWithRecovery_CleanInputNoRecovery(t *testing.T) {
	// Pure ASCII / valid UTF-8 input: hot path takes one ParseXML call,
	// no recovery diagnostic is attached.
	raw := minimumSmidumpXML([]byte("plain ascii description"))

	smi, recovery, err := parseSmidumpXMLWithRecovery("TEST-MIB", raw)
	if err != nil {
		t.Fatalf("clean input should parse, got error: %v", err)
	}
	if smi == nil {
		t.Fatal("expected non-nil SMI")
	}
	if recovery != nil {
		t.Errorf("expected no recovery diagnostic for clean input, got %+v", recovery)
	}
}

func TestParseSmidumpXMLWithRecovery_TruncatedXMLNoRetry(t *testing.T) {
	// Structurally truncated XML returns its original error and is NOT
	// fed through the sanitize-retry path — the predicate only matches
	// encoding-class failures.
	raw := []byte(`<?xml version="1.0"?><smi version="0.5"><module name="TEST-MIB"`)

	smi, recovery, err := parseSmidumpXMLWithRecovery("TEST-MIB", raw)
	if err == nil {
		t.Fatal("expected truncated XML to return an error")
	}
	if smi != nil {
		t.Error("expected nil SMI on parse failure")
	}
	if recovery != nil {
		t.Errorf("expected no recovery diagnostic on truncated XML, got %+v", recovery)
	}
	if !strings.Contains(err.Error(), "parse smidump output for TEST-MIB") {
		t.Errorf("error wrapping changed: %v", err)
	}
}

func TestParseSmidumpXMLWithRecovery_UnrecoverableAfterSanitize(t *testing.T) {
	// Latin-1 byte trips the encoding error so the sanitize pass runs;
	// but the document is also structurally broken, so the second parse
	// fails too. Caller should see the second-parse error wrapped with
	// the same prefix as the first-parse failure path, and no recovery
	// diagnostic.
	raw := []byte(`<?xml version="1.0"?><smi><module name="X"><description>` +
		string([]byte{0xB0}) + `</description><nodes`)

	smi, recovery, err := parseSmidumpXMLWithRecovery("TEST-MIB", raw)
	if err == nil {
		t.Fatal("expected unrecoverable parse failure")
	}
	if smi != nil {
		t.Error("expected nil SMI on unrecoverable failure")
	}
	if recovery != nil {
		t.Errorf("expected no recovery diagnostic when second parse fails, got %+v", recovery)
	}
	if !strings.Contains(err.Error(), "parse smidump output for TEST-MIB") {
		t.Errorf("error wrap prefix changed on unrecoverable path: %v", err)
	}
}

func TestParseSmidumpXMLWithRecovery_ValidMultibyteUTF8HotPath(t *testing.T) {
	// Valid UTF-8 multi-byte sequences (here: 'é' = 0xC3 0xA9) parse on
	// the first attempt — no recovery, no diagnostic, no allocation
	// beyond the normal parse.
	raw := minimumSmidumpXML([]byte{'a', 0xC3, 0xA9, 'b'})

	smi, recovery, err := parseSmidumpXMLWithRecovery("TEST-MIB", raw)
	if err != nil {
		t.Fatalf("valid UTF-8 should parse on first attempt, got error: %v", err)
	}
	if smi == nil {
		t.Fatal("expected non-nil SMI")
	}
	if recovery != nil {
		t.Errorf("expected no recovery diagnostic for valid UTF-8 input, got %+v", recovery)
	}
	if !strings.Contains(smi.Module.Description, "é") {
		t.Errorf("description should contain 'é', got %q", smi.Module.Description)
	}
}

func TestParseSmidumpXMLWithRecovery_MixedValidUTF8AndInvalidByte(t *testing.T) {
	// A description with a valid UTF-8 'é' alongside a stray 0xB0
	// triggers recovery. The fix preserves the valid UTF-8 sequence
	// verbatim and only Latin-1-expands the invalid byte — so 'é'
	// survives the round-trip rather than being re-encoded as 'Ã©'.
	raw := minimumSmidumpXML([]byte{'a', 0xC3, 0xA9, 0xB0, 'b'})

	smi, recovery, err := parseSmidumpXMLWithRecovery("TEST-MIB", raw)
	if err != nil {
		t.Fatalf("expected recovery to succeed, got error: %v", err)
	}
	if recovery == nil || recovery.Code != "non-utf8-source" {
		t.Fatalf("expected non-utf8-source diagnostic, got %+v", recovery)
	}
	if !strings.Contains(smi.Module.Description, "é") {
		t.Errorf("valid UTF-8 'é' was corrupted by sanitize; description = %q", smi.Module.Description)
	}
	if !strings.Contains(smi.Module.Description, "°") {
		t.Errorf("invalid 0xB0 byte was not Latin-1-recovered; description = %q", smi.Module.Description)
	}
}
