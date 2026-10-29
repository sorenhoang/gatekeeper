//go:build smoke

package smoke

import (
	"encoding/json"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

const forgedIP = "6.6.6.6"

type headerEcho struct {
	Host    string              `json:"host"`
	Headers map[string][]string `json:"headers"`
}

// backendHeaders sends GET path with extra client headers and returns what
// the backend says it received.
func backendHeaders(t *testing.T, path string, extra map[string]string) headerEcho {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, gatewayURL+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	for k, v := range extra {
		req.Header.Set(k, v)
	}
	resp, err := noRedirect.Do(req)
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	defer resp.Body.Close()
	var e headerEcho
	if err := json.NewDecoder(resp.Body).Decode(&e); err != nil {
		t.Fatalf("GET %s: backend echo is not JSON (status %d): %v", path, resp.StatusCode, err)
	}
	return e
}

func (e headerEcho) one(t *testing.T, name string) string {
	t.Helper()
	v := e.Headers[http.CanonicalHeaderKey(name)]
	if len(v) != 1 {
		t.Errorf("backend got %d %s values %q, want exactly one", len(v), name, v)
		return ""
	}
	return v[0]
}

var apiRoutes = []string{"/api/users/", "/api/orders/", "/api/products/"}

func TestShouldTellBackendsTheHostAndSchemeTheClientUsed(t *testing.T) {
	for _, path := range apiRoutes {
		e := backendHeaders(t, path, nil)
		if e.Host != "localhost" {
			t.Errorf("%s: backend Host = %q, want localhost (the client's host, not the upstream name)", path, e.Host)
		}
		if got := e.one(t, "X-Forwarded-Host"); got != "localhost" {
			t.Errorf("%s: X-Forwarded-Host = %q, want localhost", path, got)
		}
		if got := e.one(t, "X-Forwarded-Proto"); got != "http" {
			t.Errorf("%s: X-Forwarded-Proto = %q, want http", path, got)
		}
	}
}

func TestShouldTellBackendsTheClientIpOnce(t *testing.T) {
	for _, path := range apiRoutes {
		e := backendHeaders(t, path, nil)
		xff, realIP := e.one(t, "X-Forwarded-For"), e.one(t, "X-Real-IP")
		if strings.Contains(xff, ",") || net.ParseIP(xff) == nil {
			t.Errorf("%s: X-Forwarded-For = %q, want a single IP address", path, xff)
		}
		if xff != realIP {
			t.Errorf("%s: X-Forwarded-For %q != X-Real-IP %q", path, xff, realIP)
		}
	}
}

func TestShouldNeverPassClientForwardingHeadersThrough(t *testing.T) {
	forged := map[string]string{
		"X-Forwarded-For": forgedIP,
		"X-Real-IP":       forgedIP,
		"Forwarded":       "for=" + forgedIP,
	}
	for _, path := range apiRoutes {
		e := backendHeaders(t, path, forged)
		for name, values := range e.Headers {
			for _, v := range values {
				if strings.Contains(v, forgedIP) {
					t.Errorf("%s: backend got forged %s: %q", path, name, v)
				}
			}
		}
		if v, ok := e.Headers["Forwarded"]; ok {
			t.Errorf("%s: backend got a Forwarded header %q, want none", path, v)
		}
	}
}

func TestShouldProxyWithHttp11AndNoConnectionClose(t *testing.T) {
	for _, path := range apiRoutes {
		e := backendHeaders(t, path, nil)
		for _, v := range e.Headers["Connection"] {
			if strings.EqualFold(v, "close") {
				t.Errorf("%s: backend got Connection: close (HTTP/1.0 to upstream), want HTTP/1.1 with no Connection", path)
			}
		}
	}
}

var locationBlock = regexp.MustCompile(`location[^{]*\{([^{}]*)\}`)

// The inheritance trap: one proxy_set_header in a location drops all the
// inherited ones, so headers live only in the snippet every location includes.
func TestShouldIncludeTheProxyHeadersSnippetInEveryProxiedLocation(t *testing.T) {
	files, err := filepath.Glob(filepath.Join(repoRoot, "nginx", "conf.d", "*.conf"))
	if err != nil || len(files) == 0 {
		t.Fatalf("no nginx/conf.d/*.conf: %v", err)
	}
	var proxied int
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		conf := string(b)
		if strings.Contains(conf, "proxy_set_header") {
			t.Errorf("%s sets proxy_set_header directly; put headers in snippets/proxy-headers.conf", filepath.Base(f))
		}
		for _, m := range locationBlock.FindAllStringSubmatch(conf, -1) {
			if !strings.Contains(m[1], "proxy_pass") {
				continue
			}
			proxied++
			if !strings.Contains(m[1], "snippets/proxy-headers.conf") {
				t.Errorf("%s: a proxied location does not include snippets/proxy-headers.conf:\n%s", filepath.Base(f), m[0])
			}
		}
	}
	if proxied < len(apiRoutes) {
		t.Errorf("found %d proxied locations, want at least %d", proxied, len(apiRoutes))
	}
	if _, err := os.Stat(filepath.Join(repoRoot, "nginx", "snippets", "proxy-headers.conf")); err != nil {
		t.Errorf("nginx/snippets/proxy-headers.conf: %v", err)
	}
}
