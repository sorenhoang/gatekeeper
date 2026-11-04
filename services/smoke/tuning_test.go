//go:build smoke

package smoke

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// The README's tuning table decides the config: a kept candidate's directive
// is in the config, a rejected one's is not. Every candidate file in
// loadtest/candidates/ has a row, so none is applied or dropped silently.
func TestShouldConfigureExactlyTheMeasuredKeptCandidates(t *testing.T) {
	readme, err := os.ReadFile(filepath.Join(repoRoot, "README.md"))
	if err != nil {
		t.Fatal(err)
	}
	rows := regexp.MustCompile("(?m)^\\| `([a-z0-9-]+)` \\|.*\\| (kept|rejected) \\|$").FindAllStringSubmatch(string(readme), -1)
	verdict := map[string]string{}
	for _, r := range rows {
		verdict[r[1]] = r[2]
	}
	cands, _ := filepath.Glob(filepath.Join(repoRoot, "loadtest", "candidates", "*.env"))
	if len(cands) == 0 {
		t.Fatal("no candidates in loadtest/candidates/")
	}
	for _, c := range cands {
		name := strings.TrimSuffix(filepath.Base(c), ".env")
		v, ok := verdict[name]
		if !ok {
			t.Errorf("README tuning table has no kept/rejected row for candidate %s", name)
			continue
		}
		spec := map[string]string{}
		b, _ := os.ReadFile(c)
		for _, l := range strings.Split(string(b), "\n") {
			if k, val, ok := strings.Cut(l, "="); ok && !strings.HasPrefix(l, "#") {
				spec[k] = strings.ReplaceAll(val, `\n`, "\n")
			}
		}
		conf, err := os.ReadFile(filepath.Join(repoRoot, spec["FILE"]))
		if err != nil {
			t.Fatal(err)
		}
		has := strings.Contains(string(conf), spec["TO"])
		if v == "kept" && !has {
			t.Errorf("candidate %s is kept in the README but %s lacks it", name, spec["FILE"])
		}
		if v == "rejected" && has {
			t.Errorf("candidate %s is rejected in the README but %s has it", name, spec["FILE"])
		}
	}
	// worker_rlimit_nofile isn't needed: the container already allows ~1M
	// open files (measured in plan).
	if conf, _ := os.ReadFile(filepath.Join(repoRoot, "nginx", "nginx.conf")); strings.Contains(string(conf), "worker_rlimit_nofile") {
		t.Error("nginx.conf sets worker_rlimit_nofile; the container's limit (1048576) already covers 12 × 1024 connections")
	}
}
