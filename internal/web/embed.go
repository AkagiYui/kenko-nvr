// Package web embeds the built single-page frontend (Vite/SolidJS output) and
// serves it with an SPA-style fallback to index.html.
//
// The frontend lives in ../../frontend and is built into ./dist (see the
// Makefile / Dockerfile / CI). Asset filenames under dist/assets are
// content-hashed, so they are served with a long, immutable cache; index.html
// and the SPA fallback are always revalidated so a deploy is picked up at once.
package web

import (
	"embed"
	"io"
	"io/fs"
	"net/http"
	"strings"
)

//go:embed all:dist
var embedded embed.FS

// Handler serves the embedded SPA. Unknown paths fall back to index.html so
// client-side routing works.
func Handler() http.Handler {
	sub, err := fs.Sub(embedded, "dist")
	if err != nil {
		panic(err)
	}
	fileServer := http.FileServer(http.FS(sub))

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := strings.TrimPrefix(r.URL.Path, "/")
		if p == "" {
			p = "index.html"
		}

		if _, statErr := fs.Stat(sub, p); statErr != nil {
			// Not a real file: serve index.html (client-side route).
			serveIndex(w, r, sub)
			return
		}

		if strings.HasPrefix(p, "assets/") {
			// Content-hashed build assets never change under a given name.
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		} else {
			// index.html and other top-level files: revalidate every load.
			w.Header().Set("Cache-Control", "no-cache")
		}
		fileServer.ServeHTTP(w, r)
	})
}

func serveIndex(w http.ResponseWriter, r *http.Request, sub fs.FS) {
	f, err := sub.Open("index.html")
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer f.Close()
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	_, _ = io.Copy(w, f)
}
