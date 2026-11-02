//go:build smoke

package smoke

import (
	"bufio"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"
)

const plainURL = "http://localhost:8080"

// noFollow shows the redirect itself instead of following it.
var noFollow = &http.Client{Timeout: 10 * time.Second,
	CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}

func plain(t *testing.T, method, host, path string, body io.Reader) *http.Response {
	t.Helper()
	req, _ := http.NewRequest(method, plainURL+path, body)
	if host != "" {
		req.Host = host
	}
	resp, err := noFollow.Do(req)
	if err != nil {
		t.Fatalf("%s %s on :8080: %v", method, path, err)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	return resp
}

func TestShouldRedirectGetAndHeadToTheHTTPSPort(t *testing.T) {
	for _, method := range []string{http.MethodGet, http.MethodHead} {
		r := plain(t, method, "", "/api/users/x?q=1", nil)
		if r.StatusCode != http.StatusMovedPermanently || r.Header.Get("Location") != "https://localhost:8443/api/users/x?q=1" {
			t.Errorf("%s :8080/api/users/x?q=1 → %d Location %q, want 301 https://localhost:8443/api/users/x?q=1",
				method, r.StatusCode, r.Header.Get("Location"))
		}
	}
	if r := plain(t, http.MethodGet, "admin.localhost", "/", nil); r.StatusCode != 301 || r.Header.Get("Location") != "https://admin.localhost:8443/" {
		t.Errorf("Host admin.localhost on :8080 → %d %q, want 301 https://admin.localhost:8443/", r.StatusCode, r.Header.Get("Location"))
	}
}

// A 301 lets clients turn a POST into a GET; 308 keeps the method and body.
func TestShouldRedirectPostWith308AndKeepTheBody(t *testing.T) {
	r := plain(t, http.MethodPost, "", "/api/orders/", strings.NewReader(`{"item":"x"}`))
	if r.StatusCode != http.StatusPermanentRedirect || r.Header.Get("Location") != "https://localhost:8443/api/orders/" {
		t.Fatalf("POST :8080/api/orders/ → %d %q, want 308 https://localhost:8443/api/orders/", r.StatusCode, r.Header.Get("Location"))
	}
	req, _ := http.NewRequest(http.MethodPost, plainURL+"/api/orders/", strings.NewReader(`{"item":"x"}`))
	req.Header.Set("Content-Type", "application/json")
	resp, err := (&http.Client{Timeout: 10 * time.Second}).Do(req) // follows the 308
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	// The echo doesn't return bodies; the forwarded Content-Length shows the
	// 12-byte body arrived.
	var e struct {
		Method  string              `json:"method"`
		Headers map[string][]string `json:"headers"`
	}
	json.NewDecoder(resp.Body).Decode(&e)
	if cl := strings.Join(e.Headers["Content-Length"], ","); resp.Request.URL.Scheme != "https" || e.Method != http.MethodPost || cl != "12" {
		t.Errorf("after the 308: scheme %s, backend saw %s with Content-Length %q, want https POST with 12", resp.Request.URL.Scheme, e.Method, cl)
	}
}

func TestShouldRedirectEveryOtherMethodWith308(t *testing.T) {
	for _, method := range []string{http.MethodPut, http.MethodDelete, http.MethodPatch, http.MethodOptions} {
		if r := plain(t, method, "", "/api/orders/x", nil); r.StatusCode != http.StatusPermanentRedirect {
			t.Errorf("%s on :8080 → %d, want 308", method, r.StatusCode)
		}
	}
}

func TestShouldSendHSTSOnEveryKindOfHTTPSAnswer(t *testing.T) {
	for _, c := range []struct{ host, path string }{
		{"", "/"}, {"", "/some/spa/route"}, {"", "/missing.png"}, {"", "/assets/app.css"}, {"admin.localhost", "/"},
	} {
		r := mustRoute(t, c.host, c.path)
		if got := r.header.Get("Strict-Transport-Security"); got != "max-age=300" {
			t.Errorf("https %s (Host %q, %d) Strict-Transport-Security %q, want max-age=300", c.path, c.host, r.status, got)
		}
	}
}

func TestShouldKeepHealthPlainOnPort8080(t *testing.T) {
	if r := plain(t, http.MethodGet, "", "/health", nil); r.StatusCode != 200 {
		t.Errorf("GET :8080/health → %d (Location %q), want 200 without a redirect", r.StatusCode, r.Header.Get("Location"))
	}
}

func TestShouldAnswer400ToPlainHTTPOnTheTLSPort(t *testing.T) {
	conn, err := net.DialTimeout("tcp", "localhost:8443", 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(5 * time.Second))
	conn.Write([]byte("GET / HTTP/1.1\r\nHost: localhost\r\nConnection: close\r\n\r\n"))
	resp, err := http.ReadResponse(bufio.NewReader(conn), nil)
	if err != nil {
		t.Fatalf("plain HTTP on :8443: %v", err)
	}
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 400 || !strings.Contains(string(b), "The plain HTTP request was sent to HTTPS port") {
		t.Errorf("plain HTTP on :8443 → %d %q, want 400 'The plain HTTP request was sent to HTTPS port'", resp.StatusCode, b)
	}
}

func TestShouldSendHSTSOnHTTPSOnly(t *testing.T) {
	const want = "max-age=300"
	for _, path := range []string{"/health", "/api/users/", "/api/nope"} {
		resp, err := http.Get(gatewayURL + path)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if got := resp.Header.Get("Strict-Transport-Security"); got != want {
			t.Errorf("https %s (%d) Strict-Transport-Security %q, want %q (no includeSubDomains)", path, resp.StatusCode, got, want)
		}
	}
	for _, path := range []string{"/health", "/api/users/"} {
		if got := plain(t, http.MethodGet, "", path, nil).Header.Get("Strict-Transport-Security"); got != "" {
			t.Errorf(":8080 %s carries Strict-Transport-Security %q, want none (HSTS is ignored over HTTP)", path, got)
		}
	}
}
