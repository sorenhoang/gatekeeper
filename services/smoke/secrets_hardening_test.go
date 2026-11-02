//go:build smoke

package smoke

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// An empty file left by an earlier failed run must be regenerated, not kept:
// an empty key or password would be accepted forever.
func TestShouldRegenerateEmptySecretFiles(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "secrets")
	runSecrets(t, dir)
	for _, f := range secretFiles {
		os.WriteFile(filepath.Join(dir, f), nil, 0o644)
	}
	runSecrets(t, dir)
	for _, f := range secretFiles {
		if fi, err := os.Stat(filepath.Join(dir, f)); err != nil || fi.Size() == 0 {
			t.Errorf("an empty %s was kept (err %v)", f, err)
		}
	}
}

// The admin host's password is the admin user's: the htpasswd entry must be
// that password's apr1 hash, not some other (or an empty) one.
func TestShouldHashTheAdminPasswordIntoTheHtpasswd(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "secrets")
	runSecrets(t, dir)
	b, _ := os.ReadFile(filepath.Join(dir, "auth", "users.json"))
	var users map[string]struct{ Password string }
	json.Unmarshal(b, &users)
	line, _ := os.ReadFile(filepath.Join(dir, "gateway", "admin.htpasswd"))
	hash := strings.TrimSpace(strings.TrimPrefix(string(line), "admin:"))
	parts := strings.Split(hash, "$") // "", "apr1", salt, digest
	if len(parts) != 4 {
		t.Fatalf("htpasswd entry %q is not apr1", line)
	}
	want, err := exec.Command("openssl", "passwd", "-apr1", "-salt", parts[2], users["admin"].Password).Output()
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(want)) != hash {
		t.Errorf("htpasswd hash %q does not match the admin password from users.json", hash)
	}
}
