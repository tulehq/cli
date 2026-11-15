//go:build spike

package spike

import (
	"context"
	"testing"
	"time"
)

// TestHostGatewayReachesPublishedPort asks whether a container started with
// --add-host host.docker.internal:host-gateway reaches a service whose port
// is published on 127.0.0.1 only. The 0.0.0.0 publish is the control.
func TestHostGatewayReachesPublishedPort(t *testing.T) {
	for _, bindIP := range []string{"127.0.0.1", "0.0.0.0"} {
		t.Run("published on "+bindIP, func(t *testing.T) {
			port := startObjectStore(t, bindIP)
			url := "http://host.docker.internal:" + port + "/_ready"

			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Minute)
			defer cancel()
			out, err := docker(ctx, "run", "--rm",
				"--add-host", "host.docker.internal:host-gateway",
				probeImage,
				"wget", "-q", "-O", "-", "-T", "5", url,
			)
			if err != nil {
				t.Logf(
					"FINDING published on %s: UNREACHABLE from a host-gateway container at %s: %v",
					bindIP,
					url,
					err,
				)
				return
			}
			t.Logf(
				"FINDING published on %s: REACHABLE from a host-gateway container at %s (body %q)",
				bindIP,
				url,
				out,
			)
		})
	}
}
