package server

import (
	"archive/zip"
	"bytes"
	"context"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/no42-org/blittermib/internal/store"
)

func TestWalkUploadPage(t *testing.T) {
	ts := newTestServer(t)
	resp, err := http.Get(ts.URL + "/walk")
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	html := body(t, resp)
	for _, want := range []string{"<textarea", `type="file"`, `action="/walk/decode"`} {
		if !strings.Contains(html, want) {
			t.Errorf("upload page missing %q", want)
		}
	}
}

func TestWalkDecodeGroupsAndFallsBack(t *testing.T) {
	ts := newTestServer(t)
	walk := ".1.3.6.1.2.1.2.2.1.10.1 = Counter32: 12345\n" +
		".1.3.6.1.4.1.9.2.1.58.0 = INTEGER: 7\n"
	resp, err := http.PostForm(ts.URL+"/walk/decode", url.Values{"walk": {walk}})
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	html := body(t, resp)
	// The module the walk resolved into is summarised and links into its
	// workspace with the walk filter pre-applied.
	for _, want := range []string{"MIBs in this walk", "IF-MIB", "/m/IF-MIB#in-walk"} {
		if !strings.Contains(html, want) {
			t.Errorf("results missing summary element %q", want)
		}
	}
	// The results page is a summary, not a per-instance dump — symbol
	// names live in the workspace now.
	if strings.Contains(html, "ifInOctets") {
		t.Errorf("results should summarise per module, not list symbol %q", "ifInOctets")
	}
	// Unresolved Cisco enterprise OID falls back to the PEN hint.
	for _, want := range []string{"Unresolved OIDs", "ciscoSystems"} {
		if !strings.Contains(html, want) {
			t.Errorf("results missing unresolved/PEN %q", want)
		}
	}
}

// Name-prefixed captures (default snmpwalk output without -On) must
// summarise and decorate like numeric ones: a summary row with the
// #in-walk launcher link, and the walk-data payload carrying the
// reconstructed numeric instance OID.
func TestWalkDecodeNamePrefixed(t *testing.T) {
	ts := newTestServer(t)
	walk := "IF-MIB::ifInOctets.1 = Counter32: 84572301\n"
	resp, err := http.PostForm(ts.URL+"/walk/decode", url.Values{"walk": {walk}})
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	html := body(t, resp)
	for _, want := range []string{
		"MIBs in this walk",
		"/m/IF-MIB#in-walk",
		"1 object · 1 value",
		// walk-data payload: numeric instance OID reconstructed from the
		// resolved symbol OID + suffix (JSON-escaped inside the attr).
		"1.3.6.1.2.1.2.2.1.10.1",
		"84572301",
	} {
		if !strings.Contains(html, want) {
			t.Errorf("name-prefixed results missing %q", want)
		}
	}
	if strings.Contains(html, "No OIDs in this capture resolved") {
		t.Error("fully-resolved name-prefixed walk shows the nothing-resolved empty state")
	}
}

// The results page collapses many instances of the same object into one
// per-module summary row — the object count is distinct symbols, the
// value count is instances.
func TestWalkResultsSummaryAggregates(t *testing.T) {
	ts := newTestServer(t)
	// Three instances of ifInOctets → one object, three values.
	walk := ".1.3.6.1.2.1.2.2.1.10.1 = Counter32: 1\n" +
		".1.3.6.1.2.1.2.2.1.10.2 = Counter32: 2\n" +
		".1.3.6.1.2.1.2.2.1.10.3 = Counter32: 3\n"
	resp, err := http.PostForm(ts.URL+"/walk/decode", url.Values{"walk": {walk}})
	if err != nil {
		t.Fatal(err)
	}
	html := body(t, resp)

	if n := strings.Count(html, "/m/IF-MIB#in-walk"); n != 1 {
		t.Errorf("got %d IF-MIB summary rows, want exactly 1 (per module, not per instance)", n)
	}
	if !strings.Contains(html, "1 object · 3 values") {
		t.Errorf("results missing aggregated counts %q", "1 object · 3 values")
	}
}

// The results page must emit the walk-data element the workspace
// overlay (walk-overlay.js) reads to persist into localStorage.
func TestWalkDecodeEmitsWalkData(t *testing.T) {
	ts := newTestServer(t)
	walk := ".1.3.6.1.2.1.2.2.1.10.1 = Counter32: 12345\n"
	resp, err := http.PostForm(ts.URL+"/walk/decode", url.Values{"walk": {walk}})
	if err != nil {
		t.Fatal(err)
	}
	html := body(t, resp)
	for _, want := range []string{`id="blittermib-walk-data"`, "1.3.6.1.2.1.2.2.1.10.1", "12345"} {
		if !strings.Contains(html, want) {
			t.Errorf("results page missing walk-data %q", want)
		}
	}
}

const sampleMIB = `FOO-MIB DEFINITIONS ::= BEGIN
IMPORTS OBJECT-TYPE FROM SNMPv2-SMI;
foo OBJECT-TYPE SYNTAX INTEGER ::= { bar 1 }
END
`

const sampleWalk = `.1.3.6.1.2.1.1.1.0 = STRING: example
.1.3.6.1.2.1.1.5.0 = STRING: host.example
`

func TestContentSniffDetectors(t *testing.T) {
	if !contentLooksLikeMIB(sampleMIB) {
		t.Error("MIB not detected as MIB")
	}
	if contentLooksLikeMIB(sampleWalk) {
		t.Error("walk misdetected as MIB")
	}
	if !contentLooksLikeWalk(sampleWalk) {
		t.Error("walk not detected as walk")
	}
	if contentLooksLikeWalk(sampleMIB) {
		t.Error("MIB misdetected as walk")
	}
}

// A MIB pasted into the walk decoder is redirected to /import.
func TestWalkDecodeRejectsMIB(t *testing.T) {
	ts := newTestServer(t)
	resp, err := http.PostForm(ts.URL+"/walk/decode", url.Values{"walk": {sampleMIB}})
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422", resp.StatusCode)
	}
	if html := body(t, resp); !strings.Contains(html, "/import") {
		t.Errorf("MIB-rejection page should point to /import")
	}
}

// A walk uploaded to the MIB importer is redirected to /walk.
func TestUploadRejectsWalkWithHint(t *testing.T) {
	t.Setenv("BLITTERMIB_UPLOAD_ENABLED", "true")
	ts, _, _ := newUploadServer(t)
	resp, out := postUpload(t, ts, "", map[string]string{"SRX-WALK.txt": sampleWalk})
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422", resp.StatusCode)
	}
	if !strings.Contains(string(out), "/walk") {
		t.Errorf("walk-rejection should point to /walk; got %s", out)
	}
}

func TestWalkDecodeOversizedRefused(t *testing.T) {
	ts := newTestServer(t)
	big := strings.Repeat("x", walkMaxBytes+4096) // just over the cap
	resp, err := http.PostForm(ts.URL+"/walk/decode", url.Values{"walk": {big}})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413", resp.StatusCode)
	}
}

// The decode form posts multipart/form-data, so the oversize cap must
// also fire on that path — not just the urlencoded one.
func TestWalkDecodeOversizedMultipartRefused(t *testing.T) {
	ts := newTestServer(t)
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, err := mw.CreateFormField("walk")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fw.Write([]byte(strings.Repeat("x", walkMaxBytes+4096))); err != nil {
		t.Fatal(err)
	}
	if err := mw.Close(); err != nil {
		t.Fatal(err)
	}
	resp, err := http.Post(ts.URL+"/walk/decode", mw.FormDataContentType(), &buf)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("multipart oversize status = %d, want 413", resp.StatusCode)
	}
}

func TestWalkDecodeEmptyRefused(t *testing.T) {
	ts := newTestServer(t)
	resp, err := http.PostForm(ts.URL+"/walk/decode", url.Values{"walk": {"   "}})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
}

// The privacy invariant (design Decision 1): the decode path must not
// log walk values. A sentinel value posted in the walk must never
// appear in the server's log output.
func TestWalkDecodeDoesNotLogValues(t *testing.T) {
	var logBuf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logBuf, nil)))
	defer slog.SetDefault(prev)

	ts := newTestServer(t)
	const sentinel = "S3NT1NEL-secret-walk-value"
	walk := ".1.3.6.1.2.1.1.1.0 = STRING: " + sentinel + "\n"
	resp, err := http.PostForm(ts.URL+"/walk/decode", url.Values{"walk": {walk}})
	if err != nil {
		t.Fatal(err)
	}
	_ = body(t, resp)

	if strings.Contains(logBuf.String(), sentinel) {
		t.Errorf("walk value leaked into logs:\n%s", logBuf.String())
	}
}

func TestWalkBundleContents(t *testing.T) {
	ts := newTestServer(t)
	walk := ".1.3.6.1.2.1.2.2.1.10.1 = Counter32: 12345\n" +
		".1.3.6.1.4.1.9.2.1.58.0 = INTEGER: 7\n"
	resp, err := http.PostForm(ts.URL+"/walk/bundle", url.Values{"walk": {walk}})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "application/zip" {
		t.Errorf("Content-Type = %q, want application/zip", ct)
	}
	if cd := resp.Header.Get("Content-Disposition"); !strings.Contains(cd, "walk-bundle-") {
		t.Errorf("Content-Disposition = %q, want walk-bundle-<date>.zip", cd)
	}

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	zr, err := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
	if err != nil {
		t.Fatalf("zip open: %v", err)
	}

	files := map[string]string{}
	for _, f := range zr.File {
		rc, err := f.Open()
		if err != nil {
			t.Fatalf("open %s: %v", f.Name, err)
		}
		b, _ := io.ReadAll(rc)
		_ = rc.Close()
		files[f.Name] = string(b)
	}

	if _, ok := files["walk.txt"]; !ok {
		t.Fatal("bundle missing walk.txt")
	}
	if files["walk.txt"] != walk {
		t.Errorf("walk.txt not byte-identical:\n%q", files["walk.txt"])
	}
	if _, ok := files["README.txt"]; !ok {
		t.Error("bundle missing README.txt")
	}
	miss, ok := files["MISSING.txt"]
	if !ok {
		t.Fatal("bundle missing MISSING.txt")
	}
	// The unresolved Cisco enterprise OID is recorded in MISSING.txt.
	for _, want := range []string{"no covering module", "1.3.6.1.4.1.9"} {
		if !strings.Contains(miss, want) {
			t.Errorf("MISSING.txt missing %q:\n%s", want, miss)
		}
	}
}

// TestEnableWalkRespectsEnv covers the BLITTERMIB_WALK_DECODER_ENABLED gate:
// the decoder only comes up when the env var parses as truthy. Every
// other configuration leaves WalkEnabled() false (decoder disabled,
// routes unregistered).
func TestEnableWalkRespectsEnv(t *testing.T) {
	cases := []struct {
		name string
		env  string
		want bool
	}{
		{"empty env", "", false},
		{"explicit false", "false", false},
		{"non-bool junk", "yes", false},
		{"true enables", "true", true},
		{"1 enables", "1", true},
		{"True enables", "True", true},
		{"t enables", "t", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Setenv("BLITTERMIB_WALK_DECODER_ENABLED", c.env)

			st, err := store.OpenInMemory(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = st.Close() })

			s := New(st, "", "test", t.TempDir())
			s.EnableWalk()
			if got := s.WalkEnabled(); got != c.want {
				t.Errorf("WalkEnabled() = %v, want %v", got, c.want)
			}
		})
	}
}

// TestWalkRoutesGate asserts the walk routes are unreachable when the
// flag is off and registered when it is on.
func TestWalkRoutesGate(t *testing.T) {
	t.Run("disabled", func(t *testing.T) {
		t.Setenv("BLITTERMIB_WALK_DECODER_ENABLED", "")
		st, err := store.OpenInMemory(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = st.Close() })
		s := New(st, "", "test", t.TempDir())
		s.EnableWalk()
		ts := httptest.NewServer(s.Handler())
		t.Cleanup(ts.Close)

		assertStatus(t, ts, "GET", "/walk", http.StatusNotFound)
		assertStatus(t, ts, "POST", "/walk/decode", http.StatusNotFound)
		assertStatus(t, ts, "POST", "/walk/bundle", http.StatusNotFound)
	})
	t.Run("enabled", func(t *testing.T) {
		// newTestServer enables the decoder.
		ts := newTestServer(t)
		assertStatus(t, ts, "GET", "/walk", http.StatusOK)
	})
}
