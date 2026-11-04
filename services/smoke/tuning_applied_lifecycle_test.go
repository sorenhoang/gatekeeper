//go:build smoke && lifecycle

package smoke

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// A kept candidate is already in the config: comparing it again would
// measure tuned against tuned, so bench-compare refuses before any run and
// leaves the kept tuning in place.
func TestShouldRefuseToCompareAnAppliedCandidate(t *testing.T) {
	conf := filepath.Join(repoRoot, "nginx", "nginx.conf")
	before, _ := os.ReadFile(conf)
	if !bytes.Contains(before, []byte("open_file_cache max=1000")) {
		t.Fatal("open_file_cache isn't in nginx.conf; this test needs the kept candidate applied")
	}
	cmd := exec.Command("make", "-C", repoRoot, "bench-compare")
	cmd.Env = append(os.Environ(), nestedEnv+"=1", "CANDIDATE=open-file-cache", "SCENARIOS=static", "RUNS=1", "DURATION=1s")
	out, err := cmd.CombinedOutput()
	if err == nil || !bytes.Contains(out, []byte("already applied")) {
		t.Errorf("bench-compare of an applied candidate → err %v, want a refusal naming it already applied:\n%s", err, out)
	}
	if after, _ := os.ReadFile(conf); !bytes.Equal(before, after) {
		t.Error("nginx.conf changed after bench-compare refused an applied candidate")
	}
}
