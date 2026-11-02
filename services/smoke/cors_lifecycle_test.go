//go:build smoke && lifecycle

package smoke

import (
	"net/http"
	"testing"
)

func TestShouldAddCORSToTheJSON502(t *testing.T) {
	restoreStack(t)
	mustCompose(t, "stop", "user-service")
	origin := allowedOrigins[0]
	tok := freshToken("user")
	var a authAnswer
	eventually(reresolveWithin, func() bool {
		a = authed(t, http.MethodGet, "/api/users/", "Bearer "+tok, map[string]string{"Origin": origin})
		return a.status == http.StatusBadGateway
	})
	if a.status != http.StatusBadGateway || a.header.Get("Access-Control-Allow-Origin") != origin {
		t.Errorf("JSON 502 from %s → %d ACAO %q, want 502 with %s", origin, a.status, a.header.Get("Access-Control-Allow-Origin"), origin)
	}
}
