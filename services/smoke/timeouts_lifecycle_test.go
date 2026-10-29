//go:build smoke && lifecycle

package smoke

import (
	"io"
	"net/http"
	"os"
	"os/exec"
	"testing"
	"time"
)

// Longer than any route timeout, so the gateway, not this client, gives up.
var patientClient = &http.Client{Timeout: 30 * time.Second}

// withServiceEnv recreates one service with extra env (e.g. DELAY_MS) and
// makes nginx re-resolve it, so the next request reaches the new container.
func withServiceEnv(t *testing.T, svc string, env ...string) {
	t.Helper()
	restoreStack(t)
	cmd := exec.Command("docker", "compose", "--project-directory", repoRoot, "up", "-d", "--no-deps", svc)
	cmd.Env = append(os.Environ(), env...)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("recreate %s with %v: %v\n%s", svc, env, err, out)
	}
	waitHealthy(t, svc)
	if out, err := nginxCmd(t, "-s", "reload"); err != nil {
		t.Fatalf("reload after recreating %s: %v\n%s", svc, err, out)
	}
	time.Sleep(time.Second)
}

type timedResponse struct {
	status  int
	header  http.Header
	body    string
	elapsed time.Duration
}

func timedGet(t *testing.T, path string) timedResponse {
	t.Helper()
	start := time.Now()
	resp, err := patientClient.Get(gatewayURL + path)
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return timedResponse{status: resp.StatusCode, header: resp.Header, body: string(b), elapsed: time.Since(start)}
}

// One second of slack on top of each route's read timeout.
const timeoutSlack = time.Second

func TestShouldCutOffASlowUsersBackendAtTwoSeconds(t *testing.T) {
	withServiceEnv(t, "user-service", "DELAY_MS=3000")
	r := timedGet(t, "/api/users/x")
	if r.status != http.StatusGatewayTimeout {
		t.Errorf("slow users: status %d, want 504\n%s", r.status, r.body)
	}
	if r.elapsed < 2*time.Second || r.elapsed >= 2*time.Second+timeoutSlack {
		t.Errorf("slow users answered after %v, want between 2s and %v", r.elapsed, 2*time.Second+timeoutSlack)
	}
}

func TestShouldLetASlowOrdersBackendFinish(t *testing.T) {
	withServiceEnv(t, "order-service", "DELAY_MS=3000")
	r := timedGet(t, "/api/orders/x")
	if r.status != http.StatusOK {
		t.Errorf("slow orders: status %d, want 200 (orders may take up to 15s)\n%s", r.status, r.body)
	}
	if r.elapsed < 3*time.Second {
		t.Errorf("slow orders answered after %v, want >= 3s (DELAY_MS not applied?)", r.elapsed)
	}
}

func TestShouldCutOffASlowProductsBackendAtFiveSeconds(t *testing.T) {
	withServiceEnv(t, "product-service", "DELAY_MS=7000")
	r := timedGet(t, "/api/products/x")
	if r.status != http.StatusGatewayTimeout {
		t.Errorf("slow products: status %d, want 504\n%s", r.status, r.body)
	}
	if r.elapsed < 5*time.Second || r.elapsed >= 5*time.Second+timeoutSlack {
		t.Errorf("slow products answered after %v, want between 5s and %v", r.elapsed, 5*time.Second+timeoutSlack)
	}
}

func TestShouldKeepHealthFastAndRecoverAfterTheDelayIsRemoved(t *testing.T) {
	withServiceEnv(t, "user-service", "DELAY_MS=3000")
	start := time.Now()
	if _, err := fromGateway(t, "http://user-service:8080/health"); err != nil || time.Since(start) > time.Second {
		t.Errorf("user-service /health with DELAY_MS: err=%v after %v, want fast 200", err, time.Since(start))
	}

	if out, err := runMake(t, "up"); err != nil {
		t.Fatalf("make up: %v\n%s", err, out)
	}
	r := timedGet(t, "/api/users/x")
	if r.status != http.StatusOK || r.elapsed > time.Second {
		t.Errorf("after make up, /api/users/x = %d in %v, want a fast 200", r.status, r.elapsed)
	}
}
