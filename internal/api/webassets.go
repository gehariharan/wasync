package api

import (
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"net/http"
	"path"
)

// The web app's shared assets: the design system's two stylesheets and its
// bundled typeface, the manifest and icons, so a browser can pin it as an app
// (Add to Dock in Safari, Install in Chrome), and the one script the passkey
// pages run. They hold nothing private and are served without a session:
// browsers fetch a manifest and the signed-out page's styles without cookies.
// There is no service worker: the app is only useful while this computer is
// reachable.

//go:embed webassets
var webAssetFiles embed.FS

// webAssets maps each served path to its file and type.
var webAssets = map[string]struct{ file, kind string }{
	"/ui/tokens.css":           {"tokens.css", "text/css; charset=utf-8"},
	"/ui/components.css":       {"components.css", "text/css; charset=utf-8"},
	"/ui/inter.woff2":          {"inter.woff2", "font/woff2"},
	"/ui/inter-license.txt":    {"inter-license.txt", "text/plain; charset=utf-8"},
	"/ui/manifest.webmanifest": {"manifest.webmanifest", "application/manifest+json"},
	"/ui/icon.svg":             {"icon.svg", "image/svg+xml"},
	"/ui/icon-192.png":         {"icon-192.png", "image/png"},
	"/ui/icon-512.png":         {"icon-512.png", "image/png"},
	"/ui/apple-touch-icon.png": {"apple-touch-icon.png", "image/png"},
	"/ui/favicon.ico":          {"favicon.ico", "image/x-icon"},
	"/ui/passkey.js":           {"passkey.js", "text/javascript; charset=utf-8"},
}

// assetVersion is a short hash of a stylesheet's bytes. Pages link it as a
// query, so an updated wasync never shows a page with yesterday's cached styles.
func assetVersion(file string) string {
	body, err := webAssetFiles.ReadFile(path.Join("webassets", file))
	if err != nil {
		panic("missing web asset " + file)
	}
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:4])
}

var (
	tokensCSSVersion     = assetVersion("tokens.css")
	componentsCSSVersion = assetVersion("components.css")
)

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
	hd.Set("X-Content-Type-Options", "nosniff")
	hd.Set("Content-Security-Policy", "default-src 'none'")
	w.WriteHeader(http.StatusOK)
	if r.Method == http.MethodGet {
		_, _ = w.Write(body)
	}
}
