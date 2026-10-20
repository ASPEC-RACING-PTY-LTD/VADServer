package api

import (
	"bytes"
	"io/fs"
	"net/http"
	"path"
	"strings"
)

// SPAHandler serves the embedded web UI, falling back to index.html for
// client-side routes. Hashed assets are cached aggressively.
func SPAHandler(dist fs.FS) http.Handler {
	files := http.FileServer(http.FS(dist))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		p := strings.TrimPrefix(path.Clean(r.URL.Path), "/")
		if p != "" && p != "index.html" {
			if st, err := fs.Stat(dist, p); err == nil && !st.IsDir() {
				if strings.HasPrefix(p, "assets/") {
					w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
				}
				files.ServeHTTP(w, r)
				return
			}
			if strings.HasPrefix(p, "assets/") {
				http.NotFound(w, r)
				return
			}
		}
		index, err := fs.ReadFile(dist, "index.html")
		if err != nil {
			http.Error(w, "VaultDock web interface is not built. Run `npm run build` in web/.", http.StatusServiceUnavailable)
			return
		}
		if hardReload(r) {
			// A hard refresh (Ctrl+F5, Ctrl+Shift+R) starts the web UI over:
			// the app sees the mark and resets its remembered layout.
			index = bytes.Replace(index, []byte("<head>"), []byte(`<head><meta name="vd-reload" content="hard">`), 1)
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; img-src 'self' data: blob:; media-src 'self' blob:; style-src 'self' 'unsafe-inline'; script-src 'self'; connect-src 'self'; frame-src 'self'; object-src 'none'; base-uri 'self'; form-action 'self'; frame-ancestors 'none'")
		_, _ = w.Write(index)
	})
}

// hardReload reports whether the browser bypasses its cache for this page:
// a hard refresh sends "Cache-Control: no-cache" (and "Pragma: no-cache"),
// a normal refresh "max-age=0" and a plain visit neither.
func hardReload(r *http.Request) bool {
	return strings.Contains(strings.ToLower(r.Header.Get("Cache-Control")), "no-cache") || strings.EqualFold(r.Header.Get("Pragma"), "no-cache")
}
