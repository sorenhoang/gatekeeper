//go:build smoke

package smoke

import (
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// h2Get sends one request over the given client and fails unless it went over
// the expected protocol version.
func h2Get(t *testing.T, c *http.Client, path string, major int, hdr map[string]string) hit {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, tlsURL+path, nil)
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := c.Do(req)
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.ProtoMajor != major {
		t.Fatalf("GET %s went over %s, want HTTP/%d", path, resp.Proto, major)
	}
	return hit{status: resp.StatusCode, header: resp.Header, body: string(b)}
}

func TestShouldAnswerTheSameJSON404OverHTTP2(t *testing.T) {
	h2, h1 := tlsClient(t, false, nil), tlsClient(t, true, nil)
	hdr := map[string]string{"X-Request-ID": runID("h2-404")}
	a, b := h2Get(t, h2, "/api/nope", 2, hdr), h2Get(t, h1, "/api/nope", 1, hdr)
	if a.status != 404 || a.status != b.status || a.body != b.body {
		t.Errorf("/api/nope: HTTP/2 %d %q vs HTTP/1.1 %d %q, want the same JSON 404", a.status, a.body, b.status, b.body)
	}
	if a.header.Get("X-Request-ID") != hdr["X-Request-ID"] {
		t.Errorf("HTTP/2 X-Request-ID %q, want the client's %q echoed", a.header.Get("X-Request-ID"), hdr["X-Request-ID"])
	}
}

// One HTTP/2 connection carries every request, and the login limit still
// counts each of them: burst=10, so the 12th quick request is rejected.
func TestShouldRateLimitRequestsOnOneHTTP2Connection(t *testing.T) {
	afterDrain(t)
	c := tlsClient(t, false, nil)
	var limited *hit
	for i := 0; i < 20 && limited == nil; i++ {
		id := runID("h2-login")
		if h := h2Get(t, c, loginPath, 2, map[string]string{"X-Request-ID": id}); h.status == http.StatusTooManyRequests {
			h.id = id
			limited = &h
		}
	}
	if limited == nil {
		t.Fatal("20 quick logins over one HTTP/2 connection never got a 429")
	}
	assertTooMany(t, *limited, "login")
}

func TestShouldCacheAndGzipOverHTTP2(t *testing.T) {
	c := tlsClient(t, false, nil)
	gw, _ := uniqueProduct("h2")
	first, second := h2Get(t, c, gw, 2, nil), h2Get(t, c, gw, 2, nil)
	if first.header.Get("X-Cache-Status") != "MISS" || second.header.Get("X-Cache-Status") != "HIT" {
		t.Errorf("over HTTP/2 → %q then %q, want MISS then HIT", first.header.Get("X-Cache-Status"), second.header.Get("X-Cache-Status"))
	}
	// Set by hand, Go leaves the body compressed and the header visible.
	if h := h2Get(t, c, gw, 2, map[string]string{"Accept-Encoding": "gzip"}); h.header.Get("Content-Encoding") != "gzip" {
		t.Errorf("over HTTP/2 with Accept-Encoding: gzip → Content-Encoding %q, want gzip", h.header.Get("Content-Encoding"))
	}
}

func TestShouldDocumentTLSInTheReadme(t *testing.T) {
	b, err := os.ReadFile(filepath.Join(repoRoot, "GUIDE.md"))
	if err != nil {
		t.Fatal(err)
	}
	readme := string(b)
	start := strings.Index(readme, "\n## TLS & HTTP/2")
	if start < 0 {
		t.Fatal(`README has no "## TLS & HTTP/2" section`)
	}
	section := readme[start+1:]
	if end := strings.Index(section[3:], "\n## "); end >= 0 {
		section = section[:end+3]
	}
	for _, topic := range []string{"make certs", "certs/ca.pem", "mkcert -install", "8080", "8443", "301", "308",
		"https://$host:8443", "max-age=300", "The plain HTTP request was sent to HTTPS port", "ForceAttemptHTTP2",
		"ssl_session_tickets off", "Reused", "TLSv1.2 TLSv1.3"} {
		if !strings.Contains(section, topic) {
			t.Errorf("README TLS & HTTP/2 section does not cover %q", topic)
		}
	}
	if !strings.Contains(readme, "| TLS (local) | openssl (mkcert optional) |") {
		t.Error(`README tech stack row should read "| TLS (local) | openssl (mkcert optional) |"`)
	}
	for _, row := range routeTable(t) {
		if row.host == "localhost:8080" {
			t.Errorf("README route table still lists host localhost:8080 (row %s); the app is on localhost:8443", row.url)
		}
	}
}
