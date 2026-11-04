//go:build smoke && lifecycle

package smoke

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"
)

// underLoad runs the orders scenario (GET + POST, 30 users) for d while op
// runs, and returns the number of failed checks.
func underLoad(t *testing.T, d time.Duration, op func()) int {
	t.Helper()
	recreateGatewayAfterBench(t)
	done := make(chan string, 1)
	go func() {
		cmd := exec.Command(filepath.Join(repoRoot, "scripts", "bench.sh"))
		cmd.Env = append(os.Environ(), "SCENARIOS=orders", "RUNS=1", "WARMUP=0", "VUS=30", "DURATION="+d.String())
		out, err := cmd.CombinedOutput()
		if err != nil {
			done <- "error: " + err.Error() + "\n" + string(out)
			return
		}
		done <- string(out)
	}()
	time.Sleep(3 * time.Second) // load is flowing
	op()
	out := <-done
	m := regexp.MustCompile(`(?m)^orders\s+\S+\s+\S+\s+([0-9]+)\s*$`).FindStringSubmatch(out)
	if m == nil {
		t.Fatalf("no orders result from the load:\n%s", out)
	}
	n, _ := strconv.Atoi(m[1])
	return n
}

func TestShouldReloadUnderLoadWithoutFailures(t *testing.T) {
	failed := underLoad(t, 20*time.Second, func() {
		for i := 0; i < 3; i++ {
			if out, err := runMake(t, "reload"); err != nil {
				t.Errorf("make reload #%d: %v\n%s", i+1, err, out)
			}
			time.Sleep(3 * time.Second)
		}
	})
	if failed != 0 {
		t.Errorf("3 reloads under load → %d failed checks, want 0", failed)
	}
}

func deploy(t *testing.T, env ...string) (string, error) {
	t.Helper()
	cmd := exec.Command("make", "-C", repoRoot, "deploy")
	cmd.Env = append(append(os.Environ(), nestedEnv+"=1", "SERVICE=order-service"), env...)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func upstreams(t *testing.T) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(confDir, "upstreams.conf"))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// writeUpstreams writes in place (same inode) so the gateway's mount sees it.
func writeUpstreams(t *testing.T, b []byte) {
	t.Helper()
	f, err := os.OpenFile(filepath.Join(confDir, "upstreams.conf"), os.O_WRONLY|os.O_TRUNC, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	f.Write(b)
}

func TestShouldDeployOrdersUnderLoadWithoutFailures(t *testing.T) {
	restoreStack(t)
	before := upstreams(t)
	var out string
	var err error
	failed := underLoad(t, 90*time.Second, func() { out, err = deploy(t) })
	if err != nil {
		t.Fatalf("make deploy SERVICE=order-service: %v\n%s", err, out)
	}
	if failed != 0 {
		t.Errorf("a rolling deploy under load → %d failed checks (2xx and not degraded), want 0\n%s", failed, out)
	}
	if !bytes.Equal(upstreams(t), before) {
		t.Error("upstreams.conf differs after the deploy; it must end as it started")
	}
}

func TestShouldRefuseToDeployWhenAReplicaIsAlreadyDown(t *testing.T) {
	restoreStack(t)
	before := upstreams(t)
	t.Cleanup(func() { writeUpstreams(t, before); runMake(t, "reload") })
	marked := bytes.Replace(before, []byte("server order-service-2:8080"), []byte("server order-service-2:8080 down"), 1)
	writeUpstreams(t, marked)
	if out, err := deploy(t); err == nil || !strings.Contains(out, "already marked down") {
		t.Errorf("make deploy with order-service-2 already down → err %v, want a refusal saying it is already marked down:\n%s", err, out)
	}
	if !bytes.Equal(upstreams(t), marked) {
		t.Error("a refused deploy changed upstreams.conf")
	}
}

// A replica that never gets healthy (here: an invalid chaos setting makes it
// exit at start) stays marked down; the others keep serving.
func TestShouldKeepAFailedReplicaOutOfRotation(t *testing.T) {
	// Setup: after a replica that exits at start, Docker Desktop's [::1]
	// forwarding can reset bursts of new connections for minutes (measured:
	// 16 resets in the Phase 5 spike tests); a fresh gateway clears it.
	// Registered before restoreStack so it runs after make up.
	recreateGatewayAfterBench(t)
	restoreStack(t)
	before := upstreams(t)
	t.Cleanup(func() { writeUpstreams(t, before); runMake(t, "reload") })
	if out, err := deploy(t, "FAIL_RATE=abc"); err == nil || !strings.Contains(out, "did not become healthy") {
		t.Fatalf("make deploy with a replica that can't start → err %v, want it to report the replica unhealthy:\n%s", err, out)
	}
	after := upstreams(t)
	if n := bytes.Count(after, []byte(":8080 down")); n != 1 || !bytes.Contains(after, []byte("server order-service:8080 down")) {
		t.Errorf("after a failed deploy upstreams.conf has %d replicas down, want exactly order-service (the one that failed)", n)
	}
	if r := authed(t, "GET", "/api/orders/", "Bearer "+suiteToken, nil); r.status != 200 || r.header.Get("X-Degraded") != "" {
		t.Errorf("orders with one replica out → %d (degraded %q), want 200 from the others", r.status, r.header.Get("X-Degraded"))
	}
}
