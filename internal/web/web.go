// Package web serves the openFRP dashboard: a single-page app built with Vite
// and React (see web/), embedded in the binary. Its files contain no inline
// script or style, so the server's strict Content-Security-Policy applies.
package web

import (
	"embed"
	"io/fs"
	"net/http"
	"strings"
)

//go:embed static
var assets embed.FS

// Handler serves built files; any other path returns the app shell so that
// deep links and reloads work. /api/* is routed before this handler.
func Handler() http.Handler {
	sub, _ := fs.Sub(assets, "static")
	files := http.FileServer(http.FS(sub))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := strings.TrimPrefix(r.URL.Path, "/")
		if p != "" && p != "index.html" {
			if f, err := sub.Open(p); err == nil {
				st, serr := f.Stat()
				f.Close()
				if serr == nil && !st.IsDir() {
					if strings.HasPrefix(p, "assets/") {
						// Build output is content-hashed: cache forever.
						w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
					}
					files.ServeHTTP(w, r)
					return
				}
			}
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
