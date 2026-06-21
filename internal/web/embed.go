// Package web embeds the built single-page frontend and serves it with an
// SPA-style fallback to index.html.
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
			// Not a real file: serve index.html (SPA fallback).
			f, openErr := sub.Open("index.html")
			if openErr != nil {
				http.NotFound(w, r)
				return
			}
			defer f.Close()
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = io.Copy(w, f)
			return
		}
		fileServer.ServeHTTP(w, r)
	})
}
