// Package web serves the TunnelHub dashboard: a dependency-free single-page
// app (static HTML, CSS and JS, embedded in the binary). It talks to the
// /api/v1 JSON API and contains no inline script or style (see the CSP).
package web

import (
	"embed"
	"io/fs"
	"net/http"
)

//go:embed static
var assets embed.FS

// Handler serves the dashboard. Every unknown path returns the app shell so
// that client-side routes (including /claim?code=...) work on reload.
func Handler() http.Handler {
	sub, _ := fs.Sub(assets, "static")
	files := http.FileServer(http.FS(sub))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/", "/index.html":
		case "/app.js", "/app.css":
			w.Header().Set("Cache-Control", "no-cache")
			files.ServeHTTP(w, r)
			return
		default:
			// Unknown paths: the app shell; /api/* is handled before this.
		}
		b, err := fs.ReadFile(sub, "index.html")
		if err != nil {
			http.Error(w, "dashboard missing", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-cache")
		_, _ = w.Write(b)
	})
}
