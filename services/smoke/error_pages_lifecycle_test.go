//go:build smoke && lifecycle

package smoke

import (
	"encoding/json"
	"net/http"
	"regexp"
	"strings"
	"testing"
)

type gatewayError struct {
	Error  string `json:"error"`
	Status int    `json:"status"`
	Route  string `json:"route"`
}

func assertGatewayErrorJSON(t *testing.T, r timedResponse, status int, errText, route string) {
	t.Helper()
	if r.status != status {
		t.Fatalf("status %d, want %d\n%s", r.status, status, r.body)
	}
	if ct := r.header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Errorf("Content-Type %q, want application/json\n%s", ct, r.body)
	}
	var e gatewayError
	if err := json.Unmarshal([]byte(r.body), &e); err != nil {
		t.Fatalf("body is not JSON: %v\n%s", err, r.body)
	}
	if e != (gatewayError{Error: errText, Status: status, Route: route}) {
		t.Errorf("body %+v, want error=%q status=%d route=%q", e, errText, status, route)
	}
	if got := r.header.Get("X-Route"); got != route {
		t.Errorf("X-Route %q on the error response, want %q", got, route)
	}
}

func TestShouldAnswerJsonBadGatewayWhenAServiceIsDown(t *testing.T) {
	restoreStack(t)
	mustCompose(t, "stop", "user-service")
	var r timedResponse
	if !eventually(reresolveWithin, func() bool {
		r = timedGet(t, "/api/users/")
		return r.status == http.StatusBadGateway
	}) {
		t.Fatalf("/api/users/ with user-service stopped = %d, want 502\n%s", r.status, r.body)
	}
	assertGatewayErrorJSON(t, r, http.StatusBadGateway, "bad gateway", "users")
}

func TestShouldAnswerJsonGatewayTimeoutWhenAServiceIsSlow(t *testing.T) {
	withServiceEnv(t, "user-service", "DELAY_MS=3000")
	assertGatewayErrorJSON(t, timedGet(t, "/api/users/x"), http.StatusGatewayTimeout, "gateway timeout", "users")
}

// proxy_intercept_errors stays off: only errors nginx itself generates get the
// gateway page; a backend's own error is the backend's business.
func TestShouldPassABackendsOwnErrorThroughUnchanged(t *testing.T) {
	withOrderReplicasEnv(t, "FAIL_RATE=1")
	r := timedGet(t, "/api/orders/x")
	if r.status != http.StatusInternalServerError {
		t.Fatalf("/api/orders/x with FAIL_RATE=1 = %d, want 500\n%s", r.status, r.body)
	}
	var e struct {
		Error   string `json:"error"`
		Service string `json:"service"`
	}
	if err := json.Unmarshal([]byte(r.body), &e); err != nil || e.Error != "injected failure" || e.Service != "order-service" {
		t.Errorf("500 body %q (err %v), want the service's own {error: injected failure, service: order-service}", r.body, err)
	}
}

// Nothing produces a 503 before Phase 5 (rate limiting), so the mapping is
// checked in the loaded config only.
func TestShouldMapNginx503ToTheJsonHandlerInConfig(t *testing.T) {
	out, err := nginxCmd(t, "-T")
	if err != nil {
		t.Fatalf("nginx -T: %v\n%s", err, out)
	}
	if !regexp.MustCompile(`(?m)^\s*error_page\s+[^;]*\b503\b[^;]*;`).MatchString(out) {
		t.Errorf("loaded config has no error_page for 503")
	}
	if strings.Contains(out, "proxy_intercept_errors on") {
		t.Errorf("proxy_intercept_errors is on; backend errors must pass through")
	}
}
