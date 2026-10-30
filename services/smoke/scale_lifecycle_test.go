//go:build smoke && lifecycle

package smoke

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

const scaleWithin = 5 * time.Second

func scaleOrders3(t *testing.T, n string) {
	t.Helper()
	mustCompose(t, "up", "-d", "--no-deps", "--scale", "order-service-3="+n, "order-service-3")
}

// distinctHostnamesWithin sends batches of 30 GETs until one batch shows want
// distinct order hostnames, or the time runs out.
func distinctHostnamesWithin(t *testing.T, within time.Duration, want int) (int, map[string]int) {
	t.Helper()
	var last map[string]int
	eventually(within, func() bool {
		last = map[string]int{}
		for i := 0; i < 30; i++ {
			if status, e := getEcho(t, "/api/orders/"); status == http.StatusOK {
				last[e.Hostname]++
			}
		}
		return len(last) == want
	})
	return len(last), last
}

func TestShouldSendTrafficToAScaledUpReplicaWithoutReload(t *testing.T) {
	restoreStack(t)
	before := sighupCount(t)
	scaleOrders3(t, "2")

	if n, seen := distinctHostnamesWithin(t, scaleWithin, 4); n != 4 {
		t.Errorf("after --scale order-service-3=2, orders reached %d replicas %v within %s, want 4", n, seen, scaleWithin)
	}
	if after := sighupCount(t); after != before {
		t.Errorf("nginx was reloaded (%d → %d SIGHUP log lines); scaling must be picked up by resolve alone", before, after)
	}
}

func TestShouldDropAScaledDownReplicaWithoutFailingRequests(t *testing.T) {
	restoreStack(t)
	scaleOrders3(t, "2")
	if n, _ := distinctHostnamesWithin(t, scaleWithin, 4); n != 4 {
		t.Fatalf("setup: 4 replicas never answered")
	}

	var failed, sent atomic.Int64
	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		client := &http.Client{Timeout: 5 * time.Second}
		for {
			select {
			case <-stop:
				return
			default:
			}
			resp, err := client.Get(gatewayURL + "/api/orders/")
			sent.Add(1)
			if err != nil || resp.StatusCode != http.StatusOK {
				failed.Add(1)
			}
			if err == nil {
				resp.Body.Close()
			}
		}
	}()

	scaleOrders3(t, "1")
	n, seen := distinctHostnamesWithin(t, scaleWithin, 3)
	close(stop)
	wg.Wait()

	if n != 3 {
		t.Errorf("after scaling back to 1, orders reached %d replicas %v, want 3", n, seen)
	}
	// Same race as the stop test (failover_lifecycle_test.go): the request
	// waiting on the removed replica's old IP when nginx re-resolves can fail.
	const maxFailuresPerRemovedReplica = 1
	if f := failed.Load(); f > maxFailuresPerRemovedReplica {
		t.Errorf("%d of %d requests failed while a replica was removed, want at most %d", f, sent.Load(), maxFailuresPerRemovedReplica)
	}
}

func TestShouldDescribeLoadBalancingInTheReadme(t *testing.T) {
	b, err := os.ReadFile(filepath.Join(repoRoot, "README.md"))
	if err != nil {
		t.Fatal(err)
	}
	readme := string(b)
	start := strings.Index(readme, "\n## Load balancing")
	if start < 0 {
		t.Fatal("README has no \"## Load balancing\" section")
	}
	section := readme[start+1:]
	if end := strings.Index(section[3:], "\n## "); end >= 0 {
		section = section[:end+3]
	}
	for _, topic := range []string{"order-service-2", "least_conn", "retr", "backup", "keepalive", "--scale", "X-Degraded"} {
		if !strings.Contains(section, topic) {
			t.Errorf("README Load balancing section does not cover %q", topic)
		}
	}
}
