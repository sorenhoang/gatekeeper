package auth

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"
)

// User is one lab account from secrets/auth/users.json.
type User struct {
	Password string `json:"password"`
	Role     string `json:"role"`
}

type Users map[string]User

// ParseUsers reads users.json and refuses accounts that could never be
// logged into safely.
func ParseUsers(b []byte) (Users, error) {
	var u Users
	if err := json.Unmarshal(b, &u); err != nil {
		return nil, fmt.Errorf("users.json: %w", err)
	}
	for name, user := range u {
		if user.Password == "" || user.Role == "" {
			return nil, fmt.Errorf("users.json: %s needs a password and a role", name)
		}
	}
	return u, nil
}

type Config struct {
	Key   []byte
	Users Users
	TTL   time.Duration
	Now   func() time.Time
}

// Register adds POST /login and GET /verify to a service's mux.
func Register(mux *http.ServeMux, cfg Config) {
	mux.HandleFunc("POST /login", cfg.login)
	// Without this, other methods would fall through to the echo "/".
	mux.HandleFunc("/login", onlyMethod(http.MethodPost))
	mux.HandleFunc("GET /verify", cfg.verify)
	// An echo 200 here would read as "allowed" to an auth_request.
	mux.HandleFunc("/verify", onlyMethod(http.MethodGet))
}

func onlyMethod(m string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Allow", m)
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "use " + m})
	}
}

func (cfg Config) login(w http.ResponseWriter, r *http.Request) {
	var in struct{ Username, Password string }
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&in); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "body must be JSON {username, password}"})
		return
	}
	user, known := cfg.Users[in.Username]
	// Compare hashes so the time taken doesn't depend on where the password
	// differs, and do it for unknown users too, so both answers look alike.
	want := sha256.Sum256([]byte(user.Password))
	got := sha256.Sum256([]byte(in.Password))
	if !known || subtle.ConstantTimeCompare(want[:], got[:]) != 1 {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "invalid credentials"})
		return
	}
	now := cfg.Now()
	token, err := sign(cfg.Key, Claims{Sub: in.Username, Role: user.Role, Iat: now.Unix(), Exp: now.Add(cfg.TTL).Unix()})
	if err != nil {
		log.Printf("signing a token for %s failed: %v", in.Username, err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "token signing failed"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"token": token, "expires_in": int(cfg.TTL.Seconds())})
}

// verify answers the gateway's auth_request subrequest: 204 with the caller's
// identity, 401 for any bad token, 403 when the role may not do this.
func (cfg Config) verify(w http.ResponseWriter, r *http.Request) {
	token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !ok {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	// Any error is a 401: never let an unexpected one fall through to 204.
	c, err := verify(cfg.Key, token, cfg.Now())
	if err != nil {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	// The gateway sends the client's method; only admins may delete.
	if r.Header.Get("X-Original-Method") == http.MethodDelete && c.Role != "admin" {
		w.WriteHeader(http.StatusForbidden)
		return
	}
	w.Header().Set("X-User-ID", c.Sub)
	w.Header().Set("X-User-Role", c.Role)
	w.WriteHeader(http.StatusNoContent)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		log.Printf("writing response failed: %v", err)
	}
}
