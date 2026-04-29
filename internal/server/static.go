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

func staticHandler() http.Handler {
	sub, err := fs.Sub(staticAssets, "assets")
	if err != nil {
		// embed contract guarantees the directory exists, so this is
		// only reached if the build was tampered with.
		panic("server: missing embedded assets: " + err.Error())
	}
	return http.FileServer(http.FS(sub))
}
