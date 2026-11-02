//go:build smoke

package smoke

import (
	"bytes"
	"crypto/x509"
	"encoding/pem"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"
)

var certFiles = []string{"ca.pem", "gateway.pem", "gateway.key"}

func runCerts(t *testing.T, dir string) string {
	t.Helper()
	cmd := exec.Command("make", "-C", repoRoot, "certs")
	cmd.Env = append(os.Environ(), "CERTS_DIR="+dir)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("make certs CERTS_DIR=%s: %v\n%s", dir, err, out)
	}
	return string(out)
}

func readCert(t *testing.T, path string) *x509.Certificate {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	block, _ := pem.Decode(b)
	if block == nil {
		t.Fatalf("%s holds no PEM block", path)
	}
	c, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatalf("%s: %v", path, err)
	}
	return c
}

func TestShouldCreateTheLabCertificatesOnceAndKeepThem(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "certs")
	runCerts(t, dir)
	first := map[string][]byte{}
	for _, f := range certFiles {
		b, err := os.ReadFile(filepath.Join(dir, f))
		if err != nil {
			t.Fatalf("make certs did not create %s: %v", f, err)
		}
		first[f] = b
	}
	runCerts(t, dir)
	for _, f := range certFiles {
		b, _ := os.ReadFile(filepath.Join(dir, f))
		if !bytes.Equal(b, first[f]) {
			t.Errorf("a second make certs changed %s; it must keep valid files", f)
		}
	}
}

func TestShouldIssueALeafForTheLabNamesOnly(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "certs")
	runCerts(t, dir)
	leaf := readCert(t, filepath.Join(dir, "gateway.pem"))
	var names []string
	names = append(names, leaf.DNSNames...)
	for _, ip := range leaf.IPAddresses {
		names = append(names, ip.String())
	}
	sort.Strings(names)
	if got, want := strings.Join(names, ","), "127.0.0.1,admin.localhost,localhost"; got != want {
		t.Errorf("leaf SANs = %s, want %s", got, want)
	}
	// 398 days: the longest leaf validity Apple and the browsers accept.
	if d := leaf.NotAfter.Sub(leaf.NotBefore); d > 398*24*time.Hour {
		t.Errorf("leaf is valid for %v, want at most 398 days", d)
	}
	ca := readCert(t, filepath.Join(dir, "ca.pem"))
	if err := leaf.CheckSignatureFrom(ca); err != nil {
		t.Errorf("leaf is not signed by the lab CA: %v", err)
	}
}

// A leaf inside the 30-day renewal window is replaced; the CA is kept, so a
// client that trusts it keeps working.
func TestShouldRenewALeafThatExpiresWithinThirtyDays(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "certs")
	runCerts(t, dir)
	ca, _ := os.ReadFile(filepath.Join(dir, "ca.pem"))
	short := exec.Command("openssl", "x509", "-req", "-in", "/dev/stdin", "-days", "5",
		"-CA", filepath.Join(dir, "ca.pem"), "-CAkey", filepath.Join(dir, "ca.key"), "-CAcreateserial",
		"-out", filepath.Join(dir, "gateway.pem"))
	csr, err := exec.Command("openssl", "req", "-new", "-key", filepath.Join(dir, "gateway.key"), "-subj", "/CN=localhost").Output()
	if err != nil {
		t.Fatalf("csr: %v", err)
	}
	short.Stdin = bytes.NewReader(csr)
	if out, err := short.CombinedOutput(); err != nil {
		t.Fatalf("issuing a 5-day leaf: %v\n%s", err, out)
	}
	runCerts(t, dir)
	leaf := readCert(t, filepath.Join(dir, "gateway.pem"))
	if time.Until(leaf.NotAfter) < 30*24*time.Hour {
		t.Errorf("leaf still expires %v, want it renewed past the 30-day window", leaf.NotAfter)
	}
	if now, _ := os.ReadFile(filepath.Join(dir, "ca.pem")); !bytes.Equal(now, ca) {
		t.Error("renewing the leaf replaced the CA")
	}
}

func TestShouldNeverTrackAPrivateKey(t *testing.T) {
	out, err := exec.Command("git", "-C", repoRoot, "ls-files").Output()
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range strings.Split(string(out), "\n") {
		if strings.HasSuffix(f, ".key") || strings.HasSuffix(f, ".pem") {
			t.Errorf("git tracks %s", f)
		}
	}
	if err := exec.Command("git", "-C", repoRoot, "check-ignore", "-q", "certs/gateway.key").Run(); err != nil {
		t.Errorf("certs/gateway.key is not ignored by git: %v", err)
	}
}
