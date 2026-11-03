//go:build smoke

package smoke

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/wrapperspb"
)

// The gateway's own gRPC errors carry the request id like every other
// gateway answer, so a failed RPC can still be traced.
func TestShouldTagGatewayGRPCErrorsWithTheRequestID(t *testing.T) {
	conn := grpcGateway(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	id := runID("grpc-err")
	ctx = metadata.AppendToOutgoingContext(ctx, "x-request-id", id)
	var hdr, trl metadata.MD
	out := &wrapperspb.StringValue{}
	err := conn.Invoke(ctx, "/gatekeeper.Echo/Say", wrapperspb.String("hi"), out, grpc.Header(&hdr), grpc.Trailer(&trl))
	if st, _ := status.FromError(err); st.Code() != codes.Unauthenticated {
		t.Fatalf("no token → %v, want UNAUTHENTICATED", err)
	}
	got := append(hdr.Get("x-request-id"), trl.Get("x-request-id")...)
	if len(got) == 0 || got[0] != id {
		t.Errorf("UNAUTHENTICATED answer x-request-id %v, want [%s]", got, id)
	}
}

// Over client_max_body_size (1m) nginx answers 413 itself; a gRPC client
// must see that as a status, not as a malformed HTML response.
func TestShouldAnswerAnOversizedRPCWithAStatus(t *testing.T) {
	conn := grpcGateway(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, err := say(withToken(ctx, freshToken("user")), conn, strings.Repeat("x", 2<<20))
	if st, _ := status.FromError(err); st.Code() != codes.ResourceExhausted {
		t.Errorf("a 2 MiB RPC → %v (code %v), want RESOURCE_EXHAUSTED", err, st.Code())
	}
}

// gRPC is always POST over HTTP/2; anything else on a gRPC path is not a
// gRPC client and gets a plain 405.
func TestShouldRefuseNonGRPCRequestsOnGRPCPaths(t *testing.T) {
	a := authed(t, http.MethodGet, "/gatekeeper.Echo/Say", "Bearer "+freshToken("user"), nil)
	if a.status != http.StatusMethodNotAllowed {
		t.Errorf("GET /gatekeeper.Echo/Say → %d, want 405", a.status)
	}
}
