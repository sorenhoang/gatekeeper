//go:build smoke && lifecycle

package smoke

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"testing"
)

// bench-compare interleaves baseline and candidate runs, prints both medians
// and the change, and always leaves the config as it found it.
func TestShouldCompareACandidateAndRevertIt(t *testing.T) {
	recreateGatewayAfterBench(t)
	conf := filepath.Join(repoRoot, "nginx", "nginx.conf")
	before, _ := os.ReadFile(conf)
	cmd := exec.Command("make", "-C", repoRoot, "bench-compare")
	cmd.Env = append(os.Environ(), nestedEnv+"=1", "CANDIDATE=keepalive-requests", "SCENARIOS=mixed", "RUNS=1", "DURATION=2s", "VUS=10")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("make bench-compare: %v\n%s", err, out)
	}
	if !regexp.MustCompile(`(?m)^mixed keepalive-requests baseline [0-9]+ req/s p95 [0-9.]+ms failed 0 \| tuned [0-9]+ req/s p95 [0-9.]+ms failed 0 \| rps [+-][0-9.]+% p95 [+-][0-9.]+%$`).Match(out) {
		t.Errorf("bench-compare output lacks the comparison line:\n%s", out)
	}
	if after, _ := os.ReadFile(conf); !bytes.Equal(before, after) {
		t.Error("nginx.conf differs after bench-compare; the candidate must always be reverted")
	}
}
