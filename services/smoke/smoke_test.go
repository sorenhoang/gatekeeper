//go:build smoke

// Package smoke checks the running compose stack. Start it with `make up`;
// run with `make test` (go test -tags smoke ./smoke/).
package smoke

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

var services = []string{"user-service", "order-service", "product-service", "auth-service"}

const repoRoot = "../.."

func TestMain(m *testing.M) {
	out, err := exec.Command("docker", "compose", "--project-directory", repoRoot,
		"ps", "--status", "running", "-q", "gateway").Output()
	if err != nil || strings.TrimSpace(string(out)) == "" {
		fmt.Fprintln(os.Stderr, "smoke: gateway is not running; run `make up` first")
		os.Exit(1)
	}
	os.Exit(m.Run())
}

// compose runs `docker compose <args>` from the repo root and returns combined output.
func compose(t *testing.T, args ...string) (string, error) {
	t.Helper()
	cmd := exec.Command("docker", append([]string{"compose", "--project-directory", repoRoot}, args...)...)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func mustCompose(t *testing.T, args ...string) string {
	t.Helper()
	out, err := compose(t, args...)
	if err != nil {
		t.Fatalf("docker compose %v: %v\n%s", args, err, out)
	}
	return out
}

// fromGateway fetches url from inside the gateway container, i.e. over the compose network.
func fromGateway(t *testing.T, url string) (string, error) {
	t.Helper()
	return compose(t, "exec", "-T", "gateway", "wget", "-qO-", "-T", "3", url)
}

func waitHealthy(t *testing.T, svc string) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := fromGateway(t, "http://"+svc+":8080/health"); err == nil {
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatalf("%s did not become healthy", svc)
}

func TestShouldBuildAllFourServiceBinaries(t *testing.T) {
	dir := t.TempDir()
	cmd := exec.Command("go", "build", "-o", dir+string(os.PathSeparator), "./cmd/...")
	cmd.Dir = ".."
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("go build ./cmd/...: %v\n%s", err, out)
	}
	for _, svc := range services {
		if _, err := os.Stat(filepath.Join(dir, svc)); err != nil {
			t.Errorf("binary %s not built: %v", svc, err)
		}
	}
}

func TestShouldAnswerHealthFromGatewayForEveryService(t *testing.T) {
	for _, svc := range services {
		body, err := fromGateway(t, "http://"+svc+":8080/health")
		if err != nil {
			t.Errorf("%s /health from gateway: %v\n%s", svc, err, body)
			continue
		}
		if !strings.Contains(body, `"status":"ok"`) {
			t.Errorf("%s /health body = %q, want status ok", svc, body)
		}
	}
}

func TestShouldReportOwnServiceNameInEcho(t *testing.T) {
	for _, svc := range services {
		body, err := fromGateway(t, "http://"+svc+":8080/whoami")
		if err != nil {
			t.Errorf("%s echo from gateway: %v\n%s", svc, err, body)
			continue
		}
		var echo struct {
			Service string `json:"service"`
			Path    string `json:"path"`
		}
		if err := json.Unmarshal([]byte(body), &echo); err != nil {
			t.Errorf("%s echo is not JSON: %v; body %q", svc, err, body)
			continue
		}
		if echo.Service != svc || echo.Path != "/whoami" {
			t.Errorf("%s echo = %+v, want service=%s path=/whoami", svc, echo, svc)
		}
	}
}

func TestShouldNotPublishAnyServicePortToTheHost(t *testing.T) {
	for _, svc := range services {
		if out, err := compose(t, "port", svc, "8080"); err == nil && strings.TrimSpace(out) != "" && !strings.HasSuffix(strings.TrimSpace(out), ":0") {
			t.Errorf("%s publishes 8080 to the host at %q; only the gateway may", svc, strings.TrimSpace(out))
		}
	}
	out := mustCompose(t, "port", "gateway", "80")
	if !strings.HasSuffix(strings.TrimSpace(out), ":8080") {
		t.Errorf("gateway port 80 published at %q, want host :8080", strings.TrimSpace(out))
	}
}

func TestShouldStopWithinTwoSecondsOnSigterm(t *testing.T) {
	svc := "user-service"
	t.Cleanup(func() {
		mustCompose(t, "start", svc)
		waitHealthy(t, svc)
	})

	start := time.Now()
	mustCompose(t, "stop", svc)
	took := time.Since(start)

	if took >= 2*time.Second {
		t.Errorf("docker compose stop %s took %v, want < 2s (SIGTERM not handled?)", svc, took)
	}
}

func TestShouldExitNonZeroWhenChaosConfigIsInvalid(t *testing.T) {
	out, err := compose(t, "run", "--rm", "--no-deps", "-e", "FAIL_RATE=abc", "user-service")
	exitErr, ok := err.(*exec.ExitError)
	if !ok || exitErr.ExitCode() == 0 {
		t.Fatalf("service with FAIL_RATE=abc: err=%v, want non-zero exit\n%s", err, out)
	}
	if !strings.Contains(out, "FAIL_RATE") {
		t.Errorf("startup error %q does not name FAIL_RATE", out)
	}
}
