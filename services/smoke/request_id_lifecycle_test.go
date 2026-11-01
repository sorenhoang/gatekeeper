//go:build smoke && lifecycle

package smoke

import (
	"net/http"
	"testing"
)

// The JSON 502 comes from a named location reached by error_page: it sets its
// own add_header, so it needs the snippet too.
func TestShouldReturnARequestIDOnTheGatewaysJSON502(t *testing.T) {
	restoreStack(t)
	mustCompose(t, "stop", "user-service")
	var r idResponse
	if !eventually(reresolveWithin, func() bool {
		r = withIDs(t, http.MethodGet, "", "/api/users/", nil)
		return r.status == http.StatusBadGateway
	}) {
		t.Fatalf("/api/users/ with user-service stopped = %d, want 502", r.status)
	}
	if id := r.one(t, "JSON 502"); !generatedID.MatchString(id) {
		t.Errorf("JSON 502 X-Request-ID %q, want a 32-hex id", id)
	}
}
