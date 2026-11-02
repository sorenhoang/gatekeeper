//go:build smoke && lifecycle

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

const cacheSnippet = "/etc/nginx/gatekeeper/snippets/cache-products.conf"

// cacheServer adds a temporary server for host proxying to products, with
// the given cache directives (no rate limit, unlike the real API route).
func cacheServer(t *testing.T, name, host, directives string) {
	t.Helper()
	writeConf(t, name+".conf", fmt.Sprintf(`server {
    listen 80;
    server_name %s;
    location / {
        include /etc/nginx/gatekeeper/snippets/proxy-headers.conf;
        %s
        proxy_pass http://products/;
    }
}
`, host, directives))
	if out, err := nginxCmd(t, "-s", "reload"); err != nil {
		t.Fatalf("reload with %s: %v\n%s", name, err, out)
	}
	time.Sleep(time.Second)
}

func hostRequest(t *testing.T, host, path string) cacheHit {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, gatewayURL+path, nil)
	req.Host = host
	resp, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
	if err != nil {
		t.Fatalf("GET %s (Host %s): %v", path, host, err)
	}
	resp.Body.Close()
	return cacheHit{status: resp.StatusCode, cache: resp.Header.Get("X-Cache-Status")}
}

// A cold key under a concurrent burst: with the lock, one request goes to the
// (slow) service and the others wait for its answer.
func TestShouldSendACachedBurstToTheServiceOnce(t *testing.T) {
	afterDrain(t)
	withServiceEnv(t, "product-service", "DELAY_MS=500")
	gw, be := uniqueProduct("stampede")
	hits := spike(t, 50, "", gw, nil)
	if c := countStatus(hits); c[200] != 50 {
		t.Fatalf("50 concurrent GETs on a cold key → %v, want 50 × 200", c)
	}
	if n := backendSaw(t, be); n != 1 {
		t.Errorf("product-service saw the cold key %d times, want 1 (proxy_cache_lock)", n)
	}
}

func TestShouldStampedeWithoutTheLock(t *testing.T) {
	withServiceEnv(t, "product-service", "DELAY_MS=500")
	cacheServer(t, "zz-smoke-nolock", "nolock.smoke.test", "proxy_cache api_cache; proxy_cache_valid 200 30s;")
	path := fmt.Sprintf("/nolock-%d", time.Now().UnixNano())
	hits := spike(t, 50, "nolock.smoke.test", path, nil)
	if c := countStatus(hits); c[200] != 50 {
		t.Fatalf("burst without the lock → %v, want 50 × 200", c)
	}
	if n := backendSaw(t, path); n < 10 {
		t.Errorf("without proxy_cache_lock the service saw the cold key %d times, want at least 10", n)
	}
}

// Stale serving uses the shipped snippet on a server with a 2s TTL, so the
// entry can expire within the test.
func TestShouldServeStaleWhileProductServiceIsDown(t *testing.T) {
	restoreStack(t)
	cacheServer(t, "zz-smoke-stale", "stale.smoke.test", "include "+cacheSnippet+"; proxy_cache_valid 200 2s;")
	path := fmt.Sprintf("/stale-%d", time.Now().UnixNano())
	if r := hostRequest(t, "stale.smoke.test", path); r.status != 200 || r.cache != "MISS" {
		t.Fatalf("priming GET → %d %q, want 200 MISS", r.status, r.cache)
	}
	time.Sleep(3 * time.Second) // past the 2s TTL
	mustCompose(t, "stop", "product-service")

	var r cacheHit
	if !eventually(reresolveWithin, func() bool {
		r = hostRequest(t, "stale.smoke.test", path)
		return r.cache == "STALE"
	}) {
		t.Errorf("expired key with product-service down → %d %q, want 200 STALE", r.status, r.cache)
	}
	if r.status != 200 {
		t.Errorf("STALE answer status %d, want 200", r.status)
	}

	// STALE is also served on a connect timeout, so the loop above can finish
	// while nginx still holds product-service's old address (→ 504). Wait out
	// the resolver so the uncached key sees "no address".
	time.Sleep(reresolveWithin)
	gw, _ := uniqueProduct("never-cached")
	if resp := mustRoute(t, "", gw); resp.status != http.StatusBadGateway || !strings.Contains(resp.body, `"error":"bad gateway"`) {
		t.Errorf("uncached key with product-service down → %d %q, want the JSON 502", resp.status, resp.body)
	}
}

func TestShouldRefreshAnExpiredKeyOnceTheServiceIsBack(t *testing.T) {
	restoreStack(t)
	cacheServer(t, "zz-smoke-refresh", "refresh.smoke.test", "include "+cacheSnippet+"; proxy_cache_valid 200 2s;")
	path := fmt.Sprintf("/refresh-%d", time.Now().UnixNano())
	hostRequest(t, "refresh.smoke.test", path)
	time.Sleep(3 * time.Second)
	mustCompose(t, "stop", "product-service")
	eventually(reresolveWithin, func() bool { return hostRequest(t, "refresh.smoke.test", path).cache == "STALE" })
	before := backendSaw(t, path)

	mustCompose(t, "start", "product-service")
	waitHealthy(t, "product-service")
	if !eventually(reresolveWithin+3*time.Second, func() bool {
		return hostRequest(t, "refresh.smoke.test", path).cache == "HIT"
	}) {
		t.Errorf("expired key never became a HIT again after product-service came back")
	}
	if after := backendSaw(t, path); after <= before {
		t.Errorf("product-service saw the key %d times before and %d after recovery, want a refresh", before, after)
	}
}

func TestShouldShipTheLockAndStaleRules(t *testing.T) {
	b, err := os.ReadFile(filepath.Join(repoRoot, "nginx", "snippets", "cache-products.conf"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"proxy_cache_lock on;",
		"proxy_cache_lock_timeout 5s;",
		"proxy_cache_use_stale error timeout updating http_500 http_502 http_503 http_504;",
		"proxy_cache_background_update on;",
	} {
		if !strings.Contains(string(b), want) {
			t.Errorf("cache-products.conf lacks %q", want)
		}
	}
}
