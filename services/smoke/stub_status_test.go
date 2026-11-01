//go:build smoke

package smoke

import (
	"net"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"
)

const statusURL = "http://127.0.0.1:8082/nginx_status"

func statusPage(t *testing.T) string {
	t.Helper()
	out, err := compose(t, "exec", "-T", "gateway", "wget", "-qO-", "-T", "2", statusURL)
	if err != nil {
		t.Fatalf("stub_status from inside the gateway: %v\n%s", err, out)
	}
	return out
}

// requestsCounter reads the third number of the "accepts handled requests"
// line.
func requestsCounter(t *testing.T, page string) int {
	t.Helper()
	lines := strings.Split(strings.TrimSpace(page), "\n")
	for i, l := range lines {
		if strings.Contains(l, "server accepts handled requests") && i+1 < len(lines) {
			f := strings.Fields(lines[i+1])
			if len(f) == 3 {
				n, err := strconv.Atoi(f[2])
				if err == nil {
					return n
				}
			}
		}
	}
	t.Fatalf("no requests counter in stub_status page:\n%s", page)
	return 0
}

func TestShouldServeStubStatusInsideTheGateway(t *testing.T) {
	page := statusPage(t)
	for _, want := range []string{"Active connections:", "server accepts handled requests", "Reading:", "Writing:", "Waiting:"} {
		if !strings.Contains(page, want) {
			t.Errorf("stub_status page lacks %q:\n%s", want, page)
		}
	}
}

func TestShouldCountRequestsInStubStatus(t *testing.T) {
	before := requestsCounter(t, statusPage(t))
	for i := 0; i < 10; i++ {
		if r := withIDs(t, http.MethodGet, "", "/api/users/", nil); r.status != http.StatusOK {
			t.Fatalf("GET /api/users/ #%d = %d", i, r.status)
		}
	}
	after := requestsCounter(t, statusPage(t))
	if after-before < 10 {
		t.Errorf("requests counter grew by %d after 10 requests, want at least 10 (%d → %d)", after-before, before, after)
	}
}

// Loopback only: not from the host, and not from the compose network either.
func TestShouldKeepStubStatusOffEveryNetwork(t *testing.T) {
	if conn, err := net.DialTimeout("tcp", "localhost:8082", time.Second); err == nil {
		conn.Close()
		t.Errorf("localhost:8082 is reachable from the host")
	}
	out, err := compose(t, "exec", "-T", "gateway", "sh", "-c",
		`wget -qO- -T 2 "http://$(hostname -i):8082/nginx_status"`)
	if err == nil {
		t.Errorf("stub_status answered on the gateway's compose-network address:\n%s", out)
	}
}

func TestShouldPrintStubStatusWithMakeStatus(t *testing.T) {
	out, err := runMake(t, "status")
	if err != nil {
		t.Fatalf("make status: %v\n%s", err, out)
	}
	if !strings.Contains(out, "Active connections:") {
		t.Errorf("make status output lacks the stub_status page:\n%s", out)
	}
}
