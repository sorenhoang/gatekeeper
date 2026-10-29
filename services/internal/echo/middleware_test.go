package echo

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func env(vars map[string]string) func(string) string {
	return func(k string) string { return vars[k] }
}

func wrapped(cfg Config, logOut *bytes.Buffer) http.Handler {
	return Wrap(Handler("order-service", "8080"), "order-service", cfg, logOut)
}

func timed(h http.Handler, req *http.Request) (*httptest.ResponseRecorder, time.Duration) {
	rec := httptest.NewRecorder()
	start := time.Now()
	h.ServeHTTP(rec, req)
	return rec, time.Since(start)
}

func TestShouldLoadDelayAndFailRateFromEnv(t *testing.T) {
	cfg, err := LoadConfig(env(map[string]string{"DELAY_MS": "200", "FAIL_RATE": "0.5"}))

	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if cfg.Delay != 200*time.Millisecond {
		t.Errorf("Delay = %v, want 200ms", cfg.Delay)
	}
	if cfg.FailRate != 0.5 {
		t.Errorf("FailRate = %v, want 0.5", cfg.FailRate)
	}
}

func TestShouldDefaultToNoDelayAndNoFailuresWhenEnvIsUnset(t *testing.T) {
	cfg, err := LoadConfig(env(nil))

	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if cfg.Delay != 0 || cfg.FailRate != 0 {
		t.Errorf("cfg = %+v, want zero value", cfg)
	}
}

func TestShouldRejectInvalidChaosConfig(t *testing.T) {
	cases := map[string]map[string]string{
		"fail rate above 1":     {"FAIL_RATE": "1.5"},
		"fail rate below 0":     {"FAIL_RATE": "-0.1"},
		"fail rate not numeric": {"FAIL_RATE": "abc"},
		"negative delay":        {"DELAY_MS": "-1"},
		"delay not numeric":     {"DELAY_MS": "abc"},
	}
	for name, vars := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := LoadConfig(env(vars)); err == nil {
				t.Errorf("LoadConfig(%v) = nil error, want error", vars)
			}
		})
	}
}

func TestShouldDelayRequestsButNotHealthWhenDelayIsSet(t *testing.T) {
	h := wrapped(Config{Delay: 200 * time.Millisecond}, &bytes.Buffer{})

	rec, took := timed(h, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec.Code != http.StatusOK || took < 200*time.Millisecond {
		t.Errorf("GET / = %d in %v, want 200 after >=200ms", rec.Code, took)
	}

	rec, took = timed(h, httptest.NewRequest(http.MethodGet, "/health", nil))
	if rec.Code != http.StatusOK || took >= 50*time.Millisecond {
		t.Errorf("GET /health = %d in %v, want 200 in <50ms", rec.Code, took)
	}
}

func TestShouldFailRequestsButNotHealthWhenFailRateIsOne(t *testing.T) {
	h := wrapped(Config{FailRate: 1}, &bytes.Buffer{})

	rec, _ := timed(h, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("GET / status = %d, want 500", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}
	var body map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("500 body is not JSON: %v; body %s", err, rec.Body)
	}
	if body["error"] != "injected failure" || body["service"] != "order-service" {
		t.Errorf("500 body = %v, want error=injected failure, service=order-service", body)
	}

	rec, _ = timed(h, httptest.NewRequest(http.MethodGet, "/health", nil))
	if rec.Code != http.StatusOK {
		t.Errorf("GET /health status = %d, want 200", rec.Code)
	}
}

func TestShouldNeverFailWhenFailRateIsZero(t *testing.T) {
	h := wrapped(Config{}, &bytes.Buffer{})

	for i := 0; i < 200; i++ {
		rec, _ := timed(h, httptest.NewRequest(http.MethodGet, "/", nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("request %d status = %d, want 200", i, rec.Code)
		}
	}
}

func TestShouldLogOneLinePerRequestWithRequestID(t *testing.T) {
	var logOut bytes.Buffer
	h := wrapped(Config{FailRate: 1}, &logOut)
	req := httptest.NewRequest(http.MethodGet, "/api/x", nil)
	req.Header.Set("X-Request-ID", "req-123")

	timed(h, req)

	lines := strings.Split(strings.TrimRight(logOut.String(), "\n"), "\n")
	if len(lines) != 1 {
		t.Fatalf("got %d log lines, want 1: %q", len(lines), logOut.String())
	}
	for _, want := range []string{
		"service=order-service", "method=GET", "path=/api/x",
		"status=500", "duration_ms=", "request_id=req-123",
	} {
		if !strings.Contains(lines[0], want) {
			t.Errorf("log line %q missing %q", lines[0], want)
		}
	}
}

func TestShouldLogDashWhenRequestHasNoRequestID(t *testing.T) {
	var logOut bytes.Buffer
	h := wrapped(Config{}, &logOut)

	timed(h, httptest.NewRequest(http.MethodGet, "/", nil))

	if !strings.Contains(logOut.String(), "request_id=-") {
		t.Errorf("log %q missing request_id=-", logOut.String())
	}
	if !strings.Contains(logOut.String(), "status=200") {
		t.Errorf("log %q missing status=200", logOut.String())
	}
}
