//go:build smoke && lifecycle

package smoke

import (
	"os"
	"os/exec"
	"testing"
	"time"
)

// withOrderReplicasEnv recreates all three order replicas with env, so every
// orders request sees it (the chaos used to target one container).
func withOrderReplicasEnv(t *testing.T, env ...string) {
	t.Helper()
	restoreStack(t)
	args := append([]string{"compose", "--project-directory", repoRoot, "up", "-d", "--no-deps"}, orderReplicas...)
	cmd := exec.Command("docker", args...)
	cmd.Env = append(os.Environ(), env...)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("recreate order replicas with %v: %v\n%s", env, err, out)
	}
	for _, svc := range orderReplicas {
		waitHealthy(t, svc)
	}
	if out, err := nginxCmd(t, "-s", "reload"); err != nil {
		t.Fatalf("reload after recreating order replicas: %v\n%s", err, out)
	}
	time.Sleep(time.Second)
}
