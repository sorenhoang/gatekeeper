//go:build smoke

package smoke

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

const gatewayHealthBody = `{"status":"ok","from":"gateway"}`

func assertGatewayHealth(t *testing.T) {
	t.Helper()
	r := mustRoute(t, "", "/health")
	if r.status != http.StatusOK || strings.TrimSpace(r.body) != gatewayHealthBody {
		t.Errorf("GET /health = %d %q, want 200 %s", r.status, r.body, gatewayHealthBody)
	}
	if ct := r.header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Errorf("GET /health Content-Type = %q, want application/json", ct)
	}
	if got := r.header.Get("X-Route"); got != "health" {
		t.Errorf("GET /health X-Route = %q, want health", got)
	}
}

func TestShouldAnswerHealthFromTheGatewayItself(t *testing.T) {
	assertGatewayHealth(t)
}

func TestShouldAnswerUnknownApiPathsWithJson404(t *testing.T) {
	for _, path := range []string{"/api/unknown", "/api/usersX", "/api/", "/api/auth/login"} {
		r := mustRoute(t, "", path)
		if r.status != http.StatusNotFound {
			t.Errorf("GET %s = %d, want 404\n%s", path, r.status, r.body)
			continue
		}
		if ct := r.header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
			t.Errorf("GET %s Content-Type = %q, want application/json", path, ct)
		}
		var body struct {
			Error string `json:"error"`
			Path  string `json:"path"`
		}
		if err := json.Unmarshal([]byte(r.body), &body); err != nil {
			t.Errorf("GET %s body is not JSON: %v\n%s", path, err, r.body)
			continue
		}
		if body.Error != "not found" || body.Path != path {
			t.Errorf("GET %s JSON = %+v, want error=\"not found\" path=%q", path, body, path)
		}
		if got := r.header.Get("X-Route"); got != "api-404" {
			t.Errorf("GET %s X-Route = %q, want api-404", path, got)
		}
	}
}

func TestShouldOnlyAnswerExactHealthAtTheGateway(t *testing.T) {
	for _, path := range []string{"/health/", "/healthz"} {
		r := mustRoute(t, "", path)
		if strings.TrimSpace(r.body) == gatewayHealthBody || r.header.Get("X-Route") == "health" {
			t.Errorf("GET %s was answered by the exact /health location", path)
		}
	}
}
