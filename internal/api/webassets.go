package api

import (
	"embed"
	"net/http"
	"path"
)

// The web app's manifest and icons, so a browser can pin it as an app (Add to
// Dock in Safari, Install in Chrome). They hold nothing private and are served
// without a session: browsers fetch a manifest without cookies. There is no
// service worker: the app is only useful while this computer is reachable.

//go:embed webassets
var webAssetFiles embed.FS

// webAssets maps each served path to its file and type.
var webAssets = map[string]struct{ file, kind string }{
	"/ui/manifest.webmanifest": {"manifest.webmanifest", "application/manifest+json"},
	"/ui/icon.svg":             {"icon.svg", "image/svg+xml"},
	"/ui/icon-192.png":         {"icon-192.png", "image/png"},
	"/ui/icon-512.png":         {"icon-512.png", "image/png"},
	"/ui/apple-touch-icon.png": {"apple-touch-icon.png", "image/png"},
	"/ui/favicon.ico":          {"favicon.ico", "image/x-icon"},
}

func isWebAsset(p string) bool { _, ok := webAssets[p]; return ok }

func serveWebAsset(w http.ResponseWriter, r *http.Request) {
	a := webAssets[r.URL.Path]
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	body, err := webAssetFiles.ReadFile(path.Join("webassets", a.file))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	hd := w.Header()
	hd.Set("Content-Type", a.kind)
	hd.Set("Cache-Control", "max-age=86400")
	hd.Set("Content-Security-Policy", "default-src 'none'")
	w.WriteHeader(http.StatusOK)
	if r.Method == http.MethodGet {
		_, _ = w.Write(body)
	}
}
