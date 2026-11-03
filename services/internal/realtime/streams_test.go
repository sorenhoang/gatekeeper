package realtime

import (
	"bufio"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"gatekeeper/services/internal/echo"
)

// The real stack: handlers behind the shared logging/chaos middleware, so
// Flush (SSE) and Hijack (WebSocket) are proven to work through its wrapper.
func wrappedServer(t *testing.T, cfg Config) (*httptest.Server, *Streams) {
	t.Helper()
	mux := http.NewServeMux()
	streams := Register(mux, cfg)
	s := httptest.NewServer(echo.Wrap(mux, "realtime-service", echo.Config{}, io.Discard))
	t.Cleanup(s.Close)
	return s, streams
}

var timed = &http.Client{Timeout: 5 * time.Second}

func openSSE(t *testing.T, url string, hdr http.Header) (*http.Response, *bufio.Scanner) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, url, nil)
	for k, v := range hdr {
		req.Header[k] = v
	}
	resp, err := timed.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	return resp, bufio.NewScanner(resp.Body)
}

func TestShouldStreamAndUpgradeThroughTheLoggingMiddleware(t *testing.T) {
	s, _ := wrappedServer(t, fast)
	_, sc := openSSE(t, s.URL+"/events", nil)
	if !sc.Scan() || sc.Text() != "id: 1" {
		t.Errorf("SSE through echo.Wrap: first line %q, want id: 1 (Flush through the wrapper)", sc.Text())
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(s.URL, "http")+"/ws", nil)
	if err != nil {
		t.Fatalf("WebSocket through echo.Wrap: %v (Hijack through the wrapper)", err)
	}
	defer c.CloseNow()
	c.Write(ctx, websocket.MessageText, []byte("hi"))
	if _, b, err := c.Read(ctx); err != nil || string(b) != "anonymous: hi" {
		t.Errorf("echo through echo.Wrap → %q, %v", b, err)
	}
}

// Shutdown must not wait for streams that never end on their own: Close
// ends every open SSE stream and closes every WebSocket with 1001.
func TestShouldEndOpenStreamsOnClose(t *testing.T) {
	cfg := fast
	cfg.PingInterval = time.Hour
	s, streams := wrappedServer(t, cfg)
	resp, sc := openSSE(t, s.URL+"/events", nil)
	sc.Scan()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(s.URL, "http")+"/ws", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer c.CloseNow()

	start := time.Now()
	streams.Close()
	done := make(chan struct{})
	go func() { io.Copy(io.Discard, resp.Body); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Error("SSE stream still open 1s after Close")
	}
	_, _, err = c.Read(ctx)
	if websocket.CloseStatus(err) != websocket.StatusGoingAway {
		t.Errorf("WebSocket after Close: %v (status %d), want close 1001 going away", err, websocket.CloseStatus(err))
	}
	if d := time.Since(start); d > 2*time.Second {
		t.Errorf("Close took %v to end the streams, want under 2s", d)
	}
	// New streams after Close are refused right away rather than left open.
	if r, err := timed.Get(s.URL + "/events"); err == nil {
		r.Body.Close()
		if r.StatusCode != http.StatusServiceUnavailable {
			t.Errorf("new SSE stream after Close → %d, want 503", r.StatusCode)
		}
	}
}

// A peer that stops answering pings is closed instead of being kept forever.
func TestShouldCloseAPeerThatStopsAnsweringPings(t *testing.T) {
	s, _ := wrappedServer(t, fast)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(s.URL, "http")+"/ws", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer c.CloseNow()
	// Not reading means pings go unanswered (coder/websocket replies to pings
	// only inside Read). Two intervals: one ping sent, its pong timed out.
	time.Sleep(400 * time.Millisecond)
	_, _, err = c.Read(ctx)
	if err == nil {
		t.Fatal("connection still delivering data after the peer ignored pings")
	}
	if st := websocket.CloseStatus(err); st != websocket.StatusPolicyViolation {
		t.Errorf("server closed with %v (status %d), want 1008 policy violation (ping timeout)", err, st)
	}
}

func TestShouldRestartNumberingOnAnOverflowingLastEventID(t *testing.T) {
	s, _ := wrappedServer(t, fast)
	_, sc := openSSE(t, s.URL+"/events", http.Header{"Last-Event-Id": {"9223372036854775807"}})
	if !sc.Scan() || sc.Text() != "id: 1" {
		t.Errorf("Last-Event-ID at MaxInt64 → first line %q, want id: 1", sc.Text())
	}
}
