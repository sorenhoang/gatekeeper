//go:build smoke

package smoke

import (
	"net"
	"testing"
	"time"
)

// A slowloris-style client: send part of the headers, then nothing. nginx
// must hang up after client_header_timeout (5s), without answering. Since
// Phase 7 :8080 is the redirect server; the timeouts are set at http level
// (nginx.conf), so it shows the same cut-off the HTTPS servers apply.
func TestShouldCutOffAClientThatStopsSendingHeaders(t *testing.T) {
	conn, err := net.Dial("tcp", "localhost:8080")
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := conn.Write([]byte("GET / HTTP/1.1\r\nHost: localhost\r\n")); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	conn.SetReadDeadline(time.Now().Add(15 * time.Second))
	buf := make([]byte, 64)
	n, err := conn.Read(buf)
	took := time.Since(start)
	if n > 0 {
		t.Errorf("nginx answered a half-sent request: %q", buf[:n])
	}
	if ne, ok := err.(net.Error); ok && ne.Timeout() {
		t.Fatalf("connection still open after 15s, want it closed after ~5s (client_header_timeout)")
	}
	if took < 4*time.Second || took > 6*time.Second {
		t.Errorf("connection closed after %v, want 5s ± 1s (client_header_timeout 5s)", took)
	}
}
