//go:build smoke && lifecycle

package smoke

import (
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
)

// 20s idle: longer than any old route timeout (orders 15s). The service's
// 5s pings keep the connection from looking silent.
func TestShouldKeepAnIdleWebSocketOpenPastTheOldTimeouts(t *testing.T) {
	withServiceEnv(t, "realtime-service", "WS_PING_INTERVAL=5s")
	c, _, err := wsDial(t, freshToken("user"), false)
	if err != nil {
		t.Fatal(err)
	}
	defer c.CloseNow()
	// coder/websocket answers pings only while reading; read in the background.
	got := make(chan string, 1)
	go func() {
		_, b, err := c.Read(t.Context())
		if err == nil {
			got <- string(b)
		}
	}()
	time.Sleep(20 * time.Second)
	if err := c.Write(t.Context(), websocket.MessageText, []byte("still here")); err != nil {
		t.Fatalf("write after 20s idle: %v", err)
	}
	select {
	case b := <-got:
		if b != "alice: still here" {
			t.Errorf("echo after 20s idle → %q, want \"alice: still here\"", b)
		}
	case <-time.After(time.Second):
		t.Error("no echo within 1s after 20s idle: the connection was cut")
	}
}

func TestShouldKeepAnSSEStreamOpenPastTheOldTimeouts(t *testing.T) {
	withServiceEnv(t, "realtime-service", "SSE_EVENT_INTERVAL=1h", "SSE_HEARTBEAT_INTERVAL=5s")
	_, lines := sseOpen(t, "/api/events/", "Bearer "+freshToken("user"))
	start, beats := time.Now(), 0
	for time.Since(start) < 20*time.Second {
		select {
		case l, ok := <-lines:
			if !ok {
				t.Fatalf("SSE stream closed after %v", time.Since(start))
			}
			if l == ": heartbeat" {
				beats++
			}
		case <-time.After(10 * time.Second):
			t.Fatalf("SSE stream silent for 10s at %v (heartbeat every 5s)", time.Since(start))
		}
	}
	if beats < 3 {
		t.Errorf("%d heartbeats in 20s, want at least 3 at 5s", beats)
	}
}

// A reload starts new workers; old ones keep their streams until
// worker_shutdown_timeout (30s) and then close them.
func TestShouldDrainOpenStreamsWithinTheShutdownTimeoutOnReload(t *testing.T) {
	restoreStack(t)
	c, _, err := wsDial(t, freshToken("user"), false)
	if err != nil {
		t.Fatal(err)
	}
	defer c.CloseNow()
	if out, err := runMake(t, "reload"); err != nil {
		t.Fatalf("make reload: %v\n%s", err, out)
	}
	echoWithin(t, c, "after reload", "alice: after reload", time.Second)
	closed := make(chan time.Duration, 1)
	start := time.Now()
	go func() {
		for {
			if _, _, err := c.Read(t.Context()); err != nil {
				closed <- time.Since(start)
				return
			}
		}
	}()
	select {
	case d := <-closed:
		t.Logf("old worker closed the stream %v after the reload", d)
	case <-time.After(35 * time.Second):
		t.Error("the stream was still open 35s after a reload (worker_shutdown_timeout 30s + 5s)")
	}
	if out := mustCompose(t, "exec", "-T", "gateway", "sh", "-c", "ps -o args | grep -c '[n]ginx: worker process is shutting down' ; true"); strings.TrimSpace(out) != "0" {
		t.Errorf("%s old workers still shutting down after the timeout", strings.TrimSpace(out))
	}
}

func TestShouldStreamServerSentEventsPromptly(t *testing.T) {
	// A 500ms interval: with the stack's 1s default, timer jitter alone
	// would push a gap past the 1s bound and hide what this test measures.
	withServiceEnv(t, "realtime-service", "SSE_EVENT_INTERVAL=500ms")
	resp, lines := sseOpen(t, "/api/events/", "Bearer "+freshToken("user"))
	if resp.StatusCode != 200 || !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
		t.Fatalf("GET /api/events/ → %d %q, want 200 text/event-stream", resp.StatusCode, resp.Header.Get("Content-Type"))
	}
	last := time.Now()
	for got := 0; got < 3; {
		select {
		case l, ok := <-lines:
			if !ok {
				t.Fatalf("stream ended after %d events", got)
			}
			if strings.HasPrefix(l, "data: ") {
				if gap := time.Since(last); gap > time.Second {
					t.Errorf("event %d arrived %v after the previous one, want within 1s (SSE_EVENT_INTERVAL 1s)", got+1, gap)
				}
				if !strings.HasPrefix(l, "data: alice ") {
					t.Errorf("event %q does not name the caller alice", l)
				}
				last = time.Now()
				got++
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("no event for 2s after %d events", got)
		}
	}
	if r, _ := sseOpen(t, "/api/events/", ""); r.StatusCode != 401 {
		t.Errorf("GET /api/events/ without a token → %d, want 401", r.StatusCode)
	}
}
