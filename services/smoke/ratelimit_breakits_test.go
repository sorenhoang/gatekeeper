//go:build smoke

package smoke

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"
)

// limitServer adds a temporary server for host with its own 5r/s zone and the
// given limit_req options, proxying to users (limit_req never runs for a
// location answered by `return`).
func limitServer(t *testing.T, name, host, options string) {
	t.Helper()
	zone := strings.ReplaceAll(name, "-", "_")
	writeConf(t, name+".conf", fmt.Sprintf(`limit_req_zone $binary_remote_addr zone=%s:1m rate=5r/s;
server {
    listen 80;
    server_name %s;
    location / {
        %s
        include /etc/nginx/gatekeeper/snippets/proxy-headers.conf;
        proxy_pass http://users/;
    }
}
`, zone, host, fmt.Sprintf(options, zone)))
	if out, err := nginxCmd(t, "-s", "reload"); err != nil {
		t.Fatalf("reload with %s: %v\n%s", name, err, out)
	}
	time.Sleep(time.Second)
}

func passedLatencies(hits []hit) []time.Duration {
	var l []time.Duration
	for _, h := range hits {
		if h.status == http.StatusOK {
			l = append(l, h.latency)
		}
	}
	sort.Slice(l, func(i, j int) bool { return l[i] < l[j] })
	return l
}

// Without nodelay the burst is a queue: the 11 accepted requests are released
// one every 200ms (5r/s), so the last waits ~2s.
func TestShouldQueueTheBurstWithoutNodelay(t *testing.T) {
	limitServer(t, "zz-smoke-burst", "burst.smoke.test", "limit_req zone=%s burst=10;")
	hits := spike(t, 50, "burst.smoke.test", "/", nil)
	if c := countStatus(hits); c[200] != 11 || c[429] != 39 {
		t.Fatalf("burst=10 without nodelay → %v, want 11 × 200 and 39 × 429", c)
	}
	l := passedLatencies(hits)
	if l[len(l)-1] < 1800*time.Millisecond {
		t.Errorf("slowest accepted request took %v, want ≥ 1.8s (10 queued × 200ms)", l[len(l)-1])
	}
}

// With nodelay the same 11 pass at once; the slots are still used up.
func TestShouldAnswerTheWholeBurstAtOnceWithNodelay(t *testing.T) {
	limitServer(t, "zz-smoke-nodelay", "nodelay.smoke.test", "limit_req zone=%s burst=10 nodelay;")
	hits := spike(t, 50, "nodelay.smoke.test", "/", nil)
	if c := countStatus(hits); c[200] != 11 || c[429] != 39 {
		t.Fatalf("burst=10 nodelay → %v, want 11 × 200 and 39 × 429", c)
	}
	l := passedLatencies(hits)
	if l[len(l)-1] >= 500*time.Millisecond {
		t.Errorf("slowest accepted request took %v, want < 0.5s (no queueing)", l[len(l)-1])
	}
}

// Dry run: nothing is rejected, the log says what would have been.
func TestShouldOnlyLogRejectionsInDryRun(t *testing.T) {
	limitServer(t, "zz-smoke-dryrun", "dryrun.smoke.test", "limit_req zone=%s burst=10 nodelay; limit_req_dry_run on;")
	since := time.Now().Add(-time.Second)
	hits := spike(t, 50, "dryrun.smoke.test", "/", nil)
	if c := countStatus(hits); c[200] != 50 {
		t.Fatalf("dry run → %v, want 50 × 200", c)
	}
	ids := map[string]bool{}
	for _, h := range hits {
		ids[h.id] = true
	}
	var wouldReject int
	for deadline := time.Now().Add(3 * time.Second); time.Now().Before(deadline); time.Sleep(300 * time.Millisecond) {
		wouldReject = 0
		for _, m := range accessLines(t, since) {
			if ids[m["request_id"]] && m["limit_req"] == "REJECTED_DRY_RUN" {
				wouldReject++
			}
		}
		if wouldReject >= 38 {
			break
		}
	}
	// 50 − 1 − burst 10 = 39, minus one if the spike spans more than one
	// 200ms refill (measured: 38 once; in dry run all 50 are proxied, so the
	// spike spreads out more than in the rejecting variants).
	if wouldReject < 38 || wouldReject > 39 {
		t.Errorf("%d access-log lines with limit_req REJECTED_DRY_RUN, want 38–39 (50 − 1 − burst 10, minus one refill)", wouldReject)
	}
}

func TestShouldLogPassedForAnUnthrottledRequest(t *testing.T) {
	afterDrain(t)
	since := time.Now().Add(-time.Second)
	id := runID("passed")
	withIDs(t, http.MethodGet, "", "/api/users/", nil, id)
	lines := linesFor(t, since, id)
	if len(lines) != 1 || lines[0]["limit_req"] != "PASSED" {
		t.Errorf("access log for %s = %v, want one line with limit_req PASSED", id, lines)
	}
	for _, m := range accessLines(t, since) {
		if _, ok := m["limit_req"]; !ok {
			t.Errorf("access-log line without a limit_req field: %v", m)
		}
	}
}

func TestShouldDocumentRateLimitingInTheReadme(t *testing.T) {
	b, err := os.ReadFile(filepath.Join(repoRoot, "README.md"))
	if err != nil {
		t.Fatal(err)
	}
	readme := string(b)
	start := strings.Index(readme, "\n## Rate limiting")
	if start < 0 {
		t.Fatal(`README has no "## Rate limiting" section`)
	}
	section := readme[start+1:]
	if end := strings.Index(section[3:], "\n## "); end >= 0 {
		section = section[:end+3]
	}
	for _, topic := range []string{"burst=10 nodelay", "11th", "12th", "limit_req_dry_run", "Retry-After",
		"X-API-Key", "172.16.0.0/12", "return", "client_header_timeout", "50 r/s", "5 r/s"} {
		if !strings.Contains(section, topic) {
			t.Errorf("README Rate limiting section does not cover %q", topic)
		}
	}
}
