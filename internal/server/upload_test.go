package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

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
		for _, c := range []struct{ method, path string }{
			{"GET", "/upload"},
			{"POST", "/api/v1/upload"},
			{"DELETE", "/api/v1/upload/X"},
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

func noopLoad(_ context.Context, _ []string) error { return nil }

func newTestServerForUpload(t *testing.T, lf LoadFunc) *httptest.Server {
	t.Helper()
	st, err := store.OpenInMemory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	s := New(st, "", "test", t.TempDir())
	s.EnableUploads(lf)
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)
	return ts
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
