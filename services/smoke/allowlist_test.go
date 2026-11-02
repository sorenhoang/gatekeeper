//go:build smoke

package smoke

import (
	"net/http"
	"strings"
	"testing"
	"time"
)

const allowlistBurst = 150

// burstFromGateway fires `allowlistBurst` sequential requests from inside the
// gateway container at addr, in one shell, and returns how many got 429 and
// how fast they went. A loop slower than the API budget (50 r/s) would prove
// nothing, so the rate is returned for the caller to check.
func burstFromGateway(t *testing.T, addr string) (rejected int, perSecond float64) {
	t.Helper()
	script := `n=0; for i in $(seq ` + itoa(allowlistBurst) + `); do ` +
		`wget -q -O /dev/null --no-check-certificate "https://` + addr + `:443/api/products/" 2>&1 | grep -q 429 && n=$((n+1)); ` +
		`done; echo "rejected=$n"`
	start := time.Now()
	out := mustCompose(t, "exec", "-T", "gateway", "sh", "-c", script)
	took := time.Since(start).Seconds()
	for _, l := range strings.Split(out, "\n") {
		if strings.HasPrefix(l, "rejected=") {
			for _, c := range strings.TrimPrefix(l, "rejected=") {
				if c >= '0' && c <= '9' {
					rejected = rejected*10 + int(c-'0')
				}
			}
			return rejected, float64(allowlistBurst) / took
		}
	}
	t.Fatalf("no result from the in-gateway burst:\n%s", out)
	return 0, 0
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for ; n > 0; n /= 10 {
		b = append([]byte{byte('0' + n%10)}, b...)
	}
	return string(b)
}

func TestShouldNeverLimitTheComposeNetworkOrLoopback(t *testing.T) {
	afterDrain(t)
	ip := strings.TrimSpace(mustCompose(t, "exec", "-T", "gateway", "hostname", "-i"))
	for name, addr := range map[string]string{"compose network": ip, "loopback": "127.0.0.1"} {
		rejected, rate := burstFromGateway(t, addr)
		if rate <= apiRate {
			t.Errorf("%s: the burst ran at %.0f r/s, not above the %d r/s budget, so it proves nothing", name, rate, apiRate)
		}
		if rejected != 0 {
			t.Errorf("%s: %d of %d requests got 429 at %.0f r/s, want 0 (allowlisted)", name, rejected, allowlistBurst, rate)
		}
	}
}

// The allowlist reads the TCP peer; a client can't claim to be internal.
func TestShouldIgnoreForwardingHeadersThatClaimAnAllowlistedAddress(t *testing.T) {
	afterDrain(t)
	hits := spike(t, allowlistBurst, "", "/api/products/", map[string]string{
		"X-Forwarded-For": "172.17.0.1",
		"X-Real-IP":       "127.0.0.1",
	})
	if c := countStatus(hits); c[429] == 0 {
		t.Errorf("host spike claiming an internal address via headers → %v, want 429s (headers must not exempt)", c)
	}
}

func TestShouldNotLimitPagesOutsideTheAPI(t *testing.T) {
	afterDrain(t)
	drainAPIBucket(t)
	for _, path := range []string{"/health", "/settings", "/assets/app.css"} {
		if r := withIDs(t, http.MethodGet, "", path, nil); r.status != http.StatusOK {
			t.Errorf("GET %s with the host's API bucket drained = %d, want 200", path, r.status)
		}
	}
}
