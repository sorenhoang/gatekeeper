package auth

import (
	"net/http"
	"testing"
	"time"
)

// A token the issuer signed but without a role must not pass as "no role
// restrictions": fail closed.
func TestShouldRejectATokenWithoutARole(t *testing.T) {
	now := t0.Add(time.Minute)
	tok := forge(`{"alg":"HS256","typ":"JWT"}`, `{"sub":"alice","iat":1,"exp":`+itoa(t0.Add(time.Hour).Unix())+`}`, testKey)
	if rec := do(t, newMux(func() time.Time { return now }), http.MethodGet, "/verify", "", bearer(tok)); rec.Code != http.StatusUnauthorized {
		t.Errorf("token without role → %d, want 401", rec.Code)
	}
}

func TestShouldRejectTokensWithExtraSegmentsOrSpaces(t *testing.T) {
	now := t0.Add(time.Minute)
	good := tokenFor(t, "alice", "alice-pass", t0)
	mux := newMux(func() time.Time { return now })
	for name, tok := range map[string]string{"extra segment": good + ".x", "leading space": " " + good, "trailing space": good + " "} {
		if rec := do(t, mux, http.MethodGet, "/verify", "", bearer(tok)); rec.Code != http.StatusUnauthorized {
			t.Errorf("%s → %d, want 401", name, rec.Code)
		}
	}
}

// Only GET /verify answers; anything else must not fall through to the echo
// handler, whose 200 an auth_request would read as "allowed".
func TestShouldAnswer405ToOtherMethodsOnVerify(t *testing.T) {
	mux := newMux(time.Now)
	for _, m := range []string{http.MethodPost, http.MethodPut, http.MethodDelete} {
		if rec := do(t, mux, m, "/verify", "", nil); rec.Code != http.StatusMethodNotAllowed {
			t.Errorf("%s /verify → %d, want 405", m, rec.Code)
		}
	}
}

// A short or empty key would make every token forgeable.
func TestShouldRefuseASigningKeyShorterThan32Bytes(t *testing.T) {
	for _, n := range []int{0, 16, 31} {
		if err := CheckKey(make([]byte, n)); err == nil {
			t.Errorf("a %d-byte key was accepted", n)
		}
	}
	if err := CheckKey(testKey); err != nil {
		t.Errorf("a 32-byte key was refused: %v", err)
	}
}
