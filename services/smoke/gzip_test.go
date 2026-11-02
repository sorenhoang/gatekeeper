//go:build smoke

package smoke

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// rawClient keeps Go from asking for gzip and unpacking it, so the test sees
// exactly what the gateway sent.
var rawClient = &http.Client{Timeout: 10 * time.Second, Transport: &http.Transport{DisableCompression: true}}

func encodingOf(t *testing.T, path, acceptEncoding string) http.Header {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, gatewayURL+path, nil)
	if acceptEncoding != "" {
		req.Header.Set("Accept-Encoding", acceptEncoding)
	}
	resp, err := rawClient.Do(req)
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET %s = %d, want 200", path, resp.StatusCode)
	}
	return resp.Header
}

func TestShouldGzipJsonForClientsThatAcceptIt(t *testing.T) {
	gw, _ := uniqueProduct("gzip")
	h := encodingOf(t, gw, "gzip")
	if h.Get("Content-Encoding") != "gzip" {
		t.Errorf("products JSON with Accept-Encoding: gzip → Content-Encoding %q, want gzip", h.Get("Content-Encoding"))
	}
	if !strings.Contains(h.Get("Vary"), "Accept-Encoding") {
		t.Errorf("gzipped answer has Vary %q, want Accept-Encoding (caches must keep both forms apart)", h.Get("Vary"))
	}
	if got := encodingOf(t, gw, "").Get("Content-Encoding"); got != "" {
		t.Errorf("same path without Accept-Encoding → Content-Encoding %q, want none", got)
	}
}

// /health is 33 bytes: below gzip_min_length 256, compressing it costs more
// than it saves.
func TestShouldNotGzipTinyResponses(t *testing.T) {
	if got := encodingOf(t, "/health", "gzip").Get("Content-Encoding"); got != "" {
		t.Errorf("/health with Accept-Encoding: gzip → Content-Encoding %q, want none (below gzip_min_length)", got)
	}
}

func TestShouldDocumentCachingInTheReadme(t *testing.T) {
	b, err := os.ReadFile(filepath.Join(repoRoot, "README.md"))
	if err != nil {
		t.Fatal(err)
	}
	readme := string(b)
	start := strings.Index(readme, "\n## Caching")
	if start < 0 {
		t.Fatal(`README has no "## Caching" section`)
	}
	section := readme[start+1:]
	if end := strings.Index(section[3:], "\n## "); end >= 0 {
		section = section[:end+3]
	}
	for _, topic := range []string{"X-Cache-Status", "MISS", "HIT", "BYPASS", "STALE", "30s",
		"Authorization", "Cookie", "X-API-Key", "Set-Cookie", "proxy_cache_lock", "proxy_cache_use_stale",
		"gzip", "Vary", "before", "after"} {
		if !strings.Contains(section, topic) {
			t.Errorf("README Caching section does not cover %q", topic)
		}
	}
}
