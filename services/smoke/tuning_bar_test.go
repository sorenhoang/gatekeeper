//go:build smoke

package smoke

import (
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// The verdicts follow the bar: kept means at least 10% more req/s or 10%
// lower p95, rejected means neither. Every row carries its numbers.
func TestShouldKeepOnlyCandidatesThatClearTheTenPercentBar(t *testing.T) {
	readme, err := os.ReadFile(filepath.Join(repoRoot, "README.md"))
	if err != nil {
		t.Fatal(err)
	}
	// | `name` | scenario | baseline | tuned | +4.2% req/s, −7.2% p95 | verdict |
	rows := regexp.MustCompile("(?m)^\\| `([a-z0-9-]+)` \\|[^|]+\\|[^|]+\\|[^|]+\\| ([+−-][0-9.]+)% req/s, ([+−-][0-9.]+)% p95 \\| (kept|rejected) \\|$").FindAllStringSubmatch(string(readme), -1)
	cands, _ := filepath.Glob(filepath.Join(repoRoot, "loadtest", "candidates", "*.env"))
	if len(rows) != len(cands) || len(rows) == 0 {
		t.Fatalf("README tuning table has %d rows with numbers, want one per candidate (%d)", len(rows), len(cands))
	}
	for _, r := range rows {
		rps, _ := strconv.ParseFloat(strings.Replace(r[2], "−", "-", 1), 64)
		p95, _ := strconv.ParseFloat(strings.Replace(r[3], "−", "-", 1), 64)
		passes := rps >= 10 || p95 <= -10
		if r[4] == "kept" && !passes {
			t.Errorf("candidate %s is kept with %s%% req/s and %s%% p95, under the 10%% bar", r[1], r[2], r[3])
		}
		if r[4] == "rejected" && passes {
			t.Errorf("candidate %s is rejected with %s%% req/s and %s%% p95, which clears the 10%% bar", r[1], r[2], r[3])
		}
	}
}
