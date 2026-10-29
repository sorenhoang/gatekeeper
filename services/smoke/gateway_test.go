//go:build smoke

package smoke

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const (
	gatewayURL = "http://localhost:8080"
	// The gateway runs nginx with -c on the mounted directory, so every nginx
	// command in the container must name the same file.
	nginxConf = "/etc/nginx/gatekeeper/nginx.conf"
)

var confDir = filepath.Join(repoRoot, "nginx", "conf.d")

func get(t *testing.T, path, host string) (*http.Response, string) {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, gatewayURL+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	if host != "" {
		req.Host = host
	}
	client := &http.Client{Timeout: 3 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("GET %s (Host %q): %v", path, host, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body of %s: %v", path, err)
	}
	return resp, string(body)
}

func welcomePage(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(repoRoot, "frontend", "index.html"))
	if err != nil {
		t.Fatalf("frontend/index.html: %v", err)
	}
	return string(b)
}

func nginxCmd(t *testing.T, args ...string) (string, error) {
	t.Helper()
	return compose(t, append([]string{"exec", "-T", "gateway", "nginx", "-c", nginxConf}, args...)...)
}

func gatewayStartedAt(t *testing.T) string {
	t.Helper()
	id := strings.TrimSpace(mustCompose(t, "ps", "-q", "gateway"))
	out, err := exec.Command("docker", "inspect", "-f", "{{.State.StartedAt}}", id).CombinedOutput()
	if err != nil {
		t.Fatalf("docker inspect gateway: %v\n%s", err, out)
	}
	return strings.TrimSpace(string(out))
}

// writeConf drops a file into the host conf.d and removes it (and reloads)
// when the test ends, so a failing test never leaves the gateway broken.
func writeConf(t *testing.T, name, content string) {
	t.Helper()
	path := filepath.Join(confDir, name)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	t.Cleanup(func() {
		os.Remove(path)
		if out, err := nginxCmd(t, "-s", "reload"); err != nil {
			t.Errorf("reload after removing %s: %v\n%s", name, err, out)
		}
	})
}

func TestShouldServeWelcomePageAtRoot(t *testing.T) {
	resp, body := get(t, "/", "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET / = %d, want 200", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Errorf("GET / Content-Type = %q, want text/html", ct)
	}
	if body != welcomePage(t) {
		t.Errorf("GET / body is not frontend/index.html:\n%s", body)
	}
}

// Phase 1 replaced the phase-0 "/nope → 404": unknown browser paths now get
// the SPA shell (missing assets still 404, see spa_test.go).
func TestShouldServeSpaShellForUnknownPath(t *testing.T) {
	resp, body := get(t, "/nope", "")
	if resp.StatusCode != http.StatusOK || body != welcomePage(t) {
		t.Errorf("GET /nope = %d, want 200 with frontend/index.html (SPA fallback)\n%s", resp.StatusCode, body)
	}
}

func TestShouldPassNginxConfigTest(t *testing.T) {
	out, err := nginxCmd(t, "-t")
	if err != nil {
		t.Fatalf("nginx -t: %v\n%s", err, out)
	}
	if !strings.Contains(out, "syntax is ok") || !strings.Contains(out, "test is successful") {
		t.Errorf("nginx -t output = %q, want syntax ok and test successful", out)
	}
	if !strings.Contains(out, nginxConf) {
		t.Errorf("nginx -t checked %q, want the mounted %s", out, nginxConf)
	}
}

func TestShouldRunOneMasterAndAtLeastOneWorker(t *testing.T) {
	out := mustCompose(t, "exec", "-T", "gateway", "ps", "-o", "args")
	var masters, workers int
	for _, line := range strings.Split(out, "\n") {
		switch {
		case strings.Contains(line, "nginx: master process"):
			masters++
			if !strings.Contains(line, nginxConf) {
				t.Errorf("master %q does not run the mounted %s", strings.TrimSpace(line), nginxConf)
			}
		case strings.Contains(line, "nginx: worker process"):
			workers++
		}
	}
	if masters != 1 || workers < 1 {
		t.Errorf("gateway has %d master / %d worker processes, want 1 / >=1:\n%s", masters, workers, out)
	}
}

func TestShouldApplyHostConfigEditOnReloadWithoutRestart(t *testing.T) {
	before := gatewayStartedAt(t)
	marker := fmt.Sprintf("reload-%d", time.Now().UnixNano())
	writeConf(t, "zz-smoke-reload.conf", fmt.Sprintf(
		"server {\n  listen 80;\n  server_name reload.smoke.test;\n  return 200 %q;\n}\n", marker))

	if out, err := nginxCmd(t, "-t"); err != nil {
		t.Fatalf("nginx -t with the edited conf.d: %v\n%s", err, out)
	}
	if out, err := nginxCmd(t, "-s", "reload"); err != nil {
		t.Fatalf("nginx -s reload: %v\n%s", err, out)
	}

	var body string
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, body = get(t, "/", "reload.smoke.test"); body == marker {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if body != marker {
		t.Fatalf("after reload, Host reload.smoke.test got %q, want %q (host edit not visible)", body, marker)
	}
	if after := gatewayStartedAt(t); after != before {
		t.Errorf("gateway restarted (StartedAt %s → %s), want a reload only", before, after)
	}
}

func TestShouldKeepServingOldConfigWhenReloadedConfigIsInvalid(t *testing.T) {
	before := gatewayStartedAt(t)
	writeConf(t, "zz-smoke-broken.conf", "this_is_not_a_directive on;\n")

	out, err := nginxCmd(t, "-s", "reload")
	logs := mustCompose(t, "logs", "--no-log-prefix", "--since", "1m", "gateway")
	if !strings.Contains(out+logs, "[emerg]") {
		t.Errorf("invalid config reload (err=%v) logged no [emerg]:\n%s\n%s", err, out, logs)
	}

	// Give a (wrongly) accepted reload time to swap workers before checking.
	time.Sleep(500 * time.Millisecond)
	resp, body := get(t, "/", "")
	if resp.StatusCode != http.StatusOK || body != welcomePage(t) {
		t.Errorf("after invalid reload GET / = %d %q, want 200 with the old welcome page", resp.StatusCode, body)
	}
	if after := gatewayStartedAt(t); after != before {
		t.Errorf("gateway restarted (StartedAt %s → %s) on invalid config", before, after)
	}
}
