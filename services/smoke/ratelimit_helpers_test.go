//go:build smoke

package smoke

import (
	"fmt"
	"io"
	"net/http"
	"sync"
	"testing"
	"time"
)

// drainWait is how long a drained bucket needs before every client is back to
// a full budget: the API zone's burst 100 at 50r/s refills in 2s (login: burst
// 10 at 5r/s, also 2s), plus 0.5s.
const drainWait = 2500 * time.Millisecond

type hit struct {
	id      string
	status  int
	header  http.Header
	body    string
	latency time.Duration
}

// warmClient returns a client holding n open keep-alive connections to the
// gateway. Opening 50 new connections through Docker Desktop's port proxy at
// the moment of a spike spreads the arrivals over hundreds of ms under load,
// and the bucket refills mid-spike (measured: 13 passes instead of 11).
// /health is answered by `return`, so warming never touches a rate limit.
// Connections are pooled by address, not Host header, so the spike reuses
// them whatever host it targets.
func warmClient(t *testing.T, n int) *http.Client {
	t.Helper()
	client := &http.Client{
		Timeout:   10 * time.Second,
		Transport: labAuth{next: &http.Transport{TLSClientConfig: labTLS.Clone(), MaxIdleConnsPerHost: n, MaxConnsPerHost: n, IdleConnTimeout: 30 * time.Second}},
	}
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			resp, err := client.Get(gatewayURL + "/health")
			if err != nil {
				t.Errorf("warming connection: %v", err)
				return
			}
			io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
		}()
	}
	close(start)
	wg.Wait()
	return client
}

// spike sends n requests at once, each with its own X-Request-ID, on n
// already-open connections, and waits for all of them. Extra headers apply to
// every request.
func spike(t *testing.T, n int, host, path string, extra map[string]string) []hit {
	t.Helper()
	client := warmClient(t, n)
	prefix := fmt.Sprintf("spike-%d", time.Now().UnixNano())
	hits := make([]hit, n)
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			id := fmt.Sprintf("%s-%d", prefix, i)
			req, err := http.NewRequest(http.MethodGet, gatewayURL+path, nil)
			if err != nil {
				t.Error(err)
				return
			}
			if host != "" {
				req.Host = host
			}
			req.Header.Set("X-Request-ID", id)
			for k, v := range extra {
				req.Header.Set(k, v)
			}
			<-start
			t0 := time.Now()
			resp, err := client.Do(req)
			if err != nil {
				t.Errorf("spike request %d: %v", i, err)
				return
			}
			b, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			hits[i] = hit{id: id, status: resp.StatusCode, header: resp.Header, body: string(b), latency: time.Since(t0)}
		}(i)
	}
	close(start)
	wg.Wait()
	return hits
}

func countStatus(hits []hit) map[int]int {
	c := map[int]int{}
	for _, h := range hits {
		c[h.status]++
	}
	return c
}

// afterDrain makes the test wait, once it ends, until drained buckets refill,
// so the next test (same client IP) starts with a full budget.
func afterDrain(t *testing.T) {
	t.Helper()
	time.Sleep(drainWait)
	t.Cleanup(func() { time.Sleep(drainWait) })
}
