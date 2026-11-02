//go:build smoke && lifecycle

package smoke

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"
)

// The lab's only static asset is 127 bytes, below gzip_min_length, so CSS and
// JS are checked on a temporary server with bodies of 512 bytes.
func TestShouldGzipCssAndJsOfAtLeast256Bytes(t *testing.T) {
	body := strings.Repeat("a", 512)
	writeConf(t, "zz-smoke-gztypes.conf", fmt.Sprintf(`server {
    listen 80;
    server_name gztypes.smoke.test;
    location = /x.css { default_type text/css; return 200 "%[1]s"; }
    location = /x.js  { default_type application/javascript; return 200 "%[1]s"; }
}
`, body))
	if out, err := nginxCmd(t, "-s", "reload"); err != nil {
		t.Fatalf("reload with the gzip-types server: %v\n%s", err, out)
	}
	time.Sleep(time.Second)
	for _, path := range []string{"/x.css", "/x.js"} {
		req, _ := http.NewRequest(http.MethodGet, gatewayURL+path, nil)
		req.Host = "gztypes.smoke.test"
		req.Header.Set("Accept-Encoding", "gzip")
		resp, err := rawClient.Do(req)
		if err != nil {
			t.Fatalf("GET %s: %v", path, err)
		}
		resp.Body.Close()
		if resp.StatusCode != 200 || resp.Header.Get("Content-Encoding") != "gzip" {
			t.Errorf("%s (%s, 512 bytes) with Accept-Encoding: gzip → %d, Content-Encoding %q, want 200 gzip",
				path, resp.Header.Get("Content-Type"), resp.StatusCode, resp.Header.Get("Content-Encoding"))
		}
	}
}

// The control for the Set-Cookie test: the same snippet on a backend that
// sends no cookie does store the answer, so a MISS there is the cookie rule.
func TestShouldCacheTheSameBackendWithoutACookie(t *testing.T) {
	writeConf(t, "zz-smoke-nocookie.conf", `server {
    listen 127.0.0.1:8091;
    location / { return 200 "{\"ok\":true}"; }
}
server {
    listen 80;
    server_name nocookie.smoke.test;
    location / {
        include /etc/nginx/gatekeeper/snippets/cache-products.conf;
        proxy_cache_valid 200 30s;
        proxy_pass http://127.0.0.1:8091;
    }
}
`)
	if out, err := nginxCmd(t, "-s", "reload"); err != nil {
		t.Fatalf("reload with the cookie-less backend: %v\n%s", err, out)
	}
	time.Sleep(time.Second)
	path := fmt.Sprintf("/nocookie-%d", time.Now().UnixNano())
	first, second := hostRequest(t, "nocookie.smoke.test", path), hostRequest(t, "nocookie.smoke.test", path)
	if first.cache != "MISS" || second.cache != "HIT" {
		t.Errorf("cookie-less backend → %q then %q, want MISS then HIT", first.cache, second.cache)
	}
}

// The README's "before" row, measured: the same ten GETs with the cache
// bypassed (a credential header) each pay the 300ms delay and reach the
// service.
func TestShouldPayTheServiceDelayOnEveryGetWithoutTheCache(t *testing.T) {
	withServiceEnv(t, "product-service", "DELAY_MS=300")
	gw, be := uniqueProduct("before")
	var times []time.Duration
	for i := 0; i < 10; i++ {
		r := productsTimed(t, gw, map[string]string{"Authorization": "Bearer before"})
		if r.status != 200 || r.cache != "BYPASS" {
			t.Fatalf("GET #%d = %d %q, want 200 BYPASS", i+1, r.status, r.cache)
		}
		times = append(times, r.elapsed)
	}
	t.Logf("10 GETs of one key with the cache bypassed: %v", times)
	for i, d := range times {
		if d < 300*time.Millisecond {
			t.Errorf("bypassed GET #%d took %v, want at least the 300ms service delay", i+1, d)
		}
	}
	if n := backendSaw(t, be); n != 10 {
		t.Errorf("product-service saw the key %d times, want 10", n)
	}
}

type timedCache struct {
	status  int
	cache   string
	elapsed time.Duration
}

func productsTimed(t *testing.T, path string, extra map[string]string) timedCache {
	t.Helper()
	start := time.Now()
	r := productsRequest(t, http.MethodGet, path, extra)
	return timedCache{status: r.status, cache: r.cache, elapsed: time.Since(start)}
}
