//go:build smoke && lifecycle

package smoke

import (
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
