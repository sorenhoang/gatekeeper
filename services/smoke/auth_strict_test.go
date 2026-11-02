//go:build smoke

package smoke

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"net/http"
	"testing"
	"time"
)

// Well-formed tokens that only a real signature check catches.
func TestShouldRefuseWellFormedTokensWithABadSignature(t *testing.T) {
	h := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"HS256","typ":"JWT"}`))
	c := base64.RawURLEncoding.EncodeToString([]byte(fmt.Sprintf(
		`{"sub":"alice","role":"admin","iat":%d,"exp":%d}`, time.Now().Unix(), time.Now().Add(time.Hour).Unix())))
	m := hmac.New(sha256.New, []byte("not-the-lab-key-not-the-lab-key!"))
	m.Write([]byte(h + "." + c))
	otherKey := h + "." + c + "." + base64.RawURLEncoding.EncodeToString(m.Sum(nil))
	none := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none","typ":"JWT"}`)) + "." + c + "."
	for name, tok := range map[string]string{"signed with another key": otherKey, "alg none": none} {
		for _, route := range []string{"users", "orders"} {
			if a := authed(t, http.MethodGet, "/api/"+route+"/", "Bearer "+tok, nil); a.status != 401 {
				t.Errorf("%s on /api/%s/ → %d, want 401", name, route, a.status)
			}
		}
	}
}

// The verdict cache is keyed by token and method: an allowed GET must not
// make the same token's DELETE allowed within the 10s window.
func TestShouldNotLetACachedGetVerdictAllowADelete(t *testing.T) {
	tok := freshToken("user")
	if a := authed(t, http.MethodGet, "/api/orders/x", "Bearer "+tok, nil); a.status != 200 {
		t.Fatalf("setup: GET with a user token → %d, want 200", a.status)
	}
	if a := authed(t, http.MethodDelete, "/api/orders/x", "Bearer "+tok, nil); a.status != 403 {
		t.Errorf("DELETE with the same user token right after its GET → %d, want 403", a.status)
	}
}
