//go:build smoke && lifecycle

package smoke

import (
	"bufio"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func containerID(t *testing.T, svc string) string {
	t.Helper()
	return strings.TrimSpace(mustCompose(t, "ps", "-q", svc))
}

// The deploy waits for requests already in flight before it recreates the
// first replica: with a 4s POST in flight, "recreating" comes only after that
// POST has answered. Every replica gets a new container, and the whole deploy
// fits well inside the 90s load window of the under-load test.
func TestShouldDrainInFlightRequestsBeforeRecreating(t *testing.T) {
	for _, svc := range orderReplicas {
		withServiceEnv(t, svc, "DELAY_MS=4000")
	}
	before := map[string]string{}
	for _, svc := range orderReplicas {
		before[svc] = containerID(t, svc)
	}

	answered := make(chan time.Time, 1)
	status := make(chan authAnswer, 1)
	go func() {
		r := authed(t, "POST", "/api/orders/", "Bearer "+suiteToken, nil)
		answered <- time.Now()
		status <- r
	}()
	time.Sleep(500 * time.Millisecond) // the POST is on a replica

	start := time.Now()
	cmd := exec.Command("make", "-C", repoRoot, "deploy")
	cmd.Env = append(os.Environ(), nestedEnv+"=1", "SERVICE=order-service")
	pipe, _ := cmd.StdoutPipe()
	cmd.Stderr = cmd.Stdout
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	var recreating time.Time
	var out strings.Builder
	sc := bufio.NewScanner(pipe)
	for sc.Scan() {
		out.WriteString(sc.Text() + "\n")
		if recreating.IsZero() && strings.Contains(sc.Text(), "order-service: recreating") {
			recreating = time.Now()
		}
	}
	err := cmd.Wait()
	took := time.Since(start)
	if err != nil {
		t.Fatalf("make deploy: %v\n%s", err, out.String())
	}

	if r := <-status; r.status != 200 || r.header.Get("X-Degraded") != "" {
		t.Errorf("the POST in flight during the deploy → %d (degraded %q), want 200", r.status, r.header.Get("X-Degraded"))
	}
	if done := <-answered; recreating.IsZero() || recreating.Before(done) {
		t.Errorf("deploy started recreating order-service %v before the in-flight POST answered; it must drain first\n%s", done.Sub(recreating), out.String())
	}
	for _, svc := range orderReplicas {
		if id := containerID(t, svc); id == "" || id == before[svc] {
			t.Errorf("%s has container %q after the deploy (was %q), want a new one", svc, id, before[svc])
		}
	}
	if took > 60*time.Second {
		t.Errorf("the deploy took %v, want under 60s so the 90s load covers it", took)
	}
}
