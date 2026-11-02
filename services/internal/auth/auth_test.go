package auth

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

var (
	testKey   = []byte("0123456789abcdef0123456789abcdef")
	testUsers = Users{
		"alice": {Password: "alice-pass", Role: "user"},
		"admin": {Password: "admin-pass", Role: "admin"},
	}
	t0 = time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC)
)

func newMux(now func() time.Time) *http.ServeMux {
	mux := http.NewServeMux()
	Register(mux, Config{Key: testKey, Users: testUsers, TTL: 15 * time.Minute, Now: now})
	return mux
}

func do(t *testing.T, mux http.Handler, method, path, body string, hdr map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func login(t *testing.T, mux http.Handler, user, pass string) *httptest.ResponseRecorder {
	t.Helper()
	b, _ := json.Marshal(map[string]string{"username": user, "password": pass})
	return do(t, mux, http.MethodPost, "/login", string(b), map[string]string{"Content-Type": "application/json"})
}

func TestShouldIssueAFifteenMinuteHS256TokenForALabUser(t *testing.T) {
	mux := newMux(func() time.Time { return t0 })
	rec := login(t, mux, "alice", "alice-pass")
	if rec.Code != http.StatusOK {
		t.Fatalf("login alice → %d %s, want 200", rec.Code, rec.Body)
	}
	var out struct {
		Token     string `json:"token"`
		ExpiresIn int    `json:"expires_in"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil || out.Token == "" || out.ExpiresIn != 900 {
		t.Fatalf("login body %s (err %v), want {token, expires_in: 900}", rec.Body, err)
	}
	parts := strings.Split(out.Token, ".")
	if len(parts) != 3 {
		t.Fatalf("token %q is not a JWT", out.Token)
	}
	var header map[string]string
	var claims map[string]any
	hb, _ := base64.RawURLEncoding.DecodeString(parts[0])
	cb, _ := base64.RawURLEncoding.DecodeString(parts[1])
	json.Unmarshal(hb, &header)
	json.Unmarshal(cb, &claims)
	if header["alg"] != "HS256" {
		t.Errorf("JWT alg %q, want HS256", header["alg"])
	}
	if claims["sub"] != "alice" || claims["role"] != "user" ||
		claims["iat"] != float64(t0.Unix()) || claims["exp"] != float64(t0.Unix()+900) {
		t.Errorf("claims %v, want sub alice, role user, iat %d, exp iat+900", claims, t0.Unix())
	}
}

func TestShouldRefuseBadCredentialsWithOneAnswer(t *testing.T) {
	mux := newMux(time.Now)
	wrong := login(t, mux, "alice", "nope")
	unknown := login(t, mux, "mallory", "alice-pass")
	for name, rec := range map[string]*httptest.ResponseRecorder{"wrong password": wrong, "unknown user": unknown} {
		if rec.Code != http.StatusUnauthorized || strings.TrimSpace(rec.Body.String()) != `{"error":"invalid credentials"}` {
			t.Errorf("%s → %d %s, want 401 {\"error\":\"invalid credentials\"}", name, rec.Code, rec.Body)
		}
	}
	if rec := do(t, mux, http.MethodGet, "/login", "", nil); rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("GET /login → %d, want 405", rec.Code)
	}
}

func tokenFor(t *testing.T, user, pass string, at time.Time) string {
	t.Helper()
	rec := login(t, newMux(func() time.Time { return at }), user, pass)
	var out struct{ Token string }
	json.Unmarshal(rec.Body.Bytes(), &out)
	return out.Token
}

func bearer(tok string) map[string]string { return map[string]string{"Authorization": "Bearer " + tok} }

func TestShouldVerifyAValidTokenAndNameTheCaller(t *testing.T) {
	tok := tokenFor(t, "alice", "alice-pass", t0)
	rec := do(t, newMux(func() time.Time { return t0.Add(time.Minute) }), http.MethodGet, "/verify", "", bearer(tok))
	if rec.Code != http.StatusNoContent || rec.Header().Get("X-User-ID") != "alice" || rec.Header().Get("X-User-Role") != "user" {
		t.Errorf("/verify → %d X-User-ID %q X-User-Role %q, want 204 alice user",
			rec.Code, rec.Header().Get("X-User-ID"), rec.Header().Get("X-User-Role"))
	}
}

// forge builds a token from raw parts so the bad cases need no real signer.
func forge(header, claims string, key []byte) string {
	h := base64.RawURLEncoding.EncodeToString([]byte(header))
	c := base64.RawURLEncoding.EncodeToString([]byte(claims))
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(h + "." + c))
	return h + "." + c + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func TestShouldRejectEveryBadToken(t *testing.T) {
	now := t0.Add(time.Minute)
	mux := newMux(func() time.Time { return now })
	good := tokenFor(t, "alice", "alice-pass", t0)
	exp := t0.Add(time.Hour).Unix()
	claims := `{"sub":"alice","role":"user","iat":1,"exp":` + itoa(exp) + `}`
	none := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none","typ":"JWT"}`)) + "." +
		base64.RawURLEncoding.EncodeToString([]byte(claims)) + "."
	for name, hdr := range map[string]map[string]string{
		"no header":     nil,
		"not Bearer":    {"Authorization": "Basic " + good},
		"malformed":     bearer("not-a-jwt"),
		"bad signature": bearer(flipLast(good)),
		"other key":     bearer(forge(`{"alg":"HS256","typ":"JWT"}`, claims, []byte("another-key-another-key-another!!"))),
		"expired":       bearer(tokenFor(t, "alice", "alice-pass", t0.Add(-time.Hour))),
		"missing exp":   bearer(forge(`{"alg":"HS256","typ":"JWT"}`, `{"sub":"alice","role":"user","iat":1}`, testKey)),
		"alg none":      bearer(none),
		"alg HS512":     bearer(forge(`{"alg":"HS512","typ":"JWT"}`, claims, testKey)),
	} {
		if rec := do(t, mux, http.MethodGet, "/verify", "", hdr); rec.Code != http.StatusUnauthorized {
			t.Errorf("%s → %d, want 401", name, rec.Code)
		}
	}
}

func itoa(n int64) string { b, _ := json.Marshal(n); return string(b) }

// flipLast always changes the signature's last character (A↔B), so the
// result is never the original by chance.
func flipLast(tok string) string {
	last := byte('A')
	if tok[len(tok)-1] == 'A' {
		last = 'B'
	}
	return tok[:len(tok)-1] + string(last)
}

// Only admins may DELETE; the gateway passes the original method.
func TestShouldForbidDeleteForUserRole(t *testing.T) {
	mux := newMux(func() time.Time { return t0.Add(time.Minute) })
	user, admin := tokenFor(t, "alice", "alice-pass", t0), tokenFor(t, "admin", "admin-pass", t0)
	del := func(tok string) int {
		h := bearer(tok)
		h["X-Original-Method"] = http.MethodDelete
		return do(t, mux, http.MethodGet, "/verify", "", h).Code
	}
	if c := del(user); c != http.StatusForbidden {
		t.Errorf("DELETE with a user token → %d, want 403", c)
	}
	if c := del(admin); c != http.StatusNoContent {
		t.Errorf("DELETE with an admin token → %d, want 204", c)
	}
}

func TestShouldLoadLabUsersFromJSON(t *testing.T) {
	u, err := ParseUsers([]byte(`{"alice":{"password":"p1","role":"user"},"admin":{"password":"p2","role":"admin"}}`))
	if err != nil || u["alice"].Role != "user" || u["admin"].Password != "p2" {
		t.Errorf("ParseUsers → %v, %v", u, err)
	}
	if _, err := ParseUsers([]byte(`{"x":{"password":"","role":"user"}}`)); err == nil {
		t.Error("a user with an empty password was accepted")
	}
}
