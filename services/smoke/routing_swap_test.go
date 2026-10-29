//go:build smoke && lifecycle

package smoke

import (
	"net/http"
	"net/netip"
	"testing"
	"time"
)

// resolverValid mirrors `resolver ... valid=` in nginx/nginx.conf: the longest
// time a swapped address may still be routed to the old service.
const resolverValid = 2 * time.Second

// Unlike TestShouldFollowServicesWhoseIpsSwappedWithoutReload, this one fails
// when Docker did not actually swap the addresses (it would prove nothing),
// and it bounds how long wrong-service answers may last.
func TestShouldStopRoutingToTheWrongServiceWithinResolverValidAfterARealSwap(t *testing.T) {
	restoreStack(t)
	ip := func(svc string) netip.Addr { return netip.MustParseAddr(containerIP(t, svc)) }
	userBefore, orderBefore := ip("user-service"), ip("order-service")

	mustCompose(t, "rm", "-sf", "user-service", "order-service")
	first, second := "user-service", "order-service"
	if userBefore.Less(orderBefore) {
		first, second = second, first
	}
	mustCompose(t, "up", "-d", "--no-deps", first)
	mustCompose(t, "up", "-d", "--no-deps", second)
	if ip("user-service") != orderBefore || ip("order-service") != userBefore {
		t.Fatalf("IPs did not swap (user %s→%s, order %s→%s); the test needs a real swap",
			userBefore, ip("user-service"), orderBefore, ip("order-service"))
	}
	swapped := time.Now()

	var lastWrong time.Time
	deadline := swapped.Add(reresolveWithin)
	for time.Now().Before(deadline) {
		for path, want := range map[string]string{"/api/users/": "user-service", "/api/orders/": "order-service"} {
			r, err := route(t, "", path)
			if err == nil && r.status == http.StatusOK && r.echo(t).Service != want {
				lastWrong = time.Now()
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	if lastWrong.IsZero() {
		t.Logf("no wrong-service answer seen after the swap")
		return
	}
	// One lookup's worth of slack on top of the cache lifetime.
	if took := lastWrong.Sub(swapped); took > resolverValid+time.Second {
		t.Errorf("a 200 from the wrong service was still served %v after the swap, want <= %v", took, resolverValid+time.Second)
	} else {
		t.Logf("wrong-service answers stopped %v after the swap", took)
	}
}
