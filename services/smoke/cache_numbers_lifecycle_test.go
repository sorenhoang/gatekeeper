//go:build smoke && lifecycle

package smoke

import (
	"fmt"
	"testing"
	"time"
)

// nginx never stores a response that sets a cookie: it may be one client's
// session.
func TestShouldNeverCacheAResponseThatSetsACookie(t *testing.T) {
	writeConf(t, "zz-smoke-cookie.conf", `server {
    listen 127.0.0.1:8090;
    location / {
        add_header Set-Cookie "session=abc";
        return 200 "{\"ok\":true}";
    }
}
server {
    listen 80;
    server_name cookie.smoke.test;
    location / {
        include /etc/nginx/gatekeeper/snippets/cache-products.conf;
        proxy_cache_valid 200 30s;
        proxy_pass http://127.0.0.1:8090;
    }
}
`)
	if out, err := nginxCmd(t, "-s", "reload"); err != nil {
		t.Fatalf("reload with the Set-Cookie backend: %v\n%s", err, out)
	}
	time.Sleep(time.Second)
	path := fmt.Sprintf("/cookie-%d", time.Now().UnixNano())
	for i := 0; i < 3; i++ {
		if r := hostRequest(t, "cookie.smoke.test", path); r.status != 200 || r.cache != "MISS" {
			t.Errorf("GET #%d of a Set-Cookie response → %d %q, want 200 MISS every time", i+1, r.status, r.cache)
		}
	}
}

// The README's before/after numbers: a 300ms service, ten GETs of one key.
func TestShouldAnswerRepeatsFromTheCacheWithoutTheServiceDelay(t *testing.T) {
	withServiceEnv(t, "product-service", "DELAY_MS=300")
	gw, be := uniqueProduct("numbers")
	var times []time.Duration
	for i := 0; i < 10; i++ {
		r := timedGet(t, gw)
		if r.status != 200 {
			t.Fatalf("GET #%d = %d, want 200", i+1, r.status)
		}
		times = append(times, r.elapsed)
	}
	t.Logf("10 GETs of one key with a 300ms service: %v", times)
	if times[0] < 300*time.Millisecond {
		t.Errorf("first GET took %v, want at least the 300ms service delay (a MISS)", times[0])
	}
	for i, d := range times[1:] {
		if d >= 100*time.Millisecond {
			t.Errorf("GET #%d took %v, want under 100ms (a HIT)", i+2, d)
		}
	}
	if n := backendSaw(t, be); n != 1 {
		t.Errorf("product-service saw the key %d times over 10 GETs, want 1", n)
	}
}
