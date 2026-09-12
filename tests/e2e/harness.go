// Package e2e provides a native-process test harness for Purser integration
// tests. It starts a real control-plane and gateway on ephemeral ports (no
// hardcoded 8080/9443 — see docs/postmortems/demo_stack_fragility.md),
// waits for readiness by polling health endpoints (never fixed sleeps), and
// tears down cleanly on Stop.
//
// Typical usage:
//
//	func TestFoo(t *testing.T) {
//	    s := e2e.Start(t)
//	    defer s.Stop()
//	    // ... use s.CPBase, s.GatewayBase
//	}
//
// Prerequisites: bin/control-plane and rust/target/debug/purser-gateway must
// exist (built by the CI / verification step before running the suite).
package e2e

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// freePort asks the OS for an unused TCP port on loopback, then releases it.
// Ephemeral ports eliminate the fixed-port collisions (9443, 50151) seen in
// manual runs and demo-stack leftovers.
func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("freePort: %v", err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

// Stack holds all the process handles and base URLs for a running test stack.
//
// Fields intended for Task-5 tests (in the same package):
//   - CPBase      — HTTP management API base URL
//   - GatewayBase — HTTP gateway base URL
//   - grpcBase    — gRPC registration endpoint (for agent enrollment)
//   - tmp         — per-test temp dir (DB, PKI, log files live here)
//   - procs       — ordered process slice (gateway first, CP second)
//   - gwPort      — bound gateway port (RestartGateway must reuse this)
//   - gwEnv       — gateway process env (RestartGateway passes the same env)
type Stack struct {
	CPBase      string // e.g. "http://127.0.0.1:54321"
	GatewayBase string // e.g. "http://127.0.0.1:54322"

	grpcBase string // CP gRPC base for agent enrollment, e.g. "http://127.0.0.1:54323"
	token    string // cached join token (re-mint by zeroing before calling JoinToken)
	procs    []*exec.Cmd
	tmp      string // t.TempDir() — DB, PKI, and process log files

	// Gateway-restart fields (Task 5: RestartGateway must bind the same port
	// so the control plane's already-configured PURSER_GATEWAY_ADDR still works)
	gwPort int
	gwEnv  []string

	t *testing.T // registered for fatal-error reporting from Stop/JoinToken
}

// waitReady polls url until it returns HTTP < 500 or the deadline passes.
//
// This is condition-based waiting: the 100 ms between attempts is a poll
// interval, not a readiness estimate. A fixed time.Sleep in place of this
// loop would be a reliability bug (fast on fast machines, racy on slow ones).
func waitReady(t *testing.T, url string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		resp, err := http.Get(url) //nolint:noctx // test helper, context not needed
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode < 500 {
				return
			}
		}
		time.Sleep(100 * time.Millisecond) // poll interval, not a readiness guess
	}
	t.Fatalf("waitReady: %s not ready within %s", url, timeout)
}

// repoRoot returns the repository root from the cwd of tests/e2e.
func repoRoot(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("repoRoot: %v", err)
	}
	return filepath.Join(wd, "..", "..")
}

// Start launches a control-plane and gateway on ephemeral ports, waits for
// both health probes to answer, and registers Stop via t.Cleanup so the
// processes are killed even if the test panics.
//
// Prerequisites: bin/control-plane and rust/target/debug/purser-gateway must
// be built (the verification step in task-4-brief.md does this).
func Start(t *testing.T) *Stack {
	t.Helper()
	root := repoRoot(t)
	cpPort := freePort(t)
	gwPort := freePort(t)
	grpcPort := freePort(t)
	tmp := t.TempDir()

	s := &Stack{
		CPBase:      fmt.Sprintf("http://127.0.0.1:%d", cpPort),
		GatewayBase: fmt.Sprintf("http://127.0.0.1:%d", gwPort),
		grpcBase:    fmt.Sprintf("http://127.0.0.1:%d", grpcPort),
		tmp:         tmp,
		gwPort:      gwPort,
		t:           t,
	}

	// Gateway — fail-closed: PURSER_GATEWAY_API_KEYS must be set or the
	// gateway exits with code 2 ("fail-closed" rule in auth.rs).
	gwEnv := append(os.Environ(),
		"PURSER_GATEWAY_HOST=127.0.0.1",
		fmt.Sprintf("PURSER_GATEWAY_PORT=%d", gwPort),
		"PURSER_GATEWAY_INTERNAL_TOKEN=e2e",
		"PURSER_GATEWAY_API_KEYS=testkey",
	)
	s.gwEnv = gwEnv

	gw := exec.Command(filepath.Join(root, "rust", "target", "debug", "purser-gateway"))
	gw.Env = gwEnv
	startProc(t, s, gw, filepath.Join(tmp, "gw.log"))

	// Control-plane — mock engine (no GPU required), insecure gRPC for agent
	// enrollment in dev/CI, pointing at the gateway we just started.
	cp := exec.Command(filepath.Join(root, "bin", "control-plane"))
	cp.Env = append(os.Environ(),
		fmt.Sprintf("PURSER_ADDR=:%d", cpPort),
		fmt.Sprintf("PURSER_GRPC_ADDR=:%d", grpcPort),
		"PURSER_DB="+filepath.Join(tmp, "reg.db"),
		"PURSER_PKI_DIR="+filepath.Join(tmp, "pki"),
		"PURSER_ENGINE_BACKEND=mock",
		"PURSER_AGENT_GRPC_INSECURE=true",
		"PURSER_GATEWAY_ADDR="+s.GatewayBase,
		"PURSER_GATEWAY_TOKEN=e2e",
	)
	startProc(t, s, cp, filepath.Join(tmp, "cp.log"))

	// Condition-based readiness: poll until both services answer or 30 s pass.
	waitReady(t, s.GatewayBase+"/healthz", 30*time.Second)
	waitReady(t, s.CPBase+"/api/v1/cluster/health", 30*time.Second)

	// Register cleanup so tests that forget defer s.Stop() still get cleanup.
	t.Cleanup(s.Stop)

	return s
}

// startProc starts cmd with combined stdout+stderr redirected to logPath,
// and appends it to s.procs for ordered teardown.
func startProc(t *testing.T, s *Stack, cmd *exec.Cmd, logPath string) {
	t.Helper()
	f, err := os.Create(logPath)
	if err != nil {
		t.Fatalf("startProc: create log %s: %v", logPath, err)
	}
	cmd.Stdout, cmd.Stderr = f, f
	if err := cmd.Start(); err != nil {
		t.Fatalf("startProc: start %s: %v", cmd.Path, err)
	}
	s.procs = append(s.procs, cmd)
}

// Stop kills all processes in reverse start order (CP first, then gateway).
// Safe to call multiple times (idempotent via Process.Kill ignoring errors).
func (s *Stack) Stop() {
	for i := len(s.procs) - 1; i >= 0; i-- {
		if s.procs[i].Process != nil {
			_ = s.procs[i].Process.Kill()
			_ = s.procs[i].Wait() // reap so the OS releases the port promptly
		}
	}
}

// JoinToken mints a single-use cluster join token via POST /api/v1/join-token
// and caches it. The CP runs in no-auth dev mode (no API keys seeded), so the
// request needs no Bearer header.
//
// To mint a fresh token after the previous one has been consumed by an agent
// enrollment, zero s.token before calling again.
func (s *Stack) JoinToken() string {
	if s.token != "" {
		return s.token
	}
	resp, err := http.Post(s.CPBase+"/api/v1/join-token", "application/json", nil) //nolint:noctx
	if err != nil {
		s.t.Fatalf("JoinToken: POST failed: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusCreated {
		s.t.Fatalf("JoinToken: unexpected status %d body=%s", resp.StatusCode, body)
	}
	var out struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		s.t.Fatalf("JoinToken: parse response: %v body=%s", err, body)
	}
	if out.Token == "" {
		s.t.Fatalf("JoinToken: empty token in response body=%s", body)
	}
	s.token = out.Token
	return s.token
}
