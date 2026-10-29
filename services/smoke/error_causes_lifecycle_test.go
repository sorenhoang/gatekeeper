//go:build smoke && lifecycle

package smoke

import (
	"fmt"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

const errorCausesHeading = "## Why a 502 or a 504"

type causeRow struct {
	cause  string
	status int
	phrase string
}

// errorCauses reads the table under errorCausesHeading in README.md.
// Columns: Cause | Status | Error-log phrase (in backticks) | How to reproduce.
func errorCauses(t *testing.T) []causeRow {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(repoRoot, "README.md"))
	if err != nil {
		t.Fatal(err)
	}
	readme := string(b)
	start := strings.Index(readme, "\n"+errorCausesHeading)
	if start < 0 {
		t.Fatalf("README has no %q section", errorCausesHeading)
	}
	var rows []causeRow
	for _, line := range strings.Split(readme[start+1:], "\n")[1:] {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "## ") {
			break
		}
		if !strings.HasPrefix(line, "|") || strings.Contains(line, "---") {
			continue
		}
		cells := strings.Split(strings.Trim(line, "|"), "|")
		if len(cells) < 4 {
			t.Errorf("README cause row has %d cells, want 4: %s", len(cells), line)
			continue
		}
		status, err := strconv.Atoi(strings.TrimSpace(cells[1]))
		if err != nil {
			continue // header row
		}
		phrase := strings.TrimSpace(cells[2])
		if i, j := strings.Index(phrase, "`"), strings.LastIndex(phrase, "`"); i >= 0 && j > i {
			phrase = phrase[i+1 : j]
		}
		rows = append(rows, causeRow{cause: strings.ToLower(strings.TrimSpace(cells[0])), status: status, phrase: phrase})
	}
	return rows
}

// tempServer adds a server for host that proxies everything to target.
func tempServer(t *testing.T, name, host, target string) {
	t.Helper()
	writeConf(t, name, fmt.Sprintf(`server {
    listen 80;
    server_name %s;
    location / {
        include /etc/nginx/gatekeeper/snippets/proxy-headers.conf;
        proxy_pass http://%s;
    }
}
`, host, target))
	if out, err := nginxCmd(t, "-s", "reload"); err != nil {
		t.Fatalf("reload with %s: %v\n%s", name, err, out)
	}
}

// unusedIP returns an address in the gateway's network that no container
// holds, like the old IP of a container that was just stopped.
func unusedIP(t *testing.T) string {
	t.Helper()
	gw := netip.MustParseAddr(containerIP(t, "gateway")).As4()
	gw[3] = 250
	return netip.AddrFrom4(gw).String()
}

// reproduce triggers one cause and returns the final status and the marker
// of the request that produced it. Each attempt uses a fresh marker, so the
// log line checked later belongs to that request and not an earlier retry.
func reproduce(t *testing.T, cause string) (int, string) {
	t.Helper()
	newMarker := func() string { return fmt.Sprintf("cause-%d", time.Now().UnixNano()) }
	retry := func(host, prefix string, want int) (int, string) {
		var status int
		var marker string
		eventually(reresolveWithin, func() bool {
			marker = newMarker()
			r, err := route(t, host, prefix+marker)
			status = r.status
			return err == nil && r.status == want
		})
		return status, marker
	}
	switch {
	case strings.Contains(cause, "no address"):
		restoreStack(t)
		mustCompose(t, "stop", "user-service")
		return retry("", "/api/users/", http.StatusBadGateway)
	case strings.Contains(cause, "stale"):
		tempServer(t, "zz-smoke-stale.conf", "stale.smoke.test", unusedIP(t)+":8080")
		return retry("stale.smoke.test", "/", http.StatusGatewayTimeout)
	case strings.Contains(cause, "refused"):
		// A running container but nothing listening on the port.
		tempServer(t, "zz-smoke-refused.conf", "refused.smoke.test", "user-service:9999")
		return retry("refused.smoke.test", "/", http.StatusBadGateway)
	case strings.Contains(cause, "timed out"):
		withServiceEnv(t, "user-service", "DELAY_MS=3000")
		marker := newMarker()
		return timedGet(t, "/api/users/"+marker).status, marker
	}
	t.Fatalf("README cause %q has no reproducer in this test", cause)
	return 0, ""
}

func TestShouldReproduceEveryReadmeErrorCause(t *testing.T) {
	rows := errorCauses(t)
	want := []string{"no address", "stale", "refused", "timed out"}
	for _, w := range want {
		var found bool
		for _, r := range rows {
			found = found || strings.Contains(r.cause, w)
		}
		if !found {
			t.Errorf("README error table has no %q row", w)
		}
	}
	for _, row := range rows {
		t.Run(row.cause, func(t *testing.T) {
			got, marker := reproduce(t, row.cause)
			if got != row.status {
				t.Errorf("%s: status %d, README says %d", row.cause, got, row.status)
			}
			var line string
			eventually(2*time.Second, func() bool {
				for _, l := range strings.Split(mustCompose(t, "logs", "--no-log-prefix", "--since", "1m", "gateway"), "\n") {
					if strings.Contains(l, marker) && strings.Contains(l, "[error]") {
						line = l
						if strings.Contains(l, row.phrase) {
							return true
						}
					}
				}
				return false
			})
			if !strings.Contains(line, row.phrase) {
				t.Errorf("%s: error log for %s = %q, want it to contain %q", row.cause, marker, line, row.phrase)
			}
		})
	}
}
