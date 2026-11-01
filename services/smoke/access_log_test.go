//go:build smoke

package smoke

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"
)

var accessLogFields = []string{
	"time", "request_id", "remote_addr", "host", "request", "status", "bytes",
	"request_time", "route", "upstream", "upstream_addr", "upstream_status",
	"upstream_connect_time", "upstream_header_time", "upstream_response_time", "user_agent",
}

// accessLines returns the gateway's JSON access-log lines written since
// `since`. Error-log lines (plain text) are skipped; a line that starts like
// JSON but doesn't parse is a failure.
func accessLines(t *testing.T, since time.Time) []map[string]string {
	t.Helper()
	out := mustCompose(t, "logs", "--no-log-prefix", "--since", since.UTC().Format(time.RFC3339), "gateway")
	var lines []map[string]string
	for _, l := range strings.Split(out, "\n") {
		l = strings.TrimSpace(l)
		if !strings.HasPrefix(l, "{") {
			continue
		}
		var m map[string]string
		if err := json.Unmarshal([]byte(l), &m); err != nil {
			t.Errorf("access-log line is not valid JSON (all values strings): %v\n%s", err, l)
			continue
		}
		lines = append(lines, m)
	}
	return lines
}

// linesFor waits briefly for log lines carrying id and returns them.
func linesFor(t *testing.T, since time.Time, id string) []map[string]string {
	t.Helper()
	var found []map[string]string
	for deadline := time.Now().Add(2 * time.Second); time.Now().Before(deadline); time.Sleep(200 * time.Millisecond) {
		found = nil
		for _, m := range accessLines(t, since) {
			if m["request_id"] == id {
				found = append(found, m)
			}
		}
		if len(found) > 0 {
			return found
		}
	}
	return found
}

func runID(prefix string) string { return fmt.Sprintf("%s-%d", prefix, time.Now().UnixNano()) }

func isNumber(s string) bool {
	_, err := strconv.ParseFloat(s, 64)
	return err == nil
}

func TestShouldWriteEveryAccessLogLineAsJSONWithAllFields(t *testing.T) {
	since := time.Now().Add(-time.Second)
	id := runID("fields")
	withIDs(t, http.MethodGet, "", "/api/users/", nil, id)
	withIDs(t, http.MethodGet, "", "/settings", nil, runID("fields-spa"))
	if len(linesFor(t, since, id)) == 0 {
		t.Fatalf("no access-log line for %s", id)
	}
	for _, m := range accessLines(t, since) {
		for _, f := range accessLogFields {
			if _, ok := m[f]; !ok {
				t.Errorf("access-log line for %s lacks field %q: %v", m["request_id"], f, m)
			}
		}
	}
}

func TestShouldLogAProxiedRequestOnceWithRouteUpstreamAndTimings(t *testing.T) {
	since := time.Now().Add(-time.Second)
	r := withIDs(t, http.MethodGet, "", "/api/users/", nil, runID("proxied"))
	id := r.one(t, "proxied")
	lines := linesFor(t, since, id)
	if len(lines) != 1 {
		t.Fatalf("%d access-log lines for %s, want exactly 1: %v", len(lines), id, lines)
	}
	m := lines[0]
	want := map[string]string{"route": "users", "upstream": "users", "upstream_status": "200", "status": "200"}
	for k, v := range want {
		if m[k] != v {
			t.Errorf("%s = %q, want %q (%v)", k, m[k], v, m)
		}
	}
	if m["upstream_addr"] == "" {
		t.Errorf("upstream_addr is empty for a proxied request: %v", m)
	}
	for _, f := range []string{"request_time", "upstream_connect_time", "upstream_header_time", "upstream_response_time"} {
		if !isNumber(m[f]) {
			t.Errorf("%s = %q, want a number (%v)", f, m[f], m)
		}
	}
	if !strings.HasPrefix(m["request"], "GET /api/users/ ") {
		t.Errorf("request = %q, want the request line", m["request"])
	}
}

func TestShouldLogRequestsNginxAnswersItselfWithEmptyUpstreamFields(t *testing.T) {
	since := time.Now().Add(-time.Second)
	for _, path := range []string{"/assets/app.css", "/settings"} {
		id := runID("local")
		withIDs(t, http.MethodGet, "", path, nil, id)
		lines := linesFor(t, since, id)
		if len(lines) != 1 {
			t.Errorf("%s: %d access-log lines, want 1", path, len(lines))
			continue
		}
		for _, f := range []string{"upstream", "upstream_addr", "upstream_status", "upstream_response_time"} {
			if v := lines[0][f]; v != "" && v != "-" {
				t.Errorf("%s: %s = %q, want empty for a request nginx answered itself", path, f, v)
			}
		}
	}
}

func TestShouldSkipTheGatewaysOwnHealthInTheAccessLog(t *testing.T) {
	since := time.Now().Add(-time.Second)
	health, healthz := runID("health"), runID("healthz")
	withIDs(t, http.MethodGet, "", "/health", nil, health)
	withIDs(t, http.MethodGet, "", "/health?probe=1", nil, health+"-q")
	withIDs(t, http.MethodGet, "", "/healthz", nil, healthz)
	if len(linesFor(t, since, healthz)) != 1 {
		t.Errorf("/healthz was not logged exactly once")
	}
	for _, id := range []string{health, health + "-q"} {
		if n := len(linesFor(t, since, id)); n != 0 {
			t.Errorf("gateway /health (%s) logged %d times, want 0", id, n)
		}
	}
}
