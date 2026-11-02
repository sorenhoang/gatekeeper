//go:build smoke && lifecycle

package smoke

import (
	"os/exec"
	"strings"
	"testing"
)

// These tests stop or recreate the stack, so plain `make test` leaves them out;
// `make test-lifecycle` runs them on top of the default suite.

// restoreStack runs `make up` however the test ended, so a failure never
// leaves the stack down for the next command.
func restoreStack(t *testing.T) {
	t.Helper()
	t.Cleanup(func() {
		if out, err := runMake(t, "up"); err != nil {
			t.Errorf("restoring the stack with make up: %v\n%s", err, out)
			return
		}
		clearChaos(t)
		waitGatewayServing(t)
	})
}

func TestShouldMakeTestPassWithStackUpAndFailWithGatewayStopped(t *testing.T) {
	skipIfNested(t)
	restoreStack(t)
	if out, err := runMake(t, "test"); err != nil {
		t.Fatalf("make test with the stack up: %v\n%s", err, out)
	}

	mustCompose(t, "stop", "gateway")
	out, err := runMake(t, "test")
	if err == nil {
		t.Fatalf("make test with the gateway stopped exited 0, want non-zero\n%s", out)
	}
	// Fails for the right reason, not e.g. a compile error.
	if !strings.Contains(out, "gateway is not running") {
		t.Errorf("make test with the gateway stopped failed without naming it:\n%s", out)
	}
}

func TestShouldMakeDownRemoveAllContainersAndMakeUpStartAllFive(t *testing.T) {
	skipIfNested(t)
	restoreStack(t)
	if out, err := runMake(t, "down"); err != nil {
		t.Fatalf("make down: %v\n%s", err, out)
	}
	if left := strings.TrimSpace(mustCompose(t, "ps", "-a", "-q")); left != "" {
		t.Errorf("containers left after make down:\n%s", left)
	}

	if out, err := runMake(t, "up"); err != nil {
		t.Fatalf("make up: %v\n%s", err, out)
	}
	running := " " + strings.Join(strings.Fields(mustCompose(t, "ps", "--status", "running", "--services")), " ") + " "
	for _, svc := range append([]string{"gateway"}, services...) {
		if !strings.Contains(running, " "+svc+" ") {
			t.Errorf("%s not running after make up (running:%s)", svc, running)
		}
	}
	waitGatewayServing(t)
}

// clearChaos recreates any service still carrying a DELAY_MS/FAIL_RATE value.
// `make up` normally does this, but a slowed service once survived it in the
// full suite and broke the next test; this makes the restore certain.
func clearChaos(t *testing.T) {
	t.Helper()
	ids := strings.Fields(mustCompose(t, "ps", "-q"))
	var dirty []string
	for _, id := range ids {
		out, err := exec.Command("docker", "inspect", "--format",
			`{{index .Config.Labels "com.docker.compose.service"}} {{join .Config.Env " "}}`, id).Output()
		if err != nil {
			t.Errorf("docker inspect %s: %v", id, err)
			continue
		}
		f := strings.Fields(string(out))
		if len(f) < 2 {
			continue
		}
		for _, kv := range f[1:] {
			if strings.HasPrefix(kv, "DELAY_MS=") || strings.HasPrefix(kv, "FAIL_RATE=") {
				dirty = append(dirty, f[0])
				break
			}
		}
	}
	if len(dirty) == 0 {
		return
	}
	t.Logf("clearing leftover chaos env from %v", dirty)
	mustCompose(t, append([]string{"up", "-d", "--no-deps", "--force-recreate", "--wait"}, dirty...)...)
	if out, err := nginxCmd(t, "-s", "reload"); err != nil {
		t.Errorf("reload after clearing chaos: %v\n%s", err, out)
	}
}
