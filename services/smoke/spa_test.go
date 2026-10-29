//go:build smoke

package smoke

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func frontendFile(t *testing.T, rel string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(repoRoot, "frontend", rel))
	if err != nil {
		t.Fatalf("frontend/%s: %v", rel, err)
	}
	return string(b)
}

func TestShouldServeSpaShellForBrowserPaths(t *testing.T) {
	shell := frontendFile(t, "index.html")
	for _, path := range []string{"/settings", "/a/b/c", "/orders/42"} {
		r := mustRoute(t, "", path)
		if r.status != http.StatusOK || r.body != shell {
			t.Errorf("GET %s = %d, want 200 with frontend/index.html\n%s", path, r.status, r.body)
		}
		if ct := r.header.Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
			t.Errorf("GET %s Content-Type = %q, want text/html", path, ct)
		}
		if got := r.header.Get("X-Route"); got != "spa" {
			t.Errorf("GET %s X-Route = %q, want spa", path, got)
		}
	}
}

func TestShouldTellBrowsersToRevalidateTheShell(t *testing.T) {
	for _, path := range []string{"/", "/index.html", "/settings"} {
		r := mustRoute(t, "", path)
		if cc := r.header.Get("Cache-Control"); !strings.Contains(cc, "no-cache") {
			t.Errorf("GET %s Cache-Control = %q, want no-cache", path, cc)
		}
	}
}

func TestShouldServeExistingAssetsAsThemselves(t *testing.T) {
	r := mustRoute(t, "", "/assets/app.css")
	if r.status != http.StatusOK || r.body != frontendFile(t, "assets/app.css") {
		t.Fatalf("GET /assets/app.css = %d, want 200 with frontend/assets/app.css\n%s", r.status, r.body)
	}
	if ct := r.header.Get("Content-Type"); !strings.HasPrefix(ct, "text/css") {
		t.Errorf("GET /assets/app.css Content-Type = %q, want text/css", ct)
	}
	if got := r.header.Get("X-Route"); got != "static" {
		t.Errorf("GET /assets/app.css X-Route = %q, want static", got)
	}
	if !strings.Contains(frontendFile(t, "index.html"), "assets/app.css") {
		t.Errorf("frontend/index.html does not link assets/app.css")
	}
}

func TestShouldReturn404ForMissingAssetsNotTheShell(t *testing.T) {
	shell := frontendFile(t, "index.html")
	for _, path := range []string{"/missing.js", "/assets/missing.png", "/a/b/style.css"} {
		r := mustRoute(t, "", path)
		if r.status != http.StatusNotFound || r.body == shell {
			t.Errorf("GET %s = %d, want 404 and not the SPA shell", path, r.status)
		}
	}
}

// The asset regex must not shadow the API prefixes: regex locations beat
// plain prefixes, so a .png under /api/products/ would otherwise 404 here.
func TestShouldKeepApiPathsWithFileExtensionsOnTheirService(t *testing.T) {
	r := mustRoute(t, "", "/api/products/logo.png")
	if r.status != http.StatusOK || r.echo(t).Service != "product-service" || r.header.Get("X-Route") != "products" {
		t.Errorf("GET /api/products/logo.png = %d X-Route %q, want 200 from product-service\n%s",
			r.status, r.header.Get("X-Route"), r.body)
	}
}
