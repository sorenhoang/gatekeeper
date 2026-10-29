package echo

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
)

type echoBody struct {
	Service    string              `json:"service"`
	Hostname   string              `json:"hostname"`
	Port       string              `json:"port"`
	Method     string              `json:"method"`
	Path       string              `json:"path"`
	Query      string              `json:"query"`
	Host       string              `json:"host"`
	RemoteAddr string              `json:"remote_addr"`
	Headers    map[string][]string `json:"headers"`
}

func serve(t *testing.T, req *http.Request) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	Handler("user-service", "8080").ServeHTTP(rec, req)
	return rec
}

func decodeEcho(t *testing.T, rec *httptest.ResponseRecorder) echoBody {
	t.Helper()
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body %s", rec.Code, rec.Body)
	}
	var b echoBody
	if err := json.Unmarshal(rec.Body.Bytes(), &b); err != nil {
		t.Fatalf("body is not echo JSON: %v; body %s", err, rec.Body)
	}
	return b
}

func TestShouldReturnOkJSONWhenHealthIsRequested(t *testing.T) {
	rec := serve(t, httptest.NewRequest(http.MethodGet, "/health", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("body is not JSON: %v", err)
	}
	if len(body) != 1 || body["status"] != "ok" {
		t.Errorf("body = %v, want {\"status\":\"ok\"}", body)
	}
}

func TestShouldEchoMethodPathQueryAndHeadersWhenAnyPathIsRequested(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/api/x?y=1", nil)
	req.Header.Set("X-Foo", "bar")

	b := decodeEcho(t, serve(t, req))

	if b.Service != "user-service" {
		t.Errorf("service = %q, want user-service", b.Service)
	}
	if b.Method != http.MethodGet {
		t.Errorf("method = %q, want GET", b.Method)
	}
	if b.Path != "/api/x" {
		t.Errorf("path = %q, want /api/x", b.Path)
	}
	if b.Query != "y=1" {
		t.Errorf("query = %q, want y=1", b.Query)
	}
	if got := b.Headers["X-Foo"]; len(got) != 1 || got[0] != "bar" {
		t.Errorf("headers[X-Foo] = %v, want [bar]", got)
	}
}

func TestShouldEchoHostAndRemoteAddrWhenTheyAreNotInHeaders(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Host = "example.test"
	req.RemoteAddr = "10.0.0.9:5555"

	b := decodeEcho(t, serve(t, req))

	if b.Host != "example.test" {
		t.Errorf("host = %q, want example.test", b.Host)
	}
	if b.RemoteAddr != "10.0.0.9:5555" {
		t.Errorf("remote_addr = %q, want 10.0.0.9:5555", b.RemoteAddr)
	}
}

func TestShouldReportHostnameAndConfiguredPort(t *testing.T) {
	want, err := os.Hostname()
	if err != nil {
		t.Fatalf("os.Hostname: %v", err)
	}

	b := decodeEcho(t, serve(t, httptest.NewRequest(http.MethodGet, "/", nil)))

	if b.Hostname == "" || b.Hostname != want {
		t.Errorf("hostname = %q, want %q", b.Hostname, want)
	}
	if b.Port != "8080" {
		t.Errorf("port = %q, want 8080", b.Port)
	}
}

func TestShouldEchoAnyMethodWhenPathIsNotHealth(t *testing.T) {
	b := decodeEcho(t, serve(t, httptest.NewRequest(http.MethodPost, "/anything", nil)))

	if b.Method != http.MethodPost {
		t.Errorf("method = %q, want POST", b.Method)
	}
	if b.Path != "/anything" {
		t.Errorf("path = %q, want /anything", b.Path)
	}
}
