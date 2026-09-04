package server_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/purser/purser/go/controlplane/fleet"
	"github.com/purser/purser/go/controlplane/pki"
	"github.com/purser/purser/go/controlplane/registry"
	"github.com/purser/purser/go/controlplane/server"
	purserv1 "github.com/purser/purser/go/gen/purser/v1"
	"google.golang.org/protobuf/encoding/protojson"
)

// newFleetManager builds a real fleet.Manager (backed by a real internal CA) so
// minted tokens can be validated end-to-end via Join. No network, no CGO.
func newFleetManager(t *testing.T, reg registry.Registry) *fleet.Manager {
	t.Helper()
	ca, err := pki.New(context.Background(), reg, pki.Options{})
	if err != nil {
		t.Fatalf("pki: %v", err)
	}
	return fleet.NewWithSecret(reg, ca, []byte("join-token-test-secret"))
}

func TestHandleJoinToken(t *testing.T) {
	reg := newReg(t)
	mgr := newFleetManager(t, reg)
	srv := server.New(reg, server.Config{Fleet: mgr, ClusterID: "e2e"})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/join-token", strings.NewReader(`{"ttl_seconds":120}`))
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body=%s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Token     string `json:"token"`
		ExpiresAt string `json:"expires_at"`
		ClusterID string `json:"cluster_id"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v; raw=%s", err, rec.Body.String())
	}
	if resp.Token == "" {
		t.Fatal("token is empty")
	}
	if resp.ClusterID != "e2e" {
		t.Errorf("cluster_id = %q, want e2e", resp.ClusterID)
	}
	if resp.ExpiresAt == "" {
		t.Error("expires_at is empty")
	}

	// The minted token must be usable by the fleet manager: Join enrolls a node.
	jr, err := mgr.Join(context.Background(), resp.Token, &purserv1.HardwareProfile{NodeId: "n-e2e", Hostname: "h1"}, "", "")
	if err != nil {
		t.Fatalf("join with minted token: %v", err)
	}
	if jr.NodeID != "n-e2e" {
		t.Errorf("joined node id = %q, want n-e2e", jr.NodeID)
	}
}

func TestHandleJoinToken_DefaultBodyAndClusterID(t *testing.T) {
	reg := newReg(t)
	mgr := newFleetManager(t, reg)
	srv := server.New(reg, server.Config{Fleet: mgr}) // no ClusterID -> "default"

	rec := httptest.NewRecorder()
	// No body at all: TTL falls back to the fleet default.
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/v1/join-token", nil))
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body=%s", rec.Code, rec.Body.String())
	}
	var resp map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	if resp["cluster_id"] != "default" {
		t.Errorf("cluster_id = %v, want default", resp["cluster_id"])
	}
	if tok, _ := resp["token"].(string); tok == "" {
		t.Error("token is empty")
	}
}

func TestHandleJoinToken_NoFleet(t *testing.T) {
	reg := newReg(t)
	srv := server.New(reg, server.Config{}) // no fleet manager
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/v1/join-token", nil))
	if rec.Code != http.StatusNotImplemented {
		t.Errorf("status = %d, want 501", rec.Code)
	}
}

func TestHandleCreateModel(t *testing.T) {
	reg := newReg(t)
	srv := server.New(reg, server.Config{})

	spec := &purserv1.ModelSpec{
		ModelId:      "llama-3-8b",
		Family:       "llama",
		Architecture: "llama",
		ParamsTotalB: 8,
		Engine:       "vllm",
	}
	specJSON, err := protojson.Marshal(spec)
	if err != nil {
		t.Fatalf("marshal spec: %v", err)
	}

	// Create -> 201 with model_id.
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/v1/models", bytes.NewReader(specJSON)))
	if rec.Code != http.StatusCreated {
		t.Fatalf("create status = %d, want 201; body=%s", rec.Code, rec.Body.String())
	}
	var cr map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &cr)
	if cr["model_id"] != "llama-3-8b" {
		t.Errorf("model_id = %v, want llama-3-8b", cr["model_id"])
	}

	// GET /models lists it.
	rec2 := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec2, httptest.NewRequest(http.MethodGet, "/api/v1/models", nil))
	if rec2.Code != http.StatusOK || !strings.Contains(rec2.Body.String(), "llama-3-8b") {
		t.Fatalf("list: status=%d body=%s", rec2.Code, rec2.Body.String())
	}

	// Promoted columns + spec persisted, and the spec round-trips via protojson.
	m, err := reg.GetModel(context.Background(), "llama-3-8b")
	if err != nil {
		t.Fatalf("get model: %v", err)
	}
	if m.Family != "llama" || m.Engine != "vllm" || m.ParamsTotalB != 8 {
		t.Errorf("promoted columns wrong: %+v", m)
	}
	got := &purserv1.ModelSpec{}
	if err := protojson.Unmarshal(m.Spec, got); err != nil {
		t.Fatalf("decode stored spec: %v", err)
	}
	if got.GetModelId() != "llama-3-8b" || got.GetEngine() != "vllm" {
		t.Errorf("stored spec = %+v", got)
	}

	// Duplicate id -> 409.
	rec3 := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec3, httptest.NewRequest(http.MethodPost, "/api/v1/models", bytes.NewReader(specJSON)))
	if rec3.Code != http.StatusConflict {
		t.Errorf("duplicate status = %d, want 409; body=%s", rec3.Code, rec3.Body.String())
	}

	// Invalid JSON body -> 400.
	rec4 := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec4, httptest.NewRequest(http.MethodPost, "/api/v1/models", strings.NewReader(`{not json`)))
	if rec4.Code != http.StatusBadRequest {
		t.Errorf("bad body status = %d, want 400", rec4.Code)
	}

	// Missing model_id -> 400.
	rec5 := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec5, httptest.NewRequest(http.MethodPost, "/api/v1/models", strings.NewReader(`{"family":"x"}`)))
	if rec5.Code != http.StatusBadRequest {
		t.Errorf("missing model_id status = %d, want 400", rec5.Code)
	}

	// Empty body -> 400.
	rec6 := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec6, httptest.NewRequest(http.MethodPost, "/api/v1/models", nil))
	if rec6.Code != http.StatusBadRequest {
		t.Errorf("empty body status = %d, want 400", rec6.Code)
	}
}
