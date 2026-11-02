package main

import (
	"log"
	"net/http"
	"os"
	"time"

	"gatekeeper/services/internal/auth"
	"gatekeeper/services/internal/echo"
)

// From `make secrets`, mounted read-only (docker-compose.yml).
const (
	keyFile   = "/etc/gatekeeper/auth/jwt.key"
	usersFile = "/etc/gatekeeper/auth/users.json"
	// Short, because a token can't be revoked before it expires.
	tokenTTL = 15 * time.Minute
)

func main() {
	key, err := os.ReadFile(keyFile)
	if err != nil {
		log.Fatalf("auth-service: signing key: %v (run make secrets)", err)
	}
	if err := auth.CheckKey(key); err != nil {
		log.Fatalf("auth-service: %v (delete secrets/auth/jwt.key and run make secrets)", err)
	}
	raw, err := os.ReadFile(usersFile)
	if err != nil {
		log.Fatalf("auth-service: users: %v (run make secrets)", err)
	}
	users, err := auth.ParseUsers(raw)
	if err != nil {
		log.Fatalf("auth-service: %v", err)
	}
	cfg := auth.Config{Key: key, Users: users, TTL: tokenTTL, Now: time.Now}
	if err := echo.RunWith("auth-service", func(mux *http.ServeMux) { auth.Register(mux, cfg) }); err != nil {
		log.Fatal(err)
	}
}
