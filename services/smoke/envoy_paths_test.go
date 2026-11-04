//go:build smoke

package smoke

import (
	"net/http"
	"testing"
)

// The path Envoy checks is the path it routes: dot segments, encoded dots
// and doubled slashes never reach a protected service without a token.
func TestShouldNormalizePathsBeforeTheAuthCheckOnEnvoy(t *testing.T) {
	for _, p := range []string{
		"/api/products/../users/",
		"/api/products/%2e%2e/users/",
		"/api/products/%2E%2E/orders/",
		"/api/products/..%2fusers/",
		"/api//users/",
	} {
		a := viaEnvoy(t, http.MethodGet, p, nil)
		if a.echo.Service == "user-service" || a.echo.Service == "order-service" {
			t.Errorf("Envoy GET %s without a token reached %s (%d), want it checked and refused", p, a.echo.Service, a.status)
		}
		if a.status == 200 {
			t.Errorf("Envoy GET %s without a token → 200, want a refusal", p)
		}
	}
}

// An unknown path is a plain 404 from Envoy, not an auth answer.
func TestShouldAnswerUnknownPathsWith404OnEnvoy(t *testing.T) {
	for _, p := range []string{"/nope", "/verify/api/users/", "/api/auth/login"} {
		if a := viaEnvoy(t, http.MethodGet, p, nil); a.status != 404 {
			t.Errorf("Envoy GET %s → %d, want 404", p, a.status)
		}
	}
}
