//go:build smoke

package smoke

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type cacheHit struct {
	status int
	cache  string
	id     string
}

func productsRequest(t *testing.T, method, path string, extra map[string]string) cacheHit {
	t.Helper()
	req, err := http.NewRequest(method, gatewayURL+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	id := runID("cache")
	req.Header.Set("X-Request-ID", id)
	for k, v := range extra {
		req.Header.Set(k, v)
	}
	resp, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	resp.Body.Close()
	return cacheHit{status: resp.StatusCode, cache: resp.Header.Get("X-Cache-Status"), id: id}
}

// backendSaw counts product-service log lines for exactly this path.
func backendSaw(t *testing.T, path string) int {
	t.Helper()
	time.Sleep(300 * time.Millisecond)
	n := 0
	for _, l := range logLinesWith(t, "product-service", "path="+path+" ") {
		if strings.Contains(l, "path="+path+" ") {
			n++
		}
	}
	return n
}

func uniqueProduct(prefix string) (gatewayPath, backendPath string) {
	p := fmt.Sprintf("/%s-%d", prefix, time.Now().UnixNano())
	return "/api/products" + p, p
}

func TestShouldServeARepeatedProductsGetFromTheCache(t *testing.T) {
	gw, be := uniqueProduct("repeat")
	first := productsRequest(t, http.MethodGet, gw, nil)
	second := productsRequest(t, http.MethodGet, gw, nil)
	if first.status != 200 || second.status != 200 || first.cache != "MISS" || second.cache != "HIT" {
		t.Fatalf("two GETs → (%d %q), (%d %q), want (200 MISS), (200 HIT)", first.status, first.cache, second.status, second.cache)
	}
	if n := backendSaw(t, be); n != 1 {
		t.Errorf("product-service saw %s %d times, want 1 (the HIT came from the cache)", be, n)
	}
}

func TestShouldCacheProductsForThirtySeconds(t *testing.T) {
	b, err := os.ReadFile(filepath.Join(repoRoot, "nginx", "conf.d", "default.conf"))
	if err != nil {
		t.Fatal(err)
	}
	var products string
	for _, body := range locationBodies(string(b)) {
		if strings.Contains(body, "proxy_pass http://products/") {
			products = body
		}
	}
	for _, want := range []string{"proxy_cache_valid 200 30s;", "snippets/cache-products.conf"} {
		if !strings.Contains(products, want) {
			t.Errorf("products location lacks %q:\n%s", want, products)
		}
	}
}

// A credential in the request means the answer may be personal: it is never
// answered from the shared cache, and never stored in it.
func TestShouldNeverCacheRequestsCarryingCredentials(t *testing.T) {
	for name, hdr := range map[string]map[string]string{
		"Authorization": {"Authorization": "Bearer secret"},
		"Cookie":        {"Cookie": "session=secret"},
		"X-API-Key":     {"X-API-Key": knownAPIKey},
	} {
		gw, _ := uniqueProduct("cred")
		for i := 0; i < 2; i++ {
			if r := productsRequest(t, http.MethodGet, gw, hdr); r.cache != "BYPASS" {
				t.Errorf("%s request #%d → X-Cache-Status %q, want BYPASS", name, i+1, r.cache)
			}
		}
		if r := productsRequest(t, http.MethodGet, gw, nil); r.cache != "MISS" {
			t.Errorf("after two %s requests, a plain one → %q, want MISS (nothing may have been stored)", name, r.cache)
		}
	}
}

func TestShouldNeverCachePosts(t *testing.T) {
	gw, be := uniqueProduct("post")
	for i := 0; i < 2; i++ {
		if r := productsRequest(t, http.MethodPost, gw, nil); r.cache == "HIT" {
			t.Errorf("POST #%d was answered from the cache", i+1)
		}
	}
	if n := backendSaw(t, be); n != 2 {
		t.Errorf("product-service saw the POST %d times, want 2", n)
	}
}

func TestShouldOnlyCacheTheProductsRoute(t *testing.T) {
	for _, path := range []string{"/api/users/", "/api/orders/"} {
		if r := productsRequest(t, http.MethodGet, path, nil); r.cache != "" {
			t.Errorf("GET %s carries X-Cache-Status %q, want none", path, r.cache)
		}
	}
}

func TestShouldLogTheCacheResult(t *testing.T) {
	since := time.Now().Add(-time.Second)
	gw, _ := uniqueProduct("logcache")
	hits := []cacheHit{
		productsRequest(t, http.MethodGet, gw, nil),
		productsRequest(t, http.MethodGet, gw, nil),
		productsRequest(t, http.MethodGet, gw, map[string]string{"Authorization": "Bearer x"}),
	}
	for _, h := range hits {
		lines := linesFor(t, since, h.id)
		if len(lines) != 1 || lines[0]["cache"] != h.cache {
			t.Errorf("access log for %s (X-Cache-Status %q) = %v, want one line with cache=%q", h.id, h.cache, lines, h.cache)
		}
	}
}
