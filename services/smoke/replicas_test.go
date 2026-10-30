//go:build smoke

package smoke

import (
	"encoding/json"
	"net"
	"net/http"
	"regexp"
	"strings"
	"testing"
)

var orderReplicas = []string{"order-service", "order-service-2", "order-service-3"}

type hostEcho struct {
	Service    string `json:"service"`
	Hostname   string `json:"hostname"`
	RemoteAddr string `json:"remote_addr"`
}

func getEcho(t *testing.T, path string) (int, hostEcho) {
	t.Helper()
	r := mustRoute(t, "", path)
	var e hostEcho
	if r.status == http.StatusOK {
		if err := json.Unmarshal([]byte(r.body), &e); err != nil {
			t.Fatalf("GET %s: not a backend echo: %v\n%s", path, err, r.body)
		}
	}
	return r.status, e
}

// hostnameCounts sends n sequential GETs and counts answers per replica.
func hostnameCounts(t *testing.T, host, path string, n int) map[string]int {
	t.Helper()
	counts := map[string]int{}
	for i := 0; i < n; i++ {
		r := mustRoute(t, host, path)
		if r.status != http.StatusOK {
			t.Fatalf("GET %s (Host %q) #%d = %d\n%s", path, host, i, r.status, r.body)
		}
		var e hostEcho
		if err := json.Unmarshal([]byte(r.body), &e); err != nil {
			t.Fatalf("GET %s #%d: not a backend echo: %v\n%s", path, i, err, r.body)
		}
		if e.Service != "order-service" {
			t.Fatalf("GET %s #%d answered by %q, want order-service", path, i, e.Service)
		}
		counts[e.Hostname]++
	}
	return counts
}

func TestShouldDefineThreeOrderReplicasWithoutPublishedPorts(t *testing.T) {
	var cfg struct {
		Services map[string]struct {
			Build *struct {
				Args map[string]string `json:"args"`
			} `json:"build"`
			Ports     []any          `json:"ports"`
			DependsOn map[string]any `json:"depends_on"`
		} `json:"services"`
	}
	if err := json.Unmarshal([]byte(mustCompose(t, "config", "--format", "json")), &cfg); err != nil {
		t.Fatalf("compose config is not JSON: %v", err)
	}
	for _, svc := range orderReplicas {
		s, ok := cfg.Services[svc]
		if !ok {
			t.Errorf("compose has no %s", svc)
			continue
		}
		if s.Build == nil || s.Build.Args["SERVICE"] != "order-service" {
			t.Errorf("%s does not build with SERVICE=order-service", svc)
		}
		if len(s.Ports) > 0 {
			t.Errorf("%s publishes ports %v; only the gateway may", svc, s.Ports)
		}
		if _, ok := cfg.Services["gateway"].DependsOn[svc]; !ok {
			t.Errorf("gateway does not depend on %s", svc)
		}
	}
}

func TestShouldSpreadOrdersOverThreeReplicas(t *testing.T) {
	counts := hostnameCounts(t, "", "/api/orders/", 30)
	if len(counts) != len(orderReplicas) {
		t.Fatalf("30 GETs reached %d replicas %v, want %d", len(counts), counts, len(orderReplicas))
	}
	for host, n := range counts {
		if n < 5 {
			t.Errorf("replica %s got %d of 30 requests, want at least 5 (%v)", host, n, counts)
		}
	}
}

func upstreamBlock(t *testing.T, name string) string {
	t.Helper()
	out, err := nginxCmd(t, "-T")
	if err != nil {
		t.Fatalf("nginx -T: %v\n%s", err, out)
	}
	m := regexp.MustCompile(`(?s)upstream\s+` + name + `\s*\{(.*?)\}`).FindStringSubmatch(out)
	if m == nil {
		t.Fatalf("loaded config has no upstream %s", name)
	}
	return m[1]
}

func TestShouldBalanceOrdersWithLeastConnAndPassiveHealthChecks(t *testing.T) {
	block := upstreamBlock(t, "orders")
	for _, d := range []string{"least_conn;", "keepalive 32;"} {
		if !strings.Contains(block, d) {
			t.Errorf("upstream orders lacks %q:\n%s", d, block)
		}
	}
	for _, svc := range orderReplicas {
		re := regexp.MustCompile(`server\s+` + regexp.QuoteMeta(svc) + `:8080\s+([^;]*);`)
		m := re.FindStringSubmatch(block)
		if m == nil {
			t.Errorf("upstream orders has no server %s:8080", svc)
			continue
		}
		for _, p := range []string{"resolve", "max_fails=1", "fail_timeout=10s"} {
			if !strings.Contains(m[1], p) {
				t.Errorf("server %s:8080 lacks %s (has %q)", svc, p, m[1])
			}
		}
	}
	for _, up := range []string{"users", "products"} {
		if !strings.Contains(upstreamBlock(t, up), "keepalive 32;") {
			t.Errorf("upstream %s lacks keepalive 32", up)
		}
	}
}

// Without a pool every request opens a new connection, so the backend sees a
// new gateway source port each time; with keepalive the ports repeat.
func TestShouldReuseUpstreamConnections(t *testing.T) {
	const n = 40
	ports := map[string]bool{}
	for i := 0; i < n; i++ {
		status, e := getEcho(t, "/api/users/")
		if status != http.StatusOK {
			t.Fatalf("GET /api/users/ #%d = %d", i, status)
		}
		_, port, err := net.SplitHostPort(e.RemoteAddr)
		if err != nil {
			t.Fatalf("remote_addr %q: %v", e.RemoteAddr, err)
		}
		ports[port] = true
	}
	if len(ports) >= n/2 {
		t.Errorf("%d requests used %d distinct gateway source ports, want fewer than %d (keepalive pool)", n, len(ports), n/2)
	}
}
