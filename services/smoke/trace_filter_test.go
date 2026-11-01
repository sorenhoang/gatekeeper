//go:build smoke

package smoke

import (
	"net/http"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func TestShouldPrintExactlyNothingForAnUnknownRequestID(t *testing.T) {
	out, err := makeTrace(t, runID("never-sent"))
	if err != nil {
		t.Fatalf("make trace with an unknown id: %v\n%s", err, out)
	}
	if strings.TrimSpace(out) != "" {
		t.Errorf("make trace printed output for an id never sent, want none:\n%s", out)
	}
}

func TestShouldPrintOnlyTheTracedRequest(t *testing.T) {
	traced, other := runID("traced"), runID("other")
	withIDs(t, http.MethodGet, "", "/api/users/", nil, traced)
	withIDs(t, http.MethodGet, "", "/api/users/", nil, other)
	time.Sleep(500 * time.Millisecond)

	out, err := makeTrace(t, traced)
	if err != nil {
		t.Fatalf("make trace: %v\n%s", err, out)
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) < 2 {
		t.Fatalf("make trace printed %d lines, want the gateway and the service line:\n%s", len(lines), out)
	}
	for _, l := range lines {
		if !strings.Contains(l, traced) {
			t.Errorf("make trace printed a line without the traced id: %s", l)
		}
		if strings.Contains(l, other) {
			t.Errorf("make trace printed the other request: %s", l)
		}
	}
}

// "Nothing found" must not hide "could not read the logs at all".
func TestShouldFailWhenTheLogsCannotBeRead(t *testing.T) {
	cmd := exec.Command("make", "-s", "-C", repoRoot, "trace", "ID="+runID("nodocker"))
	cmd.Env = append(os.Environ(), nestedEnv+"=1", "DOCKER_HOST=unix:///nonexistent/docker.sock")
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Errorf("make trace exited 0 although docker was unreachable:\n%s", out)
	}
}
