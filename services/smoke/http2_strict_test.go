//go:build smoke

package smoke

import (
	"net/http"
	"testing"
)

// Both protocols echo the client's id, not just HTTP/2.
func TestShouldEchoTheRequestIDOnBothProtocols(t *testing.T) {
	for name, c := range map[string]struct {
		client *http.Client
		major  int
	}{"HTTP/2": {tlsClient(t, false, nil), 2}, "HTTP/1.1": {tlsClient(t, true, nil), 1}} {
		id := runID("echo")
		h := h2Get(t, c.client, "/api/nope", c.major, map[string]string{"X-Request-ID": id})
		if got := h.header.Values("X-Request-Id"); len(got) != 1 || got[0] != id {
			t.Errorf("%s /api/nope X-Request-ID %q, want exactly [%s]", name, got, id)
		}
	}
}

// The README's claim, exactly: on one HTTP/2 connection, 11 quick logins are
// answered (burst=10 nodelay + the token for the first) and the 12th is 429.
func TestShouldRejectTheTwelfthQuickLoginOnOneHTTP2Connection(t *testing.T) {
	afterDrain(t)
	c := tlsClient(t, false, nil)
	var statuses []int
	for i := 0; i < 12; i++ {
		statuses = append(statuses, h2Get(t, c, loginPath, 2, nil).status)
	}
	for i, s := range statuses[:11] {
		if s == http.StatusTooManyRequests {
			t.Fatalf("login #%d on one HTTP/2 connection was a 429 (statuses %v), want the first 11 answered", i+1, statuses)
		}
	}
	if statuses[11] != http.StatusTooManyRequests {
		t.Errorf("login #12 → %d (statuses %v), want 429", statuses[11], statuses)
	}
}
