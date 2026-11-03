//go:build smoke && lifecycle

package smoke

import (
	"context"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// No upstream: nginx answers itself, and it must answer in gRPC's terms
// (HTTP 200, application/grpc, grpc-status 14), not with an HTML 502.
func TestShouldAnswerUnavailableWhenRealtimeServiceIsDown(t *testing.T) {
	restoreStack(t)
	mustCompose(t, "stop", "realtime-service")
	conn := grpcGateway(t)
	tok := freshToken("user")
	var err error
	ok := eventually(reresolveWithin, func() bool {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, err = say(withToken(ctx, tok), conn, "hi")
		st, _ := status.FromError(err)
		return st.Code() == codes.Unavailable
	})
	if !ok {
		st, _ := status.FromError(err)
		t.Errorf("Echo/Say with realtime-service stopped → %v (code %v), want UNAVAILABLE", err, st.Code())
	}
}
