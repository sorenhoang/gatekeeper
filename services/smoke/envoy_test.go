//go:build smoke

package smoke

import (
	"encoding/json"
	"io"
	"net/http"
	"os/exec"
	"strings"
	"testing"
	"time"
)

const envoyURL = "https://localhost:9443"

// Straight to Envoy with exactly the given headers: no labAuth token.
var envoyClient = &http.Client{Timeout: 10 * time.Second,
	Transport: &http.Transport{TLSClientConfig: labTLS.Clone(), ForceAttemptHTTP2: true}}

type envoyAnswer struct {
	status int
	proto  string
	body   string
	echo   struct {
		Service, Hostname, Method, Path string
		Headers                         map[string][]string
	}
}

func viaEnvoy(t *testing.T, method, path string, hdr map[string]string) envoyAnswer {
	t.Helper()
	req, _ := http.NewRequest(method, envoyURL+path, nil)
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := envoyClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s via Envoy: %v", method, path, err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	a := envoyAnswer{status: resp.StatusCode, proto: resp.Proto, body: string(b)}
	json.Unmarshal(b, &a.echo)
	return a
}

func TestShouldServeHealthOverHTTP2FromEnvoy(t *testing.T) {
	if a := viaEnvoy(t, http.MethodGet, "/health", nil); a.status != 200 || a.proto != "HTTP/2.0" {
		t.Errorf("Envoy /health → %d %s, want 200 HTTP/2.0", a.status, a.proto)
	}
}

// The same routes as the nginx gateway: the /api/<service>/ prefix is replaced by /.
func TestShouldRouteTheAPIThroughEnvoyLikeTheGateway(t *testing.T) {
	tok := map[string]string{"Authorization": "Bearer " + freshToken("user")}
	for route, svc := range map[string]string{"users": "user-service", "orders": "order-service", "products": "product-service"} {
		a := viaEnvoy(t, http.MethodGet, "/api/"+route+"/42", tok)
		if a.status != 200 || a.echo.Service != svc || a.echo.Path != "/42" {
			t.Errorf("Envoy GET /api/%s/42 → %d service %q path %q, want 200 %s /42", route, a.status, a.echo.Service, a.echo.Path, svc)
		}
	}
}

func TestShouldAuthorizeThroughAuthServiceOnEnvoy(t *testing.T) {
	if a := viaEnvoy(t, http.MethodGet, "/api/users/", nil); a.status != 401 {
		t.Errorf("Envoy /api/users/ without a token → %d, want 401", a.status)
	}
	if a := viaEnvoy(t, http.MethodGet, "/api/orders/", map[string]string{"Authorization": "Bearer x.y.z"}); a.status != 401 {
		t.Errorf("Envoy /api/orders/ with a bad token → %d, want 401", a.status)
	}
	if a := viaEnvoy(t, http.MethodDelete, "/api/orders/x", map[string]string{"Authorization": "Bearer " + freshToken("user")}); a.status != 403 {
		t.Errorf("Envoy DELETE as a user → %d, want 403", a.status)
	}
	if a := viaEnvoy(t, http.MethodDelete, "/api/orders/x", map[string]string{"Authorization": "Bearer " + freshToken("admin")}); a.status != 200 || a.echo.Method != "DELETE" {
		t.Errorf("Envoy DELETE as admin → %d %q, want 200 from order-service", a.status, a.echo.Method)
	}
	forged := map[string]string{"Authorization": "Bearer " + freshToken("user"), "X-User-ID": "mallory", "X-User-Role": "admin"}
	a := viaEnvoy(t, http.MethodGet, "/api/users/", forged)
	if a.status != 200 || strings.Join(a.echo.Headers["X-User-Id"], ",") != "alice" || strings.Join(a.echo.Headers["X-User-Role"], ",") != "user" {
		t.Errorf("Envoy with alice's token and forged headers → %d, service saw X-User-Id %q X-User-Role %q, want 200 alice user",
			a.status, a.echo.Headers["X-User-Id"], a.echo.Headers["X-User-Role"])
	}
	p := viaEnvoy(t, http.MethodGet, "/api/products/1", map[string]string{"X-User-ID": "mallory", "X-User-Role": "admin"})
	if p.status != 200 || len(p.echo.Headers["X-User-Id"]) != 0 || len(p.echo.Headers["X-User-Role"]) != 0 {
		t.Errorf("Envoy products with forged headers → %d, service saw X-User-Id %q X-User-Role %q, want 200 and none",
			p.status, p.echo.Headers["X-User-Id"], p.echo.Headers["X-User-Role"])
	}
}

func TestShouldSpreadOrdersOverTheReplicasOnEnvoy(t *testing.T) {
	seen := map[string]bool{}
	tok := map[string]string{"Authorization": "Bearer " + freshToken("user")}
	for i := 0; i < 30; i++ {
		seen[viaEnvoy(t, http.MethodGet, "/api/orders/", tok).echo.Hostname] = true
	}
	delete(seen, "")
	if len(seen) != 3 {
		t.Errorf("30 Envoy GETs on orders reached %d replicas, want 3", len(seen))
	}
}

func TestShouldRotateTheEnvoyContainerLog(t *testing.T) {
	id := strings.TrimSpace(mustCompose(t, "ps", "-q", "envoy"))
	out, err := exec.Command("docker", "inspect", "-f",
		`{{.HostConfig.LogConfig.Type}} {{index .HostConfig.LogConfig.Config "max-size"}} {{index .HostConfig.LogConfig.Config "max-file"}}`, id).CombinedOutput()
	if err != nil || strings.TrimSpace(string(out)) != "json-file 10m 3" {
		t.Errorf("envoy log config = %q (err %v), want json-file 10m 3", strings.TrimSpace(string(out)), err)
	}
}
