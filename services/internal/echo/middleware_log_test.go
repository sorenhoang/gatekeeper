package echo

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestShouldLogHealthRequestsToo(t *testing.T) {
	var logOut bytes.Buffer
	h := wrapped(Config{FailRate: 1, Delay: 100 * time.Millisecond}, &logOut)
	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	req.Header.Set("X-Request-ID", "hc-1")

	timed(h, req)

	lines := strings.Split(strings.TrimRight(logOut.String(), "\n"), "\n")
	if len(lines) != 1 {
		t.Fatalf("got %d log lines for /health, want 1: %q", len(lines), logOut.String())
	}
	for _, want := range []string{"path=/health", "status=200", "request_id=hc-1"} {
		if !strings.Contains(lines[0], want) {
			t.Errorf("log line %q missing %q", lines[0], want)
		}
	}
}

func TestShouldLogRealDurationWhenRequestIsDelayed(t *testing.T) {
	var logOut bytes.Buffer
	h := wrapped(Config{Delay: 200 * time.Millisecond}, &logOut)

	timed(h, httptest.NewRequest(http.MethodGet, "/", nil))

	m := regexp.MustCompile(`duration_ms=(\d+)`).FindStringSubmatch(logOut.String())
	if m == nil {
		t.Fatalf("log %q has no duration_ms=<n>", logOut.String())
	}
	if ms, _ := strconv.Atoi(m[1]); ms < 200 {
		t.Errorf("duration_ms = %d, want >= 200", ms)
	}
}

func TestShouldRejectNaNFailRate(t *testing.T) {
	if _, err := LoadConfig(env(map[string]string{"FAIL_RATE": "NaN"})); err == nil {
		t.Error("LoadConfig(FAIL_RATE=NaN) = nil error, want error")
	}
}
