//go:build smoke

package smoke

import "testing"

// `location = /api/users/login` matches only the exact path; the trailing
// slash variant must not be a way around the login limit.
func TestShouldLimitTheTrailingSlashLoginLikeTheLogin(t *testing.T) {
	afterDrain(t)
	hits := spike(t, 50, "", loginPath+"/", nil)
	if c := countStatus(hits); c[200] != 11 || c[429] != 39 {
		t.Fatalf("50 parallel %s/ → %v, want 11 × 200 and 39 × 429 like %s", loginPath, c, loginPath)
	}
	for _, h := range hits {
		if h.status == 429 {
			assertTooMany(t, h, "login")
			break
		}
	}
}

// Same client, same bucket: draining one spelling drains the other.
func TestShouldShareOneLoginBucketAcrossBothSpellings(t *testing.T) {
	afterDrain(t)
	if c := countStatus(spike(t, 50, "", loginPath, nil)); c[429] == 0 {
		t.Fatalf("setup: the login spike was not limited (%v)", c)
	}
	if c := countStatus(spike(t, 10, "", loginPath+"/", nil)); c[429] < 8 {
		t.Errorf("10 parallel %s/ right after draining %s → %v, want at least 8 × 429 (one bucket)", loginPath, loginPath, c)
	}
}
