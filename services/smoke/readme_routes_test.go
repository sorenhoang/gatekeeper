//go:build smoke

package smoke

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const routeTableHeading = "## Routes"

type routeRow struct{ host, url, xRoute string }

// routeTable reads the first Markdown table under "## Routes" in README.md.
// Columns: Host | URL | Winning location (X-Route) | Answered by.
// The X-Route cell holds the header value in backticks, e.g. `users`.
func routeTable(t *testing.T) []routeRow {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(repoRoot, "README.md"))
	if err != nil {
		t.Fatal(err)
	}
	readme := string(b)
	start := strings.Index(readme, "\n"+routeTableHeading)
	if start < 0 {
		t.Fatalf("README has no %q section", routeTableHeading)
	}
	var rows []routeRow
	inTable := false
	for _, line := range strings.Split(readme[start+1:], "\n")[1:] {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "## ") {
			break
		}
		if !strings.HasPrefix(line, "|") {
			if inTable {
				break
			}
			continue
		}
		inTable = true
		cells := strings.Split(strings.Trim(line, "|"), "|")
		if len(cells) < 4 || strings.Contains(cells[0], "---") || strings.TrimSpace(cells[0]) == "Host" {
			continue
		}
		clean := func(s string) string { return strings.Trim(strings.TrimSpace(s), "`") }
		x := clean(cells[2])
		if i := strings.Index(x, "`"); i >= 0 {
			x = x[:i]
		}
		rows = append(rows, routeRow{host: clean(cells[0]), url: clean(cells[1]), xRoute: strings.TrimSpace(x)})
	}
	if len(rows) == 0 {
		t.Fatalf("README %q section has no table rows", routeTableHeading)
	}
	return rows
}

func TestShouldMatchEveryReadmeRouteRowAgainstTheLiveGateway(t *testing.T) {
	for _, row := range routeTable(t) {
		host := row.host
		if host == "localhost:8080" {
			host = ""
		}
		r := mustRoute(t, host, row.url)
		if got := r.header.Get("X-Route"); got != row.xRoute {
			t.Errorf("README says Host %s %s → X-Route %q, gateway answered %q (status %d)",
				row.host, row.url, row.xRoute, got, r.status)
		}
	}
}

func TestShouldListEveryLocationAndThePrecedenceCasesInTheReadme(t *testing.T) {
	rows := routeTable(t)
	seen := map[string]bool{}
	urls := map[string]bool{}
	for _, row := range rows {
		seen[row.xRoute] = true
		urls[row.url] = true
	}
	for _, x := range []string{"users", "orders", "products", "health", "api-404", "spa", "static", "admin"} {
		if !seen[x] {
			t.Errorf("README route table has no row for X-Route %q", x)
		}
	}
	for _, u := range []string{"/health", "/api/usersX", "/api/users/", "/api/products/logo.png"} {
		if !urls[u] {
			t.Errorf("README route table is missing the precedence case %s", u)
		}
	}
	var regexCase bool
	for u := range urls {
		if strings.HasPrefix(u, "/assets/") && strings.HasSuffix(u, ".png") {
			regexCase = true
		}
	}
	if !regexCase {
		t.Errorf("README route table is missing a /assets/*.png row (regex vs /)")
	}
}

// Updated each phase: the roadmap marks done phases ☑ and the current one ◐.
func TestShouldMarkFinishedPhasesDoneAndTheCurrentOneInProgress(t *testing.T) {
	b, err := os.ReadFile(filepath.Join(repoRoot, "README.md"))
	if err != nil {
		t.Fatal(err)
	}
	status := map[string]string{"| 0 |": "☑", "| 1 |": "☑", "| 2 |": "◐"}
	for _, line := range strings.Split(string(b), "\n") {
		for prefix, mark := range status {
			if strings.HasPrefix(line, prefix) {
				if !strings.HasSuffix(strings.TrimSpace(line), mark+" |") {
					t.Errorf("roadmap row %q, want it to end with %s", line, mark)
				}
				delete(status, prefix)
			}
		}
	}
	for prefix := range status {
		t.Errorf("README roadmap has no row starting %q", prefix)
	}
}
