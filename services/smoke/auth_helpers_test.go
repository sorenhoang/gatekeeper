//go:build smoke

package smoke

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Since Phase 8 users and orders need a token and admin.localhost a Basic
// login. Tests that aren't about auth get both from labAuth; a test that
// must send no credentials sets anonymousHeader, which labAuth strips.
const anonymousHeader = "X-Smoke-Anonymous"

type labUser struct{ Password, Role string }

// Read once from `make secrets`; missing files leave them empty, so requests
// fail with a 401 that names the cause instead of the test panicking.
var (
	labKey, _ = os.ReadFile(filepath.Join(repoRoot, "secrets", "auth", "jwt.key"))
	labUsers  = func() map[string]labUser {
		u := map[string]labUser{}
		if b, err := os.ReadFile(filepath.Join(repoRoot, "secrets", "auth", "users.json")); err == nil {
			json.Unmarshal(b, &u)
		}
		return u
	}()
)

// labToken signs a token the way auth-service does (HS256 with the lab key).
// The jti makes every token unique: iat/exp have 1s resolution, and two equal
// tokens would share a cached gateway verdict. auth-service ignores it.
func labToken(sub, role string, iat time.Time, ttl time.Duration) string {
	h := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"HS256","typ":"JWT"}`))
	c := base64.RawURLEncoding.EncodeToString([]byte(fmt.Sprintf(
		`{"sub":%q,"role":%q,"iat":%d,"exp":%d,"jti":"%d"}`, sub, role, iat.Unix(), iat.Add(ttl).Unix(), time.Now().UnixNano())))
	m := hmac.New(sha256.New, labKey)
	m.Write([]byte(h + "." + c))
	return h + "." + c + "." + base64.RawURLEncoding.EncodeToString(m.Sum(nil))
}

// A fresh token per call: the gateway caches verdicts per token for 10s, so
// tests about the auth path itself must not reuse one.
func freshToken(role string) string {
	sub := map[string]string{"user": "alice", "admin": "admin"}[role]
	return labToken(sub, role, time.Now(), 15*time.Minute)
}

// The token labAuth attaches: admin, so tests that DELETE through the API
// (Phase 3's degraded writes) are allowed, and 24h, so a long run can't
// outlive it.
var suiteToken = labToken("admin", "admin", time.Now(), 24*time.Hour)

type labAuth struct{ next http.RoundTripper }

func (a labAuth) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.Header.Get(anonymousHeader) != "" || r.Header.Get("Authorization") != "" {
		r = r.Clone(r.Context())
		r.Header.Del(anonymousHeader)
		return a.next.RoundTrip(r)
	}
	host := r.Host
	if host == "" {
		host = r.URL.Host
	}
	switch {
	case strings.HasPrefix(host, "admin.localhost"):
		r = r.Clone(r.Context())
		r.SetBasicAuth("admin", labUsers["admin"].Password)
	case strings.HasPrefix(r.URL.Path, "/api/users/") || strings.HasPrefix(r.URL.Path, "/api/orders/"):
		r = r.Clone(r.Context())
		r.Header.Set("Authorization", "Bearer "+suiteToken)
	}
	return a.next.RoundTrip(r)
}
