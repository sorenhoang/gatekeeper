//go:build smoke

package smoke

import (
	"strings"
	"testing"
	"time"
)

func TestShouldRunRealtimeServiceInsideTheComposeNetworkOnly(t *testing.T) {
	if body, err := fromGateway(t, "http://realtime-service:8080/health"); err != nil || !strings.Contains(body, `"ok"`) {
		t.Fatalf("realtime-service /health from the gateway: %v %q", err, body)
	}
	for _, port := range []string{"8080", "9090"} {
		if out, err := compose(t, "port", "realtime-service", port); err == nil && strings.TrimSpace(out) != "" && !strings.HasSuffix(strings.TrimSpace(out), ":0") {
			t.Errorf("realtime-service publishes %s at %q; only the gateway may", port, strings.TrimSpace(out))
		}
	}
	time.Sleep(300 * time.Millisecond)
	if lines := logLinesWith(t, "realtime-service", "service=realtime-service method=GET path=/health status=200"); len(lines) == 0 {
		t.Error("realtime-service logs no request line in the shared format")
	}
}
