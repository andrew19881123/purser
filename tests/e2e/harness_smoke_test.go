package e2e

import (
	"net/http"
	"testing"
)

// TestHarnessBoots is the TDD smoke test: it fails until the harness can
// actually bring up the full native stack. When it passes, both the gateway
// and the control plane are serving on ephemeral ports.
func TestHarnessBoots(t *testing.T) {
	s := Start(t)
	defer s.Stop()

	if s.CPBase == "" || s.GatewayBase == "" {
		t.Fatal("stack did not report base URLs")
	}

	// Confirm the health probes answer with a non-error status code.
	for _, url := range []string{
		s.GatewayBase + "/healthz",
		s.CPBase + "/api/v1/cluster/health",
	} {
		resp, err := http.Get(url) //nolint:noctx
		if err != nil {
			t.Fatalf("GET %s: %v", url, err)
		}
		resp.Body.Close()
		if resp.StatusCode >= 500 {
			t.Errorf("GET %s: got %d, want <500", url, resp.StatusCode)
		} else {
			t.Logf("GET %s → %d OK", url, resp.StatusCode)
		}
	}
}
