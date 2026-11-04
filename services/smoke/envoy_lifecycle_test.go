//go:build smoke && lifecycle

package smoke

import (
	"fmt"
	"net/http"
	"testing"
	"time"
)

// With a replica stopped, Envoy retries GETs on another one; a POST is sent
// to exactly one replica, never twice.
func TestShouldRetryGetsButNeverPostsOnEnvoy(t *testing.T) {
	restoreStack(t)
	mustCompose(t, "stop", "order-service-2")
	tok := map[string]string{"Authorization": "Bearer " + freshToken("user")}
	for i := 0; i < 12; i++ {
		if a := viaEnvoy(t, http.MethodGet, "/api/orders/", tok); a.status != 200 {
			t.Errorf("Envoy GET #%d with order-service-2 stopped → %d, want 200 (retried)", i+1, a.status)
		}
	}
	path := fmt.Sprintf("/once-%d", time.Now().UnixNano())
	viaEnvoy(t, http.MethodPost, "/api/orders"+path, tok)
	time.Sleep(time.Second) // the replicas' logs
	n := 0
	for _, svc := range []string{"order-service", "order-service-3"} {
		n += serviceSaw(t, svc, path)
	}
	if n != 1 {
		t.Errorf("one POST via Envoy reached the running replicas %d times, want exactly 1", n)
	}
}
