//go:build smoke

package smoke

import (
	"bytes"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"
)

const tempFileLine = "a client request body is buffered to a temporary file"

// postBody sends a body of n bytes to /api/users/ with a marker in the query
// and X-Request-ID, so log lines for this exact request can be found.
func postBody(t *testing.T, n int) (status int, marker string) {
	t.Helper()
	marker = fmt.Sprintf("body-%d-%d", n, time.Now().UnixNano())
	req, err := http.NewRequest(http.MethodPost, gatewayURL+"/api/users/?m="+marker, bytes.NewReader(bytes.Repeat([]byte("x"), n)))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("X-Request-ID", marker)
	resp, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
	if err != nil {
		t.Fatalf("POST %d bytes: %v", n, err)
	}
	resp.Body.Close()
	return resp.StatusCode, marker
}

func logLinesWith(t *testing.T, svc, marker string) []string {
	t.Helper()
	var hits []string
	for _, line := range strings.Split(mustCompose(t, "logs", "--no-log-prefix", "--since", "2m", svc), "\n") {
		if strings.Contains(line, marker) {
			hits = append(hits, line)
		}
	}
	return hits
}

func TestShouldRejectOversizedBodiesBeforeAnyBackendSeesThem(t *testing.T) {
	status, marker := postBody(t, 5<<20)
	if status != http.StatusRequestEntityTooLarge {
		t.Fatalf("POST 5 MB = %d, want 413", status)
	}
	time.Sleep(500 * time.Millisecond)
	if hits := logLinesWith(t, "user-service", marker); len(hits) > 0 {
		t.Errorf("user-service logged the rejected request:\n%s", strings.Join(hits, "\n"))
	}
}

func TestShouldBufferLargeBodiesToDiskBeforeProxying(t *testing.T) {
	status, marker := postBody(t, 200<<10)
	if status != http.StatusOK {
		t.Fatalf("POST 200 KB = %d, want 200", status)
	}
	var buffered bool
	for i := 0; i < 10 && !buffered; i++ {
		for _, line := range logLinesWith(t, "gateway", marker) {
			buffered = buffered || strings.Contains(line, tempFileLine)
		}
		time.Sleep(200 * time.Millisecond)
	}
	if !buffered {
		t.Errorf("no %q log line for the 200 KB request %s", tempFileLine, marker)
	}
	if hits := logLinesWith(t, "user-service", marker); len(hits) == 0 {
		t.Errorf("user-service never logged the 200 KB request %s", marker)
	}
}

func TestShouldKeepSmallBodiesInMemory(t *testing.T) {
	status, marker := postBody(t, 4<<10)
	if status != http.StatusOK {
		t.Fatalf("POST 4 KB = %d, want 200", status)
	}
	time.Sleep(500 * time.Millisecond)
	for _, line := range logLinesWith(t, "gateway", marker) {
		if strings.Contains(line, tempFileLine) {
			t.Errorf("4 KB body was buffered to a temporary file: %s", line)
		}
	}
}
