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
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
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
	logFiles []*os.File // one per process; closed in Stop after Wait reap
	tmp      string     // t.TempDir() — DB, PKI, and process log files

	// Gateway-restart fields (Task 5: RestartGateway must bind the same port
	// so the control plane's already-configured PURSER_GATEWAY_ADDR still works)
	gwPort int
	gwEnv  []string

	stopOnce sync.Once  // makes Stop idempotent (t.Cleanup + defer s.Stop both call it)
	t        *testing.T // registered for fatal-error reporting from Stop/JoinToken
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
		// Speed up the route-reconcile loop so TestGatewayRestartSelfHeals
		// completes in seconds rather than up to 30 s (the production default).
		"PURSER_ROUTE_RECONCILE_INTERVAL=5",
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
	s.logFiles = append(s.logFiles, f)
}

// Stop kills all processes in reverse start order (CP first, then gateway),
// reaps them, then closes the log file descriptors. Idempotent: safe to call
// from both t.Cleanup and an explicit defer in the same test.
func (s *Stack) Stop() {
	s.stopOnce.Do(func() {
		for i := len(s.procs) - 1; i >= 0; i-- {
			if s.procs[i].Process != nil {
				_ = s.procs[i].Process.Kill()
				_ = s.procs[i].Wait() // reap so the OS releases the port promptly
			}
		}
		// Close log fds only after all processes have been reaped. The OS
		// keeps the underlying file open as long as the child holds it, so
		// closing here (after Wait) is safe and prevents fd leaks across tests.
		for _, f := range s.logFiles {
			_ = f.Close()
		}
	})
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

// EnrollAgent spawns purser-agent (mock engine) with a fresh join token and
// waits for it to appear in GET /api/v1/nodes (polling every 200 ms, 30 s deadline).
func (s *Stack) EnrollAgent(t *testing.T) {
	t.Helper()
	root := repoRoot(t)

	// Zero the cached token so JoinToken mints a fresh one for this agent.
	s.token = ""
	token := s.JoinToken()

	agentPort := freePort(t)
	inferPort := freePort(t)

	ag := exec.Command(filepath.Join(root, "rust", "target", "debug", "purser-agent"))
	ag.Env = append(os.Environ(),
		fmt.Sprintf("PURSER_AGENT_BIND=127.0.0.1:%d", agentPort),
		fmt.Sprintf("PURSER_INFERENCE_PORT=%d", inferPort),
		"PURSER_CONTROL_PLANE_ADDR="+s.grpcBase, // http://127.0.0.1:<grpcPort>
		"PURSER_CLUSTER_ID=default",
		"PURSER_JOIN_TOKEN="+token,
		"PURSER_ENGINE_BACKEND=mock",
	)
	startProc(t, s, ag, filepath.Join(s.tmp, "agent.log"))

	// Condition-based wait: poll /api/v1/nodes until a node record appears.
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := http.Get(s.CPBase + "/api/v1/nodes") //nolint:noctx
		if err == nil {
			b, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			// The node object contains either "id" or "node_id" depending on
			// serialisation; both indicate at least one node is enrolled.
			if bytes.Contains(b, []byte(`"id"`)) || bytes.Contains(b, []byte(`"node_id"`)) {
				return
			}
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatal("EnrollAgent: agent did not enroll within 30s — check agent.log in " + s.tmp)
}

// DeployModel registers a model spec in the catalog and triggers deployment,
// then polls GET /api/v1/deployments until the deployment state is ACTIVE
// (polling every 200 ms, 30 s deadline).
//
// The model spec mirrors the one used by tools/e2e_full.sh, scaled down to 1 B
// parameters so it fits on a single mock node without any GPU.
func (s *Stack) DeployModel(t *testing.T, modelID string) {
	t.Helper()

	// Register model in the catalog.
	spec := fmt.Sprintf(
		`{"modelId":%q,"family":"llama","architecture":"llama",`+
			`"paramsTotalB":1,"paramsActiveB":1,`+
			`"layers":22,"hiddenSize":2048,"nKvHeads":4,"headDim":64,`+
			`"attentionType":"ATTENTION_TYPE_GQA","contextMax":2048,"isMoe":false,`+
			`"quantizations":[{"name":"q4_k_m","sizeGb":0.6,"requiresFp4":false,"quality":0.9}],`+
			`"engine":"llamacpp"}`,
		modelID,
	)
	resp, err := http.Post(s.CPBase+"/api/v1/models", "application/json", bytes.NewReader([]byte(spec))) //nolint:noctx
	if err != nil {
		t.Fatalf("DeployModel: register %s: %v", modelID, err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode >= 400 {
		t.Fatalf("DeployModel: register %s → %d: %s", modelID, resp.StatusCode, b)
	}

	// Trigger deployment — no plan body, letting the planner build one from fleet.
	resp, err = http.Post(s.CPBase+"/api/v1/models/"+modelID+"/deploy", "application/json", bytes.NewReader([]byte("{}"))) //nolint:noctx
	if err != nil {
		t.Fatalf("DeployModel: deploy %s: %v", modelID, err)
	}
	b, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode >= 400 {
		t.Fatalf("DeployModel: deploy %s → %d: %s", modelID, resp.StatusCode, b)
	}

	// Poll until ACTIVE.
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := http.Get(s.CPBase + "/api/v1/deployments") //nolint:noctx
		if err == nil {
			b, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			if bytes.Contains(b, []byte("ACTIVE")) {
				return
			}
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatalf("DeployModel: %s did not become ACTIVE within 30s — check cp.log in %s", modelID, s.tmp)
}

// RestartGateway kills the running gateway process, starts a fresh one on the
// same port (gwPort / gwEnv), and waits for its /healthz probe to answer.
//
// procs[0] is always the gateway (Start appends gateway before CP). After restart
// the slice entry is replaced with the new process so Stop still tears it down.
func (s *Stack) RestartGateway(t *testing.T) {
	t.Helper()
	root := repoRoot(t)

	// Kill the current gateway (always procs[0]) and reap it so the port is
	// released before we try to bind again.
	if s.procs[0].Process != nil {
		_ = s.procs[0].Process.Kill()
		_ = s.procs[0].Wait()
	}
	_ = s.logFiles[0].Close()

	// Start a fresh gateway with the identical env (same port, same token).
	gw := exec.Command(filepath.Join(root, "rust", "target", "debug", "purser-gateway"))
	gw.Env = s.gwEnv
	logPath := filepath.Join(s.tmp, "gw-restart.log")
	f, err := os.Create(logPath)
	if err != nil {
		t.Fatalf("RestartGateway: create log: %v", err)
	}
	gw.Stdout, gw.Stderr = f, f
	if err := gw.Start(); err != nil {
		t.Fatalf("RestartGateway: start gateway: %v", err)
	}

	// Replace the old entries so Stop() kills and reaps the new process.
	s.procs[0] = gw
	s.logFiles[0] = f

	// Condition-based wait: poll until the new gateway answers /healthz.
	waitReady(t, s.GatewayBase+"/healthz", 15*time.Second)
}

// modelListed returns true when GET {GatewayBase}/v1/models lists id in its
// data array. Bearer testkey is the API key seeded in the test harness.
func modelListed(s *Stack, id string) bool {
	req, _ := http.NewRequest("GET", s.GatewayBase+"/v1/models", nil) //nolint:noctx
	req.Header.Set("Authorization", "Bearer testkey")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	var out struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return false
	}
	for _, m := range out.Data {
		if m.ID == id {
			return true
		}
	}
	return false
}

// assertModelListed fatals if modelListed(s, id) != want.
func assertModelListed(t *testing.T, s *Stack, id string, want bool) {
	t.Helper()
	if got := modelListed(s, id); got != want {
		t.Fatalf("assertModelListed(%q): got %v, want %v", id, got, want)
	}
}
