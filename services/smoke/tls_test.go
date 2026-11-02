//go:build smoke

package smoke

import (
	"crypto/tls"
	"crypto/x509"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

const tlsURL = "https://localhost:8443"

func labCAPool(t *testing.T) *x509.CertPool {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(repoRoot, "certs", "ca.pem"))
	if err != nil {
		t.Fatalf("lab CA: %v (run make certs)", err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(b) {
		t.Fatal("certs/ca.pem holds no certificate")
	}
	return pool
}

// tlsClient trusts the lab CA. Go keeps a transport with its own TLS config
// on HTTP/1.1 unless ForceAttemptHTTP2 is set; h1 forbids HTTP/2 outright.
func tlsClient(t *testing.T, h1 bool, cfg func(*tls.Config)) *http.Client {
	t.Helper()
	c := &tls.Config{RootCAs: labCAPool(t)}
	if cfg != nil {
		cfg(c)
	}
	tr := &http.Transport{TLSClientConfig: c, ForceAttemptHTTP2: !h1}
	if h1 {
		tr.TLSNextProto = map[string]func(string, *tls.Conn) http.RoundTripper{}
	}
	return &http.Client{Timeout: 10 * time.Second, Transport: tr,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}

func tlsGet(t *testing.T, c *http.Client, url, host string) (*http.Response, string) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, url, nil)
	if host != "" {
		req.Host = host
	}
	resp, err := c.Do(req)
	if err != nil {
		t.Fatalf("GET %s (Host %q): %v", url, host, err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	return resp, string(b)
}

func TestShouldVerifyAgainstTheLabCAOnly(t *testing.T) {
	if resp, _ := tlsGet(t, tlsClient(t, false, nil), tlsURL+"/health", ""); resp.StatusCode != 200 {
		t.Errorf("https /health with the lab CA → %d, want 200", resp.StatusCode)
	}
	// Its own Transport: the default one trusts the lab CA (gateway_test.go).
	_, err := (&http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{}}).Get(tlsURL + "/health")
	var unknown x509.UnknownAuthorityError
	if err == nil || !strings.Contains(err.Error(), "certificate") {
		t.Errorf("https /health trusting only the system roots: err=%v, want a certificate verification error (%T)", err, unknown)
	}
}

func TestShouldServeEveryRouteOverHTTPS(t *testing.T) {
	c := tlsClient(t, false, nil)
	for path, route := range map[string]string{
		"/health": "health", "/api/users/": "users", "/api/orders/": "orders", "/api/products/": "products", "/": "spa",
	} {
		resp, _ := tlsGet(t, c, tlsURL+path, "")
		if resp.StatusCode != 200 || resp.Header.Get("X-Route") != route {
			t.Errorf("https %s → %d X-Route %q, want 200 %q", path, resp.StatusCode, resp.Header.Get("X-Route"), route)
		}
	}
	if resp, _ := tlsGet(t, c, "https://127.0.0.1:8443/health", ""); resp.StatusCode != 200 {
		t.Errorf("https://127.0.0.1:8443/health → %d, want 200 (IP SAN)", resp.StatusCode)
	}
	if _, body := tlsGet(t, c, tlsURL+"/", "admin.localhost"); body != adminPage(t) {
		t.Errorf("Host admin.localhost over https did not serve the admin page")
	}
}

func TestShouldNegotiateHTTP2AndStillServeHTTP11(t *testing.T) {
	if resp, _ := tlsGet(t, tlsClient(t, false, nil), tlsURL+"/api/users/", ""); resp.ProtoMajor != 2 || resp.StatusCode != 200 {
		t.Errorf("client offering h2 → %s %d, want HTTP/2.0 200", resp.Proto, resp.StatusCode)
	}
	if resp, _ := tlsGet(t, tlsClient(t, true, nil), tlsURL+"/api/users/", ""); resp.ProtoMajor != 1 || resp.StatusCode != 200 {
		t.Errorf("HTTP/1.1-only client → %s %d, want HTTP/1.1 200", resp.Proto, resp.StatusCode)
	}
}

func TestShouldRefuseTLS11AndAcceptTLS12And13(t *testing.T) {
	old := tlsClient(t, true, func(c *tls.Config) { c.MinVersion, c.MaxVersion = tls.VersionTLS10, tls.VersionTLS11 })
	_, err := old.Get(tlsURL + "/health")
	if err == nil || !strings.Contains(err.Error(), "protocol version not supported") {
		t.Errorf("TLS 1.1 handshake: err=%v, want 'protocol version not supported'", err)
	}
	for name, v := range map[string]uint16{"TLS 1.2": tls.VersionTLS12, "TLS 1.3": tls.VersionTLS13} {
		c := tlsClient(t, true, func(c *tls.Config) { c.MinVersion, c.MaxVersion = v, v })
		resp, _ := tlsGet(t, c, tlsURL+"/health", "")
		if resp.StatusCode != 200 || resp.TLS == nil || resp.TLS.Version != v {
			t.Errorf("%s handshake → %d, want 200 on %s", name, resp.StatusCode, name)
		}
	}
}

func TestShouldShipTheTLSSettings(t *testing.T) {
	b, err := os.ReadFile(filepath.Join(repoRoot, "nginx", "snippets", "tls.conf"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"ssl_protocols TLSv1.2 TLSv1.3;", "ssl_session_cache shared:SSL:10m;", "ssl_session_tickets off;"} {
		if !strings.Contains(stripComments(string(b)), want) {
			t.Errorf("snippets/tls.conf lacks %q", want)
		}
	}
	listen443 := regexp.MustCompile(`listen\s+443\s+ssl`)
	for _, f := range []string{"default.conf", "admin.conf"} {
		b, err := os.ReadFile(filepath.Join(confDir, f))
		if err != nil {
			t.Fatal(err)
		}
		for _, server := range strings.Split(stripComments(string(b)), "server {")[1:] {
			if listen443.MatchString(server) && (!strings.Contains(server, "http2 on;") || !strings.Contains(server, "snippets/tls.conf")) {
				t.Errorf("%s: a 443 server lacks `http2 on;` or the tls.conf include", f)
			}
		}
		if !listen443.MatchString(string(b)) {
			t.Errorf("%s has no `listen 443 ssl` server", f)
		}
	}
}
