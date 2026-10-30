//go:build smoke && lifecycle

package smoke

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"
)

// nginx's default is unlimited tries: with two dead primaries it would try
// both and then the backup (200). The shipped rules cap it at 2 tries, so the
// backup is never reached and the client gets an error after two connect
// timeouts. This only passes with upstream-retry.conf in effect.
func TestShouldStopAfterTwoTriesWithTheShippedRetryRules(t *testing.T) {
	restoreStack(t)
	dead := unusedIP(t)
	second := dead[:strings.LastIndex(dead, ".")+1] + "251"
	up := "zz_smoke_tries"
	writeConf(t, "zz-smoke-tries.conf", fmt.Sprintf(`upstream %s {
    server %s:8080;
    server %s:8080;
    server order-service:8080 backup;
}
server {
    listen 80;
    server_name tries.smoke.test;
    location / {
        include /etc/nginx/gatekeeper/snippets/proxy-headers.conf;
        include %s;
        proxy_pass http://%s;
    }
}
`, up, dead, second, retrySnippet, up))
	if out, err := nginxCmd(t, "-s", "reload"); err != nil {
		t.Fatalf("reload: %v\n%s", err, out)
	}
	time.Sleep(time.Second)

	status, e, took := doRequest(t, http.MethodGet, "tries.smoke.test", "/", "tries-get")
	if status == http.StatusOK {
		t.Errorf("GET with two dead primaries reached %q (200): the retry cap of 2 tries is not in effect", e.Service)
	}
	if took < 2*time.Second || took >= 3*time.Second {
		t.Errorf("GET gave up after %v, want two 1s connect timeouts (2s–3s)", took)
	}
}
