// Package auth is the lab's identity provider: auth-service issues HS256
// tokens on /login and the gateway's auth_request subrequest checks them on
// /verify (README "Auth & CORS").
package auth

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Claims is the token payload. Exp is required: a token without it would
// never expire.
type Claims struct {
	Sub  string `json:"sub"`
	Role string `json:"role"`
	Iat  int64  `json:"iat"`
	Exp  int64  `json:"exp"`
}

var errInvalid = errors.New("invalid token")

// minKeyLen matches HS256's output size; a shorter key is guessable.
const minKeyLen = 32

// CheckKey refuses a signing key that would make tokens forgeable.
func CheckKey(key []byte) error {
	if len(key) < minKeyLen {
		return fmt.Errorf("signing key is %d bytes, want at least %d", len(key), minKeyLen)
	}
	return nil
}

// The header is fixed, so verification can compare it byte for byte and
// never trusts a token's own choice of algorithm ("alg": "none" and friends).
var hs256Header = base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"HS256","typ":"JWT"}`))

func sign(key []byte, c Claims) (string, error) {
	payload, err := json.Marshal(c)
	if err != nil {
		return "", err
	}
	unsigned := hs256Header + "." + base64.RawURLEncoding.EncodeToString(payload)
	return unsigned + "." + mac(key, unsigned), nil
}

func mac(key []byte, s string) string {
	m := hmac.New(sha256.New, key)
	m.Write([]byte(s))
	return base64.RawURLEncoding.EncodeToString(m.Sum(nil))
}

func verify(key []byte, token string, now time.Time) (Claims, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 || parts[0] != hs256Header {
		return Claims{}, errInvalid
	}
	// hmac.Equal is constant-time, so the comparison leaks nothing about how
	// much of a forged signature was right.
	if !hmac.Equal([]byte(parts[2]), []byte(mac(key, parts[0]+"."+parts[1]))) {
		return Claims{}, errInvalid
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return Claims{}, errInvalid
	}
	var c Claims
	if err := json.Unmarshal(raw, &c); err != nil || c.Exp == 0 || c.Sub == "" || c.Role == "" || now.Unix() >= c.Exp {
		return Claims{}, errInvalid
	}
	return c, nil
}
