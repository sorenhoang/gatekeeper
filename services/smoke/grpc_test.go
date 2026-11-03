//go:build smoke

package smoke

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/wrapperspb"
)

// grpcGateway dials the gateway over TLS (lab CA) and HTTP/2, as any
// grpc-go client of the lab would.
func grpcGateway(t *testing.T) *grpc.ClientConn {
	t.Helper()
	conn, err := grpc.NewClient("localhost:8443", grpc.WithTransportCredentials(credentials.NewTLS(labTLS.Clone())))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	return conn
}

func withToken(ctx context.Context, tok string) context.Context {
	if tok == "" {
		return ctx
	}
	return metadata.AppendToOutgoingContext(ctx, "authorization", "Bearer "+tok)
}

func say(ctx context.Context, conn *grpc.ClientConn, msg string) (string, error) {
	out := &wrapperspb.StringValue{}
	err := conn.Invoke(ctx, "/gatekeeper.Echo/Say", wrapperspb.String(msg), out)
	return out.GetValue(), err
}

func TestShouldCallGRPCThroughTheGatewayWithAToken(t *testing.T) {
	conn := grpcGateway(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ctx = withToken(ctx, freshToken("user"))
	h, err := healthpb.NewHealthClient(conn).Check(ctx, &healthpb.HealthCheckRequest{})
	if err != nil || h.GetStatus() != healthpb.HealthCheckResponse_SERVING {
		t.Errorf("Health/Check through the gateway → %v, %v; want SERVING", h.GetStatus(), err)
	}
	// A client-sent x-user-id must be replaced by the verified caller.
	ctx = metadata.AppendToOutgoingContext(ctx, "x-user-id", "mallory")
	if got, err := say(ctx, conn, "hi"); err != nil || got != "alice: hi" {
		t.Errorf("Echo/Say through the gateway → %q, %v; want \"alice: hi\"", got, err)
	}
}

func TestShouldAnswerGRPCAuthFailuresWithAStatus(t *testing.T) {
	conn := grpcGateway(t)
	for name, tok := range map[string]string{"no token": "", "garbage token": "not-a-jwt"} {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		_, err := say(withToken(ctx, tok), conn, "hi")
		cancel()
		if st, _ := status.FromError(err); st.Code() != codes.Unauthenticated {
			t.Errorf("%s → %v (code %v), want UNAUTHENTICATED", name, err, st.Code())
		}
	}
}

// Each RPC is one request to nginx, so the Phase 5 API budget (50 r/s,
// burst 100) counts them even on one HTTP/2 connection.
func TestShouldRateLimitRPCsOnOneConnection(t *testing.T) {
	afterDrain(t)
	conn := grpcGateway(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	ctx = withToken(ctx, suiteToken)
	var mu sync.Mutex
	codesSeen := map[codes.Code]int{}
	var wg sync.WaitGroup
	for i := 0; i < 150; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := say(ctx, conn, "x")
			st, _ := status.FromError(err)
			mu.Lock()
			codesSeen[st.Code()]++
			mu.Unlock()
		}()
	}
	wg.Wait()
	if codesSeen[codes.ResourceExhausted] == 0 || codesSeen[codes.OK] == 0 {
		t.Errorf("150 RPCs at once → %v, want some OK and some RESOURCE_EXHAUSTED", codesSeen)
	}
	for c, n := range codesSeen {
		if c != codes.OK && c != codes.ResourceExhausted {
			t.Errorf("%d RPCs ended with %v, want only OK or RESOURCE_EXHAUSTED", n, c)
		}
	}
}

func TestShouldDocumentRealtimeAndGRPCInTheReadme(t *testing.T) {
	b, err := os.ReadFile(filepath.Join(repoRoot, "README.md"))
	if err != nil {
		t.Fatal(err)
	}
	readme := string(b)
	start := strings.Index(readme, "\n## Realtime & gRPC")
	if start < 0 {
		t.Fatal(`README has no "## Realtime & gRPC" section`)
	}
	section := readme[start+1:]
	if end := strings.Index(section[3:], "\n## "); end >= 0 {
		section = section[:end+3]
	}
	for _, topic := range []string{"$connection_upgrade", "Upgrade", "proxy_read_timeout", "heartbeat", "proxy_buffering",
		"RFC 8441", "access_token", "limit_conn", "worker_shutdown_timeout", "grpc_pass", "h2c", "grpc-status",
		"UNAVAILABLE", "UNAUTHENTICATED", "RESOURCE_EXHAUSTED", "204", "application/grpc", "coder/websocket", "grpc-go"} {
		if !strings.Contains(section, topic) {
			t.Errorf("README Realtime & gRPC section does not cover %q", topic)
		}
	}
	if !strings.Contains(readme, "Go 1.26") {
		t.Error(`README should say the services need Go 1.26`)
	}
}
