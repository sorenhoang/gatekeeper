//go:build smoke && lifecycle

package smoke

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"
)

// Tokens a lax verifier might accept: validly signed payloads with the wrong
// claim types (Go's json.Unmarshal into int64/string rejects them), payloads
// that aren't a JSON object, and malformed token shapes and Bearer values.
// njs must answer each exactly as auth_request (auth-service) does.
func strictMatrix(t *testing.T) map[string]string {
	t.Helper()
	h := `{"alg":"HS256","typ":"JWT"}`
	exp := time.Now().Add(10 * time.Minute).Unix()
	jti := func() string { return fmt.Sprint(time.Now().UnixNano()) }
	payloads := map[string]string{
		"exp as string":   fmt.Sprintf(`{"sub":"alice","role":"user","exp":"%d","jti":"%s"}`, exp, jti()),
		"exp as array":    fmt.Sprintf(`{"sub":"alice","role":"user","exp":[%d],"jti":"%s"}`, exp, jti()),
		"exp fractional":  fmt.Sprintf(`{"sub":"alice","role":"user","exp":%d.5,"jti":"%s"}`, exp, jti()),
		"role as array":   fmt.Sprintf(`{"sub":"alice","role":["admin"],"exp":%d,"jti":"%s"}`, exp, jti()),
		"sub as object":   fmt.Sprintf(`{"sub":{"a":1},"role":"user","exp":%d,"jti":"%s"}`, exp, jti()),
		"payload null":    `null`,
		"payload number":  `5`,
		"payload array":   `[]`,
		"payload garbage": `not json`,
	}
	out := map[string]string{}
	for name, p := range payloads {
		a := authed(t, http.MethodGet, "/api/users/", "Bearer "+signed(labKey, h, p), nil)
		var e struct{ Headers map[string][]string }
		json.Unmarshal([]byte(a.body), &e)
		out[name] = fmt.Sprintf("%d %s %s", a.status, strings.Join(e.Headers["X-User-Id"], ","), strings.Join(e.Headers["X-User-Role"], ","))
	}
	good := freshToken("user")
	parts := strings.Split(good, ".")
	for name, authz := range map[string]string{
		"empty Bearer":     "Bearer ",
		"lowercase bearer": "bearer " + good,
		"two parts":        "Bearer " + parts[0] + "." + parts[1],
		"four parts":       "Bearer " + good + ".x",
		"padded payload":   "Bearer " + parts[0] + "." + base64.URLEncoding.EncodeToString([]byte(mustDecode(t, parts[1]))) + "." + parts[2],
		"double space":     "Bearer  " + good,
		"trailing garbage": "Bearer " + good + " x",
	} {
		out[name] = fmt.Sprint(authed(t, http.MethodGet, "/api/users/", authz, nil).status)
	}
	// The realtime route takes ?access_token=; its events name the caller.
	resp, lines := sseOpen(t, "/api/events/?access_token="+freshToken("admin"), "")
	caller := "no event"
	for deadline := time.After(5 * time.Second); caller == "no event"; {
		select {
		case l, ok := <-lines:
			if !ok {
				caller = "closed"
			} else if f := strings.Fields(strings.TrimPrefix(l, "data: ")); strings.HasPrefix(l, "data: ") && len(f) > 0 {
				caller = f[0]
			}
		case <-deadline:
			caller = "timeout"
		}
	}
	out["events identity"] = fmt.Sprintf("%d %s", resp.StatusCode, caller)
	return out
}

func mustDecode(t *testing.T, s string) string {
	t.Helper()
	b, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestShouldRejectMistypedAndMalformedTokensLikeAuthRequestWithNjs(t *testing.T) {
	afterDrain(t)
	base := strictMatrix(t)
	if base["exp as string"] != "401  " || base["events identity"] != "200 admin" {
		t.Fatalf("baseline strict matrix looks wrong, the comparison would mean nothing: %v", base)
	}
	withNjsAuth(t)
	njs := strictMatrix(t)
	for name, want := range base {
		if got := njs[name]; got != want {
			t.Errorf("%s: njs auth → %q, auth_request → %q", name, got, want)
		}
	}
}
