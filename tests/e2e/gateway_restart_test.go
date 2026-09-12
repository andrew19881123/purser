package e2e

import (
	"testing"
	"time"
)

// TestGatewayRestartSelfHeals is the regression test for the in-memory route
// table bug fixed in release/v0.6 (see docs/postmortems/demo_stack_fragility.md).
//
// Before the fix, a gateway restart silently emptied the route table and every
// subsequent inference request failed with 503 until an operator re-deployed a
// model. The reconcile loop in the control plane now re-pushes ACTIVE routes
// periodically (every PURSER_ROUTE_RECONCILE_INTERVAL seconds, defaulting to 30s;
// the harness sets it to 5s for test speed), so a restarted gateway self-heals.
//
// If this test fails, the reconcile loop has regressed. Do not disable it —
// diagnose via cp.log in the harness tmp dir.
func TestGatewayRestartSelfHeals(t *testing.T) {
	s := Start(t)
	defer s.Stop()

	s.EnrollAgent(t)
	s.DeployModel(t, "tinyllama-1b")

	// Precondition: the model must be routable before the restart.
	assertModelListed(t, s, "tinyllama-1b", true)

	// Kill and restart the gateway — its route table is in-memory only and is
	// now empty. The CP reconcile loop must push the ACTIVE route back.
	s.RestartGateway(t)

	// Poll until the model reappears or the deadline expires.
	// The reconciler interval is 5s (harness override); 60s gives ample margin
	// for slow CI runners and any CP→gateway retry after the gateway came up.
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		if modelListed(s, "tinyllama-1b") {
			t.Logf("gateway self-healed in %s", time.Until(deadline).Round(time.Second))
			return
		}
		time.Sleep(500 * time.Millisecond)
	}
	t.Fatal("TestGatewayRestartSelfHeals: route table did not self-heal — reconcile loop regression. Check cp.log in " + s.tmp)
}
