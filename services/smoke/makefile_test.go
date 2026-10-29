//go:build smoke

package smoke

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// `make test` runs this suite, so the tests below that call `make test` set
// this in the child to stop it from recursing into itself.
const nestedEnv = "GATEKEEPER_SMOKE_NESTED"

func skipIfNested(t *testing.T) {
	t.Helper()
	if os.Getenv(nestedEnv) == "1" {
		t.Skip("running inside `make test` from a smoke test")
	}
}

func makeCmd(ctx context.Context, target string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, "make", "-C", repoRoot, target)
	cmd.Env = append(os.Environ(), nestedEnv+"=1")
	return cmd
}

func runMake(t *testing.T, target string) (string, error) {
	t.Helper()
	out, err := makeCmd(context.Background(), target).CombinedOutput()
	return string(out), err
}

func sighupCount(t *testing.T) int {
	t.Helper()
	return strings.Count(mustCompose(t, "logs", "--no-log-prefix", "gateway"), "SIGHUP")
}

func waitGatewayServing(t *testing.T) {
	t.Helper()
	client := &http.Client{Timeout: time.Second}
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if resp, err := client.Get(gatewayURL + "/"); err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return
			}
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatalf("gateway not serving %s/ after 15s", gatewayURL)
}

func TestShouldMakeReloadApplyValidConfig(t *testing.T) {
	skipIfNested(t)
	before := sighupCount(t)
	marker := fmt.Sprintf("make-reload-%d", time.Now().UnixNano())
	writeConf(t, "zz-smoke-make-reload.conf", fmt.Sprintf(
		"server {\n  listen 80;\n  server_name make-reload.smoke.test;\n  return 200 %q;\n}\n", marker))

	if out, err := runMake(t, "reload"); err != nil {
		t.Fatalf("make reload with valid config: %v\n%s", err, out)
	}

	var body string
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, body = get(t, "/", "make-reload.smoke.test"); body == marker {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if body != marker {
		t.Errorf("after make reload, Host make-reload.smoke.test got %q, want %q", body, marker)
	}
	if sighupCount(t) == before {
		t.Errorf("make reload sent no SIGHUP to the master")
	}
}

func TestShouldMakeReloadFailAtConfigTestWithoutSignalling(t *testing.T) {
	skipIfNested(t)
	before := sighupCount(t)
	writeConf(t, "zz-smoke-make-broken.conf", "this_is_not_a_directive on;\n")

	out, err := runMake(t, "reload")
	if err == nil {
		t.Fatalf("make reload with invalid config exited 0, want non-zero\n%s", out)
	}
	if !strings.Contains(out, "[emerg]") || !strings.Contains(out, "test failed") {
		t.Errorf("make reload output does not show nginx -t failing:\n%s", out)
	}

	time.Sleep(500 * time.Millisecond)
	if after := sighupCount(t); after != before {
		t.Errorf("make reload signalled the master (%d → %d SIGHUP log lines) despite nginx -t failing", before, after)
	}
}

func TestShouldMakeLogsTailGatewayAccessLog(t *testing.T) {
	skipIfNested(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var out bytes.Buffer
	cmd := makeCmd(ctx, "logs")
	cmd.Stdout, cmd.Stderr = &out, &out
	// Killing only make orphans its `docker compose logs -f`, which keeps the
	// stdout pipe open and blocks Wait forever: kill the whole process group.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.WaitDelay = 2 * time.Second
	if err := cmd.Start(); err != nil {
		t.Fatalf("make logs: %v", err)
	}
	defer func() {
		cancel()
		cmd.Wait()
	}()

	// Requested after `make logs` started, so seeing it proves it follows.
	time.Sleep(time.Second)
	marker := fmt.Sprintf("/make-logs-%d", time.Now().UnixNano())
	get(t, marker, "")

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && !strings.Contains(out.String(), marker) {
		time.Sleep(100 * time.Millisecond)
	}
	if !strings.Contains(out.String(), "GET "+marker) {
		t.Errorf("make logs did not show the gateway access line for %s:\n%s", marker, out.String())
	}
}

func TestShouldReadmeShowLayoutCAndOnlyRealMakeTargets(t *testing.T) {
	b, err := os.ReadFile(filepath.Join(repoRoot, "README.md"))
	if err != nil {
		t.Fatal(err)
	}
	readme := string(b)
	for _, entry := range []string{"go.mod", "internal/", "echo/", "cmd/", "user-service/", "auth-service/", "Dockerfile", "smoke/", "conf.d/", "frontend/", "Makefile"} {
		if !strings.Contains(readme, entry) {
			t.Errorf("README tree is missing %q", entry)
		}
	}
	if strings.Contains(readme, "Available after Phase 0") {
		t.Errorf("README still says Getting started is not available yet")
	}

	start := strings.Index(readme, "## Getting started")
	if start < 0 {
		t.Fatal("README has no Getting started section")
	}
	section := readme[start:]
	if end := strings.Index(section[len("## "):], "\n## "); end >= 0 {
		section = section[:end+len("## ")]
	}
	var targets int
	for _, line := range strings.Split(section, "\n") {
		f := strings.Fields(line)
		if len(f) < 2 || f[0] != "make" {
			continue
		}
		targets++
		// -n: the target exists and has a recipe, without running it.
		if out, err := exec.Command("make", "-C", repoRoot, "-n", f[1]).CombinedOutput(); err != nil {
			t.Errorf("README Getting started runs `make %s`, which make rejects: %v\n%s", f[1], err, out)
		}
	}
	if targets < 5 {
		t.Errorf("README Getting started lists %d make commands, want up, test, reload, logs, down", targets)
	}
}

func TestShouldKeepStackCyclingTestsOutOfPlainMakeTest(t *testing.T) {
	dry := func(target string) string {
		out, err := exec.Command("make", "-C", repoRoot, "-n", target).CombinedOutput()
		if err != nil {
			t.Fatalf("make -n %s: %v\n%s", target, err, out)
		}
		return string(out)
	}
	if plain := dry("test"); strings.Contains(plain, "lifecycle") {
		t.Errorf("make test runs the lifecycle tests, want them opt-in only:\n%s", plain)
	}
	if full := dry("test-lifecycle"); !strings.Contains(full, "smoke,lifecycle") {
		t.Errorf("make test-lifecycle does not build with tags smoke,lifecycle:\n%s", full)
	}
}
