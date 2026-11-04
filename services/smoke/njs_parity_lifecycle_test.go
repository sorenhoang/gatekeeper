//go:build smoke && lifecycle

package smoke

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// signed builds a token from raw header and claims JSON, signed with key.
func signed(key []byte, header, claims string) string {
	u := base64.RawURLEncoding.EncodeToString([]byte(header)) + "." + base64.RawURLEncoding.EncodeToString([]byte(claims))
	m := hmac.New(sha256.New, key)
	m.Write([]byte(u))
	return u + "." + base64.RawURLEncoding.EncodeToString(m.Sum(nil))
}

// statusOf sends one request and reads only the status: the realtime routes
// stream, so their body never ends.
func statusOf(t *testing.T, method, path, authz string) int {
	t.Helper()
	req, _ := http.NewRequest(method, gatewayURL+path, nil)
	req.Header.Set(anonymousHeader, "1")
	if authz != "" {
		req.Header.Set("Authorization", authz)
	}
	resp, err := noFollow.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	resp.Body.Close()
	return resp.StatusCode
}

// authMatrix answers every Phase 8 auth case with "<status> <user> <role>",
// the identity being what user-service saw. Tokens are made fresh each call,
// so no cached verdict decides the answer.
func authMatrix(t *testing.T) map[string]string {
	t.Helper()
	h := `{"alg":"HS256","typ":"JWT"}`
	now := time.Now().Unix()
	claims := func(extra string) string {
		return fmt.Sprintf(`{%s"iat":%d,"jti":"%d"}`, extra, now, time.Now().UnixNano())
	}
	tokens := map[string]string{
		"valid user":      freshToken("user"),
		"valid admin":     freshToken("admin"),
		"expired":         labToken("alice", "user", time.Now().Add(-time.Hour), 30*time.Minute),
		"wrong signature": signed(bytes.Repeat([]byte("k"), 32), h, claims(fmt.Sprintf(`"sub":"alice","role":"user","exp":%d,`, now+600))),
		"alg none": base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none","typ":"JWT"}`)) + "." +
			base64.RawURLEncoding.EncodeToString([]byte(claims(fmt.Sprintf(`"sub":"alice","role":"admin","exp":%d,`, now+600)))) + ".",
		"missing sub":       signed(labKey, h, claims(fmt.Sprintf(`"role":"user","exp":%d,`, now+600))),
		"missing role":      signed(labKey, h, claims(fmt.Sprintf(`"sub":"alice","exp":%d,`, now+600))),
		"missing exp":       signed(labKey, h, claims(`"sub":"alice","role":"user",`)),
		"spaced header alg": signed(labKey, `{"alg": "HS256","typ":"JWT"}`, claims(fmt.Sprintf(`"sub":"alice","role":"user","exp":%d,`, now+600))),
	}
	out := map[string]string{}
	for name, tok := range tokens {
		a := authed(t, http.MethodGet, "/api/users/", "Bearer "+tok, nil)
		var e struct{ Headers map[string][]string }
		json.Unmarshal([]byte(a.body), &e)
		out[name] = fmt.Sprintf("%d %s %s", a.status, strings.Join(e.Headers["X-User-Id"], ","), strings.Join(e.Headers["X-User-Role"], ","))
	}
	out["non-Bearer"] = fmt.Sprint(authed(t, http.MethodGet, "/api/users/", "Basic YWxpY2U6eA==", nil).status)
	out["no token"] = fmt.Sprint(authed(t, http.MethodGet, "/api/users/", "", nil).status)
	out["DELETE as user"] = fmt.Sprint(authed(t, http.MethodDelete, "/api/orders/x", "Bearer "+freshToken("user"), nil).status)
	out["DELETE as admin"] = fmt.Sprint(authed(t, http.MethodDelete, "/api/orders/x", "Bearer "+freshToken("admin"), nil).status)
	out["events ?access_token="] = fmt.Sprint(statusOf(t, http.MethodGet, "/api/events/?access_token="+freshToken("user"), ""))
	out["events bad ?access_token="] = fmt.Sprint(statusOf(t, http.MethodGet, "/api/events/?access_token=nope", ""))
	out["events ambiguous token"] = fmt.Sprint(statusOf(t, http.MethodGet, "/api/events/?access_token=a&access_token=b", ""))
	return out
}

// recreateGateway recreates the gateway from docker-compose.yml plus the
// given override files, and waits until it serves on both address families.
func recreateGateway(t *testing.T, overrides ...string) {
	t.Helper()
	args := []string{"-f", filepath.Join(repoRoot, "docker-compose.yml")}
	for _, o := range overrides {
		args = append(args, "-f", filepath.Join(repoRoot, o))
	}
	mustCompose(t, append(args, "up", "-d", "--no-deps", "--force-recreate", "--wait", "gateway")...)
	waitGatewayServing(t)
	for _, addr := range []string{"[::1]:8443", "127.0.0.1:8443"} {
		waitAnswersOn(t, addr)
	}
}

// The candidate in loadtest/candidates/njs-auth.env, applied in place (same
// inode, so the gateway's bind mount sees it) and always reverted.
func withNjsAuth(t *testing.T) {
	t.Helper()
	// Setup: the signing key is mounted only with docker-compose.njs.yml, so
	// the default gateway never holds it (Phase 8). Registered first, so the
	// plain gateway comes back after the config is reverted.
	recreateGateway(t, "docker-compose.njs.yml")
	t.Cleanup(func() { recreateGateway(t) })
	p := filepath.Join(repoRoot, "nginx", "snippets", "auth-check.conf")
	before, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	write := func(b []byte) {
		f, err := os.OpenFile(p, os.O_WRONLY|os.O_TRUNC, 0)
		if err != nil {
			t.Fatal(err)
		}
		f.Write(b)
		f.Close()
		if out, err := runMake(t, "reload"); err != nil {
			t.Fatalf("make reload: %v\n%s", err, out)
		}
		time.Sleep(time.Second)
	}
	if !bytes.Contains(before, []byte("auth_request /_auth;")) {
		t.Fatal("auth-check.conf has no `auth_request /_auth;` to switch")
	}
	t.Cleanup(func() { write(before) })
	write(bytes.Replace(before, []byte("auth_request /_auth;"), []byte("auth_request /_auth_njs;"), 1))
}

func TestShouldAnswerEveryAuthCaseLikeAuthRequestWithNjs(t *testing.T) {
	afterDrain(t)
	base := authMatrix(t)
	if base["valid user"] != "200 alice user" || base["no token"] != "401" || base["DELETE as user"] != "403" {
		t.Fatalf("baseline auth matrix looks wrong, the comparison would mean nothing: %v", base)
	}
	withNjsAuth(t)
	njs := authMatrix(t)
	for name, want := range base {
		if got := njs[name]; got != want {
			t.Errorf("%s: njs auth → %q, auth_request → %q", name, got, want)
		}
	}
}
