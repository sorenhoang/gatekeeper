//go:build smoke && lifecycle

package smoke

import (
	"os"
	"path/filepath"
	"testing"
)

// A fresh clone has no certs/: `make up` must create them before the gateway
// starts, or nginx would refuse the missing certificate file.
func TestShouldCreateCertificatesOnMakeUp(t *testing.T) {
	dir := filepath.Join(repoRoot, "certs")
	// Setup: the gateway must not be running while certs/ is deleted (its
	// bind mount would keep pointing at the old directory), and the original
	// files, the CA key included, come back afterwards so the developer's lab
	// CA stays the one their tools trust.
	saved := map[string][]byte{}
	for _, f := range append([]string{"ca.key"}, certFiles...) {
		if b, err := os.ReadFile(filepath.Join(dir, f)); err == nil {
			saved[f] = b
		}
	}
	restoreStack(t)
	t.Cleanup(func() {
		if len(saved) == 0 {
			return
		}
		mustCompose(t, "rm", "-sf", "gateway")
		os.RemoveAll(dir)
		os.MkdirAll(dir, 0o755)
		for f, b := range saved {
			mode := os.FileMode(0o644)
			if f == "ca.key" || f == "gateway.key" {
				mode = 0o600
			}
			if err := os.WriteFile(filepath.Join(dir, f), b, mode); err != nil {
				t.Errorf("restoring certs/%s: %v", f, err)
			}
		}
	})
	mustCompose(t, "rm", "-sf", "gateway")
	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}

	if out, err := runMake(t, "up"); err != nil {
		t.Fatalf("make up without certs/: %v\n%s", err, out)
	}
	for _, f := range certFiles {
		if _, err := os.Stat(filepath.Join(dir, f)); err != nil {
			t.Errorf("make up did not create certs/%s: %v", f, err)
		}
	}
	if resp, _ := tlsGet(t, tlsClient(t, false, nil), tlsURL+"/health", ""); resp.StatusCode != 200 {
		t.Errorf("https /health after make up created the certs → %d, want 200", resp.StatusCode)
	}
}
