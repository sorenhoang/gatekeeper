//go:build smoke && lifecycle

package smoke

import (
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"
)

// The phase's break-it: make product-service slow and find it from the
// gateway log alone, without looking at any service.
func TestShouldFindTheSlowServiceFromGatewayLogFieldsAlone(t *testing.T) {
	// 1500ms: below products' 5s proxy_read_timeout, so the requests succeed.
	withServiceEnv(t, "product-service", "DELAY_MS=1500")
	since := time.Now().Add(-time.Second)
	ids := map[string][]string{}
	for _, route := range []string{"users", "products"} {
		for i := 0; i < 5; i++ {
			id := runID("slow-" + route)
			r := withIDsPatient(t, "/api/"+route+"/x", id)
			if r != http.StatusOK {
				t.Fatalf("GET /api/%s/x = %d, want 200", route, r)
			}
			ids[route] = append(ids[route], id)
		}
	}

	byRoute := map[string][]float64{}
	for _, m := range accessLines(t, since) {
		for route, list := range ids {
			for _, id := range list {
				if m["request_id"] == id && m["route"] == route {
					v, err := strconv.ParseFloat(strings.TrimSpace(m["upstream_response_time"]), 64)
					if err != nil {
						t.Fatalf("upstream_response_time %q for %s is not a number", m["upstream_response_time"], id)
					}
					byRoute[route] = append(byRoute[route], v)
				}
			}
		}
	}
	for route, want := range map[string]int{"users": 5, "products": 5} {
		if len(byRoute[route]) != want {
			t.Fatalf("found %d gateway lines for route=%s, want %d", len(byRoute[route]), route, want)
		}
	}
	for _, v := range byRoute["products"] {
		if v < 1.5 {
			t.Errorf("route=products upstream_response_time %.3f, want >= 1.5 (DELAY_MS=1500)", v)
		}
	}
	for _, v := range byRoute["users"] {
		if v >= 0.5 {
			t.Errorf("route=users upstream_response_time %.3f, want < 0.5", v)
		}
	}
}

func withIDsPatient(t *testing.T, path, id string) int {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, gatewayURL+path, nil)
	req.Header.Set("X-Request-ID", id)
	resp, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	resp.Body.Close()
	return resp.StatusCode
}
