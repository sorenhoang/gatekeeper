//go:build smoke

package smoke

import (
	"encoding/base64"
	"encoding/hex"
	"net/http"
	"strings"
	"testing"
)

// The njs check is internal: only auth_request may run it.
func TestShouldKeepTheNjsAuthCheckInternal(t *testing.T) {
	if a := authed(t, http.MethodGet, "/_auth_njs", "Bearer "+freshToken("user"), nil); a.status != http.StatusNotFound {
		t.Errorf("GET /_auth_njs from outside → %d, want 404", a.status)
	}
}

// The gateway holds the signing key for njs, read from a file at start: it is
// never part of the config nginx prints, nor of anything it logs.
func TestShouldNeverPrintTheSigningKey(t *testing.T) {
	if len(labKey) == 0 {
		t.Fatal("no lab signing key; run make secrets")
	}
	forms := []string{string(labKey), hex.EncodeToString(labKey), base64.StdEncoding.EncodeToString(labKey), base64.RawURLEncoding.EncodeToString(labKey)}
	conf, err := nginxCmd(t, "-T")
	if err != nil {
		t.Fatalf("nginx -T: %v\n%s", err, conf)
	}
	if !strings.Contains(conf, "ngx_http_js_module") || !strings.Contains(conf, "_auth_njs") {
		t.Fatal("nginx -T shows no njs module or /_auth_njs location; the njs check isn't configured")
	}
	logs := mustCompose(t, "logs", "--tail", "5000", "gateway")
	for _, f := range forms {
		if strings.Contains(conf, f) {
			t.Error("nginx -T output contains the signing key")
		}
		if strings.Contains(logs, f) {
			t.Error("the gateway's logs contain the signing key")
		}
	}
}
