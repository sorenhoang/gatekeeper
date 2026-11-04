//go:build smoke && lifecycle

package smoke

import (
	"os"
	"os/exec"
	"regexp"
	"strings"
	"testing"
)

// The same k6 scenarios run against Envoy from the compose network (login
// still goes through the nginx gateway: Envoy has no /api/auth/ route).
func TestShouldBenchEnvoyWithTheSameScenarios(t *testing.T) {
	recreateGatewayAfterBench(t)
	// The load must reach Envoy, not the nginx gateway: count Envoy's access
	// log lines for the scenario's products path before and after.
	envoyHits := func() int { return strings.Count(mustCompose(t, "logs", "envoy"), "/api/products/bench") }
	before := envoyHits()
	out, err := bench(t, "TARGET=envoy", "SCENARIOS=mixed", "RUNS=1", "DURATION=3s")
	if err != nil {
		t.Fatalf("make bench TARGET=envoy: %v\n%s", err, out)
	}
	if n := envoyHits() - before; n < 100 {
		t.Errorf("Envoy logged %d /api/products/bench requests during make bench TARGET=envoy, want the load (≥100)", n)
	}
	if !regexp.MustCompile(`(?m)^mixed\s+[0-9.]+\s+[0-9.]+\s+0$`).MatchString(out) {
		t.Errorf("make bench TARGET=envoy printed no `mixed <rps> <p95> 0` line:\n%s", out)
	}
}

// bench-gateways interleaves nginx and Envoy runs and prints one median line
// per gateway.
func TestShouldCompareTheGatewaysInterleaved(t *testing.T) {
	recreateGatewayAfterBench(t)
	cmd := exec.Command("make", "-C", repoRoot, "bench-gateways")
	cmd.Env = append(os.Environ(), nestedEnv+"=1", "SCENARIOS=mixed", "RUNS=2", "DURATION=2s", "VUS=10")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("make bench-gateways: %v\n%s", err, out)
	}
	for _, gw := range []string{"gateway", "envoy"} {
		if !regexp.MustCompile(`(?m)^mixed ` + gw + ` [0-9]+ req/s p95 [0-9.]+ms failed 0$`).Match(out) {
			t.Errorf("make bench-gateways printed no `mixed %s <rps> req/s p95 <ms>ms failed 0` line:\n%s", gw, out)
		}
	}
}
