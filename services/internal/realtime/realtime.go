// Package realtime is realtime-service's WebSocket, SSE and gRPC endpoints:
// the long-lived and HTTP/2 traffic the gateway proxies in Phase 9 (README
// "Realtime & gRPC"). Each answer names the caller from the X-User-ID the
// gateway set, never from anything the client sent.
package realtime

import (
	"fmt"
	"time"
)

// Config holds the stream intervals. The defaults are short enough to keep
// a connection inside the gateway's 1h read timeout and long enough to cost
// nothing; tests shorten them through the environment.
type Config struct {
	PingInterval      time.Duration // WebSocket pings
	EventInterval     time.Duration // SSE data events
	HeartbeatInterval time.Duration // SSE comment lines

	onPong func() // test hook: called after each answered ping
}

var defaults = map[string]time.Duration{
	"WS_PING_INTERVAL":       30 * time.Second,
	"SSE_EVENT_INTERVAL":     time.Second,
	"SSE_HEARTBEAT_INTERVAL": 15 * time.Second,
}

// LoadConfig reads the intervals (Go durations, e.g. "5s") from getenv.
func LoadConfig(getenv func(string) string) (Config, error) {
	got := map[string]time.Duration{}
	for name, def := range defaults {
		got[name] = def
		if v := getenv(name); v != "" {
			d, err := time.ParseDuration(v)
			if err != nil || d <= 0 {
				return Config{}, fmt.Errorf("%s=%q: want a positive duration like 5s", name, v)
			}
			got[name] = d
		}
	}
	return Config{
		PingInterval:      got["WS_PING_INTERVAL"],
		EventInterval:     got["SSE_EVENT_INTERVAL"],
		HeartbeatInterval: got["SSE_HEARTBEAT_INTERVAL"],
	}, nil
}
