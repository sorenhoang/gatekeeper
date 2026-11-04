//go:build smoke

package smoke

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func readmeSection(t *testing.T, heading string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(repoRoot, "GUIDE.md"))
	if err != nil {
		t.Fatal(err)
	}
	_, s, ok := strings.Cut(string(b), "\n"+heading+"\n")
	if !ok {
		t.Fatalf("README has no %q section", heading)
	}
	s, _, _ = strings.Cut(s, "\n## ")
	return s
}

func TestShouldCompareNginxAndEnvoyWithNumbers(t *testing.T) {
	s := readmeSection(t, "## Nginx vs Envoy")
	for _, scenario := range []string{"mixed", "orders"} {
		row := `(?m)^\| ` + scenario + ` \| [0-9]+ req/s, p95 [0-9.]+ms \| [0-9]+ req/s, p95 [0-9.]+ms \|`
		if !regexp.MustCompile(row).MatchString(s) {
			t.Errorf("Nginx vs Envoy has no %s row with req/s and p95 for both gateways", scenario)
		}
	}
	for _, diff := range []string{"cache", "rate limit", "error pages", "verdict cache", "admin", "interleaved"} {
		if !strings.Contains(strings.ToLower(s), diff) {
			t.Errorf("Nginx vs Envoy doesn't mention %q", diff)
		}
	}
}

func TestShouldMapGatekeeperToKubernetesOnPaper(t *testing.T) {
	s := readmeSection(t, "## Kubernetes")
	for _, want := range []string{"Ingress", "Gateway API", "HTTPRoute", "ingress-nginx", "retired"} {
		if !strings.Contains(s, want) {
			t.Errorf("Kubernetes section doesn't mention %q", want)
		}
	}
	manifests, _ := filepath.Glob(filepath.Join(repoRoot, "k8s", "*.yaml"))
	all := ""
	for _, m := range manifests {
		b, _ := os.ReadFile(m)
		all += string(b)
	}
	for _, kind := range []string{"kind: Ingress", "kind: Gateway", "kind: HTTPRoute"} {
		if !strings.Contains(all, kind) {
			t.Errorf("k8s/*.yaml has no %q example", kind)
		}
	}
	// Nothing here may talk to a cluster: the default kube context can be a
	// real one.
	files, _ := filepath.Glob(filepath.Join(repoRoot, "scripts", "*"))
	files = append(files, filepath.Join(repoRoot, "Makefile"))
	for _, f := range files {
		b, _ := os.ReadFile(f)
		if regexp.MustCompile(`\b(kubectl|helm)\b`).Match(b) {
			t.Errorf("%s runs kubectl or helm", filepath.Base(f))
		}
	}
}

func TestShouldCloseTheLabWithWhereToGoNext(t *testing.T) {
	if s := readmeSection(t, "## Where to go next"); len(strings.TrimSpace(s)) < 200 {
		t.Error("README \"Where to go next\" is missing or nearly empty")
	}
}
