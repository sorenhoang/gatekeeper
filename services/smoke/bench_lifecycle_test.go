//go:build smoke && lifecycle

package smoke

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"testing"
	"time"
)

// A short bench: the tool runs k6 from the compose network and reports a
// median and a p95 with no failures. Numbers themselves are never asserted
// (they depend on the machine); the README records them.
func TestShouldBenchTheGatewayFromTheComposeNetwork(t *testing.T) {
	recreateGatewayAfterBench(t)
	results := filepath.Join(repoRoot, "loadtest", "results")
	before, _ := filepath.Glob(filepath.Join(results, "*.json"))
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "make", "-C", repoRoot, "bench")
	cmd.Env = append(os.Environ(), nestedEnv+"=1", "SCENARIOS=mixed", "RUNS=1", "DURATION=5s")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("make bench: %v\n%s", err, out)
	}
	m := regexp.MustCompile(`(?m)^mixed\s+([0-9.]+)\s+([0-9.]+)\s+([0-9]+)\s*$`).FindSubmatch(out)
	if m == nil {
		t.Fatalf("make bench printed no `mixed <median_rps> <p95_ms> <failed>` line:\n%s", out)
	}
	if rps, _ := strconv.ParseFloat(string(m[1]), 64); rps <= 0 {
		t.Errorf("median_rps = %s, want > 0", m[1])
	}
	if string(m[3]) != "0" {
		t.Errorf("failed = %s requests at the default load, want 0", m[3])
	}
	after, _ := filepath.Glob(filepath.Join(results, "*.json"))
	if len(after) <= len(before) {
		t.Errorf("make bench wrote no new JSON summary in loadtest/results/ (%d before, %d after)", len(before), len(after))
	}
}

// The scripts ship with the repo and read the lab user from the secrets.
func TestShouldShipTheLoadTestScripts(t *testing.T) {
	for _, f := range []string{"mixed.js", "orders.js"} {
		b, err := os.ReadFile(filepath.Join(repoRoot, "loadtest", f))
		if err != nil {
			t.Errorf("loadtest/%s: %v", f, err)
			continue
		}
		if !regexp.MustCompile(`/secrets/users\.json`).Match(b) {
			t.Errorf("loadtest/%s doesn't read the lab user from the mounted secrets", f)
		}
	}
}
