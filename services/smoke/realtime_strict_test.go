//go:build smoke

package smoke

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
)

// A query token is a realtime-only concession: the users and orders routes
// still want the header.
func TestShouldIgnoreAQueryTokenOnOtherRoutes(t *testing.T) {
	if a := authed(t, http.MethodGet, "/api/users/?access_token="+freshToken("user"), "", nil); a.status != 401 {
		t.Errorf("/api/users/?access_token=<valid> → %d, want 401", a.status)
	}
}

// Anything that could smuggle a second or disguised token is refused before
// auth; a different capitalisation is still stripped (nginx's $arg_ lookup
// ignores case, so the strip must too).
func TestShouldRefuseAmbiguousAccessTokens(t *testing.T) {
	tok := freshToken("user")
	for _, q := range []string{
		"access_token=" + tok + "&access_token=" + tok,
		"a=1&access_token=" + tok + "&Access_Token=" + tok,
		"access%5Ftoken=" + tok,
		"ACCESS%5fTOKEN=" + tok,
	} {
		if a := authed(t, http.MethodGet, "/api/events/?"+q, "", nil); a.status != http.StatusBadRequest {
			t.Errorf("/api/events/?%s → %d, want 400", strings.ReplaceAll(q, tok, "<token>"), a.status)
		}
	}
	writeConf(t, "zz-smoke-args2.conf", `server {
    listen 80;
    server_name args2.smoke.test;
    location / { return 200 "[$args_without_token]"; }
}
`)
	if out, err := nginxCmd(t, "-s", "reload"); err != nil {
		t.Fatalf("reload: %v\n%s", err, out)
	}
	time.Sleep(time.Second)
	for query, want := range map[string]string{
		"Access_Token=SECRET&a=1": "[a=1]",
		"a=1&ACCESS_TOKEN=SECRET": "[a=1]",
	} {
		if r := mustRoute(t, "args2.smoke.test", "/?"+query); r.body != want {
			t.Errorf("?%s → %s, want %s", query, r.body, want)
		}
	}
}

// The paths where nginx writes an error-log line (rejections) must not
// print the query either.
func TestShouldNotLogAQueryTokenWhenARealtimeRequestIsRefused(t *testing.T) {
	tok := freshToken("user")
	sig := tok[strings.LastIndex(tok, ".")+1:]
	var open []*websocket.Conn
	defer func() {
		for _, c := range open {
			c.CloseNow()
		}
	}()
	for i := 0; i < 10; i++ {
		c, _, err := wsDial(t, tok, true)
		if err != nil {
			t.Fatalf("WebSocket #%d: %v", i+1, err)
		}
		open = append(open, c)
	}
	if _, resp, err := wsDial(t, tok, true); err == nil || resp == nil || resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("11th WebSocket with ?access_token= → %v, %v; want 429", resp, err)
	}
	time.Sleep(500 * time.Millisecond)
	if logs := mustCompose(t, "logs", "--no-log-prefix", "--since", "1m", "gateway"); strings.Contains(logs, sig) {
		t.Error("gateway logs contain the token after a refused (limit_conn) realtime request")
	}
}

// The request field shows what the client asked for, even when nginx
// rewrites the URI internally (SPA fallback to /index.html).
func TestShouldLogTheRequestedPathWithoutTheQuery(t *testing.T) {
	since := time.Now().Add(-time.Second)
	id := runID("logpath")
	withIDs(t, http.MethodGet, "", "/some/spa/route?x=1", nil, id)
	time.Sleep(300 * time.Millisecond)
	lines := linesFor(t, since, id)
	if len(lines) != 1 || !strings.HasPrefix(lines[0]["request"], "GET /some/spa/route HTTP/") {
		t.Errorf("access log for %s = %v, want request \"GET /some/spa/route HTTP/…\" (requested path, no query)", id, lines)
	}
}

// Only the WebSocket route forwards an upgrade.
func TestShouldNotForwardUpgradeOnOtherRoutes(t *testing.T) {
	a := authed(t, http.MethodGet, "/api/users/", "Bearer "+freshToken("user"),
		map[string]string{"Upgrade": "websocket", "Connection": "Upgrade"})
	var e struct{ Headers map[string][]string }
	json.Unmarshal([]byte(a.body), &e)
	if up := e.Headers["Upgrade"]; len(up) > 0 {
		t.Errorf("/api/users/ forwarded Upgrade %q to user-service", up)
	}
	if c := strings.Join(e.Headers["Connection"], ","); strings.Contains(strings.ToLower(c), "upgrade") {
		t.Errorf("/api/users/ forwarded Connection %q to user-service", c)
	}
}
