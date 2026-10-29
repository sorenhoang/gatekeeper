// Package echo is the shared behaviour of every Gatekeeper backend: a health
// check and a catch-all route that reports what the gateway actually sent.
package echo

import (
	"encoding/json"
	"log"
	"net/http"
	"os"
)

type echoResponse struct {
	Service    string              `json:"service"`
	Hostname   string              `json:"hostname"`
	Port       string              `json:"port"`
	Method     string              `json:"method"`
	Path       string              `json:"path"`
	Query      string              `json:"query"`
	Host       string              `json:"host"`
	RemoteAddr string              `json:"remote_addr"`
	Headers    map[string][]string `json:"headers"`
}

// Handler returns the mux for a service. Callers add their own routes to it
// in later phases; "/" stays the echo fallback.
func Handler(service, port string) *http.ServeMux {
	hostname, err := os.Hostname()
	if err != nil {
		log.Printf("os.Hostname failed, reporting empty hostname: %v", err)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		// Host and RemoteAddr are stripped out of r.Header by net/http, and the
		// escaped path shows whether the gateway re-encoded the URI; all three
		// are exactly what the proxy phases need to see.
		writeJSON(w, http.StatusOK, echoResponse{
			Service:    service,
			Hostname:   hostname,
			Port:       port,
			Method:     r.Method,
			Path:       r.URL.EscapedPath(),
			Query:      r.URL.RawQuery,
			Host:       r.Host,
			RemoteAddr: r.RemoteAddr,
			Headers:    r.Header,
		})
	})
	return mux
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		log.Printf("writing response failed: %v", err)
	}
}
