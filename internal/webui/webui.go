// Package webui serves the built React dashboard from inside the binary, so a
// deployment is one file with no asset directory to keep in sync.
package webui

import (
	"embed"
	"errors"
	"io/fs"
	"net/http"
	"path"
	"strings"
)

// dist holds the Vite build output.
//
// internal/webui/dist/.gitkeep is tracked precisely so this compiles in a
// fresh checkout, before the frontend has ever been built: go:embed fails the
// build outright when a pattern matches nothing. Handler then reports
// ErrNotBuilt at runtime, and the API still serves.
//
//go:embed all:dist
var dist embed.FS

// ErrNotBuilt reports that the binary was compiled without a frontend build.
var ErrNotBuilt = errors.New("the web UI has not been built; run `make ui`")

// Handler serves the dashboard.
//
// It is a single-page application, so any path that does not name a real file
// returns index.html and lets the client-side router take over. Hashed assets
// are cached aggressively while index.html never is, which is what makes an
// upgrade take effect on the next reload instead of stranding a browser on a
// stale bundle that references deleted chunks.
func Handler() (http.Handler, error) {
	root, err := fs.Sub(dist, "dist")
	if err != nil {
		return nil, err
	}
	index, err := fs.ReadFile(root, "index.html")
	if err != nil {
		return nil, ErrNotBuilt
	}

	files := http.FileServerFS(root)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name := strings.TrimPrefix(path.Clean(r.URL.Path), "/")

		if name != "" && name != "index.html" {
			if _, err := fs.Stat(root, name); err == nil {
				if strings.HasPrefix(name, "assets/") {
					// Vite fingerprints these filenames, so the content
					// at a given URL never changes.
					w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
				}
				files.ServeHTTP(w, r)
				return
			}
		}

		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-cache")
		_, _ = w.Write(index)
	}), nil
}
