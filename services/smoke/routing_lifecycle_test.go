//go:build smoke && lifecycle

package smoke

import (
	"net/http"
	"net/netip"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// Longer than resolver valid=5s plus a lookup, short enough to fail fast.
const reresolveWithin = 10 * time.Second

func eventually(within time.Duration, ok func() bool) bool {
	deadline := time.Now().Add(within)
	for {
		if ok() {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(200 * time.Millisecond)
	}
}

func containerIP(t *testing.T, svc string) string {
	t.Helper()
	id := strings.TrimSpace(mustCompose(t, "ps", "-q", svc))
	out, err := exec.Command("docker", "inspect", "-f", "{{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}", id).CombinedOutput()
	if err != nil {
		t.Fatalf("inspect %s: %v\n%s", svc, err, out)
	}
	return strings.TrimSpace(string(out))
}

func reaches(t *testing.T, path, service string) bool {
	r, err := route(t, "", path)
	if err != nil || r.status != http.StatusOK {
		return false
	}
	return strings.Contains(r.body, `"service":"`+service+`"`)
}

func TestShouldFollowServicesWhoseIpsSwappedWithoutReload(t *testing.T) {
	restoreStack(t)
	before := map[string]string{
		"user-service":  containerIP(t, "user-service"),
		"order-service": containerIP(t, "order-service"),
	}
	startedAt := gatewayStartedAt(t)

	mustCompose(t, "rm", "-sf", "user-service", "order-service")
	// Docker hands out the freed addresses again; starting the services in
	// the opposite order of their old IPs makes them trade places.
	first, second := "user-service", "order-service"
	if netip.MustParseAddr(before["user-service"]).Less(netip.MustParseAddr(before["order-service"])) {
		first, second = second, first
	}
	mustCompose(t, "up", "-d", "--no-deps", first)
	mustCompose(t, "up", "-d", "--no-deps", second)
	after := map[string]string{
		"user-service":  containerIP(t, "user-service"),
		"order-service": containerIP(t, "order-service"),
	}
	t.Logf("IPs before %v, after %v (swapped: %v)", before, after,
		after["user-service"] == before["order-service"] && after["order-service"] == before["user-service"])

	ok := eventually(reresolveWithin, func() bool {
		return reaches(t, "/api/users/", "user-service") && reaches(t, "/api/orders/", "order-service")
	})
	if !ok {
		users := mustRoute(t, "", "/api/users/")
		orders := mustRoute(t, "", "/api/orders/")
		t.Errorf("after recreating services, %s later: /api/users/ → %d %s, /api/orders/ → %d %s",
			reresolveWithin, users.status, users.body, orders.status, orders.body)
	}
	if got := gatewayStartedAt(t); got != startedAt {
		t.Errorf("gateway restarted (%s → %s); routing must recover without it", startedAt, got)
	}
}

func TestShouldKeepReloadingAndServingOtherRoutesWhileAServiceIsStopped(t *testing.T) {
	restoreStack(t)
	mustCompose(t, "stop", "user-service")

	if out, err := nginxCmd(t, "-t"); err != nil {
		t.Errorf("nginx -t with user-service stopped: %v\n%s", err, out)
	}
	if out, err := runMake(t, "reload"); err != nil {
		t.Errorf("make reload with user-service stopped: %v\n%s", err, out)
	}
	if !eventually(reresolveWithin, func() bool {
		r, err := route(t, "", "/api/users/")
		return err == nil && r.status == http.StatusBadGateway
	}) {
		t.Errorf("/api/users/ with user-service stopped is not 502")
	}
	if !reaches(t, "/api/orders/", "order-service") {
		t.Errorf("/api/orders/ stopped working while only user-service is down")
	}

	mustCompose(t, "start", "user-service")
	if !eventually(reresolveWithin, func() bool { return reaches(t, "/api/users/", "user-service") }) {
		t.Errorf("/api/users/ did not recover within %s of user-service starting", reresolveWithin)
	}
}

func TestShouldStartGatewayWhileAServiceHasNoContainer(t *testing.T) {
	restoreStack(t)
	mustCompose(t, "rm", "-sf", "user-service")
	mustCompose(t, "restart", "gateway")

	if !eventually(reresolveWithin, func() bool {
		r, err := route(t, "", "/")
		return err == nil && r.status == http.StatusOK
	}) {
		logs, _ := compose(t, "logs", "--tail", "20", "gateway")
		t.Fatalf("gateway does not serve / after restarting without user-service:\n%s", logs)
	}
	if !reaches(t, "/api/orders/", "order-service") {
		t.Errorf("/api/orders/ not served after gateway restart without user-service")
	}
}
