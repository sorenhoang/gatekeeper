package realtime

import (
	"bufio"
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/test/bufconn"
	"google.golang.org/protobuf/types/known/wrapperspb"
)

var fast = Config{PingInterval: 100 * time.Millisecond, EventInterval: 50 * time.Millisecond, HeartbeatInterval: 120 * time.Millisecond}

func newServer(t *testing.T, cfg Config) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	Register(mux, cfg)
	s := httptest.NewServer(mux)
	t.Cleanup(s.Close)
	return s
}

func TestShouldEchoWebSocketMessagesWithTheCallersIdentity(t *testing.T) {
	s := newServer(t, fast)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(s.URL, "http")+"/ws", &websocket.DialOptions{
		HTTPHeader: http.Header{"X-User-Id": {"alice"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer c.CloseNow()
	c.Write(ctx, websocket.MessageText, []byte("hi"))
	_, b, err := c.Read(ctx)
	if err != nil || string(b) != "alice: hi" {
		t.Errorf("echo → %q, %v; want \"alice: hi\"", b, err)
	}
}

// The server pings on its interval; coder/websocket answers pings while the
// client reads, and the server counts the pongs it got.
func TestShouldPingWebSocketClientsOnTheInterval(t *testing.T) {
	pongs := make(chan struct{}, 10)
	cfg := fast
	cfg.onPong = func() { pongs <- struct{}{} }
	s := newServer(t, cfg)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	c, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(s.URL, "http")+"/ws", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer c.CloseNow()
	go c.Read(ctx) // keeps reading so control frames are answered
	select {
	case <-pongs:
	case <-time.After(time.Second):
		t.Error("no ping answered within 1s with a 100ms ping interval")
	}
}

func readSSE(t *testing.T, url string, hdr http.Header, lines int) []string {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, url, nil)
	for k, v := range hdr {
		req.Header[k] = v
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	for k, want := range map[string]string{"Content-Type": "text/event-stream", "Cache-Control": "no-cache", "X-Accel-Buffering": "no"} {
		if got := resp.Header.Get(k); got != want {
			t.Errorf("SSE %s = %q, want %q", k, got, want)
		}
	}
	var out []string
	sc := bufio.NewScanner(resp.Body)
	for sc.Scan() && len(out) < lines {
		if l := sc.Text(); l != "" {
			out = append(out, l)
		}
	}
	return out
}

func TestShouldStreamNumberedEventsAndHeartbeats(t *testing.T) {
	s := newServer(t, fast)
	got := strings.Join(readSSE(t, s.URL+"/events", nil, 10), "\n")
	for _, want := range []string{"id: 1", "data: ", "id: 2", ": heartbeat"} {
		if !strings.Contains(got, want) {
			t.Errorf("SSE stream lacks %q:\n%s", want, got)
		}
	}
}

func TestShouldResumeAfterLastEventID(t *testing.T) {
	s := newServer(t, fast)
	got := readSSE(t, s.URL+"/events", http.Header{"Last-Event-Id": {"5"}}, 1)
	if len(got) == 0 || got[0] != "id: 6" {
		t.Errorf("first line after Last-Event-ID: 5 = %v, want id: 6", got)
	}
}

func grpcConn(t *testing.T) *grpc.ClientConn {
	t.Helper()
	lis := bufconn.Listen(1 << 20)
	srv := NewGRPCServer()
	go srv.Serve(lis)
	t.Cleanup(srv.Stop)
	conn, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) { return lis.DialContext(ctx) }),
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	return conn
}

func TestShouldServeGRPCHealthAndEcho(t *testing.T) {
	conn := grpcConn(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	h, err := healthpb.NewHealthClient(conn).Check(ctx, &healthpb.HealthCheckRequest{})
	if err != nil || h.GetStatus() != healthpb.HealthCheckResponse_SERVING {
		t.Errorf("Health/Check → %v, %v; want SERVING", h.GetStatus(), err)
	}
	out := &wrapperspb.StringValue{}
	ctx = metadata.AppendToOutgoingContext(ctx, "x-user-id", "alice")
	if err := conn.Invoke(ctx, "/gatekeeper.Echo/Say", wrapperspb.String("hi"), out); err != nil || out.GetValue() != "alice: hi" {
		t.Errorf("Echo/Say → %q, %v; want \"alice: hi\"", out.GetValue(), err)
	}
}

func TestShouldReadIntervalsFromTheEnvironmentWithDefaults(t *testing.T) {
	cfg, err := LoadConfig(func(string) string { return "" })
	if err != nil || cfg.PingInterval != 30*time.Second || cfg.EventInterval != time.Second || cfg.HeartbeatInterval != 15*time.Second {
		t.Errorf("defaults = %+v, %v; want ping 30s, events 1s, heartbeat 15s", cfg, err)
	}
	env := map[string]string{"WS_PING_INTERVAL": "5s", "SSE_EVENT_INTERVAL": "200ms", "SSE_HEARTBEAT_INTERVAL": "5s"}
	cfg, err = LoadConfig(func(k string) string { return env[k] })
	if err != nil || cfg.PingInterval != 5*time.Second || cfg.EventInterval != 200*time.Millisecond || cfg.HeartbeatInterval != 5*time.Second {
		t.Errorf("from env = %+v, %v", cfg, err)
	}
	if _, err := LoadConfig(func(k string) string { return map[string]string{"WS_PING_INTERVAL": "soon"}[k] }); err == nil {
		t.Error("an invalid WS_PING_INTERVAL was accepted")
	}
}
