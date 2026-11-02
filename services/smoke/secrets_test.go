//go:build smoke

package smoke

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

var secretFiles = []string{"auth/jwt.key", "auth/users.json", "gateway/admin.htpasswd"}

func runSecrets(t *testing.T, dir string) {
	t.Helper()
	cmd := exec.Command("make", "-C", repoRoot, "secrets")
	cmd.Env = append(os.Environ(), "SECRETS_DIR="+dir)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("make secrets SECRETS_DIR=%s: %v\n%s", dir, err, out)
	}
}

func TestShouldCreateTheLabSecretsOnceAndKeepThem(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "secrets")
	runSecrets(t, dir)
	first := map[string][]byte{}
	for _, f := range secretFiles {
		b, err := os.ReadFile(filepath.Join(dir, f))
		if err != nil {
			t.Fatalf("make secrets did not create %s: %v", f, err)
		}
		first[f] = b
	}
	if n := len(first["auth/jwt.key"]); n != 32 {
		t.Errorf("jwt.key is %d bytes, want 32 random bytes", n)
	}
	var users map[string]struct{ Password, Role string }
	if err := json.Unmarshal(first["auth/users.json"], &users); err != nil ||
		users["alice"].Role != "user" || users["admin"].Role != "admin" || users["alice"].Password == "" || users["admin"].Password == "" {
		t.Errorf("users.json = %s (err %v), want alice:user and admin:admin with passwords", first["auth/users.json"], err)
	}
	if !strings.HasPrefix(string(first["gateway/admin.htpasswd"]), "admin:$apr1$") {
		t.Errorf("admin.htpasswd = %q, want an apr1 entry for admin", first["gateway/admin.htpasswd"])
	}
	runSecrets(t, dir)
	for _, f := range secretFiles {
		if b, _ := os.ReadFile(filepath.Join(dir, f)); !bytes.Equal(b, first[f]) {
			t.Errorf("a second make secrets changed %s", f)
		}
	}
}

func TestShouldNeverTrackASecret(t *testing.T) {
	out, err := exec.Command("git", "-C", repoRoot, "ls-files", "secrets").Output()
	if err != nil {
		t.Fatal(err)
	}
	if s := strings.TrimSpace(string(out)); s != "" {
		t.Errorf("git tracks files under secrets/: %s", s)
	}
	if err := exec.Command("git", "-C", repoRoot, "check-ignore", "-q", "secrets/auth/jwt.key").Run(); err != nil {
		t.Errorf("secrets/auth/jwt.key is not ignored by git: %v", err)
	}
}

// Each container gets only its own half: the gateway can check the admin
// password but can never sign a token.
func TestShouldGiveEachContainerOnlyItsOwnSecrets(t *testing.T) {
	// `compose cp` works on the distroless services too (no shell needed).
	ls := func(svc, path string) bool {
		_, err := compose(t, "cp", svc+":"+path, t.TempDir())
		return err == nil
	}
	if ls("gateway", "/etc/nginx/secrets/admin.htpasswd") == false {
		t.Error("gateway lacks /etc/nginx/secrets/admin.htpasswd")
	}
	if out := mustCompose(t, "exec", "-T", "gateway", "sh", "-c", "find / -name jwt.key 2>/dev/null; true"); strings.TrimSpace(out) != "" {
		t.Errorf("gateway container holds the signing key at %s", strings.TrimSpace(out))
	}
	if !ls("auth-service", "/etc/gatekeeper/auth/jwt.key") {
		t.Error("auth-service lacks /etc/gatekeeper/auth/jwt.key")
	}
	if ls("auth-service", "/etc/gatekeeper/auth/admin.htpasswd") {
		t.Error("auth-service holds the admin htpasswd")
	}
}
