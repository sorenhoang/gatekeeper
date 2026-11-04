//go:build smoke && lifecycle

package smoke

import (
	"context"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

// Setup: a bench writes hundreds of thousands of access-log lines; tests that
// read the gateway's log afterwards need a fresh container.
func recreateGatewayAfterBench(t *testing.T) {
	t.Helper()
	t.Cleanup(func() {
		mustCompose(t, "up", "-d", "--no-deps", "--force-recreate", "--wait", "gateway")
		waitGatewayServing(t)
		// Docker Desktop brings IPv6 localhost forwarding back later than
		// IPv4; tests that burst new connections to localhost need both.
		for _, addr := range []string{"[::1]:8443", "127.0.0.1:8443"} {
			waitAnswersOn(t, addr)
		}
	})
}

// waitAnswersOn polls /health through one address family until it answers.
func waitAnswersOn(t *testing.T, addr string) {
	t.Helper()
	tr := &http.Transport{TLSClientConfig: labTLS.Clone(), DisableKeepAlives: true,
		DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, network, addr)
		}}
	c := &http.Client{Timeout: time.Second, Transport: tr}
	for deadline := time.Now().Add(15 * time.Second); time.Now().Before(deadline); time.Sleep(200 * time.Millisecond) {
		if resp, err := c.Get(gatewayURL + "/health"); err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return
			}
		}
	}
	t.Errorf("gateway not answering on %s 15s after being recreated", addr)
}

func bench(t *testing.T, env ...string) (string, error) {
	t.Helper()
	cmd := exec.Command("make", "-C", repoRoot, "bench")
	cmd.Env = append(append(os.Environ(), nestedEnv+"=1"), env...)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// Two measured runs: exactly two summaries, so the warm-up writes none and
// the median is taken over more than one run.
func TestShouldWriteOneSummaryPerMeasuredRun(t *testing.T) {
	recreateGatewayAfterBench(t)
	results := filepath.Join(repoRoot, "loadtest", "results")
	before, _ := filepath.Glob(filepath.Join(results, "*.json"))
	if out, err := bench(t, "SCENARIOS=mixed", "RUNS=2", "DURATION=3s", "VUS=20"); err != nil {
		t.Fatalf("make bench: %v\n%s", err, out)
	}
	after, _ := filepath.Glob(filepath.Join(results, "*.json"))
	if n := len(after) - len(before); n != 2 {
		t.Errorf("make bench RUNS=2 wrote %d new summaries, want exactly 2 (the warm-up writes none)", n)
	}
}

// A k6 error (here a script failing in setup) fails the bench instead of
// printing a misleading line.
func TestShouldFailTheBenchWhenK6Fails(t *testing.T) {
	bad := filepath.Join(repoRoot, "loadtest", "zz-smoke-broken.js")
	if err := os.WriteFile(bad, []byte("export function setup() { throw new Error('boom'); }\nexport default function () {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Remove(bad) })
	if out, err := bench(t, "SCENARIOS=zz-smoke-broken", "RUNS=1", "DURATION=1s"); err == nil {
		t.Errorf("make bench with a failing k6 script exited 0:\n%s", out)
	}
}

// Failed checks count as failures: a scenario whose responses all fail a
// check prints a non-zero failed count even when every request was 2xx.
func TestShouldCountFailedChecksAsFailures(t *testing.T) {
	recreateGatewayAfterBench(t)
	f := filepath.Join(repoRoot, "loadtest", "zz-smoke-checks.js")
	script := `import http from 'k6/http';
import { check } from 'k6';
export const options = { insecureSkipTLSVerify: true, vus: 2, duration: __ENV.DURATION || '1s' };
const users = JSON.parse(open('/secrets/users.json'));
export default function () {
  const r = http.get('https://gateway/health');
  check(r, { 'never true': () => false });
}
`
	if err := os.WriteFile(f, []byte(script), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Remove(f) })
	out, err := bench(t, "SCENARIOS=zz-smoke-checks", "RUNS=1", "DURATION=1s")
	if err != nil {
		t.Fatalf("make bench: %v\n%s", err, out)
	}
	m := regexp.MustCompile(`(?m)^zz-smoke-checks\s+\S+\s+\S+\s+([0-9]+)\s*$`).FindStringSubmatch(out)
	if m == nil || m[1] == "0" {
		t.Errorf("a scenario whose checks all fail → %v, want a non-zero failed count:\n%s", m, strings.TrimSpace(out))
	}
}

// A load test must not grow the gateway's container log without bound.
func TestShouldRotateTheGatewayContainerLog(t *testing.T) {
	id := strings.TrimSpace(mustCompose(t, "ps", "-q", "gateway"))
	out, err := exec.Command("docker", "inspect", "-f",
		`{{.HostConfig.LogConfig.Type}} {{index .HostConfig.LogConfig.Config "max-size"}} {{index .HostConfig.LogConfig.Config "max-file"}}`, id).CombinedOutput()
	if err != nil || strings.TrimSpace(string(out)) != "json-file 10m 3" {
		t.Errorf("gateway log config = %q (err %v), want json-file 10m 3", strings.TrimSpace(string(out)), err)
	}
}
