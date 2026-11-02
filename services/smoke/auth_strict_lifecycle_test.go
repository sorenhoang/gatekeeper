//go:build smoke && lifecycle

package smoke

import (
	"net/http"
	"strings"
	"testing"
)

// Fail closed on orders too, not only users.
func TestShouldFailClosedOnOrdersWhenAuthServiceIsDown(t *testing.T) {
	restoreStack(t)
	mustCompose(t, "stop", "auth-service")
	tok := freshToken("user")
	var a authAnswer
	if !eventually(reresolveWithin, func() bool {
		a = authed(t, http.MethodGet, "/api/orders/", "Bearer "+tok, nil)
		return a.status == 503
	}) || !strings.Contains(a.body, `"error":"auth unavailable"`) {
		t.Errorf("/api/orders/ with auth-service stopped → %d %q, want 503 JSON auth unavailable", a.status, a.body)
	}
}
