package echo

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
)

const (
	listenPort = "8080"
	// Long enough for in-flight requests without DELAY_MS to finish, short
	// enough that `docker compose stop` never reaches its 10s SIGKILL.
	shutdownTimeout = 5 * time.Second
)

// Run serves the echo backend until SIGTERM/SIGINT, then drains in-flight
// requests. Invalid chaos config is returned before listening.
func Run(service string) error { return RunWith(service, nil) }

// RunWith is Run with a service's own routes added to the echo mux; they go
// through the same chaos and logging middleware. routes may return a func
// run when shutdown starts: streams that never end on their own must stop
// there, or the drain would wait out shutdownTimeout.
func RunWith(service string, routes func(*http.ServeMux) (onShutdown func())) error {
	cfg, err := LoadConfig(os.Getenv)
	if err != nil {
		return fmt.Errorf("%s: %w", service, err)
	}

	mux := Handler(service, listenPort)
	var onShutdown func()
	if routes != nil {
		onShutdown = routes(mux)
	}
	srv := &http.Server{
		Addr:    ":" + listenPort,
		Handler: Wrap(mux, service, cfg, os.Stdout),
	}
	if onShutdown != nil {
		srv.RegisterOnShutdown(onShutdown)
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, os.Interrupt)
	defer stop()

	serveErr := make(chan error, 1)
	go func() { serveErr <- srv.ListenAndServe() }()
	fmt.Printf("%s listening on :%s (delay=%v fail_rate=%v)\n", service, listenPort, cfg.Delay, cfg.FailRate)

	select {
	case err := <-serveErr:
		return fmt.Errorf("%s: %w", service, err)
	case <-ctx.Done():
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("%s: shutdown: %w", service, err)
	}
	return nil
}
