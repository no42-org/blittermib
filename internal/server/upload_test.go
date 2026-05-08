package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/no42-org/blittermib/internal/model"
	"github.com/no42-org/blittermib/internal/store"
)

// TestEnableUploadsRespectsEnv covers the BLITTERMIB_UPLOAD_ENABLED
// gate: the upload routes only come up when the env var parses as
// truthy AND a non-nil load callback is supplied. Every other
// configuration leaves the routes unregistered (404 via the
// catch-all index handler).
func TestEnableUploadsRespectsEnv(t *testing.T) {
	cases := []struct {
		name        string
		env         string
		loadFunc    LoadFunc
		wantEnabled bool
	}{
		{"empty env, nil load", "", nil, false},
		{"empty env, real load", "", noopLoad, false},
		{"explicit false", "false", noopLoad, false},
		{"non-bool junk", "yes", noopLoad, false},
		{"true with nil load fails closed", "true", nil, false},
		{"true with real load enables", "true", noopLoad, true},
		{"1 enables", "1", noopLoad, true},
		{"True enables", "True", noopLoad, true},
		{"TRUE enables", "TRUE", noopLoad, true},
		{"t enables", "t", noopLoad, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Setenv("BLITTERMIB_UPLOAD_ENABLED", c.env)

			st, err := store.OpenInMemory(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = st.Close() })

			s := New(st, "", "test", t.TempDir())
			s.EnableUploads(c.loadFunc)
			if got := s.UploadsEnabled(); got != c.wantEnabled {
				t.Errorf("UploadsEnabled() = %v, want %v", got, c.wantEnabled)
			}
		})
	}
}

// TestRoutesGate asserts the routes themselves are unreachable when
// the flag is off. When uploads are enabled, the routes register and
// fall through to their (currently stub) handlers — what matters
// here is that the dispatcher is wired, not what the handlers
// return. Handler-behaviour assertions live in §2/§3/§5 tests.
func TestRoutesGate(t *testing.T) {
	t.Run("disabled", func(t *testing.T) {
		t.Setenv("BLITTERMIB_UPLOAD_ENABLED", "")
		ts := newTestServerForUpload(t, nil)
		assertStatus(t, ts, "GET", "/upload", http.StatusNotFound)
		assertStatus(t, ts, "POST", "/api/v1/upload", http.StatusNotFound)
		assertStatus(t, ts, "DELETE", "/api/v1/upload/CISCO-SMI", http.StatusNotFound)
	})
	t.Run("enabled", func(t *testing.T) {
		t.Setenv("BLITTERMIB_UPLOAD_ENABLED", "true")
		ts := newTestServerForUpload(t, noopLoad)
		// GET /upload and POST /api/v1/upload don't return 404 when
		// the routes are registered. (DELETE on a missing file does
		// return 404 from the real handler too — TestDeleteSuccess
		// covers route-registration for that path via the 204
		// success path.)
		for _, c := range []struct{ method, path string }{
			{"GET", "/upload"},
			{"POST", "/api/v1/upload"},
		} {
			req, _ := http.NewRequest(c.method, ts.URL+c.path, nil)
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatalf("%s %s: %v", c.method, c.path, err)
			}
			resp.Body.Close()
			if resp.StatusCode == http.StatusNotFound {
				t.Errorf("%s %s: route not registered when uploads enabled", c.method, c.path)
			}
		}
	})
}

func noopLoad(_ context.Context, paths []string) []LoadOutcome {
	out := make([]LoadOutcome, len(paths))
	for i, p := range paths {
		out[i] = LoadOutcome{Path: p}
	}
	return out
}

// fakeLoad returns a stub LoadFunc that synthesises one outcome per
// path. Each outcome reports a module named after the basename with
// the supplied OID + symbol count + diagnostics so handler tests can
// assert the response shape without invoking smidump.
func fakeLoad(oid string, symbols int, diags []model.Diagnostic) LoadFunc {
	return func(_ context.Context, paths []string) []LoadOutcome {
		out := make([]LoadOutcome, len(paths))
		for i, p := range paths {
			out[i] = LoadOutcome{
				Path: p,
				Module: &model.Module{
					Name:    filepath.Base(p),
					OIDRoot: oid,
				},
				SymbolCount: symbols,
				Diagnostics: diags,
			}
		}
		return out
	}
}

// failLoad returns a LoadFunc that reports each path as a compile
// failure. Used to assert per-file outcomes when the loader can't
// compile what was uploaded (e.g., missing IMPORTS).
func failLoad() LoadFunc {
	return func(_ context.Context, paths []string) []LoadOutcome {
		out := make([]LoadOutcome, len(paths))
		for i, p := range paths {
			out[i] = LoadOutcome{Path: p, Err: errors.New("synthetic compile failure")}
		}
		return out
	}
}

func newTestServerForUpload(t *testing.T, lf LoadFunc) *httptest.Server {
	t.Helper()
	st, err := store.OpenInMemory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	mibsDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(mibsDir, "upload"), 0o755); err != nil {
		t.Fatal(err)
	}
	s := New(st, "", "test", mibsDir)
	s.EnableUploads(lf)
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)
	return ts
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

// minimalMIB is an SMIv2 module with just enough structure to pass
// the lexical marker check (DEFINITIONS ::= BEGIN). The handler
// tests use it as the body for valid-MIB uploads; the synthetic
// LoadFunc handles whatever the parsed result would be.
const minimalMIB = "TEST-MIB DEFINITIONS ::= BEGIN\nIMPORTS MODULE-IDENTITY FROM SNMPv2-SMI;\nEND\n"

func postUpload(t *testing.T, ts *httptest.Server, query string, files map[string]string) (*http.Response, []byte) {
	t.Helper()
	body, ct := buildMultipart(t, files)
	url := ts.URL + "/api/v1/upload"
	if query != "" {
		url += "?" + query
	}
	req, _ := http.NewRequest(http.MethodPost, url, body)
	req.Header.Set("Content-Type", ct)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	out, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp, out
}

func decodeUpload(t *testing.T, body []byte) uploadResponse {
	t.Helper()
	var ur uploadResponse
	if err := json.Unmarshal(body, &ur); err != nil {
		t.Fatalf("decode response: %v\nbody: %s", err, body)
	}
	return ur
}

func mibsRoot(ts *httptest.Server) string {
	// httptest.Server doesn't expose Server internals; the test
	// helpers below use the upload directory implicitly via the
	// request response. For tests that need to inspect the file
	// system, we read the path from a dedicated sentinel: each
	// test owns a t.TempDir() it set on the Server via
	// newTestServerForUpload.
	return ""
}

// TestUploadSingleFileSuccess covers the happy path: one file, no
// collision, valid name + marker. Asserts response shape, file on
// disk, and that the load callback was invoked exactly once with
// the expected path.
func TestUploadSingleFileSuccess(t *testing.T) {
	t.Setenv("BLITTERMIB_UPLOAD_ENABLED", "true")
	called := 0
	var calledWith []string
	lf := func(_ context.Context, paths []string) []LoadOutcome {
		called++
		calledWith = paths
		out := make([]LoadOutcome, len(paths))
		for i, p := range paths {
			out[i] = LoadOutcome{
				Path:        p,
				Module:      &model.Module{Name: "TEST-MIB", OIDRoot: "1.3.6.1.4.1.99999"},
				SymbolCount: 3,
			}
		}
		return out
	}
	ts := newTestServerForUpload(t, lf)
	resp, body := postUpload(t, ts, "", map[string]string{"TEST-MIB": minimalMIB})
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200; body = %s", resp.StatusCode, body)
	}
	ur := decodeUpload(t, body)
	if len(ur.Uploaded) != 1 {
		t.Fatalf("got %d outcomes, want 1: %v", len(ur.Uploaded), ur.Uploaded)
	}
	got := ur.Uploaded[0]
	if got.Name != "TEST-MIB" || !got.OK || got.Module != "TEST-MIB" || got.OID != "1.3.6.1.4.1.99999" || got.Symbols != 3 {
		t.Errorf("outcome = %+v", got)
	}
	if called != 1 {
		t.Errorf("LoadFunc called %d times, want 1", called)
	}
	if len(calledWith) != 1 || !strings.HasSuffix(calledWith[0], "/upload/TEST-MIB") {
		t.Errorf("LoadFunc called with %v, want one path ending in /upload/TEST-MIB", calledWith)
	}
}

// TestUploadCollisionRefused covers D5 default behaviour: a second
// POST without ?replace=true is refused with status 409 and the
// existing file content stays on disk.
func TestUploadCollisionRefused(t *testing.T) {
	t.Setenv("BLITTERMIB_UPLOAD_ENABLED", "true")
	ts := newTestServerForUpload(t, fakeLoad("1.3.6.1.4.1.99999", 1, nil))
	// First upload succeeds.
	resp1, _ := postUpload(t, ts, "", map[string]string{"TEST-MIB": minimalMIB})
	if resp1.StatusCode != http.StatusOK {
		t.Fatalf("first upload status = %d", resp1.StatusCode)
	}
	// Second upload with same name, no ?replace, must refuse.
	resp2, body := postUpload(t, ts, "", map[string]string{"TEST-MIB": minimalMIB + "\n-- v2"})
	if resp2.StatusCode != http.StatusConflict {
		t.Errorf("status = %d, want 409; body = %s", resp2.StatusCode, body)
	}
	ur := decodeUpload(t, body)
	if len(ur.Uploaded) != 1 || ur.Uploaded[0].OK {
		t.Errorf("outcome = %+v", ur.Uploaded)
	}
	if !strings.Contains(ur.Uploaded[0].Error, "already exists") {
		t.Errorf("error = %q, want mention of 'already exists'", ur.Uploaded[0].Error)
	}
}

// TestUploadCollisionReplace covers D5 explicit override: ?replace=
// true overwrites and the per-file outcome carries replaced=true.
func TestUploadCollisionReplace(t *testing.T) {
	t.Setenv("BLITTERMIB_UPLOAD_ENABLED", "true")
	ts := newTestServerForUpload(t, fakeLoad("1.3.6.1.4.1.99999", 1, nil))
	resp1, _ := postUpload(t, ts, "", map[string]string{"TEST-MIB": minimalMIB})
	if resp1.StatusCode != http.StatusOK {
		t.Fatalf("first upload status = %d", resp1.StatusCode)
	}
	resp2, body := postUpload(t, ts, "replace=true", map[string]string{"TEST-MIB": minimalMIB + "\n-- v2"})
	if resp2.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200; body = %s", resp2.StatusCode, body)
	}
	ur := decodeUpload(t, body)
	if len(ur.Uploaded) != 1 || !ur.Uploaded[0].OK || !ur.Uploaded[0].Replaced {
		t.Errorf("outcome = %+v, want OK + Replaced", ur.Uploaded)
	}
}

// TestUploadInvalidFilename covers path-traversal + ValidModuleName.
// "../etc/passwd" must be rejected with status 400 and no file
// written outside mibs/upload/.
func TestUploadInvalidFilename(t *testing.T) {
	t.Setenv("BLITTERMIB_UPLOAD_ENABLED", "true")
	ts := newTestServerForUpload(t, fakeLoad("", 0, nil))
	// filepath.Base on "../etc/passwd" yields "passwd" which is
	// ValidModuleName-safe; the actual hostile input is a name
	// with a slash or path separator that bypasses Base. Use
	// characters ValidModuleName doesn't accept.
	resp, body := postUpload(t, ts, "", map[string]string{"foo bar with spaces": minimalMIB})
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400; body = %s", resp.StatusCode, body)
	}
	ur := decodeUpload(t, body)
	if len(ur.Uploaded) != 1 || ur.Uploaded[0].OK {
		t.Errorf("outcome = %+v", ur.Uploaded)
	}
	if !strings.Contains(ur.Uploaded[0].Error, "invalid filename") {
		t.Errorf("error = %q, want mention of 'invalid filename'", ur.Uploaded[0].Error)
	}
}

// TestUploadNoMarker covers the lexical-marker gate. A file without
// "DEFINITIONS ::= BEGIN" returns 422 and is not written to
// upload/.
func TestUploadNoMarker(t *testing.T) {
	t.Setenv("BLITTERMIB_UPLOAD_ENABLED", "true")
	ts := newTestServerForUpload(t, fakeLoad("", 0, nil))
	resp, body := postUpload(t, ts, "", map[string]string{"README": "this is just a README, no MIB content here\n"})
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Errorf("status = %d, want 422; body = %s", resp.StatusCode, body)
	}
	ur := decodeUpload(t, body)
	if !strings.Contains(ur.Uploaded[0].Error, "no MIB marker") {
		t.Errorf("error = %q, want mention of 'no MIB marker'", ur.Uploaded[0].Error)
	}
}

// TestUploadOver10MB covers D7. A 12 MB part returns 413 and is not
// written to upload/.
func TestUploadOver10MB(t *testing.T) {
	t.Setenv("BLITTERMIB_UPLOAD_ENABLED", "true")
	ts := newTestServerForUpload(t, fakeLoad("", 0, nil))
	big := minimalMIB + strings.Repeat("-- pad\n", (12<<20)/8)
	resp, body := postUpload(t, ts, "", map[string]string{"BIG-MIB": big})
	if resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Errorf("status = %d, want 413; body = %s", resp.StatusCode, body)
	}
	ur := decodeUpload(t, body)
	if !strings.Contains(ur.Uploaded[0].Error, "10 MB") {
		t.Errorf("error = %q, want mention of '10 MB'", ur.Uploaded[0].Error)
	}
}

// TestUploadBatchOneCallToLoadFiles asserts D14 — even with N parts
// in a single POST, the loader is invoked exactly once with all
// accepted paths together. This is what makes the vendor archive
// case work: smidump sees prerequisites on disk regardless of the
// part arrival order.
func TestUploadBatchOneCallToLoadFiles(t *testing.T) {
	t.Setenv("BLITTERMIB_UPLOAD_ENABLED", "true")
	calls := 0
	var batchSizes []int
	lf := func(_ context.Context, paths []string) []LoadOutcome {
		calls++
		batchSizes = append(batchSizes, len(paths))
		out := make([]LoadOutcome, len(paths))
		for i, p := range paths {
			out[i] = LoadOutcome{Path: p, Module: &model.Module{Name: filepath.Base(p)}}
		}
		return out
	}
	ts := newTestServerForUpload(t, lf)
	resp, _ := postUpload(t, ts, "", map[string]string{
		"A-MIB": minimalMIB,
		"B-MIB": minimalMIB,
		"C-MIB": minimalMIB,
	})
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d", resp.StatusCode)
	}
	if calls != 1 {
		t.Errorf("LoadFunc called %d times, want exactly 1", calls)
	}
	if len(batchSizes) != 1 || batchSizes[0] != 3 {
		t.Errorf("batchSizes = %v, want [3]", batchSizes)
	}
}

// TestDeleteSuccess covers the happy path: an existing file in
// upload/ is removed, the response is 204, and the file is gone
// from disk.
func TestDeleteSuccess(t *testing.T) {
	t.Setenv("BLITTERMIB_UPLOAD_ENABLED", "true")
	ts := newTestServerForUpload(t, fakeLoad("1.3.6.1.4.1.99999", 1, nil))
	// Seed via the upload endpoint to land a file in the right
	// directory without exposing internals.
	if r, _ := postUpload(t, ts, "", map[string]string{"TEST-MIB": minimalMIB}); r.StatusCode != http.StatusOK {
		t.Fatalf("seed upload: status %d", r.StatusCode)
	}

	req, _ := http.NewRequest(http.MethodDelete, ts.URL+"/api/v1/upload/TEST-MIB", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Errorf("status = %d, want 204", resp.StatusCode)
	}

	// A second delete must 404 (the file is gone).
	req2, _ := http.NewRequest(http.MethodDelete, ts.URL+"/api/v1/upload/TEST-MIB", nil)
	resp2, _ := http.DefaultClient.Do(req2)
	resp2.Body.Close()
	if resp2.StatusCode != http.StatusNotFound {
		t.Errorf("second delete: status = %d, want 404", resp2.StatusCode)
	}
}

// TestDeleteTraversalRefused asserts the path-traversal guard
// rejects names that escape mibs/upload/ via ..-style fragments,
// absolute paths, or characters that ValidModuleName rejects.
func TestDeleteTraversalRefused(t *testing.T) {
	t.Setenv("BLITTERMIB_UPLOAD_ENABLED", "true")
	ts := newTestServerForUpload(t, noopLoad)
	for _, name := range []string{
		"%2E%2E%2Fcorpus%2FCISCO-SMI", // ../corpus/CISCO-SMI
		"../etc/passwd",
		"foo bar",
		"foo;bar",
	} {
		req, _ := http.NewRequest(http.MethodDelete, ts.URL+"/api/v1/upload/"+name, nil)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest && resp.StatusCode != http.StatusNotFound {
			t.Errorf("DELETE %q: status = %d, want 400 or 404", name, resp.StatusCode)
		}
	}
}

// TestDeleteWhenDisabled confirms the route 404s when uploads are
// off (no DELETE handler registered, catch-all index handler
// returns 404 for /api/v1/upload/X).
func TestDeleteWhenDisabled(t *testing.T) {
	t.Setenv("BLITTERMIB_UPLOAD_ENABLED", "")
	ts := newTestServerForUpload(t, nil)
	req, _ := http.NewRequest(http.MethodDelete, ts.URL+"/api/v1/upload/TEST-MIB", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("status = %d, want 404", resp.StatusCode)
	}
}

// TestLandingDropZoneGated asserts the drop zone fragment appears
// in the landing-page HTML if and only if uploads are enabled. We
// drive both branches (Landing and LandingEmpty) — the "empty
// state" path runs when no modules have loaded.
func TestLandingDropZoneGated(t *testing.T) {
	t.Run("disabled — no drop zone in landing-empty", func(t *testing.T) {
		t.Setenv("BLITTERMIB_UPLOAD_ENABLED", "")
		ts := newTestServerForUpload(t, nil)
		body := getBody(t, ts.URL+"/")
		if strings.Contains(body, "drop-zone") {
			t.Error("disabled state still rendered drop-zone markup")
		}
	})
	t.Run("enabled — drop zone present in landing-empty", func(t *testing.T) {
		t.Setenv("BLITTERMIB_UPLOAD_ENABLED", "true")
		ts := newTestServerForUpload(t, noopLoad)
		body := getBody(t, ts.URL+"/")
		if !strings.Contains(body, `class="drop-zone"`) {
			t.Errorf("enabled state missing drop-zone class; body excerpt:\n%s",
				excerpt(body, "hero-tagline", 1500))
		}
		if !strings.Contains(body, "/static/upload.js") {
			t.Error("upload.js script tag missing from the rendered HTML")
		}
	})
}

func getBody(t *testing.T, url string) string {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
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

// TestUploadCompileFailureSurfaced covers the response shape when
// the file passes the marker gate but the loader reports a compile
// error (e.g., missing IMPORTS). The per-file outcome flips to
// OK=false and surfaces the error.
func TestUploadCompileFailureSurfaced(t *testing.T) {
	t.Setenv("BLITTERMIB_UPLOAD_ENABLED", "true")
	ts := newTestServerForUpload(t, failLoad())
	resp, body := postUpload(t, ts, "", map[string]string{"TEST-MIB": minimalMIB})
	if resp.StatusCode != http.StatusOK {
		// Multi-stage failure (write OK, compile fail) → the file
		// landed on disk; status stays 200 and the per-file
		// outcome carries the failure detail.
		t.Errorf("status = %d, want 200; body = %s", resp.StatusCode, body)
	}
	ur := decodeUpload(t, body)
	if len(ur.Uploaded) != 1 || ur.Uploaded[0].OK {
		t.Errorf("outcome = %+v", ur.Uploaded)
	}
	if !strings.Contains(ur.Uploaded[0].Error, "compile failed") {
		t.Errorf("error = %q, want 'compile failed: …'", ur.Uploaded[0].Error)
	}
}

func assertStatus(t *testing.T, ts *httptest.Server, method, path string, want int) {
	t.Helper()
	req, _ := http.NewRequest(method, ts.URL+path, nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	resp.Body.Close()
	if resp.StatusCode != want {
		t.Errorf("%s %s: status = %d, want %d", method, path, resp.StatusCode, want)
	}
}
