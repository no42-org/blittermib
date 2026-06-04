/*
 * Copyright 2026 Ronny Trommer <ronny@no42.org>
 * SPDX-License-Identifier: MIT
 */

package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/no42-org/blittermib/internal/store"
)

// TestStaticCacheControl asserts the /static/ caching policy: release
// builds serve embedded assets as immutable (URLs are version-busted
// by web's assetURL, so deploys invalidate), while dev builds disable
// caching so local rebuilds are never stale. Embedded files have zero
// modtime, so conditional revalidation (304) is impossible — explicit
// Cache-Control is the only lever.
func TestStaticCacheControl(t *testing.T) {
	tests := []struct {
		version string
		want    string
	}{
		{"test", "public, max-age=31536000, immutable"},
		{"v0.5.0", "public, max-age=31536000, immutable"},
		{"dev", "no-store"},
	}
	for _, tt := range tests {
		t.Run(tt.version, func(t *testing.T) {
			st, err := store.OpenInMemory(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = st.Close() }()
			srv := New(st, "", tt.version, t.TempDir())
			ts := httptest.NewServer(srv.Handler())
			defer ts.Close()

			resp, err := http.Get(ts.URL + "/static/styles.css")
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = resp.Body.Close() }()
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("status = %d", resp.StatusCode)
			}
			if got := resp.Header.Get("Cache-Control"); got != tt.want {
				t.Errorf("Cache-Control = %q, want %q", got, tt.want)
			}

			// Negative responses must never carry the cache policy —
			// an immutable 404 would wedge clients for a year.
			missing, err := http.Get(ts.URL + "/static/no-such-asset.js")
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = missing.Body.Close() }()
			if missing.StatusCode != http.StatusNotFound {
				t.Fatalf("missing asset status = %d, want 404", missing.StatusCode)
			}
			if got := missing.Header.Get("Cache-Control"); got != "" {
				t.Errorf("404 Cache-Control = %q, want empty", got)
			}
		})
	}
}
