//go:build smoke

package smoke

import (
	"encoding/json"
	"path/filepath"
	"testing"
)

func TestShouldBuildEveryServiceImageFromTheSingleDockerfile(t *testing.T) {
	var cfg struct {
		Services map[string]struct {
			Build *struct {
				Context    string            `json:"context"`
				Dockerfile string            `json:"dockerfile"`
				Args       map[string]string `json:"args"`
			} `json:"build"`
		} `json:"services"`
	}
	if err := json.Unmarshal([]byte(mustCompose(t, "config", "--format", "json")), &cfg); err != nil {
		t.Fatalf("compose config is not JSON: %v", err)
	}

	for _, svc := range services {
		b := cfg.Services[svc].Build
		if b == nil {
			t.Errorf("%s has no build section", svc)
			continue
		}
		if filepath.Base(b.Context) != "services" || b.Dockerfile != "Dockerfile" {
			t.Errorf("%s builds from %s/%s, want services/Dockerfile", svc, b.Context, b.Dockerfile)
		}
		if b.Args["SERVICE"] != svc {
			t.Errorf("%s build arg SERVICE = %q, want %q", svc, b.Args["SERVICE"], svc)
		}
	}

	// Rebuild from current source so the other smoke tests never pass on a stale image.
	mustCompose(t, append([]string{"build"}, services...)...)
}
