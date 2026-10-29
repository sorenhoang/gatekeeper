//go:build smoke

package smoke

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

type routed struct {
	status int
	header http.Header
	body   string
}

type echoed struct {
	Service string `json:"service"`
	Path    string `json:"path"`
	Query   string `json:"query"`
}

// Redirects are not followed: the 301 itself is part of what is asserted.
var noRedirect = &http.Client{
	Timeout:       3 * time.Second,
	CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
}

func route(t *testing.T, host, path string) (routed, error) {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, gatewayURL+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	if host != "" {
		req.Host = host
	}
	resp, err := noRedirect.Do(req)
	if err != nil {
		return routed{}, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	return routed{status: resp.StatusCode, header: resp.Header, body: string(b)}, err
}

func mustRoute(t *testing.T, host, path string) routed {
	t.Helper()
	r, err := route(t, host, path)
	if err != nil {
		t.Fatalf("GET %s (Host %q): %v", path, host, err)
	}
	return r
}

func (r routed) echo(t *testing.T) echoed {
	t.Helper()
	var e echoed
	if err := json.Unmarshal([]byte(r.body), &e); err != nil {
		t.Fatalf("body is not a backend echo: %v\n%s", err, r.body)
	}
	return e
}

func TestShouldRouteEachApiPrefixToItsServiceWithPrefixStripped(t *testing.T) {
	cases := []struct {
		path, service, backendPath, query, xRoute string
	}{
		{"/api/users/42?x=1", "user-service", "/42", "x=1", "users"},
		{"/api/orders/7", "order-service", "/7", "", "orders"},
		{"/api/products/7", "product-service", "/7", "", "products"},
		{"/api/users/", "user-service", "/", "", "users"},
		{"/api/products/a/b?q=1&r=2", "product-service", "/a/b", "q=1&r=2", "products"},
	}
	for _, c := range cases {
		r := mustRoute(t, "", c.path)
		if r.status != http.StatusOK {
			t.Errorf("GET %s = %d, want 200\n%s", c.path, r.status, r.body)
			continue
		}
		e := r.echo(t)
		if e.Service != c.service || e.Path != c.backendPath || e.Query != c.query {
			t.Errorf("GET %s reached %s path=%q query=%q, want %s path=%q query=%q",
				c.path, e.Service, e.Path, e.Query, c.service, c.backendPath, c.query)
		}
		if got := r.header.Get("X-Route"); got != c.xRoute {
			t.Errorf("GET %s X-Route = %q, want %q", c.path, got, c.xRoute)
		}
	}
}

func TestShouldRedirectApiPrefixWithoutTrailingSlash(t *testing.T) {
	r := mustRoute(t, "", "/api/users")
	if r.status != http.StatusMovedPermanently {
		t.Fatalf("GET /api/users = %d, want 301\n%s", r.status, r.body)
	}
	if loc := r.header.Get("Location"); !strings.HasSuffix(loc, "/api/users/") {
		t.Errorf("GET /api/users Location = %q, want it to end with /api/users/", loc)
	}
}
