//go:build smoke && lifecycle

package smoke

import "testing"

func TestShouldAnswerTheSameJSON502OverHTTP2(t *testing.T) {
	restoreStack(t)
	mustCompose(t, "stop", "user-service")
	h2, h1 := tlsClient(t, false, nil), tlsClient(t, true, nil)
	var a, b hit
	if !eventually(reresolveWithin, func() bool {
		a = h2Get(t, h2, "/api/users/", 2, nil)
		return a.status == 502
	}) {
		t.Fatalf("HTTP/2 /api/users/ with user-service stopped → %d %q, want 502", a.status, a.body)
	}
	b = h2Get(t, h1, "/api/users/", 1, nil)
	if b.status != 502 || a.body != b.body {
		t.Errorf("JSON 502: HTTP/2 %q vs HTTP/1.1 %d %q, want the same body", a.body, b.status, b.body)
	}
}
