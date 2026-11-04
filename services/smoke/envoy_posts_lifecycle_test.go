//go:build smoke && lifecycle

package smoke

import (
	"fmt"
	"net/http"
	"testing"
	"time"
)

// With a replica stopped, Envoy keeps its last address (Docker's DNS stops
// answering for it), so round-robin still sends some requests there and they
// fail to connect. A POST that lands on it must come back as an error, not be
// retried on another replica: every POST is seen at most once, and only the
// ones that were answered.
func TestShouldNotRetryAPostThatFailedToConnectOnEnvoy(t *testing.T) {
	restoreStack(t)
	mustCompose(t, "stop", "order-service-2")
	tok := map[string]string{"Authorization": "Bearer " + freshToken("user")}
	const posts = 9
	paths := make([]string, posts)
	failed, answered := 0, 0
	for i := range paths {
		paths[i] = fmt.Sprintf("/post-%d-%d", i, time.Now().UnixNano())
		if a := viaEnvoy(t, http.MethodPost, "/api/orders"+paths[i], tok); a.status >= 500 {
			failed++
		} else if a.status == 200 {
			answered++
		}
	}
	if failed == 0 {
		t.Fatalf("none of %d POSTs failed with order-service-2 stopped; either Envoy dropped the replica or retried the POSTs", posts)
	}
	time.Sleep(time.Second) // the replicas' logs
	seen := 0
	for _, p := range paths {
		n := serviceSaw(t, "order-service", p) + serviceSaw(t, "order-service-3", p)
		if n > 1 {
			t.Errorf("POST %s reached the running replicas %d times, want at most 1", p, n)
		}
		seen += n
	}
	if seen != answered {
		t.Errorf("%d POSTs answered 200 but the running replicas saw %d: a failed POST was retried", answered, seen)
	}
}
