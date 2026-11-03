package main

import (
	"log"
	"net"
	"net/http"
	"os"
	"time"

	"google.golang.org/grpc"

	"gatekeeper/services/internal/echo"
	"gatekeeper/services/internal/realtime"
)

const (
	// gRPC speaks HTTP/2 on its own port; the gateway reaches it with
	// grpc_pass grpc://realtime-service:9090 (h2c, no TLS inside compose).
	grpcAddr = ":9090"
	// How long in-flight RPCs may finish after SIGTERM before they are cut;
	// with HTTP's own drain this stays inside docker's 10s stop window.
	grpcDrain = 3 * time.Second
)

func main() {
	cfg, err := realtime.LoadConfig(os.Getenv)
	if err != nil {
		log.Fatalf("realtime-service: %v", err)
	}
	lis, err := net.Listen("tcp", grpcAddr)
	if err != nil {
		log.Fatalf("realtime-service: gRPC listen: %v", err)
	}
	gs := realtime.NewGRPCServer()
	go func() {
		if err := gs.Serve(lis); err != nil {
			log.Printf("realtime-service: gRPC stopped: %v", err)
		}
	}()

	grpcStopped := make(chan struct{})
	err = echo.RunWith("realtime-service", func(mux *http.ServeMux) func() {
		streams := realtime.Register(mux, cfg)
		// On SIGTERM: end the open streams so the HTTP drain isn't held up,
		// and drain gRPC at the same time rather than after.
		return func() {
			streams.Close()
			go func() {
				stopGRPC(gs, grpcDrain)
				close(grpcStopped)
			}()
		}
	})
	if err != nil {
		// HTTP failed (e.g. its port was taken), maybe before any shutdown.
		gs.Stop()
		log.Fatal(err)
	}
	<-grpcStopped
}

// stopGRPC lets in-flight RPCs finish, then cuts whatever is left.
func stopGRPC(gs *grpc.Server, drain time.Duration) {
	done := make(chan struct{})
	go func() { gs.GracefulStop(); close(done) }()
	select {
	case <-done:
	case <-time.After(drain):
		log.Printf("realtime-service: gRPC drain over %v, stopping", drain)
		gs.Stop()
	}
}
