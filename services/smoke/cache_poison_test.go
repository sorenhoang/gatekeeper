//go:build smoke

package smoke

import (
	"net/http"
	"strings"
	"testing"
)

// The echo reflects the Host header and the default server accepts any host,
// so a cache key without the host lets one client's Host end up in what every
// other client gets.
func TestShouldNotLetOneClientsHostPoisonTheCache(t *testing.T) {
	gw, _ := uniqueProduct("poison")
	evil := mustRoute(t, "evil.example", gw)
	if evil.status != http.StatusOK || !strings.Contains(evil.body, "evil.example") {
		t.Fatalf("setup: GET with Host evil.example → %d, body without evil.example:\n%s", evil.status, evil.body)
	}
	plain := mustRoute(t, "", gw)
	if strings.Contains(plain.body, "evil.example") {
		t.Errorf("a localhost client got the body cached for Host evil.example (cache key ignores the host):\n%s", plain.body)
	}
	if got := plain.header.Get("X-Cache-Status"); got != "MISS" {
		t.Errorf("same path, different host → X-Cache-Status %q, want MISS (separate entry)", got)
	}
}

func TestShouldTreatProxyAuthorizationAsACredential(t *testing.T) {
	gw, _ := uniqueProduct("proxyauth")
	hdr := map[string]string{"Proxy-Authorization": "Basic c2VjcmV0"}
	for i := 0; i < 2; i++ {
		if r := productsRequest(t, http.MethodGet, gw, hdr); r.cache != "BYPASS" {
			t.Errorf("Proxy-Authorization request #%d → %q, want BYPASS", i+1, r.cache)
		}
	}
	if r := productsRequest(t, http.MethodGet, gw, nil); r.cache != "MISS" {
		t.Errorf("after Proxy-Authorization requests, a plain one → %q, want MISS (nothing stored)", r.cache)
	}
}
