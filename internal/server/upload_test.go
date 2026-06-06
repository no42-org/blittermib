package server

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/no42-org/blittermib/internal/mibcorpus"
	"github.com/no42-org/blittermib/internal/mibimport"
	"github.com/no42-org/blittermib/internal/model"
	"github.com/no42-org/blittermib/internal/store"
)

// snmpv2SMI is the libsmi SNMPv2-SMI module read from the in-tree
// corpus. The import pipeline compiles uploaded MIBs with smidump, so
// any fixture importing SNMPv2-SMI needs the real module on the search
// path. newUploadServer seeds it under ietf/core/ in the engine root.
var snmpv2SMI = mustReadCorpus("ietf/core/SNMPv2-SMI")

func mustReadCorpus(rel string) string {
	// Tests run from internal/server; the corpus lives at repo-root mibs/.
	b, err := os.ReadFile(filepath.Join("..", "..", "mibs", rel))
	if err != nil {
		panic("read corpus fixture " + rel + ": " + err.Error())
	}
	return string(b)
}

// vendorMIB is a minimal but smidump-compilable SMIv2 module rooted
// under .1.3.6.1.4.1.99999 (PEN 99999, unknown → vendors/99999-unknown).
// It imports SNMPv2-SMI, so the engine must see that module on the
// search path for the compile to succeed.
const vendorMIB = `TEST-MIB DEFINITIONS ::= BEGIN
IMPORTS
    MODULE-IDENTITY, enterprises FROM SNMPv2-SMI;

testMib MODULE-IDENTITY
    LAST-UPDATED "202601010000Z"
    ORGANIZATION "Test Org"
    CONTACT-INFO  "test@example.com"
    DESCRIPTION   "A minimal test MIB."
    REVISION      "202601010000Z"
    DESCRIPTION   "Initial revision."
    ::= { enterprises 99999 }

END
`

// brokenMIB carries the DEFINITIONS ::= BEGIN marker (so it passes the
// synchronous sniff at the door) but fails to compile to a usable
// module: the leading garbage means smidump emits no MODULE-IDENTITY,
// so the engine quarantines it as "compile produced no module
// identity". (smidump runs with -k, so a merely-missing IMPORT still
// yields a named module and would import — this fixture defeats that by
// denying any module name at all.)
const brokenMIB = `garbage tokens before the header @@@ %%%
DEFINITIONS ::= BEGIN
   not valid SMI body ::= nonsense
END
`

// nonMIB lacks the lexical marker; the sniff rejects it at the door.
const nonMIB = "this is just a README, no MIB content here\n"

// newUploadServer wires a real import Engine (rooted at a temp dir with
// SNMPv2-SMI seeded under ietf/core/) backed by a real on-disk store,
// enables uploads, and returns the httptest server plus the engine and
// its root so tests can inspect the curated tree / quarantine dirs.
func newUploadServer(t *testing.T) (*httptest.Server, *mibimport.Engine, string) {
	t.Helper()
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })

	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "ietf", "core"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "ietf", "core", "SNMPv2-SMI"), []byte(snmpv2SMI), 0o644); err != nil {
		t.Fatal(err)
	}

	eng := mibimport.New(root, st, mibcorpus.GroupMap{})
	if err := eng.EnsureDirs(); err != nil {
		t.Fatal(err)
	}

	s := New(st, "", "test", root)
	s.EnableUploads(eng)
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)
	return ts, eng, root
}

// buildMultipart builds a multipart/form-data body with N files. The
// returned content-type carries the boundary, suitable for the
// request's Content-Type header.
func buildMultipart(t *testing.T, files map[string]string) (io.Reader, string) {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	for name, body := range files {
		w, err := mw.CreateFormFile("files", name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := mw.Close(); err != nil {
		t.Fatal(err)
	}
	return &buf, mw.FormDataContentType()
}

func postUpload(t *testing.T, ts *httptest.Server, query string, files map[string]string) (*http.Response, []byte) {
	t.Helper()
	body, ct := buildMultipart(t, files)
	url := ts.URL + "/api/v1/upload"
	if query != "" {
		url += "?" + query
	}
	req, _ := http.NewRequest(http.MethodPost, url, body)
	req.Header.Set("Content-Type", ct)
	req.Header.Set("X-Blittermib-Upload", "1")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	out, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp, out
}

// deleteUpload issues a DELETE /api/v1/upload/<name> with the sentinel
// header. query is appended verbatim (e.g. "from=failed").
func deleteUpload(t *testing.T, ts *httptest.Server, name, query string) *http.Response {
	t.Helper()
	url := ts.URL + "/api/v1/upload/" + name
	if query != "" {
		url += "?" + query
	}
	req, _ := http.NewRequest(http.MethodDelete, url, nil)
	req.Header.Set("X-Blittermib-Upload", "1")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	return resp
}

func decodeUpload(t *testing.T, body []byte) uploadResponse {
	t.Helper()
	var ur uploadResponse
	if err := json.Unmarshal(body, &ur); err != nil {
		t.Fatalf("decode response: %v\nbody: %s", err, body)
	}
	return ur
}

func getBody(t *testing.T, url string) string {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func excerpt(body, anchor string, n int) string {
	i := strings.Index(body, anchor)
	if i < 0 {
		i = 0
	}
	end := i + n
	if end > len(body) {
		end = len(body)
	}
	return body[i:end]
}

func assertStatus(t *testing.T, ts *httptest.Server, method, path string, want int) {
	t.Helper()
	req, _ := http.NewRequest(method, ts.URL+path, nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != want {
		t.Errorf("%s %s: status = %d, want %d", method, path, resp.StatusCode, want)
	}
}

// TestEnableUploadsRespectsEnv covers the BLITTERMIB_UPLOAD_ENABLED
// gate: the upload routes only come up when the env var parses as
// truthy AND a non-nil import engine is supplied. Every other
// configuration leaves the routes unregistered (uploads disabled,
// fails closed).
func TestEnableUploadsRespectsEnv(t *testing.T) {
	cases := []struct {
		name        string
		env         string
		wireEngine  bool
		wantEnabled bool
	}{
		{"empty env, nil engine", "", false, false},
		{"empty env, real engine", "", true, false},
		{"explicit false", "false", true, false},
		{"non-bool junk", "yes", true, false},
		{"true with nil engine fails closed", "true", false, false},
		{"true with real engine enables", "true", true, true},
		{"1 enables", "1", true, true},
		{"True enables", "True", true, true},
		{"TRUE enables", "TRUE", true, true},
		{"t enables", "t", true, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Setenv("BLITTERMIB_UPLOAD_ENABLED", c.env)

			st, err := store.OpenInMemory(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = st.Close() })

			root := t.TempDir()
			s := New(st, "", "test", root)
			var eng *mibimport.Engine
			if c.wireEngine {
				eng = mibimport.New(root, st, mibcorpus.GroupMap{})
			}
			s.EnableUploads(eng)
			if got := s.UploadsEnabled(); got != c.wantEnabled {
				t.Errorf("UploadsEnabled() = %v, want %v", got, c.wantEnabled)
			}
		})
	}
}

// TestRoutesGate asserts the routes themselves are unreachable when
// the flag is off, and registered (with /upload → 301 /import) when
// uploads are enabled.
func TestRoutesGate(t *testing.T) {
	t.Run("disabled", func(t *testing.T) {
		t.Setenv("BLITTERMIB_UPLOAD_ENABLED", "")
		st, err := store.OpenInMemory(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = st.Close() })
		s := New(st, "", "test", t.TempDir())
		s.EnableUploads(nil)
		ts := httptest.NewServer(s.Handler())
		t.Cleanup(ts.Close)

		assertStatus(t, ts, "GET", "/import", http.StatusNotFound)
		assertStatus(t, ts, "GET", "/upload", http.StatusNotFound)
		assertStatus(t, ts, "POST", "/api/v1/upload", http.StatusNotFound)
		assertStatus(t, ts, "DELETE", "/api/v1/upload/CISCO-SMI", http.StatusNotFound)
	})
	t.Run("enabled", func(t *testing.T) {
		t.Setenv("BLITTERMIB_UPLOAD_ENABLED", "true")
		ts, _, _ := newUploadServer(t)

		// /import is registered (200, not 404).
		assertStatus(t, ts, "GET", "/import", http.StatusOK)

		// /upload redirects (301) to /import. Disable redirect-following
		// so we see the 301 itself.
		noRedirect := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		}}
		req, _ := http.NewRequest("GET", ts.URL+"/upload", nil)
		resp, err := noRedirect.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusMovedPermanently {
			t.Errorf("GET /upload: status = %d, want 301", resp.StatusCode)
		}
		if loc := resp.Header.Get("Location"); loc != "/import" {
			t.Errorf("GET /upload: Location = %q, want /import", loc)
		}

		// POST /api/v1/upload is registered (not 404).
		req2, _ := http.NewRequest("POST", ts.URL+"/api/v1/upload", nil)
		req2.Header.Set("X-Blittermib-Upload", "1")
		resp2, err := http.DefaultClient.Do(req2)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp2.Body.Close()
		if resp2.StatusCode == http.StatusNotFound {
			t.Error("POST /api/v1/upload: route not registered when uploads enabled")
		}
	})
}

// TestUploadSingleFileSuccess covers the happy path: one valid file
// runs through the pipeline, reports status "imported" with the module
// name + OID, ends up in the curated tree (vendors/…), and no longer
// sits in import/.
func TestUploadSingleFileSuccess(t *testing.T) {
	t.Setenv("BLITTERMIB_UPLOAD_ENABLED", "true")
	ts, eng, root := newUploadServer(t)

	resp, body := postUpload(t, ts, "", map[string]string{"TEST-MIB": vendorMIB})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %s", resp.StatusCode, body)
	}
	ur := decodeUpload(t, body)
	if len(ur.Uploaded) != 1 {
		t.Fatalf("got %d outcomes, want 1: %v", len(ur.Uploaded), ur.Uploaded)
	}
	got := ur.Uploaded[0]
	if !got.OK || got.Status != "imported" {
		t.Errorf("outcome = %+v, want OK + status imported", got)
	}
	if got.Module != "TEST-MIB" {
		t.Errorf("module = %q, want TEST-MIB", got.Module)
	}
	if got.OID != "1.3.6.1.4.1.99999" {
		t.Errorf("oid = %q, want 1.3.6.1.4.1.99999", got.OID)
	}
	if got.Dest == "" {
		t.Errorf("dest empty; want curated path")
	}

	// File landed in the curated tree, not in import/.
	dest := filepath.Join(root, got.Dest)
	if _, err := os.Stat(dest); err != nil {
		t.Errorf("curated file missing at %s: %v", dest, err)
	}
	if !strings.HasPrefix(got.Dest, "vendors/") {
		t.Errorf("dest = %q, want a vendors/ path", got.Dest)
	}
	if _, err := os.Stat(filepath.Join(eng.Dir(), "TEST-MIB")); !os.IsNotExist(err) {
		t.Errorf("file still in import/: err = %v", err)
	}
}

// TestUploadBatchInterdependent proves the batch is compiled in one
// pipeline pass: two interdependent MIBs (the second IMPORTS a symbol
// the first defines) both import successfully in a single POST.
func TestUploadBatchInterdependent(t *testing.T) {
	t.Setenv("BLITTERMIB_UPLOAD_ENABLED", "true")
	ts, _, root := newUploadServer(t)

	baseMIB := `BASE-MIB DEFINITIONS ::= BEGIN
IMPORTS
    MODULE-IDENTITY, OBJECT-TYPE, enterprises FROM SNMPv2-SMI;

baseMib MODULE-IDENTITY
    LAST-UPDATED "202601010000Z"
    ORGANIZATION "Test Org"
    CONTACT-INFO  "test@example.com"
    DESCRIPTION   "Base MIB."
    REVISION      "202601010000Z"
    DESCRIPTION   "Initial revision."
    ::= { enterprises 99998 }

baseRoot OBJECT IDENTIFIER ::= { baseMib 1 }

END
`
	leafMIB := `LEAF-MIB DEFINITIONS ::= BEGIN
IMPORTS
    MODULE-IDENTITY FROM SNMPv2-SMI
    baseRoot FROM BASE-MIB;

leafMib MODULE-IDENTITY
    LAST-UPDATED "202601010000Z"
    ORGANIZATION "Test Org"
    CONTACT-INFO  "test@example.com"
    DESCRIPTION   "Leaf MIB depending on BASE-MIB."
    REVISION      "202601010000Z"
    DESCRIPTION   "Initial revision."
    ::= { baseRoot 1 }

END
`
	resp, body := postUpload(t, ts, "", map[string]string{
		"BASE-MIB": baseMIB,
		"LEAF-MIB": leafMIB,
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %s", resp.StatusCode, body)
	}
	ur := decodeUpload(t, body)
	if len(ur.Uploaded) != 2 {
		t.Fatalf("got %d outcomes, want 2: %+v", len(ur.Uploaded), ur.Uploaded)
	}
	for _, oc := range ur.Uploaded {
		if !oc.OK || oc.Status != "imported" {
			t.Errorf("%s: outcome = %+v, want imported", oc.Name, oc)
			continue
		}
		if _, err := os.Stat(filepath.Join(root, oc.Dest)); err != nil {
			t.Errorf("%s: curated file missing at %s: %v", oc.Name, oc.Dest, err)
		}
	}
}

// TestUploadBrokenFails covers the broken-MIB path: the file passes the
// marker sniff but fails to compile → 422, status "failed", and the
// file quarantines in import/failed/ with a sidecar.
func TestUploadBrokenFails(t *testing.T) {
	t.Setenv("BLITTERMIB_UPLOAD_ENABLED", "true")
	ts, eng, _ := newUploadServer(t)

	resp, body := postUpload(t, ts, "", map[string]string{"BROKEN-MIB": brokenMIB})
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422; body = %s", resp.StatusCode, body)
	}
	ur := decodeUpload(t, body)
	if len(ur.Uploaded) != 1 || ur.Uploaded[0].OK {
		t.Fatalf("outcome = %+v, want OK=false", ur.Uploaded)
	}
	if ur.Uploaded[0].Status != "failed" {
		t.Errorf("status = %q, want failed", ur.Uploaded[0].Status)
	}
	if ur.Uploaded[0].ErrorCode != errCodeCompile {
		t.Errorf("errorCode = %q, want %q", ur.Uploaded[0].ErrorCode, errCodeCompile)
	}

	// File + sidecar in import/failed/.
	if _, err := os.Stat(filepath.Join(eng.FailedDir(), "BROKEN-MIB")); err != nil {
		t.Errorf("broken file not in import/failed/: %v", err)
	}
	if _, err := os.Stat(filepath.Join(eng.FailedDir(), "BROKEN-MIB.reason.json")); err != nil {
		t.Errorf("sidecar missing in import/failed/: %v", err)
	}
}

// TestUploadDuplicate covers the duplicate path: uploading the same
// content a second time (different filename) is detected as a
// byte-identical duplicate → 409, status "duplicate", the existing
// curated path reported, and the file quarantined in import/duplicate/.
func TestUploadDuplicate(t *testing.T) {
	t.Setenv("BLITTERMIB_UPLOAD_ENABLED", "true")
	ts, eng, _ := newUploadServer(t)

	// First upload imports cleanly.
	resp1, body1 := postUpload(t, ts, "", map[string]string{"TEST-MIB": vendorMIB})
	if resp1.StatusCode != http.StatusOK {
		t.Fatalf("first upload status = %d; body = %s", resp1.StatusCode, body1)
	}

	// Second upload, identical bytes, different filename → duplicate.
	resp2, body2 := postUpload(t, ts, "", map[string]string{"TEST-MIB-COPY": vendorMIB})
	if resp2.StatusCode != http.StatusConflict {
		t.Fatalf("second upload status = %d, want 409; body = %s", resp2.StatusCode, body2)
	}
	ur := decodeUpload(t, body2)
	if len(ur.Uploaded) != 1 || ur.Uploaded[0].OK {
		t.Fatalf("outcome = %+v, want OK=false", ur.Uploaded)
	}
	if ur.Uploaded[0].Status != "duplicate" {
		t.Errorf("status = %q, want duplicate", ur.Uploaded[0].Status)
	}
	if ur.Uploaded[0].ErrorCode != errCodeDuplicate {
		t.Errorf("errorCode = %q, want %q", ur.Uploaded[0].ErrorCode, errCodeDuplicate)
	}
	if ur.Uploaded[0].Existing == "" {
		t.Errorf("existing path empty; want the curated path of the original")
	}
	if _, err := os.Stat(filepath.Join(eng.DuplicateDir(), "TEST-MIB-COPY")); err != nil {
		t.Errorf("duplicate file not in import/duplicate/: %v", err)
	}
}

// TestUploadCollisionExists covers the pending-collision guard: a file
// already sitting in import/<name> (not yet consumed) blocks a same-name
// upload without ?replace → 409, errorCode "exists". The pipeline
// consumes uploaded files immediately, so the pending file is dropped
// directly into import/ to arrange the collision deterministically.
func TestUploadCollisionExists(t *testing.T) {
	t.Setenv("BLITTERMIB_UPLOAD_ENABLED", "true")
	ts, eng, _ := newUploadServer(t)

	// Pre-stage a pending file in import/.
	pending := filepath.Join(eng.Dir(), "TEST-MIB")
	if err := os.WriteFile(pending, []byte(vendorMIB), 0o644); err != nil {
		t.Fatal(err)
	}

	resp, body := postUpload(t, ts, "", map[string]string{"TEST-MIB": vendorMIB})
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("status = %d, want 409; body = %s", resp.StatusCode, body)
	}
	ur := decodeUpload(t, body)
	if len(ur.Uploaded) != 1 || ur.Uploaded[0].OK {
		t.Fatalf("outcome = %+v, want OK=false", ur.Uploaded)
	}
	if ur.Uploaded[0].ErrorCode != errCodeExists {
		t.Errorf("errorCode = %q, want %q", ur.Uploaded[0].ErrorCode, errCodeExists)
	}
}

// TestUploadInvalidFilename covers the ValidModuleName gate: a name
// with characters the regex rejects (spaces) → 400, errorCode
// "invalid-name", nothing written.
func TestUploadInvalidFilename(t *testing.T) {
	t.Setenv("BLITTERMIB_UPLOAD_ENABLED", "true")
	ts, _, _ := newUploadServer(t)

	resp, body := postUpload(t, ts, "", map[string]string{"foo bar with spaces": vendorMIB})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body = %s", resp.StatusCode, body)
	}
	ur := decodeUpload(t, body)
	if len(ur.Uploaded) != 1 || ur.Uploaded[0].OK {
		t.Errorf("outcome = %+v, want OK=false", ur.Uploaded)
	}
	if ur.Uploaded[0].ErrorCode != errCodeInvalidName {
		t.Errorf("errorCode = %q, want %q", ur.Uploaded[0].ErrorCode, errCodeInvalidName)
	}
}

// TestUploadFilenameTraversalRejected covers the defensive filename
// guard: separators and literal . / .. segments that survive stdlib
// normalisation are rejected with 400 + errorCode "invalid-name".
func TestUploadFilenameTraversalRejected(t *testing.T) {
	t.Setenv("BLITTERMIB_UPLOAD_ENABLED", "true")
	ts, _, _ := newUploadServer(t)

	for _, hostile := range []string{
		"..\\..\\windows.cfg", // backslash preserved by Base on Linux
		"foo\\bar",
		"..",
		".",
	} {
		resp, body := postUpload(t, ts, "", map[string]string{hostile: vendorMIB})
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("filename %q: status = %d, want 400; body = %s", hostile, resp.StatusCode, body)
			continue
		}
		ur := decodeUpload(t, body)
		if len(ur.Uploaded) != 1 || ur.Uploaded[0].OK {
			t.Errorf("filename %q: outcome = %+v, want OK=false", hostile, ur.Uploaded)
			continue
		}
		if ur.Uploaded[0].ErrorCode != errCodeInvalidName {
			t.Errorf("filename %q: errorCode = %q, want %q", hostile, ur.Uploaded[0].ErrorCode, errCodeInvalidName)
		}
	}
}

// TestUploadStdlibNormalisesPath documents Go's mime/multipart
// behaviour: traversal-shaped filenames arrive as plain basenames, so
// the escape attack never reaches the handler. "../../../etc/passwd"
// normalises to "passwd" and imports normally.
func TestUploadStdlibNormalisesPath(t *testing.T) {
	t.Setenv("BLITTERMIB_UPLOAD_ENABLED", "true")
	ts, _, _ := newUploadServer(t)

	resp, body := postUpload(t, ts, "", map[string]string{"../../../etc/passwd": vendorMIB})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("traversal-shaped filename: status = %d, want 200 (stdlib normalises to 'passwd'); body = %s",
			resp.StatusCode, body)
	}
	ur := decodeUpload(t, body)
	if len(ur.Uploaded) != 1 || ur.Uploaded[0].Name != "passwd" {
		t.Errorf("expected single outcome with Name='passwd', got %+v", ur.Uploaded)
	}
}

// TestUploadNoMarker covers the lexical-marker gate. A file without the
// DEFINITIONS ::= BEGIN marker → 422, errorCode "no-marker", and
// nothing is written to import/.
func TestUploadNoMarker(t *testing.T) {
	t.Setenv("BLITTERMIB_UPLOAD_ENABLED", "true")
	ts, eng, _ := newUploadServer(t)

	resp, body := postUpload(t, ts, "", map[string]string{"README": nonMIB})
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422; body = %s", resp.StatusCode, body)
	}
	ur := decodeUpload(t, body)
	if !strings.Contains(ur.Uploaded[0].Error, "no MIB marker") {
		t.Errorf("error = %q, want mention of 'no MIB marker'", ur.Uploaded[0].Error)
	}
	if ur.Uploaded[0].ErrorCode != errCodeNoMarker {
		t.Errorf("errorCode = %q, want %q", ur.Uploaded[0].ErrorCode, errCodeNoMarker)
	}
	// Synchronous front rejects without touching import/.
	if _, err := os.Stat(filepath.Join(eng.Dir(), "README")); !os.IsNotExist(err) {
		t.Errorf("README written to import/: err = %v", err)
	}
	if _, err := os.Stat(filepath.Join(eng.FailedDir(), "README")); !os.IsNotExist(err) {
		t.Errorf("README quarantined in import/failed/: err = %v", err)
	}
}

// TestUploadOver10MB covers the per-file size cap. A 12 MB part → 413,
// errorCode "too-large", nothing written.
func TestUploadOver10MB(t *testing.T) {
	t.Setenv("BLITTERMIB_UPLOAD_ENABLED", "true")
	ts, eng, _ := newUploadServer(t)

	big := vendorMIB + strings.Repeat("-- pad\n", (12<<20)/8)
	resp, body := postUpload(t, ts, "", map[string]string{"BIG-MIB": big})
	if resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413; body = %s", resp.StatusCode, body)
	}
	ur := decodeUpload(t, body)
	if !strings.Contains(ur.Uploaded[0].Error, "10 MB") {
		t.Errorf("error = %q, want mention of '10 MB'", ur.Uploaded[0].Error)
	}
	if ur.Uploaded[0].ErrorCode != errCodeTooLarge {
		t.Errorf("errorCode = %q, want %q", ur.Uploaded[0].ErrorCode, errCodeTooLarge)
	}
	if _, err := os.Stat(filepath.Join(eng.Dir(), "BIG-MIB")); !os.IsNotExist(err) {
		t.Errorf("oversized file written to import/: err = %v", err)
	}
}

// TestUploadCSRFHeaderRequired covers the sentinel-header gate: POST
// and DELETE without X-Blittermib-Upload are refused with 403.
func TestUploadCSRFHeaderRequired(t *testing.T) {
	t.Setenv("BLITTERMIB_UPLOAD_ENABLED", "true")
	ts, _, _ := newUploadServer(t)

	body, ct := buildMultipart(t, map[string]string{"TEST-MIB": vendorMIB})
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/api/v1/upload", body)
	req.Header.Set("Content-Type", ct)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("POST without sentinel header: status = %d, want 403", resp.StatusCode)
	}

	req2, _ := http.NewRequest(http.MethodDelete, ts.URL+"/api/v1/upload/X", nil)
	resp2, err := http.DefaultClient.Do(req2)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp2.Body.Close()
	if resp2.StatusCode != http.StatusForbidden {
		t.Errorf("DELETE without sentinel header: status = %d, want 403", resp2.StatusCode)
	}
}

// TestUploadEmptyMultipartReturns400 covers the empty-body path: a
// multipart POST with no parts yields 400 instead of 200 + empty
// uploaded[].
func TestUploadEmptyMultipartReturns400(t *testing.T) {
	t.Setenv("BLITTERMIB_UPLOAD_ENABLED", "true")
	ts, _, _ := newUploadServer(t)

	resp, _ := postUpload(t, ts, "", map[string]string{})
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("empty multipart: status = %d, want 400", resp.StatusCode)
	}
}

// TestDeletePending covers the pending-file delete path: a file in
// import/ is removed via DELETE → 204, and is gone from disk (a second
// delete 404s).
func TestDeletePending(t *testing.T) {
	t.Setenv("BLITTERMIB_UPLOAD_ENABLED", "true")
	ts, eng, _ := newUploadServer(t)

	pending := filepath.Join(eng.Dir(), "TEST-MIB")
	if err := os.WriteFile(pending, []byte(vendorMIB), 0o644); err != nil {
		t.Fatal(err)
	}

	resp := deleteUpload(t, ts, "TEST-MIB", "")
	if resp.StatusCode != http.StatusNoContent {
		t.Errorf("status = %d, want 204", resp.StatusCode)
	}
	if _, err := os.Stat(pending); !os.IsNotExist(err) {
		t.Errorf("file still present after delete: err = %v", err)
	}
	resp2 := deleteUpload(t, ts, "TEST-MIB", "")
	if resp2.StatusCode != http.StatusNotFound {
		t.Errorf("second delete: status = %d, want 404", resp2.StatusCode)
	}
}

// TestDeleteQuarantined covers deleting a failed-quarantine entry with
// ?from=failed: both the file and its sidecar are removed.
func TestDeleteQuarantined(t *testing.T) {
	t.Setenv("BLITTERMIB_UPLOAD_ENABLED", "true")
	ts, eng, _ := newUploadServer(t)

	// Arrange a quarantine entry by uploading a broken MIB.
	if r, b := postUpload(t, ts, "", map[string]string{"BROKEN-MIB": brokenMIB}); r.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("seed broken upload: status %d; body %s", r.StatusCode, b)
	}
	failedFile := filepath.Join(eng.FailedDir(), "BROKEN-MIB")
	sidecar := failedFile + ".reason.json"
	if _, err := os.Stat(failedFile); err != nil {
		t.Fatalf("precondition: quarantined file missing: %v", err)
	}

	resp := deleteUpload(t, ts, "BROKEN-MIB", "from=failed")
	if resp.StatusCode != http.StatusNoContent {
		t.Errorf("status = %d, want 204", resp.StatusCode)
	}
	if _, err := os.Stat(failedFile); !os.IsNotExist(err) {
		t.Errorf("quarantined file still present: err = %v", err)
	}
	if _, err := os.Stat(sidecar); !os.IsNotExist(err) {
		t.Errorf("sidecar still present: err = %v", err)
	}
}

// TestDeleteTraversalRefused asserts the name guard rejects names that
// escape via ..-style fragments or characters ValidModuleName rejects
// (400), or 404 when normalisation yields a missing file.
func TestDeleteTraversalRefused(t *testing.T) {
	t.Setenv("BLITTERMIB_UPLOAD_ENABLED", "true")
	ts, _, _ := newUploadServer(t)

	for _, name := range []string{
		"%2E%2E%2Fcorpus%2FCISCO-SMI", // ../corpus/CISCO-SMI
		"../etc/passwd",
		"foo bar",
		"foo;bar",
	} {
		resp := deleteUpload(t, ts, name, "")
		if resp.StatusCode != http.StatusBadRequest && resp.StatusCode != http.StatusNotFound {
			t.Errorf("DELETE %q: status = %d, want 400 or 404", name, resp.StatusCode)
		}
	}
}

// TestDeleteWhenDisabled confirms the route 404s when uploads are off.
func TestDeleteWhenDisabled(t *testing.T) {
	t.Setenv("BLITTERMIB_UPLOAD_ENABLED", "")
	st, err := store.OpenInMemory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	s := New(st, "", "test", t.TempDir())
	s.EnableUploads(nil)
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)

	resp := deleteUpload(t, ts, "TEST-MIB", "")
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("status = %d, want 404", resp.StatusCode)
	}
}

// TestReplaceFromDuplicate covers the sanctioned overwrite path: after
// a duplicate lands in import/duplicate/, POST
// /api/v1/upload/<name>?action=replace&from=duplicate re-runs it with
// replacement allowed → 200 JSON with status imported, curated file
// updated.
func TestReplaceFromDuplicate(t *testing.T) {
	t.Setenv("BLITTERMIB_UPLOAD_ENABLED", "true")
	ts, eng, root := newUploadServer(t)

	// Import once.
	if r, b := postUpload(t, ts, "", map[string]string{"TEST-MIB": vendorMIB}); r.StatusCode != http.StatusOK {
		t.Fatalf("first import: status %d; body %s", r.StatusCode, b)
	}
	// Upload a same-name module with different content (different
	// DESCRIPTION) → quarantines as duplicate (module exists, content
	// differs).
	v2 := strings.Replace(vendorMIB, "A minimal test MIB.", "A minimal test MIB v2.", 1)
	if r, b := postUpload(t, ts, "", map[string]string{"TEST-MIB-V2": v2}); r.StatusCode != http.StatusConflict {
		t.Fatalf("second upload: status %d, want 409; body %s", r.StatusCode, b)
	}
	if _, err := os.Stat(filepath.Join(eng.DuplicateDir(), "TEST-MIB-V2")); err != nil {
		t.Fatalf("precondition: duplicate not quarantined: %v", err)
	}

	// Replace from duplicate.
	req, _ := http.NewRequest(http.MethodPost,
		ts.URL+"/api/v1/upload/TEST-MIB-V2?action=replace&from=duplicate", nil)
	req.Header.Set("X-Blittermib-Upload", "1")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("replace: status = %d, want 200; body = %s", resp.StatusCode, body)
	}
	var oc mibimport.Outcome
	if err := json.Unmarshal(body, &oc); err != nil {
		t.Fatalf("decode outcome: %v; body = %s", err, body)
	}
	if oc.Status != mibimport.StatusImported {
		t.Errorf("status = %q, want imported", oc.Status)
	}
	// Curated file carries the v2 content.
	dest := filepath.Join(root, oc.Dest)
	curated, err := os.ReadFile(dest)
	if err != nil {
		t.Fatalf("read curated file: %v", err)
	}
	if !strings.Contains(string(curated), "v2") {
		t.Errorf("curated file did not get v2 content")
	}
}

// TestImportPageRendersStates asserts /import surfaces failed and
// duplicate quarantine entries with their names + reasons.
func TestImportPageRendersStates(t *testing.T) {
	t.Setenv("BLITTERMIB_UPLOAD_ENABLED", "true")
	ts, eng, _ := newUploadServer(t)

	// Create a failed entry.
	if r, b := postUpload(t, ts, "", map[string]string{"BROKEN-MIB": brokenMIB}); r.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("seed failed: status %d; body %s", r.StatusCode, b)
	}
	// Create a duplicate entry: import once, then a differing same-name.
	if r, b := postUpload(t, ts, "", map[string]string{"TEST-MIB": vendorMIB}); r.StatusCode != http.StatusOK {
		t.Fatalf("seed import: status %d; body %s", r.StatusCode, b)
	}
	v2 := strings.Replace(vendorMIB, "A minimal test MIB.", "A minimal test MIB v2.", 1)
	if r, b := postUpload(t, ts, "", map[string]string{"TEST-MIB-DUP": v2}); r.StatusCode != http.StatusConflict {
		t.Fatalf("seed duplicate: status %d, want 409; body %s", r.StatusCode, b)
	}
	if _, err := os.Stat(filepath.Join(eng.FailedDir(), "BROKEN-MIB")); err != nil {
		t.Fatalf("precondition: failed entry missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(eng.DuplicateDir(), "TEST-MIB-DUP")); err != nil {
		t.Fatalf("precondition: duplicate entry missing: %v", err)
	}

	body := getBody(t, ts.URL+"/import")
	for _, want := range []string{
		"BROKEN-MIB",
		"TEST-MIB-DUP",
		"Failed",
		"Duplicates",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("/import missing %q\nexcerpt:\n%s", want, excerpt(body, "Failed", 1500))
		}
	}
}

// TestImportPageGatedOff asserts /import returns 404 when uploads are
// disabled (route not registered).
func TestImportPageGatedOff(t *testing.T) {
	t.Setenv("BLITTERMIB_UPLOAD_ENABLED", "")
	st, err := store.OpenInMemory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	s := New(st, "", "test", t.TempDir())
	s.EnableUploads(nil)
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)

	resp, err := http.Get(ts.URL + "/import")
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("status = %d, want 404", resp.StatusCode)
	}
}

// TestPrivacyMentionsUploads covers the /privacy page's uploads-enabled
// posture disclosure.
func TestPrivacyMentionsUploads(t *testing.T) {
	t.Setenv("BLITTERMIB_UPLOAD_ENABLED", "")
	st, err := store.OpenInMemory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	s := New(st, "", "test", t.TempDir())
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)

	body := getBody(t, ts.URL+"/privacy")
	for _, want := range []string{
		`id="web-uploads"`,
		"BLITTERMIB_UPLOAD_ENABLED",
		"unauthenticated write surface",
		"verbatim",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("/privacy missing %q", want)
		}
	}
}

// TestLandingDropZoneGated asserts the drop zone fragment appears in
// the populated landing page HTML iff uploads are enabled. The
// empty-state landing (zero modules) never renders the drop zone.
func TestLandingDropZoneGated(t *testing.T) {
	seed := func(t *testing.T, st *store.Store) {
		t.Helper()
		if err := st.ReplaceModule(context.Background(),
			&model.Module{Name: "SEED-MIB", ParseStatus: model.ParseStatusClean},
			nil, nil, nil); err != nil {
			t.Fatal(err)
		}
	}
	build := func(t *testing.T, withSeed bool) *httptest.Server {
		t.Helper()
		st, err := store.OpenInMemory(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = st.Close() })
		if withSeed {
			seed(t, st)
		}
		root := t.TempDir()
		s := New(st, "", "test", root)
		if uploadEnvEnabled() {
			s.EnableUploads(mibimport.New(root, st, mibcorpus.GroupMap{}))
		}
		ts := httptest.NewServer(s.Handler())
		t.Cleanup(ts.Close)
		return ts
	}

	t.Run("disabled — no drop zone on populated landing", func(t *testing.T) {
		t.Setenv("BLITTERMIB_UPLOAD_ENABLED", "")
		ts := build(t, true)
		body := getBody(t, ts.URL+"/")
		if strings.Contains(body, "drop-zone") {
			t.Error("disabled state still rendered drop-zone markup")
		}
	})
	t.Run("enabled — drop zone present on populated landing", func(t *testing.T) {
		t.Setenv("BLITTERMIB_UPLOAD_ENABLED", "true")
		ts := build(t, true)
		body := getBody(t, ts.URL+"/")
		if !strings.Contains(body, `class="drop-zone"`) {
			t.Errorf("enabled state missing drop-zone class; body excerpt:\n%s",
				excerpt(body, "hero-tagline", 1500))
		}
		if !strings.Contains(body, "/static/upload.js") {
			t.Error("upload.js script tag missing from the rendered HTML")
		}
	})
	t.Run("empty-state landing never has drop zone, even when enabled", func(t *testing.T) {
		t.Setenv("BLITTERMIB_UPLOAD_ENABLED", "true")
		ts := build(t, false)
		body := getBody(t, ts.URL+"/")
		if strings.Contains(body, "drop-zone") {
			t.Error("empty-state landing rendered drop zone (D11 scopes it to populated landing only)")
		}
	})
}

// TestModulePageNoDeleteForCurated covers the dormant inline-delete
// affordance under the import pipeline: imported modules live in the
// curated tree (not import/), so a curated module page never renders
// the module-info-delete button even when uploads are enabled.
func TestModulePageNoDeleteForCurated(t *testing.T) {
	t.Setenv("BLITTERMIB_UPLOAD_ENABLED", "true")
	ts, _, root := newUploadServer(t)

	// Import a module so it lands in the curated tree via the pipeline.
	if r, b := postUpload(t, ts, "", map[string]string{"TEST-MIB": vendorMIB}); r.StatusCode != http.StatusOK {
		t.Fatalf("import: status %d; body %s", r.StatusCode, b)
	}
	// Sanity: the curated tree holds it, not import/.
	if _, err := os.Stat(filepath.Join(root, "import", "TEST-MIB")); !os.IsNotExist(err) {
		t.Errorf("module unexpectedly still in import/: err = %v", err)
	}

	body := getBody(t, ts.URL+"/m/TEST-MIB")
	if strings.Contains(body, "module-info-delete") {
		t.Errorf("curated module page should not render module-info-delete; excerpt:\n%s",
			excerpt(body, "module-info", 800))
	}
}
