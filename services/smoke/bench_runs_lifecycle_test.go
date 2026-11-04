//go:build smoke && lifecycle

package smoke

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// The warm-up is a real k6 run: a scenario that sends exactly one request
// per run, tagged with this test's marker, reaches the gateway RUNS+1 times.
func TestShouldSendAWarmUpRunBeforeTheMeasuredRuns(t *testing.T) {
	marker := runID("bench-warmup")
	f := filepath.Join(repoRoot, "loadtest", "zz-smoke-warmup.js")
	script := fmt.Sprintf(`import http from 'k6/http';
import { check } from 'k6';
export const options = { insecureSkipTLSVerify: true, vus: 1, iterations: 1 };
const users = JSON.parse(open('/secrets/users.json'));
export default function () {
  check(http.get('https://gateway/api/products/%s'), { '2xx': (r) => r.status < 300 });
}
`, marker)
	if err := os.WriteFile(f, []byte(script), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Remove(f) })
	if out, err := bench(t, "SCENARIOS=zz-smoke-warmup", "RUNS=2"); err != nil {
		t.Fatalf("make bench: %v\n%s", err, out)
	}
	n := 0
	for _, l := range logLinesWith(t, "gateway", marker) {
		if strings.HasPrefix(l, "{") { // access-log lines only, not error-log warnings
			n++
		}
	}
	if n != 3 {
		t.Errorf("RUNS=2 sent the scenario %d times, want 3 (one warm-up + 2 measured)", n)
	}
}

type benchSummary struct {
	Metrics struct {
		HTTPReqs struct {
			Rate float64 `json:"rate"`
		} `json:"http_reqs"`
		Duration struct {
			P95 float64 `json:"p(95)"`
		} `json:"http_req_duration"`
	} `json:"metrics"`
}

func median3(v []float64) float64 {
	s := append([]float64(nil), v...)
	sort.Float64s(s)
	return s[len(s)/2]
}

// Without RUNS, each scenario gets 3 measured runs, and the printed numbers
// are the medians of those runs' summaries. orders.js passes its own checks
// (2xx, no X-Degraded) at this load.
func TestShouldPrintTheMedianOfThreeRunsByDefault(t *testing.T) {
	recreateGatewayAfterBench(t)
	results := filepath.Join(repoRoot, "loadtest", "results")
	for _, s := range []string{"mixed", "orders"} {
		before, _ := filepath.Glob(filepath.Join(results, s+"-*.json"))
		out, err := bench(t, "SCENARIOS="+s, "DURATION=2s", "VUS=10")
		if err != nil {
			t.Fatalf("make bench %s: %v\n%s", s, err, out)
		}
		after, _ := filepath.Glob(filepath.Join(results, s+"-*.json"))
		seen := map[string]bool{}
		for _, p := range before {
			seen[p] = true
		}
		var rps, p95 []float64
		for _, p := range after {
			if seen[p] {
				continue
			}
			b, err := os.ReadFile(p)
			if err != nil {
				t.Fatal(err)
			}
			var sum benchSummary
			if err := json.Unmarshal(b, &sum); err != nil {
				t.Fatalf("%s: %v", p, err)
			}
			rps = append(rps, sum.Metrics.HTTPReqs.Rate)
			p95 = append(p95, sum.Metrics.Duration.P95)
		}
		if len(rps) != 3 {
			t.Fatalf("make bench %s without RUNS wrote %d summaries, want 3", s, len(rps))
		}
		m := regexp.MustCompile(`(?m)^` + s + `\s+(\S+)\s+(\S+)\s+(\S+)\s*$`).FindStringSubmatch(out)
		if m == nil {
			t.Fatalf("make bench %s printed no summary line:\n%s", s, out)
		}
		if want := fmt.Sprintf("%.0f", median3(rps)); m[1] != want {
			t.Errorf("%s median_rps = %s, want %s (median of %v)", s, m[1], want, rps)
		}
		if want := fmt.Sprintf("%.2f", median3(p95)); m[2] != want {
			t.Errorf("%s p95_ms = %s, want %s (median of %v)", s, m[2], want, p95)
		}
		if m[3] != "0" {
			t.Errorf("%s failed = %s, want 0", s, m[3])
		}
	}
}
