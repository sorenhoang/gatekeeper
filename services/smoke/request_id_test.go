//go:build smoke

package smoke

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

var generatedID = regexp.MustCompile(`^[0-9a-f]{32}$`)

type idResponse struct {
	status int
	ids    []string // every X-Request-ID header on the response
	body   string
}

// withIDs sends a request with the given X-Request-ID values (none, one, or
// several headers) and returns the response's X-Request-ID headers.
func withIDs(t *testing.T, method, host, path string, body []byte, ids ...string) idResponse {
	t.Helper()
	req, err := http.NewRequest(method, gatewayURL+path, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if host != "" {
		req.Host = host
	}
	for _, id := range ids {
		req.Header.Add("X-Request-ID", id)
	}
	resp, err := noRedirect.Do(req)
	if err != nil {
		t.Fatalf("%s %s (Host %q): %v", method, path, host, err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return idResponse{status: resp.StatusCode, ids: resp.Header.Values("X-Request-Id"), body: string(b)}
}

func (r idResponse) one(t *testing.T, what string) string {
	t.Helper()
	if len(r.ids) != 1 {
		t.Fatalf("%s: response has %d X-Request-ID headers %q, want exactly 1", what, len(r.ids), r.ids)
	}
	return r.ids[0]
}

func backendID(t *testing.T, r idResponse) string {
	t.Helper()
	var e headerEcho
	if err := json.Unmarshal([]byte(r.body), &e); err != nil {
		t.Fatalf("not a backend echo: %v\n%s", err, r.body)
	}
	v := e.Headers["X-Request-Id"]
	if len(v) != 1 {
		t.Fatalf("backend got %d X-Request-ID values %q, want 1", len(v), v)
	}
	return v[0]
}

func serviceLogged(t *testing.T, svc, id string) bool {
	t.Helper()
	for deadline := time.Now().Add(2 * time.Second); time.Now().Before(deadline); time.Sleep(200 * time.Millisecond) {
		for _, l := range logLinesWith(t, svc, "request_id="+id) {
			if strings.HasSuffix(strings.TrimSpace(l), "request_id="+id) {
				return true
			}
		}
	}
	return false
}

func TestShouldGenerateARequestIDAndCarryItToTheServiceLog(t *testing.T) {
	r := withIDs(t, http.MethodGet, "", "/api/users/", nil)
	id := r.one(t, "no client id")
	if !generatedID.MatchString(id) {
		t.Fatalf("generated X-Request-ID %q, want 32 lowercase hex chars", id)
	}
	if got := backendID(t, r); got != id {
		t.Errorf("backend got X-Request-ID %q, response says %q", got, id)
	}
	if !serviceLogged(t, "user-service", id) {
		t.Errorf("user-service did not log request_id=%s", id)
	}
}

func TestShouldKeepAWellFormedClientRequestID(t *testing.T) {
	const id = "abc-123.x_Y"
	r := withIDs(t, http.MethodGet, "", "/api/users/", nil, id)
	if got := r.one(t, "valid client id"); got != id {
		t.Errorf("response X-Request-ID %q, want the client's %q", got, id)
	}
	if got := backendID(t, r); got != id {
		t.Errorf("backend got %q, want %q", got, id)
	}
	if !serviceLogged(t, "user-service", id) {
		t.Errorf("user-service did not log request_id=%s", id)
	}
}

func TestShouldReplaceAMalformedClientRequestID(t *testing.T) {
	// Unique per run, so log lines from an earlier run can never match.
	run := fmt.Sprintf("%d", time.Now().UnixNano())
	cases := map[string][]string{
		"spaces":      {"x" + run + " status=500"},
		"65 chars":    {run + strings.Repeat("a", 65-len(run))},
		"two headers": {"one" + run, "two" + run},
	}
	for name, sent := range cases {
		r := withIDs(t, http.MethodGet, "", "/api/users/", nil, sent...)
		id := r.one(t, name)
		if !generatedID.MatchString(id) {
			t.Errorf("%s: response X-Request-ID %q, want a generated 32-hex id", name, id)
		}
		if got := backendID(t, r); got != id {
			t.Errorf("%s: backend got %q, response says %q", name, got, id)
		}
		for _, s := range sent {
			if len(logLinesWith(t, "user-service", "request_id="+s)) > 0 {
				t.Errorf("%s: user-service logged the client's value %q", name, s)
			}
		}
	}
}

func TestShouldReturnARequestIDFromEveryKindOfResponse(t *testing.T) {
	cases := []struct {
		name, method, host, path string
		body                     []byte
		status                   int
	}{
		{"proxied API", http.MethodGet, "", "/api/products/1", nil, http.StatusOK},
		{"JSON 404", http.MethodGet, "", "/api/unknown", nil, http.StatusNotFound},
		{"gateway /health", http.MethodGet, "", "/health", nil, http.StatusOK},
		{"static asset", http.MethodGet, "", "/assets/app.css", nil, http.StatusOK},
		{"SPA shell", http.MethodGet, "", "/settings", nil, http.StatusOK},
		{"admin host", http.MethodGet, adminHost, "/", nil, http.StatusOK},
		{"413", http.MethodPost, "", "/api/users/", bytes.Repeat([]byte("x"), 2<<20), http.StatusRequestEntityTooLarge},
	}
	for _, c := range cases {
		r := withIDs(t, c.method, c.host, c.path, c.body)
		if r.status != c.status {
			t.Errorf("%s: status %d, want %d", c.name, r.status, c.status)
		}
		if id := r.one(t, c.name); !generatedID.MatchString(id) {
			t.Errorf("%s: X-Request-ID %q, want a 32-hex id", c.name, id)
		}
	}
}

// locationBodies returns the body of every `location … { … }` block, counting
// braces outside quotes: JSON bodies in `return 404 '{"error":…}'` contain
// braces that a regex would stop at.
func locationBodies(conf string) []string {
	conf = stripComments(conf)
	var bodies []string
	for i := 0; i < len(conf); {
		j := strings.Index(conf[i:], "location")
		if j < 0 {
			break
		}
		start := i + j
		if start > 0 && !strings.ContainsRune(" \t\n", rune(conf[start-1])) {
			i = start + len("location")
			continue
		}
		open := strings.IndexByte(conf[start:], '{')
		if open < 0 {
			break
		}
		depth, k, quote := 0, start+open, byte(0)
		for ; k < len(conf); k++ {
			c := conf[k]
			switch {
			case quote != 0:
				if c == quote {
					quote = 0
				}
			case c == '\'' || c == '"':
				quote = c
			case c == '#':
				for k < len(conf) && conf[k] != '\n' {
					k++
				}
			case c == '{':
				depth++
			case c == '}':
				depth--
			}
			if depth == 0 {
				break
			}
		}
		bodies = append(bodies, conf[start+open+1:k])
		i = k + 1
	}
	return bodies
}

// stripComments drops `#` comments (outside quotes), so a comment that
// mentions "location" is never mistaken for a block.
func stripComments(conf string) string {
	var out strings.Builder
	for _, line := range strings.Split(conf, "\n") {
		quote := byte(0)
		cut := len(line)
		for i := 0; i < len(line); i++ {
			c := line[i]
			if quote != 0 {
				if c == quote {
					quote = 0
				}
			} else if c == '\'' || c == '"' {
				quote = c
			} else if c == '#' {
				cut = i
				break
			}
		}
		out.WriteString(line[:cut])
		out.WriteByte('\n')
	}
	return out.String()
}

// The inheritance trap: one add_header in a location drops all inherited ones,
// so X-Request-ID lives in a snippet every header-setting location includes.
func TestShouldIncludeTheResponseHeadersSnippetWhereverHeadersAreAdded(t *testing.T) {
	var checked int
	for _, file := range []string{"default.conf", "admin.conf"} {
		b, err := os.ReadFile(filepath.Join(repoRoot, "nginx", "conf.d", file))
		if err != nil {
			t.Fatal(err)
		}
		for _, body := range locationBodies(string(b)) {
			if !strings.Contains(body, "add_header") {
				continue
			}
			checked++
			if !strings.Contains(body, "snippets/response-headers.conf") {
				t.Errorf("%s: a location with add_header does not include snippets/response-headers.conf:\n%s", file, body)
			}
		}
	}
	if checked < 11 {
		t.Errorf("checked %d header-setting locations, want at least 11", checked)
	}
	fallback, err := os.ReadFile(filepath.Join(repoRoot, "nginx", "conf.d", "orders-fallback.conf"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(fallback), "response-headers.conf") {
		t.Errorf("orders-fallback.conf includes response-headers.conf: its answer passes through the orders location, which already adds X-Request-ID")
	}
}
