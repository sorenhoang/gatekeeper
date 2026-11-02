//go:build smoke

package smoke

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

type authAnswer struct {
	status int
	header http.Header
	body   string
}

// authed sends one request with exactly the given Authorization (or none);
// labAuth is told to keep out of it.
func authed(t *testing.T, method, path, authz string, extra map[string]string) authAnswer {
	t.Helper()
	req, _ := http.NewRequest(method, gatewayURL+path, nil)
	req.Header.Set(anonymousHeader, "1")
	if authz != "" {
		req.Header.Set("Authorization", authz)
	}
	for k, v := range extra {
		req.Header.Set(k, v)
	}
	resp, err := noFollow.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	return authAnswer{status: resp.StatusCode, header: resp.Header, body: string(b)}
}

func serviceSaw(t *testing.T, svc, path string) int {
	t.Helper()
	time.Sleep(300 * time.Millisecond)
	return len(logLinesWith(t, svc, "path="+path+" "))
}

func TestShouldRefuseRequestsWithoutAValidToken(t *testing.T) {
	expired := labToken("alice", "user", time.Now().Add(-time.Hour), 15*time.Minute)
	for route, svc := range map[string]string{"users": "user-service", "orders": "order-service"} {
		for name, authz := range map[string]string{"no token": "", "garbage": "Bearer not-a-jwt", "expired": "Bearer " + expired} {
			p := fmt.Sprintf("/unauth-%s-%d", strings.ReplaceAll(name, " ", ""), time.Now().UnixNano())
			a := authed(t, http.MethodGet, "/api/"+route+p, authz, nil)
			var e struct {
				Error  string `json:"error"`
				Status int    `json:"status"`
				Route  string `json:"route"`
			}
			json.Unmarshal([]byte(a.body), &e)
			if a.status != 401 || e != (struct {
				Error  string `json:"error"`
				Status int    `json:"status"`
				Route  string `json:"route"`
			}{"unauthorized", 401, route}) {
				t.Errorf("%s /api/%s/ → %d %q, want 401 {unauthorized, 401, %s}", name, route, a.status, a.body, route)
			}
			if !strings.HasPrefix(a.header.Get("WWW-Authenticate"), "Bearer") {
				t.Errorf("%s /api/%s/ 401 WWW-Authenticate %q, want Bearer", name, route, a.header.Get("WWW-Authenticate"))
			}
			if n := serviceSaw(t, svc, p); n != 0 {
				t.Errorf("%s: %s saw the unauthenticated request %d times, want 0", name, svc, n)
			}
		}
	}
}

func TestShouldPassTheCallersIdentityToServices(t *testing.T) {
	tok := freshToken("user")
	forged := map[string]string{"X-User-ID": "mallory", "X-User-Role": "admin"}
	for _, route := range []string{"users", "orders"} {
		a := authed(t, http.MethodGet, "/api/"+route+"/", "Bearer "+tok, forged)
		var e struct{ Headers map[string][]string }
		json.Unmarshal([]byte(a.body), &e)
		if a.status != 200 || strings.Join(e.Headers["X-User-Id"], ",") != "alice" || strings.Join(e.Headers["X-User-Role"], ",") != "user" {
			t.Errorf("/api/%s/ with alice's token and forged headers → %d, service saw X-User-Id %q X-User-Role %q, want 200 alice user",
				route, a.status, e.Headers["X-User-Id"], e.Headers["X-User-Role"])
		}
	}
	gw, _ := uniqueProduct("forged")
	a := authed(t, http.MethodGet, gw, "", forged)
	var e struct{ Headers map[string][]string }
	json.Unmarshal([]byte(a.body), &e)
	if strings.Join(e.Headers["X-User-Id"], "") != "" || strings.Join(e.Headers["X-User-Role"], "") != "" {
		t.Errorf("products got forged identity X-User-Id %q X-User-Role %q, want them empty", e.Headers["X-User-Id"], e.Headers["X-User-Role"])
	}
}

func TestShouldLetOnlyAdminsDeleteOrders(t *testing.T) {
	if a := authed(t, http.MethodDelete, "/api/orders/x", "Bearer "+freshToken("user"), nil); a.status != 403 || !strings.Contains(a.body, `"error":"forbidden"`) {
		t.Errorf("DELETE with a user token → %d %q, want 403 JSON forbidden", a.status, a.body)
	}
	if a := authed(t, http.MethodDelete, "/api/orders/x", "Bearer "+freshToken("admin"), nil); a.status != 200 || !strings.Contains(a.body, `"service":"order-service"`) {
		t.Errorf("DELETE with an admin token → %d %q, want 200 from order-service", a.status, a.body)
	}
}

func TestShouldLoginThroughTheGatewayAndUseTheToken(t *testing.T) {
	afterDrain(t)
	body := fmt.Sprintf(`{"username":"alice","password":%q}`, labUsers["alice"].Password)
	req, _ := http.NewRequest(http.MethodPost, gatewayURL+"/api/auth/login", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	var out struct {
		Token     string `json:"token"`
		ExpiresIn int    `json:"expires_in"`
	}
	json.NewDecoder(resp.Body).Decode(&out)
	resp.Body.Close()
	if resp.StatusCode != 200 || out.Token == "" || out.ExpiresIn != 900 {
		t.Fatalf("POST /api/auth/login → %d, token %t, expires_in %d; want 200 with a 900s token", resp.StatusCode, out.Token != "", out.ExpiresIn)
	}
	if a := authed(t, http.MethodGet, "/api/users/", "Bearer "+out.Token, nil); a.status != 200 {
		t.Errorf("/api/users/ with the login token → %d, want 200", a.status)
	}
	for _, old := range []string{"/api/users/login", "/api/users/login/"} {
		a := authed(t, http.MethodPost, old, "", nil)
		if a.status != http.StatusPermanentRedirect || a.header.Get("Location") != "/api/auth/login" {
			t.Errorf("POST %s → %d Location %q, want 308 /api/auth/login", old, a.status, a.header.Get("Location"))
		}
	}
}

// Five calls with one token inside the 10s window: one /verify subrequest.
func TestShouldCacheATokenVerdictForTenSeconds(t *testing.T) {
	tok := freshToken("user")
	// Each GET has its own id; the subrequest carries it to auth-service's log.
	var ids []string
	for i := 0; i < 5; i++ {
		id := runID("verdict")
		ids = append(ids, id)
		if a := authed(t, http.MethodGet, "/api/users/", "Bearer "+tok, map[string]string{"X-Request-ID": id}); a.status != 200 {
			t.Fatalf("GET #%d → %d, want 200", i+1, a.status)
		}
	}
	time.Sleep(300 * time.Millisecond)
	n := 0
	for _, l := range logLinesWith(t, "auth-service", "path=/verify ") {
		for _, id := range ids {
			if strings.HasSuffix(l, "request_id="+id) {
				n++
			}
		}
	}
	if n != 1 {
		t.Errorf("5 GETs with one token → %d /verify calls, want 1 (verdict cached 10s)", n)
	}
}

func TestShouldNeverLogTheToken(t *testing.T) {
	since := time.Now().Add(-time.Second)
	tok := freshToken("user")
	authed(t, http.MethodGet, "/api/users/", "Bearer "+tok, nil)
	sig := tok[strings.LastIndex(tok, ".")+1:]
	time.Sleep(300 * time.Millisecond)
	for _, l := range accessLines(t, since) {
		for k, v := range l {
			if strings.Contains(v, sig) || strings.Contains(strings.ToLower(k), "authorization") {
				t.Errorf("access log field %s = %q carries the token", k, v)
			}
		}
	}
	if logs := mustCompose(t, "logs", "--no-log-prefix", "--since", "1m", "gateway"); strings.Contains(logs, sig) {
		t.Error("gateway logs contain the token")
	}
}

func TestShouldRequireTheAdminLogin(t *testing.T) {
	get := func(user, pass string) authAnswer {
		req, _ := http.NewRequest(http.MethodGet, gatewayURL+"/", nil)
		req.Host = "admin.localhost"
		req.Header.Set(anonymousHeader, "1")
		if user != "" {
			req.SetBasicAuth(user, pass)
		}
		resp, err := noFollow.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		return authAnswer{status: resp.StatusCode, header: resp.Header, body: string(b)}
	}
	if a := get("", ""); a.status != 401 || !strings.HasPrefix(a.header.Get("WWW-Authenticate"), "Basic realm=") {
		t.Errorf("admin.localhost without credentials → %d WWW-Authenticate %q, want 401 Basic realm=…", a.status, a.header.Get("WWW-Authenticate"))
	}
	if a := get("admin", labUsers["admin"].Password); a.status != 200 || a.body != adminPage(t) {
		t.Errorf("admin.localhost with the lab admin password → %d, want 200 and the admin page", a.status)
	}
	if a := get("admin", "wrong"); a.status != 401 {
		t.Errorf("admin.localhost with a wrong password → %d, want 401", a.status)
	}
}
