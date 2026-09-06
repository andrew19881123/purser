package config_test

import (
	"context"
	"testing"

	"github.com/purser/purser/go/controlplane/config"
)

const validYAML = `
apiVersion: purser/v1
kind: ClusterConfig
metadata:
  name: test-cluster
cluster:
  id: test-cluster-01
models:
  - id: qwen3
    source:
      type: huggingface
      repo: Qwen/Qwen3-8B
    quantizations: [Q4_K_M]
deployments:
  - model: qwen3
    quantization: Q4_K_M
quotas:
  - team: eng
    monthly_requests: 100000
`

// --- Load / Validate ---

func TestLoad_ValidConfig(t *testing.T) {
	cfg, err := config.Load([]byte(validYAML))
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.APIVersion != "purser/v1" {
		t.Errorf("APIVersion = %q, want purser/v1", cfg.APIVersion)
	}
	if len(cfg.Models) != 1 {
		t.Errorf("len(Models) = %d, want 1", len(cfg.Models))
	}
	if cfg.Models[0].ID != "qwen3" {
		t.Errorf("Models[0].ID = %q, want qwen3", cfg.Models[0].ID)
	}
	if len(cfg.Deployments) != 1 {
		t.Errorf("len(Deployments) = %d, want 1", len(cfg.Deployments))
	}
	if cfg.Deployments[0].Model != "qwen3" {
		t.Errorf("Deployments[0].Model = %q, want qwen3", cfg.Deployments[0].Model)
	}
}

func TestLoad_UnknownAPIVersion(t *testing.T) {
	_, err := config.Load([]byte("apiVersion: purser/v99\nkind: ClusterConfig\n"))
	if err == nil {
		t.Fatal("Load() expected error, got nil")
	}
	want := "unsupported apiVersion"
	if !contains(err.Error(), want) {
		t.Errorf("err = %q, want to contain %q", err.Error(), want)
	}
}

func TestLoad_UnknownKind(t *testing.T) {
	_, err := config.Load([]byte("apiVersion: purser/v1\nkind: Unknown\n"))
	if err == nil {
		t.Fatal("Load() expected error, got nil")
	}
	want := "unsupported kind"
	if !contains(err.Error(), want) {
		t.Errorf("err = %q, want to contain %q", err.Error(), want)
	}
}

func TestLoad_DeploymentReferencesUnknownModel(t *testing.T) {
	yaml := `apiVersion: purser/v1
kind: ClusterConfig
metadata:
  name: bad
deployments:
  - model: nonexistent
    quantization: Q4_K_M
`
	_, err := config.Load([]byte(yaml))
	if err == nil {
		t.Fatal("Load() expected error, got nil")
	}
	want := "unknown model"
	if !contains(err.Error(), want) {
		t.Errorf("err = %q, want to contain %q", err.Error(), want)
	}
}

func TestLoad_ModelMissingID(t *testing.T) {
	yaml := `apiVersion: purser/v1
kind: ClusterConfig
models:
  - source:
      type: huggingface
      repo: Qwen/Qwen3-8B
`
	_, err := config.Load([]byte(yaml))
	if err == nil {
		t.Fatal("Load() expected error, got nil")
	}
	want := "missing required field 'id'"
	if !contains(err.Error(), want) {
		t.Errorf("err = %q, want to contain %q", err.Error(), want)
	}
}

func TestLoad_UnknownSourceType(t *testing.T) {
	yaml := `apiVersion: purser/v1
kind: ClusterConfig
models:
  - id: mymodel
    source:
      type: gcs
`
	_, err := config.Load([]byte(yaml))
	if err == nil {
		t.Fatal("Load() expected error, got nil")
	}
	want := "unknown source type"
	if !contains(err.Error(), want) {
		t.Errorf("err = %q, want to contain %q", err.Error(), want)
	}
}

func TestLoad_DuplicateModelID(t *testing.T) {
	yaml := `apiVersion: purser/v1
kind: ClusterConfig
models:
  - id: mymodel
    source:
      type: huggingface
      repo: Foo/Bar
  - id: mymodel
    source:
      type: huggingface
      repo: Foo/Baz
`
	_, err := config.Load([]byte(yaml))
	if err == nil {
		t.Fatal("Load() expected error, got nil")
	}
	want := "duplicate model id"
	if !contains(err.Error(), want) {
		t.Errorf("err = %q, want to contain %q", err.Error(), want)
	}
}

// --- Diff ---

func TestDiff_NilLister(t *testing.T) {
	cfg, err := config.Load([]byte(validYAML))
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	diff, err := config.Diff(context.Background(), cfg, nil)
	if err != nil {
		t.Fatalf("Diff() error = %v", err)
	}
	if len(diff.ModelsToAdd) != 1 {
		t.Errorf("len(ModelsToAdd) = %d, want 1", len(diff.ModelsToAdd))
	}
	if len(diff.DeploymentsToAdd) != 1 {
		t.Errorf("len(DeploymentsToAdd) = %d, want 1", len(diff.DeploymentsToAdd))
	}
	if len(diff.ModelsToRemove) != 0 {
		t.Errorf("len(ModelsToRemove) = %d, want 0", len(diff.ModelsToRemove))
	}
}

func TestDiff_EmptyLive(t *testing.T) {
	cfg, _ := config.Load([]byte(validYAML))
	lister := &fakeLister{}
	diff, err := config.Diff(context.Background(), cfg, lister)
	if err != nil {
		t.Fatalf("Diff() error = %v", err)
	}
	if len(diff.ModelsToAdd) != 1 {
		t.Errorf("len(ModelsToAdd) = %d, want 1", len(diff.ModelsToAdd))
	}
	if len(diff.ModelsToRemove) != 0 {
		t.Errorf("len(ModelsToRemove) = %d, want 0", len(diff.ModelsToRemove))
	}
}

func TestDiff_ModelAlreadyPresent(t *testing.T) {
	cfg, _ := config.Load([]byte(validYAML))
	lister := &fakeLister{
		models:      []string{"qwen3"},
		deployments: []string{"qwen3"},
	}
	diff, err := config.Diff(context.Background(), cfg, lister)
	if err != nil {
		t.Fatalf("Diff() error = %v", err)
	}
	if len(diff.ModelsToAdd) != 0 {
		t.Errorf("len(ModelsToAdd) = %d, want 0 (already present)", len(diff.ModelsToAdd))
	}
	if len(diff.ModelsToRemove) != 0 {
		t.Errorf("len(ModelsToRemove) = %d, want 0", len(diff.ModelsToRemove))
	}
}

func TestDiff_ObsoleteModelRemoved(t *testing.T) {
	cfg, _ := config.Load([]byte(validYAML))
	lister := &fakeLister{
		models: []string{"qwen3", "old-model"},
	}
	diff, err := config.Diff(context.Background(), cfg, lister)
	if err != nil {
		t.Fatalf("Diff() error = %v", err)
	}
	if len(diff.ModelsToRemove) != 1 || diff.ModelsToRemove[0] != "old-model" {
		t.Errorf("ModelsToRemove = %v, want [old-model]", diff.ModelsToRemove)
	}
}

func TestDiff_QuotasAlwaysUpserted(t *testing.T) {
	cfg, _ := config.Load([]byte(validYAML))
	diff, _ := config.Diff(context.Background(), cfg, nil)
	if len(diff.QuotasToUpsert) != 1 {
		t.Errorf("len(QuotasToUpsert) = %d, want 1", len(diff.QuotasToUpsert))
	}
	if diff.QuotasToUpsert[0].Team != "eng" {
		t.Errorf("QuotasToUpsert[0].Team = %q, want eng", diff.QuotasToUpsert[0].Team)
	}
}

// --- helpers ---

type fakeLister struct {
	models      []string
	deployments []string
}

func (f *fakeLister) CurrentModelIDs(_ context.Context) ([]string, error) {
	return f.models, nil
}

func (f *fakeLister) CurrentDeploymentModelIDs(_ context.Context) ([]string, error) {
	return f.deployments, nil
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || len(sub) == 0 ||
		func() bool {
			for i := 0; i <= len(s)-len(sub); i++ {
				if s[i:i+len(sub)] == sub {
					return true
				}
			}
			return false
		}())
}
