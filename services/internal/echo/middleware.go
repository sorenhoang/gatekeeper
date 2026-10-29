package echo

import (
	"fmt"
	"io"
	"log"
	"math/rand/v2"
	"net/http"
	"strconv"
	"time"
)

const (
	healthPath = "/health"
	// Nginx's own code for "client closed the connection before the response".
	statusClientClosedRequest = 499
)

// sleep waits d unless the request is cancelled first; false means cancelled.
func sleep(r *http.Request, d time.Duration) bool {
	if d == 0 {
		return true
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return true
	case <-r.Context().Done():
		return false
	}
}

// Config holds the knobs that make a backend misbehave on purpose. Values are
// read once at startup; change them by recreating the container.
type Config struct {
	Delay    time.Duration
	FailRate float64
}

// LoadConfig reads DELAY_MS and FAIL_RATE. A bad value is an error rather
// than a silent zero, so a typo never looks like a healthy backend.
func LoadConfig(getenv func(string) string) (Config, error) {
	var cfg Config
	if v := getenv("DELAY_MS"); v != "" {
		ms, err := strconv.Atoi(v)
		if err != nil || ms < 0 {
			return Config{}, fmt.Errorf("DELAY_MS must be a non-negative integer, got %q", v)
		}
		cfg.Delay = time.Duration(ms) * time.Millisecond
	}
	if v := getenv("FAIL_RATE"); v != "" {
		rate, err := strconv.ParseFloat(v, 64)
		// Written as a negated range so NaN, which fails every comparison, is rejected.
		if err != nil || !(rate >= 0 && rate <= 1) {
			return Config{}, fmt.Errorf("FAIL_RATE must be a number between 0 and 1, got %q", v)
		}
		cfg.FailRate = rate
	}
	return cfg, nil
}

// Wrap applies the chaos knobs to every path except /health, and logs one
// line per request so backend hits can be counted and traced by request id.
func Wrap(next http.Handler, service string, cfg Config, logOut io.Writer) http.Handler {
	logger := log.New(logOut, "", log.LstdFlags|log.LUTC)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}

		switch {
		case r.URL.Path == healthPath:
			next.ServeHTTP(rec, r)
		case !sleep(r, cfg.Delay):
			// The gateway gave up (proxy_read_timeout) or the client left: log
			// it as such instead of a 200 nobody received.
			rec.status = statusClientClosedRequest
		case cfg.FailRate > 0 && rand.Float64() < cfg.FailRate:
			writeJSON(rec, http.StatusInternalServerError, map[string]string{
				"error":   "injected failure",
				"service": service,
			})
		default:
			next.ServeHTTP(rec, r)
		}

		requestID := r.Header.Get("X-Request-ID")
		if requestID == "" {
			requestID = "-"
		}
		logger.Printf("service=%s method=%s path=%s status=%d duration_ms=%d request_id=%s",
			service, r.Method, r.URL.EscapedPath(), rec.status, time.Since(start).Milliseconds(), requestID)
	})
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (s *statusRecorder) WriteHeader(code int) {
	s.status = code
	s.ResponseWriter.WriteHeader(code)
}

// Unwrap lets http.ResponseController reach Flush/Hijack on the real writer
// (needed by the SSE and WebSocket routes of Phase 9).
func (s *statusRecorder) Unwrap() http.ResponseWriter { return s.ResponseWriter }
