package realtime

import (
	"context"
	"errors"
	"fmt"
	"log"
	"math"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/coder/websocket"
)

// Browsers on the gateway's CORS origins (Phase 8) may open WebSockets; a
// client without an Origin header (curl, Go) is always accepted.
var originPatterns = []string{"localhost:8443", "app.localhost:8443", "localhost:5173"}

// Streams tracks the open streams so shutdown can end them: http.Server's
// Shutdown waits for SSE responses that never finish on their own and
// doesn't know about hijacked WebSockets at all.
type Streams struct {
	closing chan struct{}
	once    sync.Once
}

// Close ends every open SSE stream, closes every WebSocket with 1001
// (going away) and refuses new streams. Safe to call more than once.
func (s *Streams) Close() { s.once.Do(func() { close(s.closing) }) }

func (s *Streams) closed() bool {
	select {
	case <-s.closing:
		return true
	default:
		return false
	}
}

// Register adds /ws and GET /events to a service's mux.
func Register(mux *http.ServeMux, cfg Config) *Streams {
	s := &Streams{closing: make(chan struct{})}
	h := handlers{cfg: cfg, streams: s}
	mux.HandleFunc("/ws", h.websocket)
	mux.HandleFunc("GET /events", h.events)
	return s
}

type handlers struct {
	cfg     Config
	streams *Streams
}

func caller(r *http.Request) string {
	if id := r.Header.Get("X-User-ID"); id != "" {
		return id
	}
	return "anonymous"
}

// websocket echoes text messages as "<caller>: <message>" and pings on the
// interval so an idle connection is never silent past the gateway timeout.
func (h handlers) websocket(w http.ResponseWriter, r *http.Request) {
	if h.streams.closed() {
		http.Error(w, "shutting down", http.StatusServiceUnavailable)
		return
	}
	c, err := websocket.Accept(w, r, &websocket.AcceptOptions{OriginPatterns: originPatterns})
	if err != nil {
		log.Printf("websocket accept: %v", err)
		return
	}
	defer c.CloseNow()
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	go h.watch(ctx, c)

	who := caller(r)
	for {
		typ, msg, err := c.Read(ctx)
		if err != nil {
			if websocket.CloseStatus(err) == -1 && !errors.Is(err, context.Canceled) {
				log.Printf("websocket read: %v", err)
			}
			return
		}
		if err := c.Write(ctx, typ, []byte(who+": "+string(msg))); err != nil {
			log.Printf("websocket write: %v", err)
			return
		}
	}
}

// watch pings on the interval and closes the connection when a pong doesn't
// come back in time (1008) or the service shuts down (1001); either makes the
// read loop return.
func (h handlers) watch(ctx context.Context, c *websocket.Conn) {
	t := time.NewTicker(h.cfg.PingInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-h.streams.closing:
			c.Close(websocket.StatusGoingAway, "shutting down")
			return
		case <-t.C:
			// Ping waits for the pong; the read loop answers it.
			pctx, cancel := context.WithTimeout(ctx, h.cfg.PingInterval)
			err := c.Ping(pctx)
			cancel()
			if err != nil {
				if ctx.Err() == nil {
					c.Close(websocket.StatusPolicyViolation, "ping timeout")
				}
				return
			}
			if h.cfg.onPong != nil {
				h.cfg.onPong()
			}
		}
	}
}

// events streams numbered SSE events and heartbeat comments until the client
// goes away or the service shuts down. Last-Event-ID resumes after the given
// id, as EventSource does when it reconnects.
func (h handlers) events(w http.ResponseWriter, r *http.Request) {
	if h.streams.closed() {
		http.Error(w, "shutting down", http.StatusServiceUnavailable)
		return
	}
	next := 1
	// A last id at the top of the range would overflow; start over instead.
	if last, err := strconv.Atoi(r.Header.Get("Last-Event-ID")); err == nil && last >= 0 && last < math.MaxInt {
		next = last + 1
	}
	hd := w.Header()
	hd.Set("Content-Type", "text/event-stream")
	hd.Set("Cache-Control", "no-cache")
	// Tells nginx not to buffer this response, whatever proxy_buffering says.
	hd.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	// The logging middleware wraps w; ResponseController finds the flusher.
	rc := http.NewResponseController(w)

	events, beats := time.NewTicker(h.cfg.EventInterval), time.NewTicker(h.cfg.HeartbeatInterval)
	defer events.Stop()
	defer beats.Stop()
	send := func(s string) bool {
		if _, err := fmt.Fprint(w, s); err != nil {
			return false
		}
		return rc.Flush() == nil
	}
	who := caller(r)
	if !send(fmt.Sprintf("id: %d\ndata: %s %d\n\n", next, who, next)) {
		return
	}
	for {
		select {
		case <-r.Context().Done():
			return
		case <-h.streams.closing:
			return
		case <-events.C:
			next++
			if !send(fmt.Sprintf("id: %d\ndata: %s %d\n\n", next, who, next)) {
				return
			}
		case <-beats.C:
			if !send(": heartbeat\n\n") {
				return
			}
		}
	}
}
