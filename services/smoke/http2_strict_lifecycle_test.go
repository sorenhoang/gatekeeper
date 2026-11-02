//go:build smoke && lifecycle

package smoke

import (
	"net/http"
	"testing"
	"time"
)

// The JSON 502 echoes the client's id on both protocols. Requests are sent
// directly (not via h2Get) so a transport error while user-service stops is
// retried rather than fatal.
func TestShouldEchoTheRequestIDOnThe502OverBothProtocols(t *testing.T) {
	restoreStack(t)
	mustCompose(t, "stop", "user-service")
	for name, c := range map[string]struct {
		client *http.Client
		major  int
	}{"HTTP/2": {tlsClient(t, false, nil), 2}, "HTTP/1.1": {tlsClient(t, true, nil), 1}} {
		var status, major int
		var got []string
		id := runID("echo502")
		ok := eventually(reresolveWithin, func() bool {
			req, _ := http.NewRequest(http.MethodGet, tlsURL+"/api/users/", nil)
			req.Header.Set("X-Request-ID", id)
			resp, err := c.client.Do(req)
			if err != nil {
				time.Sleep(100 * time.Millisecond)
				return false
			}
			resp.Body.Close()
			status, major, got = resp.StatusCode, resp.ProtoMajor, resp.Header.Values("X-Request-Id")
			return status == http.StatusBadGateway
		})
		if !ok || major != c.major || len(got) != 1 || got[0] != id {
			t.Errorf("%s /api/users/ with user-service stopped → %d over HTTP/%d, X-Request-ID %q; want 502 over HTTP/%d echoing [%s]",
				name, status, major, got, c.major, id)
		}
	}
}
