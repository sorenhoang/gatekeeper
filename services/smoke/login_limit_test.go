//go:build smoke

package smoke

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"
)

// Since Phase 8 login is auth-service's POST /login; the spikes send GET, so
// the requests that pass the limit are answered 405.
const loginPath = "/api/auth/login"

type tooMany struct {
	Error  string `json:"error"`
	Status int    `json:"status"`
	Route  string `json:"route"`
}

func assertTooMany(t *testing.T, h hit, route string) {
	t.Helper()
	if ct := h.header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Errorf("429 Content-Type %q, want application/json", ct)
	}
	var b tooMany
	if err := json.Unmarshal([]byte(h.body), &b); err != nil || b != (tooMany{Error: "too many requests", Status: 429, Route: route}) {
		t.Errorf("429 body %q (err %v), want {error: too many requests, status: 429, route: %s}", h.body, err, route)
	}
	if got := h.header.Get("Retry-After"); got != "1" {
		t.Errorf("429 Retry-After %q, want 1", got)
	}
	if got := h.header.Get("X-Route"); got != route {
		t.Errorf("429 X-Route %q, want %s", got, route)
	}
	if ids := h.header.Values("X-Request-Id"); len(ids) != 1 || ids[0] != h.id {
		t.Errorf("429 X-Request-ID %q, want exactly [%s]", ids, h.id)
	}
}

func TestShouldLetElevenLoginsThroughASpikeAndRejectTheRest(t *testing.T) {
	afterDrain(t)
	since := time.Now().Add(-time.Second)
	hits := spike(t, 50, "", loginPath, nil)
	if c := countStatus(hits); c[405] != 11 || c[429] != 39 {
		t.Fatalf("50 parallel logins → %v, want 11 × 405 and 39 × 429 (1 + burst 10, nodelay)", c)
	}

	var rejected []hit
	for _, h := range hits {
		switch h.status {
		case http.StatusMethodNotAllowed:
			if !strings.Contains(h.body, "use POST") {
				t.Errorf("405 login not from auth-service /login: %q", h.body)
			}
		case http.StatusTooManyRequests:
			assertTooMany(t, h, "login")
			rejected = append(rejected, h)
		}
	}

	time.Sleep(500 * time.Millisecond)
	for _, h := range rejected[:5] {
		// Exact match: spike ids share a prefix (…-1 is a substring of …-10).
		for _, l := range logLinesWith(t, "user-service", "request_id="+h.id) {
			if strings.HasSuffix(strings.TrimSpace(l), "request_id="+h.id) {
				t.Errorf("rejected login %s reached user-service: %s", h.id, l)
			}
		}
		lines := linesFor(t, since, h.id)
		if len(lines) != 1 || lines[0]["status"] != "429" || lines[0]["limit_req"] != "REJECTED" {
			t.Errorf("rejected login %s: gateway log %v, want one line with status 429 and limit_req REJECTED", h.id, lines)
		}
	}
}

func TestShouldAcceptALoginAgainAfterRetryAfter(t *testing.T) {
	afterDrain(t)
	if c := countStatus(spike(t, 50, "", loginPath, nil)); c[429] == 0 {
		t.Fatalf("setup: the spike was not limited (%v)", c)
	}
	time.Sleep(time.Second) // Retry-After: 1; 5r/s frees 5 slots per second
	if r := withIDs(t, http.MethodGet, "", loginPath, nil); r.status != http.StatusMethodNotAllowed {
		t.Errorf("login 1s after the spike = %d, want 405 (answered, not limited)", r.status)
	}
}

func TestShouldKeepTheLoginLimitOffOtherRoutes(t *testing.T) {
	afterDrain(t)
	if c := countStatus(spike(t, 50, "", loginPath, nil)); c[429] == 0 {
		t.Fatalf("setup: the spike was not limited (%v)", c)
	}
	if r := withIDs(t, http.MethodGet, "", "/api/users/", nil); r.status != http.StatusOK {
		t.Errorf("GET /api/users/ right after a login spike = %d, want 200", r.status)
	}
}
