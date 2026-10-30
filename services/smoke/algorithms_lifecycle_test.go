//go:build smoke && lifecycle

package smoke

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
)

// algoServer adds a temporary upstream over the three order replicas with the
// given balancing directive ("" = round-robin) and a server for host.
func algoServer(t *testing.T, name, host, directive string) {
	t.Helper()
	var servers strings.Builder
	for _, svc := range orderReplicas {
		fmt.Fprintf(&servers, "    server %s:8080;\n", svc)
	}
	writeConf(t, name+".conf", fmt.Sprintf(`upstream %s {
    %s
%s}
server {
    listen 80;
    server_name %s;
    location / {
        include /etc/nginx/gatekeeper/snippets/proxy-headers.conf;
        proxy_pass http://%s;
    }
}
`, strings.ReplaceAll(name, "-", "_"), directive, servers.String(), host, strings.ReplaceAll(name, "-", "_")))
	if out, err := nginxCmd(t, "-s", "reload"); err != nil {
		t.Fatalf("reload with %s: %v\n%s", name, err, out)
	}
	time.Sleep(time.Second)
}

func replicaHostname(t *testing.T, svc string) string {
	t.Helper()
	id := strings.TrimSpace(mustCompose(t, "ps", "-q", svc))
	if len(id) < 12 {
		t.Fatalf("no container id for %s: %q", svc, id)
	}
	return id[:12]
}

func TestShouldSpreadEvenlyWithRoundRobin(t *testing.T) {
	restoreStack(t)
	algoServer(t, "zz-smoke-rr", "rr.smoke.test", "")
	counts := hostnameCounts(t, "rr.smoke.test", "/", 30)
	if len(counts) != len(orderReplicas) {
		t.Fatalf("round-robin reached %d replicas %v, want 3", len(counts), counts)
	}
	for host, n := range counts {
		if n < 8 || n > 12 {
			t.Errorf("round-robin gave %s %d of 30, want 8–12 (%v)", host, n, counts)
		}
	}
}

// Every request from the host arrives from the same client IP, so ip_hash
// pins all of them to one replica: stickiness by IP, not by user.
func TestShouldPinEveryHostRequestToOneReplicaWithIpHash(t *testing.T) {
	restoreStack(t)
	algoServer(t, "zz-smoke-iphash", "iphash.smoke.test", "ip_hash;")
	if counts := hostnameCounts(t, "iphash.smoke.test", "/", 30); len(counts) != 1 {
		t.Errorf("ip_hash spread 30 host requests over %d replicas %v, want 1", len(counts), counts)
	}
}

type loadResult struct {
	slowShare float64
	mean, p95 time.Duration
}

// load sends total GETs at the given concurrency and reports the share that
// reached slowHost and the p95 latency.
func load(t *testing.T, host, path, slowHost string, total, concurrency int) loadResult {
	t.Helper()
	var mu sync.Mutex
	var latencies []time.Duration
	slow := 0
	var wg sync.WaitGroup
	for w := 0; w < concurrency; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			client := &http.Client{Timeout: 10 * time.Second}
			for i := 0; i < total/concurrency; i++ {
				req, _ := http.NewRequest(http.MethodGet, gatewayURL+path, nil)
				req.Host = host
				start := time.Now()
				resp, err := client.Do(req)
				took := time.Since(start)
				if err != nil {
					t.Errorf("GET %s (Host %q): %v", path, host, err)
					return
				}
				var e hostEcho
				decodeErr := json.NewDecoder(resp.Body).Decode(&e)
				resp.Body.Close()
				mu.Lock()
				latencies = append(latencies, took)
				if decodeErr == nil && e.Hostname == slowHost {
					slow++
				}
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if len(latencies) == 0 {
		t.Fatal("no responses")
	}
	sort.Slice(latencies, func(i, j int) bool { return latencies[i] < latencies[j] })
	var sum time.Duration
	for _, l := range latencies {
		sum += l
	}
	return loadResult{
		slowShare: float64(slow) / float64(len(latencies)),
		mean:      sum / time.Duration(len(latencies)),
		p95:       latencies[len(latencies)*95/100],
	}
}

func TestShouldAvoidASlowReplicaBetterWithLeastConnThanRoundRobin(t *testing.T) {
	withServiceEnv(t, "order-service-2", "DELAY_MS=800")
	slowHost := replicaHostname(t, "order-service-2")
	algoServer(t, "zz-smoke-rr-load", "rrload.smoke.test", "")

	rr := load(t, "rrload.smoke.test", "/", slowHost, 60, 6)
	lc := load(t, "", "/api/orders/", slowHost, 60, 6)
	t.Logf("round-robin: %.0f%% to the slow replica, mean %v, p95 %v; least_conn: %.0f%%, mean %v, p95 %v",
		rr.slowShare*100, rr.mean, rr.p95, lc.slowShare*100, lc.mean, lc.p95)

	if lc.slowShare >= rr.slowShare {
		t.Errorf("least_conn sent %.0f%% to the slow replica, round-robin %.0f%%; want least_conn lower", lc.slowShare*100, rr.slowShare*100)
	}
	// Not p95: least_conn still sends ~8% here, and anything over 5% puts the
	// 800ms replica into both p95s. The mean shows the difference.
	if lc.mean >= rr.mean {
		t.Errorf("least_conn mean %v, round-robin mean %v; want least_conn lower", lc.mean, rr.mean)
	}
}

func TestShouldDocumentEachAlgorithmInTheReadme(t *testing.T) {
	b, err := os.ReadFile(filepath.Join(repoRoot, "README.md"))
	if err != nil {
		t.Fatal(err)
	}
	readme := string(b)
	start := strings.Index(readme, "\n## Load balancing")
	if start < 0 {
		t.Fatal("README has no \"## Load balancing\" section")
	}
	section := readme[start+1:]
	if end := strings.Index(section[3:], "\n## "); end >= 0 {
		section = section[:end+3]
	}
	for _, algo := range []string{"round-robin", "least_conn", "ip_hash"} {
		var row bool
		for _, line := range strings.Split(section, "\n") {
			cells := strings.Split(strings.Trim(strings.TrimSpace(line), "|"), "|")
			if strings.HasPrefix(strings.TrimSpace(line), "|") && len(cells) >= 3 && strings.Contains(cells[0], algo) {
				row = true
			}
		}
		if !row {
			t.Errorf("README Load balancing table has no row for %s", algo)
		}
	}
}
