//go:build smoke && lifecycle

package smoke

import (
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

func requestOrders(t *testing.T, method string) (*http.Response, error) {
	t.Helper()
	req, err := http.NewRequest(method, gatewayURL+"/api/orders/x", strings.NewReader(`{"item":"book"}`))
	if err != nil {
		t.Fatal(err)
	}
	resp, err := (&http.Client{Timeout: 5 * time.Second}).Do(req)
	if err == nil {
		resp.Body.Close()
	}
	return resp, err
}

// A degraded 200 to a POST would read as "order created" while nothing was
// written: only safe methods may get the fallback's success answer.
func TestShouldNeverAnswerAWriteWithADegraded200(t *testing.T) {
	restoreStack(t)
	mustCompose(t, append([]string{"stop"}, orderReplicas...)...)
	if !eventually(reresolveWithin, func() bool {
		r, err := route(t, "", "/api/orders/x")
		return err == nil && isDegraded(r)
	}) {
		t.Fatal("GET never became degraded with every replica stopped")
	}

	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete} {
		resp, err := requestOrders(t, method)
		if err != nil {
			t.Errorf("%s /api/orders/x: %v", method, err)
			continue
		}
		if resp.StatusCode != http.StatusServiceUnavailable {
			t.Errorf("%s /api/orders/x with every replica down = %d, want 503 (never a success)", method, resp.StatusCode)
		}
		if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
			t.Errorf("%s 503 Content-Type %q, want application/json", method, ct)
		}
	}
	if resp, err := requestOrders(t, http.MethodHead); err != nil || resp.StatusCode != http.StatusOK {
		t.Errorf("HEAD /api/orders/x degraded: err=%v status=%v, want 200", err, resp)
	}
}

// Phase 6 adds caching: a degraded answer must never be stored and replayed
// after the replicas are back.
func TestShouldMarkTheDegradedAnswerUncacheable(t *testing.T) {
	restoreStack(t)
	mustCompose(t, append([]string{"stop"}, orderReplicas...)...)
	var r routed
	if !eventually(reresolveWithin, func() bool {
		var err error
		r, err = route(t, "", "/api/orders/x")
		return err == nil && isDegraded(r)
	}) {
		t.Fatal("GET never became degraded with every replica stopped")
	}
	if cc := r.header.Get("Cache-Control"); !strings.Contains(cc, "no-store") {
		t.Errorf("degraded answer Cache-Control = %q, want no-store", cc)
	}
}

// Unpublished is not enough: the fallback must also be bound to loopback, so
// no other container on the compose network can reach it either.
func TestShouldBindTheFallbackToLoopbackOnly(t *testing.T) {
	b, err := os.ReadFile(filepath.Join(repoRoot, "nginx", "conf.d", "orders-fallback.conf"))
	if err != nil {
		t.Fatal(err)
	}
	listens := regexp.MustCompile(`(?m)^\s*listen\s+([^;]+);`).FindAllStringSubmatch(string(b), -1)
	if len(listens) == 0 {
		t.Fatal("orders-fallback.conf has no listen directive")
	}
	for _, l := range listens {
		if !strings.HasPrefix(strings.TrimSpace(l[1]), "127.0.0.1:8081") {
			t.Errorf("orders-fallback.conf listens on %q, want 127.0.0.1:8081 only", l[1])
		}
	}
	if out, err := fromGateway(t, "http://order-service-2:8081/"); err == nil {
		t.Errorf("port 8081 answered on a compose-network address: %q", out)
	}
}
