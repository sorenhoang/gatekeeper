//go:build smoke

package smoke

import (
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func makeTrace(t *testing.T, id string) (string, error) {
	t.Helper()
	cmd := exec.Command("make", "-s", "-C", repoRoot, "trace", "ID="+id)
	cmd.Env = append(os.Environ(), nestedEnv+"=1")
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func TestShouldTraceARequestAcrossGatewayAndServiceLogs(t *testing.T) {
	id := runID("trace")
	if r := withIDs(t, http.MethodGet, "", "/api/users/", nil, id); r.status != http.StatusOK {
		t.Fatalf("GET /api/users/ = %d", r.status)
	}
	var out string
	for deadline := time.Now().Add(3 * time.Second); time.Now().Before(deadline); time.Sleep(300 * time.Millisecond) {
		var err error
		out, err = makeTrace(t, id)
		if err != nil {
			t.Fatalf("make trace ID=%s: %v\n%s", id, err, out)
		}
		if strings.Contains(out, `"request_id":"`+id+`"`) && strings.Contains(out, "request_id="+id) {
			break
		}
	}
	if !strings.Contains(out, `"request_id":"`+id+`"`) {
		t.Errorf("make trace output lacks the gateway JSON line for %s:\n%s", id, out)
	}
	if !strings.Contains(out, "service=user-service") || !strings.Contains(out, "request_id="+id) {
		t.Errorf("make trace output lacks the user-service line for %s:\n%s", id, out)
	}
}

func TestShouldPrintNothingForAnUnknownRequestID(t *testing.T) {
	id := runID("never-sent")
	out, err := makeTrace(t, id)
	if err != nil {
		t.Fatalf("make trace with an unknown id exited non-zero: %v\n%s", err, out)
	}
	for _, l := range strings.Split(out, "\n") {
		if strings.Contains(l, id) {
			t.Errorf("make trace printed a line for an id never sent: %s", l)
		}
	}
}

func TestShouldDocumentObservabilityInTheReadme(t *testing.T) {
	b, err := os.ReadFile(filepath.Join(repoRoot, "GUIDE.md"))
	if err != nil {
		t.Fatal(err)
	}
	readme := string(b)
	start := strings.Index(readme, "\n## Observability")
	if start < 0 {
		t.Fatal(`README has no "## Observability" section`)
	}
	section := readme[start+1:]
	if end := strings.Index(section[3:], "\n## "); end >= 0 {
		section = section[:end+3]
	}
	for _, topic := range append(append([]string{}, accessLogFields...),
		"make trace", "make status", "stub_status", "X-Request-ID", "[A-Za-z0-9._-]") {
		if !strings.Contains(section, topic) {
			t.Errorf("README Observability section does not cover %q", topic)
		}
	}
}
