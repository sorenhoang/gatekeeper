//go:build smoke

package smoke

import (
	"bufio"
	"context"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
)

const wssURL = "wss://localhost:8443"

// The handshake is HTTP/1.1 (nginx has no WebSocket over HTTP/2, RFC 8441):
// a transport with its own TLS config and no ForceAttemptHTTP2 offers only
// http/1.1 in ALPN.
func wsClient(nextProtos ...string) *http.Client {
	cfg := labTLS.Clone()
	cfg.NextProtos = nextProtos
	return &http.Client{Timeout: 10 * time.Second, Transport: &http.Transport{TLSClientConfig: cfg}}
}

// wsDial connects to /ws/ with the token in the Authorization header, or in
// ?access_token= when inQuery (how a browser has to send it).
func wsDial(t *testing.T, token string, inQuery bool) (*websocket.Conn, *http.Response, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	url, hdr := wssURL+"/ws/", http.Header{}
	switch {
	case token != "" && inQuery:
		url += "?access_token=" + token
	case token != "":
		hdr.Set("Authorization", "Bearer "+token)
	}
	return websocket.Dial(ctx, url, &websocket.DialOptions{HTTPClient: wsClient(), HTTPHeader: hdr})
}

func echoWithin(t *testing.T, c *websocket.Conn, msg, want string, within time.Duration) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), within)
	defer cancel()
	if err := c.Write(ctx, websocket.MessageText, []byte(msg)); err != nil {
		t.Fatalf("write %q: %v", msg, err)
	}
	_, b, err := c.Read(ctx)
	if err != nil || string(b) != want {
		t.Errorf("echo of %q → %q, %v; want %q within %v", msg, b, err, want, within)
	}
}

func TestShouldProxyAWebSocketWithAToken(t *testing.T) {
	for name, inQuery := range map[string]bool{"Authorization header": false, "access_token query": true} {
		c, resp, err := wsDial(t, freshToken("user"), inQuery)
		if err != nil {
			t.Errorf("%s: dial: %v", name, err)
			continue
		}
		if resp.StatusCode != http.StatusSwitchingProtocols {
			t.Errorf("%s: handshake status %d, want 101", name, resp.StatusCode)
		}
		echoWithin(t, c, "hi", "alice: hi", time.Second)
		c.Close(websocket.StatusNormalClosure, "")
	}
}

func TestShouldRefuseAWebSocketWithoutAToken(t *testing.T) {
	_, resp, err := wsDial(t, "", false)
	if err == nil {
		t.Fatal("WebSocket without a token was accepted")
	}
	if resp == nil || resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("handshake without a token → %v, %v; want 401 before any upgrade", resp, err)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Errorf("401 Content-Type %q, want application/json", ct)
	}
}

// nginx has no WebSocket over HTTP/2 (RFC 8441), yet a client whose ALPN
// offered only h2 still gets its WebSocket: the upgrade request goes out as
// HTTP/1.1 and nginx accepts HTTP/1.1 on that connection (measured).
func TestShouldUpgradeOverHTTP11EvenWhenALPNSaidH2(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cfg := labTLS.Clone()
	cfg.NextProtos = []string{"h2"}
	// ForceAttemptHTTP2 with its default TLSNextProto: the client really
	// speaks HTTP/2 on the connection, as a browser offering h2 would.
	h2 := &http.Client{Transport: &http.Transport{TLSClientConfig: cfg, ForceAttemptHTTP2: true}}
	hdr := http.Header{"Authorization": {"Bearer " + freshToken("user")}}
	c, resp, err := websocket.Dial(ctx, wssURL+"/ws/", &websocket.DialOptions{HTTPClient: h2, HTTPHeader: hdr})
	if err != nil {
		t.Fatalf("a client offering only h2: %v", err)
	}
	defer c.CloseNow()
	if resp.StatusCode != http.StatusSwitchingProtocols || resp.ProtoMajor != 1 {
		t.Errorf("handshake answer %s %d, want HTTP/1.1 101", resp.Proto, resp.StatusCode)
	}
	echoWithin(t, c, "hi", "alice: hi", time.Second)
}

// sseOpen starts an SSE stream through the gateway and returns its lines.
func sseOpen(t *testing.T, path, authz string) (*http.Response, <-chan string) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, gatewayURL+path, nil)
	req.Header.Set(anonymousHeader, "1")
	if authz != "" {
		req.Header.Set("Authorization", authz)
	}
	resp, err := (&http.Client{}).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	lines := make(chan string, 64)
	go func() {
		defer close(lines)
		sc := bufio.NewScanner(resp.Body)
		for sc.Scan() {
			if l := sc.Text(); l != "" {
				lines <- l
			}
		}
	}()
	return resp, lines
}

// The token in the query: removed from what the service gets (the
// $args_without_token map, checked on a temporary server that echoes it)
// and kept out of every log line.
func TestShouldStripTheAccessTokenFromTheQuery(t *testing.T) {
	writeConf(t, "zz-smoke-args.conf", `server {
    listen 80;
    server_name args.smoke.test;
    location / { return 200 "[$args_without_token]"; }
}
`)
	if out, err := nginxCmd(t, "-s", "reload"); err != nil {
		t.Fatalf("reload: %v\n%s", err, out)
	}
	time.Sleep(time.Second)
	for query, want := range map[string]string{
		"access_token=SECRET":         "[]",
		"access_token=SECRET&a=1":     "[a=1]",
		"a=1&access_token=SECRET":     "[a=1]",
		"a=1&access_token=SECRET&b=2": "[a=1&b=2]",
		"a=1&b=2":                     "[a=1&b=2]",
		"my_access_token=keep&a=1":    "[my_access_token=keep&a=1]",
	} {
		if r := mustRoute(t, "args.smoke.test", "/?"+query); r.body != want {
			t.Errorf("?%s → %s, want %s", query, r.body, want)
		}
	}
	b, err := os.ReadFile(filepath.Join(confDir, "default.conf"))
	if err != nil {
		t.Fatal(err)
	}
	for _, loc := range []string{"/ws", "/events"} {
		if !regexp.MustCompile(`proxy_pass http://realtime` + loc + `\$is_args\$args_without_token;`).Match(b) {
			t.Errorf("default.conf: the %s location does not proxy with $args_without_token", loc)
		}
	}
}

func TestShouldNeverLogARealtimeToken(t *testing.T) {
	since := time.Now().Add(-time.Second)
	tok := freshToken("user")
	c, _, err := wsDial(t, tok, true)
	if err != nil {
		t.Fatal(err)
	}
	echoWithin(t, c, "x", "alice: x", time.Second)
	c.Close(websocket.StatusNormalClosure, "")
	resp, lines := sseOpen(t, "/api/events/?access_token="+tok, "")
	if resp.StatusCode != 200 {
		t.Fatalf("SSE with ?access_token= → %d, want 200", resp.StatusCode)
	}
	<-lines
	resp.Body.Close()
	time.Sleep(500 * time.Millisecond)
	sig := tok[strings.LastIndex(tok, ".")+1:]
	for _, l := range accessLines(t, since) {
		for k, v := range l {
			if strings.Contains(v, sig) {
				t.Errorf("access log field %s carries the token: %q", k, v)
			}
		}
		if strings.Contains(l["request"], "?") {
			t.Errorf("access log request %q still has a query string", l["request"])
		}
	}
	if logs := mustCompose(t, "logs", "--no-log-prefix", "--since", "1m", "gateway"); strings.Contains(logs, sig) {
		t.Error("gateway logs contain the token")
	}
}

func TestShouldCapRealtimeConnectionsPerClient(t *testing.T) {
	var open []*websocket.Conn
	defer func() {
		for _, c := range open {
			c.CloseNow()
		}
	}()
	for i := 0; i < 10; i++ {
		c, _, err := wsDial(t, suiteToken, false)
		if err != nil {
			t.Fatalf("WebSocket #%d of 10: %v", i+1, err)
		}
		open = append(open, c)
	}
	_, resp, err := wsDial(t, suiteToken, false)
	if err == nil || resp == nil || resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("11th WebSocket → %v, %v; want 429", resp, err)
	}
	if b, _ := io.ReadAll(resp.Body); !strings.Contains(string(b), `"error":"too many connections"`) {
		t.Errorf("11th WebSocket body %q, want JSON too many connections", b)
	}
	open[0].Close(websocket.StatusNormalClosure, "")
	open = open[1:]
	// nginx frees the slot when it sees the close; allow it a moment.
	ok := false
	for deadline := time.Now().Add(5 * time.Second); !ok && time.Now().Before(deadline); time.Sleep(200 * time.Millisecond) {
		if c, _, err := wsDial(t, suiteToken, false); err == nil {
			open = append(open, c)
			ok = true
		}
	}
	if !ok {
		t.Error("after closing one of 10 WebSockets a new one was still refused")
	}
}
