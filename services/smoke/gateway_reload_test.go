//go:build smoke

package smoke

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// A killed test run skips t.Cleanup; a leftover broken zz-smoke-*.conf would
// stop the gateway from starting on the next `make up`, so purge them first.
func init() {
	stale, _ := filepath.Glob(filepath.Join(confDir, "zz-smoke-*.conf"))
	for _, f := range stale {
		os.Remove(f)
	}
}

func masterPID(t *testing.T) string {
	t.Helper()
	return strings.TrimSpace(mustCompose(t, "exec", "-T", "gateway", "cat", "/run/nginx.pid"))
}

func emergCount(t *testing.T) int {
	t.Helper()
	return strings.Count(mustCompose(t, "logs", "--no-log-prefix", "gateway"), "[emerg]")
}

// `nginx -s reload` checks the config in the CLI process and never signals the
// master when it is invalid; SIGHUP goes straight to the running master, which
// is the path that must keep the old config.
func TestShouldMasterKeepOldConfigWhenSighupHitsInvalidConfig(t *testing.T) {
	pid := masterPID(t)
	emergBefore := emergCount(t)
	writeConf(t, "zz-smoke-sighup.conf", "this_is_not_a_directive on;\n")

	mustCompose(t, "exec", "-T", "gateway", "kill", "-HUP", pid)

	deadline := time.Now().Add(5 * time.Second)
	for emergCount(t) == emergBefore && time.Now().Before(deadline) {
		time.Sleep(100 * time.Millisecond)
	}
	if emergCount(t) == emergBefore {
		t.Fatalf("master got SIGHUP with an invalid conf.d but logged no new [emerg]")
	}

	resp, body := get(t, "/", "")
	if resp.StatusCode != http.StatusOK || body != welcomePage(t) {
		t.Errorf("after SIGHUP with invalid config GET / = %d %q, want 200 with the old welcome page", resp.StatusCode, body)
	}
	if after := masterPID(t); after != pid {
		t.Errorf("master pid %s → %s, want the same master still running", pid, after)
	}
}
