//go:build smoke && lifecycle

package smoke

import (
	"os/exec"
	"strings"
	"testing"
	"time"
)

// SIGTERM with a stream open: the service must end it and exit cleanly well
// inside docker's 10s window, not wait out its HTTP drain and fail.
func TestShouldStopRealtimeServiceCleanlyWithAStreamOpen(t *testing.T) {
	restoreStack(t)
	mustCompose(t, "exec", "-d", "gateway", "wget", "-qO", "/dev/null", "http://realtime-service:8080/events")
	time.Sleep(time.Second)
	start := time.Now()
	mustCompose(t, "stop", "realtime-service")
	if took := time.Since(start); took > 5*time.Second {
		t.Errorf("docker compose stop realtime-service with an SSE stream open took %v, want under 5s", took)
	}
	id := strings.TrimSpace(mustCompose(t, "ps", "-a", "-q", "realtime-service"))
	out, err := exec.Command("docker", "inspect", "-f", "{{.State.ExitCode}}", id).CombinedOutput()
	if err != nil || strings.TrimSpace(string(out)) != "0" {
		t.Errorf("realtime-service exit code %q (err %v), want 0", strings.TrimSpace(string(out)), err)
	}
}
