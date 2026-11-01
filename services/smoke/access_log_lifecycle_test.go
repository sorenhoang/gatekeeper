//go:build smoke && lifecycle

package smoke

import (
	"net/http"
	"strings"
	"testing"
	"time"
)

// A retry is still one request: one line, with every address tried.
func TestShouldLogARetriedRequestOnceWithBothUpstreamAddresses(t *testing.T) {
	restoreStack(t)
	dead := unusedIP(t)
	retryServer(t, "zz-smoke-logretry", "logretry.smoke.test", dead+":8080", "order-service:8080", "")
	since := time.Now().Add(-time.Second)
	id := runID("retry")
	if r := withIDs(t, http.MethodGet, "logretry.smoke.test", "/", nil, id); r.status != http.StatusOK {
		t.Fatalf("GET via a dead primary = %d, want 200", r.status)
	}
	lines := linesFor(t, since, id)
	if len(lines) != 1 {
		t.Fatalf("%d access-log lines for the retried request, want 1: %v", len(lines), lines)
	}
	addrs := strings.Split(lines[0]["upstream_addr"], ", ")
	if len(addrs) != 2 || !strings.HasPrefix(addrs[0], dead) {
		t.Errorf("upstream_addr = %q, want the dead address then the backup, comma-separated", lines[0]["upstream_addr"])
	}
}

// The degraded answer comes from a second nginx server; only the gateway
// server may log the request.
func TestShouldLogADegradedRequestExactlyOnce(t *testing.T) {
	restoreStack(t)
	mustCompose(t, append([]string{"stop"}, orderReplicas...)...)
	if !eventually(reresolveWithin, func() bool {
		r, err := route(t, "", "/api/orders/x")
		return err == nil && isDegraded(r)
	}) {
		t.Fatal("orders never became degraded")
	}
	since := time.Now().Add(-time.Second)
	id := runID("degraded")
	withIDs(t, http.MethodGet, "", "/api/orders/x", nil, id)
	lines := linesFor(t, since, id)
	if len(lines) != 1 {
		t.Fatalf("%d access-log lines for the degraded request, want exactly 1: %v", len(lines), lines)
	}
	if !strings.Contains(lines[0]["upstream_addr"], "127.0.0.1:8081") {
		t.Errorf("degraded line upstream_addr = %q, want the 127.0.0.1:8081 backup", lines[0]["upstream_addr"])
	}
}
