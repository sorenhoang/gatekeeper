//go:build smoke

package smoke

import (
	"fmt"
	"net/http"
	"testing"
	"time"
)

const (
	knownAPIKey = "lab-key-alpha"
	apiRate     = 50  // r/s
	apiBurst    = 100 // requests
)

func drainAPIBucket(t *testing.T) []hit {
	t.Helper()
	hits := spike(t, 150, "", "/api/products/", nil)
	if countStatus(hits)[429] == 0 {
		t.Fatalf("setup: 150 parallel API requests were not limited (%v)", countStatus(hits))
	}
	return hits
}

func TestShouldLimitAnAPISpikeToBurstPlusRefill(t *testing.T) {
	afterDrain(t)
	start := time.Now()
	hits := spike(t, 150, "", "/api/products/", nil)
	took := time.Since(start).Seconds()
	c := countStatus(hits)
	// 1 + burst pass at once; the bucket refills at apiRate while the spike
	// is still arriving (1.5x margin on the refill).
	low, high := 1+apiBurst, 1+apiBurst+int(apiRate*took*1.5)
	if c[200] < low || c[200] > high {
		t.Errorf("150 parallel requests in %.2fs → %v, want %d–%d × 200 and the rest 429", took, c, low, high)
	}
	if c[200]+c[429] != 150 {
		t.Errorf("unexpected statuses in the spike: %v", c)
	}
	for _, h := range hits {
		if h.status == http.StatusTooManyRequests {
			assertTooMany(t, h, "products")
			break
		}
	}
}

// The zone refills one slot every 20ms (50r/s), so a single request after the
// drain usually finds a fresh slot. A burst of 10 arriving within a few ms can
// find at most one or two.
const (
	followBurst   = 10
	minFollowDeny = 8
)

// One bucket per client for the whole API, not per route.
func TestShouldShareTheAPIBudgetAcrossRoutes(t *testing.T) {
	afterDrain(t)
	drainAPIBucket(t)
	if c := countStatus(spike(t, followBurst, "", "/api/users/", nil)); c[429] < minFollowDeny {
		t.Errorf("%d parallel GET /api/users/ with the host's API bucket drained → %v, want at least %d × 429", followBurst, c, minFollowDeny)
	}
}

func apiKeyRequest(t *testing.T, key string) int {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, gatewayURL+"/api/users/", nil)
	req.Header.Set("X-API-Key", key)
	resp, err := (&http.Client{Timeout: 5 * time.Second}).Do(req)
	if err != nil {
		t.Fatalf("GET with X-API-Key %q: %v", key, err)
	}
	resp.Body.Close()
	return resp.StatusCode
}

// A known key has its own bucket; an invented one is counted against the
// sender's IP, so rotating random keys buys nothing.
func TestShouldGiveOnlyKnownAPIKeysTheirOwnBudget(t *testing.T) {
	afterDrain(t)
	drainAPIBucket(t)
	if got := apiKeyRequest(t, knownAPIKey); got != http.StatusOK {
		t.Errorf("known key %s with the IP bucket drained = %d, want 200 (own bucket)", knownAPIKey, got)
	}
	random := fmt.Sprintf("random-%d", time.Now().UnixNano())
	c := countStatus(spike(t, followBurst, "", "/api/users/", map[string]string{"X-API-Key": random}))
	if c[429] < minFollowDeny {
		t.Errorf("%d parallel requests with invented key %s, IP bucket drained → %v, want at least %d × 429 (counted against the IP)", followBurst, random, c, minFollowDeny)
	}
}

func TestShouldRefillTheAPIBudget(t *testing.T) {
	afterDrain(t)
	drainAPIBucket(t)
	time.Sleep(drainWait)
	if r := withIDs(t, http.MethodGet, "", "/api/products/", nil); r.status != http.StatusOK {
		t.Errorf("GET %s after the bucket refilled = %d, want 200", drainWait, r.status)
	}
}
