package auth

import (
	"net/http"
	"testing"
	"time"
)

// Envoy's ext_authz checks with the client's own method at /verify<path>.
// Every request under /verify/ is a real check (the method is the check
// request's), never the echo 200 that would read as "allowed".
func TestShouldCheckEnvoyStyleRequestsUnderVerify(t *testing.T) {
	user := tokenFor(t, "alice", "alice-pass", t0)
	admin := tokenFor(t, "admin", "admin-pass", t0)
	mux := newMux(func() time.Time { return t0.Add(time.Minute) })
	cases := []struct {
		name, method, path string
		hdr                map[string]string
		want               int
		wantUser           string
	}{
		{"no token", http.MethodGet, "/verify/api/users/42", nil, http.StatusUnauthorized, ""},
		{"bad token", http.MethodGet, "/verify/api/users/42", bearer("x.y.z"), http.StatusUnauthorized, ""},
		{"user GET", http.MethodGet, "/verify/api/users/42", bearer(user), http.StatusOK, "alice"},
		{"user POST", http.MethodPost, "/verify/api/orders/", bearer(user), http.StatusOK, "alice"},
		{"user DELETE", http.MethodDelete, "/verify/api/orders/x", bearer(user), http.StatusForbidden, ""},
		{"admin DELETE", http.MethodDelete, "/verify/api/orders/x", bearer(admin), http.StatusOK, "admin"},
		{"bare /verify/", http.MethodGet, "/verify/", nil, http.StatusUnauthorized, ""},
	}
	for _, c := range cases {
		rec := do(t, mux, c.method, c.path, "", c.hdr)
		if rec.Code != c.want || rec.Header().Get("X-User-ID") != c.wantUser {
			t.Errorf("%s: %s %s → %d X-User-ID %q, want %d %q", c.name, c.method, c.path, rec.Code, rec.Header().Get("X-User-ID"), c.want, c.wantUser)
		}
	}
	// The nginx gateway's form is unchanged: GET /verify with X-Original-Method.
	if rec := do(t, mux, http.MethodGet, "/verify", "", map[string]string{"Authorization": "Bearer " + user, "X-Original-Method": http.MethodDelete}); rec.Code != http.StatusForbidden {
		t.Errorf("GET /verify as user with X-Original-Method DELETE → %d, want 403", rec.Code)
	}
}
