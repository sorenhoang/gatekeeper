//go:build smoke && lifecycle

package smoke

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const retrySnippet = "/etc/nginx/gatekeeper/snippets/upstream-retry.conf"

// retryServer adds a temporary upstream (primary + backup) behind a server for
// host that uses the shipped retry rules, plus extra location directives.
func retryServer(t *testing.T, name, host, primary, backup, extra string) {
	t.Helper()
	up := strings.ReplaceAll(name, "-", "_")
	writeConf(t, name+".conf", fmt.Sprintf(`upstream %s {
    server %s;
    server %s backup;
}
server {
    listen 80;
    server_name %s;
    location / {
        include /etc/nginx/gatekeeper/snippets/proxy-headers.conf;
        include %s;
        %s
        proxy_pass http://%s;
    }
}
`, up, primary, backup, host, retrySnippet, extra, up))
	if out, err := nginxCmd(t, "-s", "reload"); err != nil {
		t.Fatalf("reload with %s: %v\n%s", name, err, out)
	}
	time.Sleep(time.Second)
}

func doRequest(t *testing.T, method, host, path, requestID string) (int, hostEcho, time.Duration) {
	t.Helper()
	req, err := http.NewRequest(method, gatewayURL+path, strings.NewReader(`{"item":"book"}`))
	if err != nil {
		t.Fatal(err)
	}
	req.Host = host
	req.Header.Set("X-Request-ID", requestID)
	start := time.Now()
	resp, err := (&http.Client{Timeout: 20 * time.Second}).Do(req)
	took := time.Since(start)
	if err != nil {
		t.Fatalf("%s %s (Host %s): %v", method, path, host, err)
	}
	defer resp.Body.Close()
	var e hostEcho
	if resp.StatusCode == http.StatusOK {
		_ = json.NewDecoder(resp.Body).Decode(&e)
	}
	return resp.StatusCode, e, took
}

func TestShouldKeepEveryGetSucceedingWhenOneReplicaStops(t *testing.T) {
	restoreStack(t)
	gone := replicaHostname(t, "order-service-2")
	mustCompose(t, "stop", "order-service-2")

	// One GET may fail: if nginx re-resolves the upstream while that request
	// waits out the connect timeout on the stopped replica's old IP, its retry
	// finds "no live upstreams". Measured: 0 or 1 per stop, never more.
	const maxFailuresPerStop = 1
	seen := map[string]int{}
	var failed []string
	for i := 0; i < 30; i++ {
		status, e, _ := doRequest(t, http.MethodGet, "", "/api/orders/x", fmt.Sprintf("stop-%d", i))
		if status != http.StatusOK {
			failed = append(failed, fmt.Sprintf("#%d=%d", i, status))
			continue
		}
		seen[e.Hostname]++
	}
	if len(failed) > maxFailuresPerStop {
		t.Errorf("%d GETs failed after order-service-2 stopped (%v), want at most %d", len(failed), failed, maxFailuresPerStop)
	}
	if seen[gone] > 0 {
		t.Errorf("stopped replica %s still answered %d times", gone, seen[gone])
	}
	if len(seen) != 2 {
		t.Errorf("GETs were answered by %d replicas %v, want the 2 remaining", len(seen), seen)
	}
}

func TestShouldRetryAGetPastADeadAddressQuickly(t *testing.T) {
	restoreStack(t)
	retryServer(t, "zz-smoke-dead", "dead.smoke.test", unusedIP(t)+":8080", "order-service:8080", "")
	status, e, took := doRequest(t, http.MethodGet, "dead.smoke.test", "/", "dead-get")
	if status != http.StatusOK || e.Service != "order-service" {
		t.Fatalf("GET via a dead primary = %d from %q, want 200 from order-service", status, e.Service)
	}
	if took >= 2*time.Second {
		t.Errorf("GET via a dead primary took %v, want < 2s (1s connect timeout, one retry)", took)
	}
}

// A request that reached a replica may already have done its work: nginx
// retries a timed-out GET elsewhere, but never a POST.
func TestShouldRetryATimedOutGetButNeverAPostThatReachedAReplica(t *testing.T) {
	withServiceEnv(t, "order-service-2", "DELAY_MS=3000")
	// max_fails=0: the slow primary is tried by every request, never ejected.
	retryServer(t, "zz-smoke-slow", "slow.smoke.test", "order-service-2:8080 max_fails=0", "order-service:8080", "proxy_read_timeout 1s;")
	fastHost := replicaHostname(t, "order-service")

	status, e, took := doRequest(t, http.MethodGet, "slow.smoke.test", "/", "slow-get")
	if status != http.StatusOK || e.Hostname != fastHost {
		t.Errorf("GET via a slow primary = %d from %q, want 200 from order-service (%s) after a retry", status, e.Hostname, fastHost)
	}
	if took < time.Second {
		t.Errorf("GET answered in %v; it should have waited out the 1s read timeout first", took)
	}

	marker := fmt.Sprintf("slow-post-%d", time.Now().UnixNano())
	if status, _, _ := doRequest(t, http.MethodPost, "slow.smoke.test", "/", marker); status != http.StatusGatewayTimeout {
		t.Errorf("POST via a slow primary = %d, want 504 (not retried)", status)
	}
	time.Sleep(3 * time.Second) // let the slow replica finish and log the POST
	if n := len(logLinesWith(t, "order-service-2", marker)); n != 1 {
		t.Errorf("slow replica logged the POST %d times, want exactly 1", n)
	}
	if n := len(logLinesWith(t, "order-service", marker)); n != 0 {
		t.Errorf("backup replica saw the POST %d times, want 0 (POSTs are never retried once sent)", n)
	}
}

func TestShouldCapOrdersRetriesInConfig(t *testing.T) {
	conf, err := os.ReadFile(filepath.Join(repoRoot, "nginx", "conf.d", "default.conf"))
	if err != nil {
		t.Fatal(err)
	}
	var orders string
	for _, m := range locationBlock.FindAllStringSubmatch(string(conf), -1) {
		if strings.Contains(m[0], "/api/orders/") {
			orders = m[1]
		}
	}
	if !strings.Contains(orders, "snippets/upstream-retry.conf") {
		t.Errorf("the orders location does not include snippets/upstream-retry.conf:\n%s", orders)
	}
	snippet, err := os.ReadFile(filepath.Join(repoRoot, "nginx", "snippets", "upstream-retry.conf"))
	if err != nil {
		t.Fatalf("snippets/upstream-retry.conf: %v", err)
	}
	for _, d := range []string{
		"proxy_next_upstream error timeout http_502 http_503;",
		"proxy_next_upstream_tries 2;",
		"proxy_next_upstream_timeout 15s;",
	} {
		if !strings.Contains(string(snippet), d) {
			t.Errorf("upstream-retry.conf lacks %q", d)
		}
	}
}
