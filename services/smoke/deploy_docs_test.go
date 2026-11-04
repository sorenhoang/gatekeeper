//go:build smoke

package smoke

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// The README explains the zero-downtime operations, including what still
// causes downtime, and the roadmap shows Phase 9 done and Phase 10 underway.
func TestShouldDocumentZeroDowntimeOperations(t *testing.T) {
	b, err := os.ReadFile(filepath.Join(repoRoot, "README.md"))
	if err != nil {
		t.Fatal(err)
	}
	readme := string(b)
	_, section, ok := strings.Cut(readme, "## Performance & zero-downtime")
	if !ok {
		t.Fatal(`README has no "## Performance & zero-downtime" section`)
	}
	section, _, _ = strings.Cut(section, "\n## ")
	for _, want := range []string{"### Zero-downtime", "make reload", "make deploy SERVICE=order-service", "down", "worker_shutdown_timeout", "recreating the gateway"} {
		if !strings.Contains(section, want) {
			t.Errorf("README performance section doesn't mention %q", want)
		}
	}
	for _, row := range []string{`(?m)^\| 9 \|.*\| ☑ \|$`, `(?m)^\| 10 \|.*\| ◐ \|$`} {
		if !regexp.MustCompile(row).MatchString(readme) {
			t.Errorf("roadmap lacks a row matching %s", row)
		}
	}
	mk, _ := os.ReadFile(filepath.Join(repoRoot, "Makefile"))
	if !regexp.MustCompile(`(?m)^deploy:`).Match(mk) {
		t.Error("Makefile has no deploy target")
	}
}
