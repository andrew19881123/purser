package server_test

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/purser/purser/go/controlplane/registry"
	"github.com/purser/purser/go/controlplane/server"
	purserv1 "github.com/purser/purser/go/gen/purser/v1"
	"google.golang.org/protobuf/encoding/protojson"
)

// fakeDeployer records calls and returns a canned deployment id.
type fakeDeployer struct {
	applied  *purserv1.DeploymentPlan
	tornDown string
	id       string
	err      error
}

func (f *fakeDeployer) Apply(_ context.Context, plan *purserv1.DeploymentPlan) (string, error) {
	f.applied = plan
	return f.id, f.err
}
func (f *fakeDeployer) Teardown(_ context.Context, id string) error {
	f.tornDown = id
	return f.err
}

func newReg(t *testing.T) registry.Registry {
	t.Helper()
	reg, err := registry.Open(filepath.Join(t.TempDir(), "registry.db"))
	if err != nil {
		t.Fatalf("open registry: %v", err)
	}
	if err := reg.Migrate(context.Background()); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	t.Cleanup(func() { reg.Close() })
	return reg
}

func TestHandleGetNode(t *testing.T) {
	reg := newReg(t)
	_ = reg.CreateNode(context.Background(), &registry.Node{ID: "n1", Hostname: "h1", State: "NODE_STATE_READY"})
	srv := server.New(reg, server.Config{})

	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/nodes/n1", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
	var n registry.Node
	if err := json.Unmarshal(rec.Body.Bytes(), &n); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if n.ID != "n1" {
		t.Errorf("id = %q", n.ID)
	}

	rec2 := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec2, httptest.NewRequest(http.MethodGet, "/api/v1/nodes/missing", nil))
	if rec2.Code != http.StatusNotFound {
		t.Errorf("missing node status = %d, want 404", rec2.Code)
	}
}

func TestHandleListModelsAndDeployments(t *testing.T) {
	reg := newReg(t)
	ctx := context.Background()
	_ = reg.CreateModel(ctx, &registry.Model{ID: "m1", Family: "llama"})
	_ = reg.CreateDeployment(ctx, &registry.Deployment{ID: "d1", ModelID: "m1", State: "DEPLOYMENT_STATE_ACTIVE"})
	srv := server.New(reg, server.Config{})

	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/models", nil))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "m1") {
		t.Fatalf("models: status=%d body=%s", rec.Code, rec.Body.String())
	}

	rec2 := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec2, httptest.NewRequest(http.MethodGet, "/api/v1/deployments", nil))
	if rec2.Code != http.StatusOK || !strings.Contains(rec2.Body.String(), "d1") {
		t.Fatalf("deployments: status=%d body=%s", rec2.Code, rec2.Body.String())
	}
}

func TestHandleGetPlan(t *testing.T) {
	reg := newReg(t)
	_ = reg.CreatePlan(context.Background(), &registry.Plan{ID: "p1", ModelID: "m1", Quantization: "q4"})
	srv := server.New(reg, server.Config{})
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/plans/p1", nil))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "p1") {
		t.Fatalf("plan: status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestHandleDeployModel_InlinePlan(t *testing.T) {
	reg := newReg(t)
	fd := &fakeDeployer{id: "dep-123"}
	srv := server.New(reg, server.Config{Deployer: fd})

	plan := &purserv1.DeploymentPlan{
		ModelId: "m1",
		Assignments: []*purserv1.Assignment{
			{NodeId: "w1", Role: purserv1.Role_ROLE_WORKER},
			{NodeId: "h1", Role: purserv1.Role_ROLE_HOST},
		},
	}
	planJSON, _ := protojson.Marshal(plan)
	body := `{"plan": ` + string(planJSON) + `}`

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/models/m1/deploy", strings.NewReader(body))
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
	var resp map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	if resp["deployment_id"] != "dep-123" {
		t.Errorf("deployment_id = %v, want dep-123", resp["deployment_id"])
	}
	if fd.applied == nil || fd.applied.GetModelId() != "m1" || len(fd.applied.GetAssignments()) != 2 {
		t.Errorf("deployer received wrong plan: %+v", fd.applied)
	}
}

func TestHandleDeployModel_NoDeployer(t *testing.T) {
	reg := newReg(t)
	srv := server.New(reg, server.Config{}) // no deployer
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/models/m1/deploy", strings.NewReader(`{"plan_id":"p1"}`))
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusNotImplemented {
		t.Errorf("status = %d, want 501", rec.Code)
	}
}

func TestHandleDeleteDeployment(t *testing.T) {
	reg := newReg(t)
	fd := &fakeDeployer{}
	srv := server.New(reg, server.Config{Deployer: fd})
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodDelete, "/api/v1/deployments/d1", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
	if fd.tornDown != "d1" {
		t.Errorf("Teardown called with %q, want d1", fd.tornDown)
	}
}

func TestHandleCreateAPIKey(t *testing.T) {
	reg := newReg(t)
	srv := server.New(reg, server.Config{})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/apikeys", strings.NewReader(`{"name":"ci","tenant":"team-a","quota":1000}`))
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
	var resp map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	plaintext, _ := resp["key"].(string)
	if !strings.HasPrefix(plaintext, "psk_") {
		t.Fatalf("returned key = %q, want psk_ prefix", plaintext)
	}
	// Only the hash must be persisted, matching sha256(plaintext).
	keys, _ := reg.ListAPIKeys(context.Background())
	if len(keys) != 1 {
		t.Fatalf("expected 1 stored key, got %d", len(keys))
	}
	sum := sha256.Sum256([]byte(plaintext))
	if keys[0].KeyHash != hex.EncodeToString(sum[:]) {
		t.Errorf("stored hash mismatch")
	}
	if !keys[0].Enabled || keys[0].Tenant != "team-a" {
		t.Errorf("stored key wrong: %+v", keys[0])
	}
}

func TestHandleMetricsSSE(t *testing.T) {
	reg := newReg(t)
	_ = reg.CreateNode(context.Background(), &registry.Node{ID: "n1", State: "NODE_STATE_READY"})
	srv := server.New(reg, server.Config{MetricsInterval: 50 * time.Millisecond})

	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, ts.URL+"/api/v1/metrics", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET metrics: %v", err)
	}
	defer resp.Body.Close()
	if ct := resp.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Errorf("content-type = %q, want text/event-stream", ct)
	}
	line, err := bufio.NewReader(resp.Body).ReadString('\n')
	if err != nil {
		t.Fatalf("read first SSE line: %v", err)
	}
	if !strings.HasPrefix(line, "data: ") {
		t.Errorf("first SSE line = %q, want data: prefix", line)
	}
}
