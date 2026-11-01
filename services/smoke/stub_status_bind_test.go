//go:build smoke

package smoke

import (
	"net"
	"strings"
	"testing"
)

// A positive control and a real address, so "not reachable on the compose
// network" can only pass because the listener is bound to loopback.
func TestShouldAnswerStubStatusOnLoopbackButRefuseItOnTheComposeAddress(t *testing.T) {
	ip := strings.TrimSpace(mustCompose(t, "exec", "-T", "gateway", "hostname", "-i"))
	if net.ParseIP(ip) == nil || ip == "127.0.0.1" {
		t.Fatalf("gateway compose address = %q, want one non-loopback IP", ip)
	}

	// Control: the same tool, same container, port 80 on that address works.
	if out, err := compose(t, "exec", "-T", "gateway", "wget", "-qO-", "-T", "2", "http://"+ip+":80/health"); err != nil {
		t.Fatalf("control: gateway :80 on %s failed (%v), so a refusal on :8082 would prove nothing\n%s", ip, err, out)
	}
	// Control: the status page itself works on loopback.
	if _, err := compose(t, "exec", "-T", "gateway", "wget", "-qO-", "-T", "2", statusURL); err != nil {
		t.Fatalf("control: stub_status on 127.0.0.1:8082 failed: %v", err)
	}

	out, err := compose(t, "exec", "-T", "gateway", "wget", "-qO-", "-T", "2", "http://"+ip+":8082/nginx_status")
	if err == nil {
		t.Fatalf("stub_status answered on the compose address %s:8082:\n%s", ip, out)
	}
	if !strings.Contains(strings.ToLower(out), "refused") {
		t.Errorf("request to %s:8082 failed, but not with a refused connection: %s", ip, out)
	}
}
