package server

import (
	"embed"
	"io/fs"
	"net/http"
)

// staticAssets embeds the design-system CSS and (later) self-hosted
// fonts and JS islands at build time so the binary serves them
// without any external file dependency.
//
// The build expects assets/ to mirror the prototype's styles.css —
// see prepare-assets in the Makefile, which copies prototype/styles.css
// here on each build.
//
//go:embed assets
var staticAssets embed.FS

func staticHandler(version string) http.Handler {
	sub, err := fs.Sub(staticAssets, "assets")
	if err != nil {
		// embed contract guarantees the directory exists, so this is
		// only reached if the build was tampered with.
		panic("server: missing embedded assets: " + err.Error())
	}
	fileServer := http.FileServer(http.FS(sub))

	// Embedded files carry a zero modtime, so the FileServer can never
	// emit Last-Modified/ETag and clients can never revalidate (no
	// 304s) — without explicit caching every navigation re-downloads
	// the render-blocking CSS, the JS islands, and the fonts, which
	// shows up as an intermittent paint/text flash between pages.
	// Release builds serve the assets as immutable; the base template
	// version-busts the CSS/JS URLs (web's assetURL) so a deploy
	// invalidates them. Font URLs live inside the stylesheet and are
	// NOT busted — if a font file's bytes ever change, rename the file
	// (see the @font-face note in styles.css). Dev builds use no-store
	// instead — local rebuilds change asset contents without changing
	// the "dev" version string, so nothing may be cached at all.
	cacheControl := "no-store"
	if version != "dev" {
		cacheControl = "public, max-age=31536000, immutable"
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fileServer.ServeHTTP(&cacheControlWriter{ResponseWriter: w, value: cacheControl}, r)
	})
}

// cacheControlWriter stamps Cache-Control on successful responses
// only. The FileServer also emits 404s (missing asset), 301s
// (directory redirects), and 416s — caching those immutable for a
// year would wedge clients on a stale negative response (e.g. a 404
// cached just before a deploy adds the asset).
type cacheControlWriter struct {
	http.ResponseWriter
	value string
}

func (w *cacheControlWriter) WriteHeader(code int) {
	if code == http.StatusOK || code == http.StatusPartialContent {
		w.Header().Set("Cache-Control", w.value)
	}
	w.ResponseWriter.WriteHeader(code)
}
