//go:build smoke

package smoke

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

var allowedOrigins = []string{"https://app.localhost:8443", "http://localhost:5173"}

func preflight(t *testing.T, origin, path, method string) authAnswer {
	t.Helper()
	return authed(t, http.MethodOptions, path, "", map[string]string{
		"Origin": origin, "Access-Control-Request-Method": method,
		"Access-Control-Request-Headers": "authorization, content-type",
	})
}

func TestShouldAnswerPreflightsFromAllowedOriginsWithoutAToken(t *testing.T) {
	for _, origin := range allowedOrigins {
		a := preflight(t, origin, "/api/orders/", http.MethodDelete)
		if a.status != http.StatusNoContent {
			t.Errorf("preflight from %s → %d, want 204 (no token needed)", origin, a.status)
			continue
		}
		want := map[string]string{
			"Access-Control-Allow-Origin":  origin,
			"Access-Control-Allow-Headers": "Authorization, Content-Type, X-Request-ID",
			"Access-Control-Max-Age":       "600",
		}
		for k, v := range want {
			if got := a.header.Get(k); got != v {
				t.Errorf("preflight from %s: %s = %q, want %q", origin, k, got, v)
			}
		}
		methods := a.header.Get("Access-Control-Allow-Methods")
		for _, m := range []string{"GET", "POST", "DELETE", "OPTIONS"} {
			if !strings.Contains(methods, m) {
				t.Errorf("preflight from %s: Allow-Methods %q lacks %s", origin, methods, m)
			}
		}
		if !strings.Contains(strings.Join(a.header.Values("Vary"), ","), "Origin") {
			t.Errorf("preflight from %s: Vary %q lacks Origin", origin, a.header.Values("Vary"))
		}
	}
}

func TestShouldGiveOtherOriginsNoCORSHeaders(t *testing.T) {
	a := preflight(t, "https://evil.example", "/api/orders/", http.MethodDelete)
	for k := range a.header {
		if strings.HasPrefix(k, "Access-Control-Allow-") {
			t.Errorf("preflight from https://evil.example got %s: %q", k, a.header.Get(k))
		}
	}
	if g := authed(t, http.MethodGet, "/api/users/", "Bearer "+freshToken("user"), map[string]string{"Origin": "https://evil.example"}); g.header.Get("Access-Control-Allow-Origin") != "" {
		t.Errorf("GET from https://evil.example got Access-Control-Allow-Origin %q", g.header.Get("Access-Control-Allow-Origin"))
	}
}

func TestShouldAddCORSToRealAnswersForAllowedOrigins(t *testing.T) {
	origin := allowedOrigins[0]
	a := authed(t, http.MethodGet, "/api/users/", "Bearer "+freshToken("user"), map[string]string{"Origin": origin})
	if a.status != 200 || a.header.Get("Access-Control-Allow-Origin") != origin {
		t.Fatalf("GET /api/users/ from %s → %d ACAO %q, want 200 %s", origin, a.status, a.header.Get("Access-Control-Allow-Origin"), origin)
	}
	if got := a.header.Get("Access-Control-Expose-Headers"); got != "X-Request-ID" {
		t.Errorf("Access-Control-Expose-Headers %q, want X-Request-ID", got)
	}
	if !strings.Contains(strings.Join(a.header.Values("Vary"), ","), "Origin") {
		t.Errorf("Vary %q lacks Origin", a.header.Values("Vary"))
	}
	if got := a.header.Get("Access-Control-Allow-Credentials"); got != "" {
		t.Errorf("Access-Control-Allow-Credentials %q, want none (tokens travel in Authorization, not cookies)", got)
	}
}

// Without CORS headers on errors, a browser hides the real status from the app.
func TestShouldAddCORSToTheJSONErrorPages(t *testing.T) {
	origin := allowedOrigins[1]
	o := map[string]string{"Origin": origin}
	for name, a := range map[string]authAnswer{
		"401": authed(t, http.MethodGet, "/api/users/", "", o),
		"403": authed(t, http.MethodDelete, "/api/orders/x", "Bearer "+freshToken("user"), o),
	} {
		if a.header.Get("Access-Control-Allow-Origin") != origin {
			t.Errorf("JSON %s (%d) from %s: ACAO %q, want %s", name, a.status, origin, a.header.Get("Access-Control-Allow-Origin"), origin)
		}
	}
	afterDrain(t)
	var limited *hit
	for _, h := range spike(t, 30, "", loginPath, o) {
		if h.status == http.StatusTooManyRequests {
			limited = &h
			break
		}
	}
	if limited == nil || limited.header.Get("Access-Control-Allow-Origin") != origin {
		t.Errorf("JSON 429 from %s: %v, want Access-Control-Allow-Origin %s", origin, limited, origin)
	}
}

// add_header applies when the answer is served, so a cached products answer
// gets each requesting origin's own header.
func TestShouldGiveEachOriginItsOwnHeaderOnACachedAnswer(t *testing.T) {
	gw, _ := uniqueProduct("cors")
	first := authed(t, http.MethodGet, gw, "", map[string]string{"Origin": allowedOrigins[0]})
	second := authed(t, http.MethodGet, gw, "", map[string]string{"Origin": allowedOrigins[1]})
	if first.header.Get("X-Cache-Status") != "MISS" || second.header.Get("X-Cache-Status") != "HIT" {
		t.Fatalf("setup: %q then %q, want MISS then HIT", first.header.Get("X-Cache-Status"), second.header.Get("X-Cache-Status"))
	}
	if first.header.Get("Access-Control-Allow-Origin") != allowedOrigins[0] || second.header.Get("Access-Control-Allow-Origin") != allowedOrigins[1] {
		t.Errorf("ACAO MISS %q, HIT %q; want %s then %s", first.header.Get("Access-Control-Allow-Origin"),
			second.header.Get("Access-Control-Allow-Origin"), allowedOrigins[0], allowedOrigins[1])
	}
}

func TestShouldDocumentAuthAndCORSInTheReadme(t *testing.T) {
	b, err := os.ReadFile(filepath.Join(repoRoot, "GUIDE.md"))
	if err != nil {
		t.Fatal(err)
	}
	readme := string(b)
	start := strings.Index(readme, "\n## Auth & CORS")
	if start < 0 {
		t.Fatal(`README has no "## Auth & CORS" section`)
	}
	section := readme[start+1:]
	if end := strings.Index(section[3:], "\n## "); end >= 0 {
		section = section[:end+3]
	}
	for _, topic := range []string{"make secrets", "POST /api/auth/login", "auth_request", "/verify", "X-User-ID", "X-User-Role",
		"503", "auth unavailable", "10s", "exp", "limit_req", "njs", "admin", "DELETE", "403", "auth_basic",
		"Access-Control-Allow-Origin", "https://app.localhost:8443", "http://localhost:5173", "preflight", "Max-Age", "308"} {
		if !strings.Contains(section, topic) {
			t.Errorf("README Auth & CORS section does not cover %q", topic)
		}
	}
}
