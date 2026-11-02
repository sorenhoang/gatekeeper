//go:build smoke && lifecycle

package smoke

import (
	"net/http"
	"strings"
	"testing"
)

// With auth-service gone the protected routes must close, not open; the
// public ones keep working. A fresh token, so no cached verdict hides it.
func TestShouldFailClosedWhenAuthServiceIsDown(t *testing.T) {
	restoreStack(t)
	mustCompose(t, "stop", "auth-service")
	tok := freshToken("user")
	var a authAnswer
	if !eventually(reresolveWithin, func() bool {
		a = authed(t, http.MethodGet, "/api/users/", "Bearer "+tok, nil)
		return a.status == 503
	}) || !strings.Contains(a.body, `"error":"auth unavailable"`) {
		t.Errorf("/api/users/ with auth-service stopped → %d %q, want 503 JSON auth unavailable", a.status, a.body)
	}
	gw, _ := uniqueProduct("authdown")
	for _, path := range []string{gw, "/health"} {
		if r := authed(t, http.MethodGet, path, "", nil); r.status != 200 {
			t.Errorf("%s with auth-service stopped → %d, want 200 (public)", path, r.status)
		}
	}
}
