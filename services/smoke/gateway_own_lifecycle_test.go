//go:build smoke && lifecycle

package smoke

import "testing"

func TestShouldAnswerGatewayHealthWhileEveryServiceIsStopped(t *testing.T) {
	restoreStack(t)
	mustCompose(t, append([]string{"stop"}, services...)...)
	assertGatewayHealth(t)
}
