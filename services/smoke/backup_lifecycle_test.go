//go:build smoke && lifecycle

package smoke

import (
	"encoding/json"
	"net"
	"net/http"
	"regexp"
	"testing"
	"time"
)

type degraded struct {
	Degraded bool   `json:"degraded"`
	Service  string `json:"service"`
}

func isDegraded(r routed) bool {
	var d degraded
	return r.status == http.StatusOK && r.header.Get("X-Degraded") == "true" &&
		json.Unmarshal([]byte(r.body), &d) == nil && d.Degraded && d.Service == "orders-fallback"
}

// A replica that failed while going down stays ejected for fail_timeout (10s)
// after it is back, even on the same IP; +3s covers the re-resolve.
const recoverWithin = 13 * time.Second

func TestShouldAnswerDegradedWhenEveryOrderReplicaIsGone(t *testing.T) {
	restoreStack(t)
	mustCompose(t, append([]string{"stop"}, orderReplicas...)...)

	var r routed
	if !eventually(reresolveWithin, func() bool {
		var err error
		r, err = route(t, "", "/api/orders/x")
		return err == nil && isDegraded(r)
	}) {
		t.Fatalf("with every order replica stopped, /api/orders/x = %d X-Degraded=%q\n%s",
			r.status, r.header.Get("X-Degraded"), r.body)
	}
	if got := r.header.Get("X-Route"); got != "orders" {
		t.Errorf("degraded response X-Route = %q, want orders", got)
	}

	mustCompose(t, "start", "order-service")
	if !eventually(recoverWithin, func() bool {
		r, err := route(t, "", "/api/orders/x")
		return err == nil && r.status == http.StatusOK && r.header.Get("X-Degraded") == "" &&
			r.echo(t).Service == "order-service"
	}) {
		t.Errorf("order-service back, but /api/orders/x is still not a normal answer within %s", recoverWithin)
	}
}

func TestShouldNeverDegradeWhileReplicasAreUp(t *testing.T) {
	for i := 0; i < 30; i++ {
		r := mustRoute(t, "", "/api/orders/")
		if r.header.Get("X-Degraded") != "" || r.status != http.StatusOK {
			t.Fatalf("GET /api/orders/ #%d with all replicas up = %d X-Degraded=%q", i, r.status, r.header.Get("X-Degraded"))
		}
	}
}

// The backup listens inside the gateway container only; the host must not
// reach it (Phase 8 relies on only :8080 being published).
func TestShouldKeepTheBackupListenerOffTheHost(t *testing.T) {
	block := upstreamBlock(t, "orders")
	if !regexp.MustCompile(`server\s+127\.0\.0\.1:8081\s+backup\s*;`).MatchString(block) {
		t.Fatalf("upstream orders has no `server 127.0.0.1:8081 backup;`:\n%s", block)
	}
	if conn, err := net.DialTimeout("tcp", "localhost:8081", time.Second); err == nil {
		conn.Close()
		t.Errorf("localhost:8081 is reachable from the host; the backup must stay inside the gateway")
	}
	if out, err := compose(t, "port", "gateway", "8081"); err == nil && out != "" && out != ":0\n" {
		t.Errorf("gateway publishes 8081: %q", out)
	}
}
