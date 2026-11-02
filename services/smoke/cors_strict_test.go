//go:build smoke

package smoke

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func corsHeaders(h http.Header) []string {
	var out []string
	for k := range h {
		if strings.HasPrefix(k, "Access-Control-") {
			out = append(out, k+": "+h.Get(k))
		}
	}
	return out
}

// A plain OPTIONS (no Access-Control-Request-Method) is not a preflight: it
// must go through auth like any other request, never get the free 204.
func TestShouldNotTreatAPlainOptionsAsAPreflight(t *testing.T) {
	a := authed(t, http.MethodOptions, "/api/orders/", "", map[string]string{"Origin": allowedOrigins[0]})
	if a.status == http.StatusNoContent || a.header.Get("Access-Control-Allow-Methods") != "" {
		t.Errorf("plain OPTIONS from an allowed origin → %d with Allow-Methods %q, want no preflight answer (401 without a token)",
			a.status, a.header.Get("Access-Control-Allow-Methods"))
	}
}

// Every API location answers preflights, not only orders.
func TestShouldAnswerPreflightsOnEveryAPIRoute(t *testing.T) {
	gw, _ := uniqueProduct("preflight")
	for _, path := range []string{"/api/users/", "/api/orders/", gw, "/api/auth/login", "/api/auth/login/"} {
		a := preflight(t, allowedOrigins[0], path, http.MethodPost)
		if a.status != http.StatusNoContent || a.header.Get("Access-Control-Allow-Origin") != allowedOrigins[0] {
			t.Errorf("preflight to %s → %d ACAO %q, want 204 %s", path, a.status, a.header.Get("Access-Control-Allow-Origin"), allowedOrigins[0])
		}
	}
}

// Since Phase 8 users and orders need a token: a README example that calls
// them must send one, or say it expects the 401.
func TestShouldKeepTheReadmeExamplesWorkingWithAuth(t *testing.T) {
	b, err := os.ReadFile(filepath.Join(repoRoot, "README.md"))
	if err != nil {
		t.Fatal(err)
	}
	readme := string(b)
	for i, line := range strings.Split(readme, "\n") {
		if !strings.Contains(line, "curl") || !strings.Contains(line, "localhost:8443/api/") {
			continue
		}
		if !strings.Contains(line, "/api/users/") && !strings.Contains(line, "/api/orders/") {
			continue
		}
		// :8080 only redirects (no auth there); preflights need no token.
		if strings.Contains(line, "/api/users/login") || strings.Contains(line, "OPTIONS") || strings.Contains(line, "localhost:8080") {
			continue
		}
		if !strings.Contains(line, "Authorization") && !strings.Contains(line, "401") {
			t.Errorf("README line %d calls a protected route without a token: %s", i+1, strings.TrimSpace(line))
		}
	}
	if strings.Contains(readme, "Phase 8 replaces") {
		t.Error(`README still says "Phase 8 replaces" the API keys; they stay (see Auth & CORS)`)
	}
}

// None of the CORS headers leak to an origin that isn't allowed, whatever its
// shape: another site, "null" (sandboxed iframes, file://), a trailing slash.
func TestShouldGiveNoCORSHeaderAtAllToOtherOrigins(t *testing.T) {
	for _, origin := range []string{"https://evil.example", "null", "https://app.localhost:8443/", "https://app.localhost"} {
		pre := preflight(t, origin, "/api/orders/", http.MethodDelete)
		get := authed(t, http.MethodGet, "/api/users/", "Bearer "+freshToken("user"), map[string]string{"Origin": origin})
		for name, h := range map[string]http.Header{"preflight": pre.header, "GET": get.header} {
			if leaked := corsHeaders(h); len(leaked) > 0 {
				t.Errorf("%s from %q got %v, want no Access-Control-* header", name, origin, leaked)
			}
		}
	}
}
