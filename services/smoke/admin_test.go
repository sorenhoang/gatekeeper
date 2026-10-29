//go:build smoke

package smoke

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const adminHost = "admin.localhost"

func adminPage(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(repoRoot, "admin", "index.html"))
	if err != nil {
		t.Fatalf("admin/index.html: %v", err)
	}
	return string(b)
}

func TestShouldServeAdminPageOnAdminHost(t *testing.T) {
	page := adminPage(t)
	if page == frontendFile(t, "index.html") {
		t.Fatal("admin/index.html is identical to frontend/index.html; it must differ")
	}
	r := mustRoute(t, adminHost, "/")
	if r.status != http.StatusOK || r.body != page {
		t.Fatalf("GET / (Host %s) = %d, want 200 with admin/index.html\n%s", adminHost, r.status, r.body)
	}
	if ct := r.header.Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Errorf("GET / (Host %s) Content-Type = %q, want text/html", adminHost, ct)
	}
	if got := r.header.Get("X-Route"); got != "admin" {
		t.Errorf("GET / (Host %s) X-Route = %q, want admin", adminHost, got)
	}
}

// Phase 8 puts auth_basic on the admin server only, so the main host must
// never be a back door to the admin content.
func TestShouldNeverServeAdminContentOnTheMainHost(t *testing.T) {
	page := adminPage(t)
	for _, host := range []string{"", "127.0.0.1:8080", "unknown.test"} {
		for _, path := range []string{"/admin/", "/admin/index.html", "/admin"} {
			r := mustRoute(t, host, path)
			if r.body == page {
				t.Errorf("GET %s (Host %q) served admin/index.html", path, host)
			}
		}
	}
}

func TestShouldNotRouteApisOrTheMainFrontendOnAdminHost(t *testing.T) {
	shell := frontendFile(t, "index.html")
	for _, path := range []string{"/api/users/", "/settings", "/health"} {
		r := mustRoute(t, adminHost, path)
		if r.status != http.StatusNotFound {
			t.Errorf("GET %s (Host %s) = %d, want 404\n%s", path, adminHost, r.status, r.body)
		}
		if r.body == shell || strings.Contains(r.body, `"service":`) {
			t.Errorf("GET %s (Host %s) leaked the main frontend or a backend echo", path, adminHost)
		}
	}
}

func TestShouldSendOtherHostsToTheDefaultServer(t *testing.T) {
	shell := frontendFile(t, "index.html")
	for _, host := range []string{"127.0.0.1:8080", "unknown.test", "localhost:8080"} {
		r := mustRoute(t, host, "/")
		if r.status != http.StatusOK || r.body != shell {
			t.Errorf("GET / (Host %s) = %d, want 200 with frontend/index.html", host, r.status)
		}
	}
}
