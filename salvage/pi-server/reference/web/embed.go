// Package web embeds the built Vue single-page app and serves it, with an
// index.html fallback so client-side routes resolve. The frontend build writes
// into ./dist (see frontend/vite.config.ts).
package web

import (
	"embed"
	"io/fs"
	"net/http"
	"strings"
)

//go:embed all:dist
var dist embed.FS

// Register serves the SPA at "/" (and any path not handled by /api/*).
func Register(mux *http.ServeMux) {
	sub, err := fs.Sub(dist, "dist")
	if err != nil {
		panic(err)
	}
	fileServer := http.FileServer(http.FS(sub))

	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		// Unknown /api/* paths are API 404s, not client-side routes — don't
		// mask them by serving the SPA.
		if strings.HasPrefix(r.URL.Path, "/api/") {
			http.NotFound(w, r)
			return
		}
		path := strings.TrimPrefix(r.URL.Path, "/")
		if path == "" {
			path = "index.html"
		}
		// Unknown paths (client-side routes) fall back to index.html.
		if _, err := fs.Stat(sub, path); err != nil {
			http.ServeFileFS(w, r, sub, "index.html")
			return
		}
		fileServer.ServeHTTP(w, r)
	})
}
