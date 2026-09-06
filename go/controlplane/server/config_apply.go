// config_apply.go — REST handlers for config-as-code (purser.yaml) apply/diff/export.
//
// Three endpoints are added to the management API:
//
//	POST /api/v1/config/apply  — apply desired state from a purser.yaml body
//	POST /api/v1/config/diff   — dry-run: show what would change (no mutations)
//	GET  /api/v1/config/export — export current cluster state as purser.yaml
//
// They wrap the config package (go/controlplane/config) and surface it over
// HTTP so operators can drive the cluster from a GitOps pipeline without
// needing the purser CLI. The --config startup flag (main.go) uses
// ApplyClusterConfig to apply a file at boot.
package server

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"

	"github.com/purser/purser/go/controlplane/config"
	"github.com/purser/purser/go/controlplane/registry"
	plannerplan "github.com/purser/purser/go/planner/plan"
	"gopkg.in/yaml.v3"
)

// registryLister adapts registry.Registry to config.Lister.
// The config package only needs the two read methods; this thin adapter keeps
// it independent of the full registry surface.
type registryLister struct {
	r registry.Registry
}

func (l *registryLister) CurrentModelIDs(ctx context.Context) ([]string, error) {
	models, err := l.r.ListModels(ctx)
	if err != nil {
		return nil, err
	}
	ids := make([]string, len(models))
	for i, m := range models {
		ids[i] = m.ID
	}
	return ids, nil
}

func (l *registryLister) CurrentDeploymentModelIDs(ctx context.Context) ([]string, error) {
	deps, err := l.r.ListDeployments(ctx)
	if err != nil {
		return nil, err
	}
	// Collect non-terminal deployments only; terminal ones no longer "occupy" a slot.
	seen := map[string]bool{}
	for _, d := range deps {
		if !deploymentTerminal(d.State) {
			seen[d.ModelID] = true
		}
	}
	ids := make([]string, 0, len(seen))
	for id := range seen {
		ids = append(ids, id)
	}
	return ids, nil
}

// ApplyResult summarises the mutations made by ApplyClusterConfig.
type ApplyResult struct {
	ModelsAdded       int `json:"models_added"`
	DeploymentsAdded  int `json:"deployments_added"`
	QuotasUpserted    int `json:"quotas_upserted"`
}

// ApplyClusterConfig reconciles the live cluster state towards cfg.
// It is called by handleConfigApply (HTTP) and by main.go at startup when
// --config / PURSER_CONFIG is set.
//
// Behaviour:
//   - Models in diff.ModelsToAdd are created via CreateModel.
//   - Deployments in diff.DeploymentsToAdd are submitted if both Planner and
//     Deployer are configured; otherwise they are logged and skipped.
//   - Quota upsert is not yet implemented in the registry layer; the call is
//     logged and skipped.
//   - Non-fatal individual errors (model already exists, plan infeasible) are
//     logged and do not abort the whole apply.
func (s *Server) ApplyClusterConfig(ctx context.Context, cfg *config.ClusterConfig) (ApplyResult, error) {
	diff, err := config.Diff(ctx, cfg, &registryLister{s.reg})
	if err != nil {
		return ApplyResult{}, fmt.Errorf("config diff: %w", err)
	}

	var result ApplyResult

	// --- Models ---
	for _, spec := range diff.ModelsToAdd {
		sourceBlob, err := json.Marshal(spec.Source)
		if err != nil {
			s.log.Warn("config apply: marshal source failed; skipping model",
				"model", spec.ID, "err", err)
			continue
		}
		m := &registry.Model{
			ID:     spec.ID,
			Family: spec.Source.Repo,
			Type:   "llm",
			Source: sourceBlob,
		}
		if err := s.reg.CreateModel(ctx, m); err != nil {
			if strings.Contains(strings.ToLower(err.Error()), "unique") {
				// Idempotent: already present due to a race with another apply.
				continue
			}
			s.log.Warn("config apply: create model failed; skipping",
				"model", spec.ID, "err", err)
			continue
		}
		_ = s.reg.AppendAudit(ctx, &registry.AuditEntry{
			Actor:  "config-apply",
			Action: "model.created",
			Target: spec.ID,
		})
		result.ModelsAdded++
	}

	// --- Deployments ---
	for _, spec := range diff.DeploymentsToAdd {
		if s.planner == nil || s.deployer == nil {
			s.log.Info("config apply: skipping deployment — planner or deployer not configured",
				"model", spec.Model)
			continue
		}
		constraints := plannerplan.Constraints{}
		if spec.Quantization != "" {
			constraints.ForceQuant = &spec.Quantization
		}
		plan, err := s.planner.Plan(ctx, spec.Model, constraints)
		if err != nil {
			s.log.Warn("config apply: planner failed; skipping deployment",
				"model", spec.Model, "err", err)
			continue
		}
		if _, err := s.deployer.Apply(ctx, plan); err != nil {
			s.log.Warn("config apply: deployer failed; skipping deployment",
				"model", spec.Model, "err", err)
			continue
		}
		result.DeploymentsAdded++
	}

	// --- Quotas ---
	// UpsertQuota is not yet in the Registry interface; log and skip so the
	// apply does not fail for environments that use quotas.
	if len(diff.QuotasToUpsert) > 0 {
		s.log.Info("config apply: skipping quota upsert — not yet implemented in registry",
			"count", len(diff.QuotasToUpsert))
	}

	return result, nil
}

// handleConfigApply handles POST /api/v1/config/apply.
// It reads a raw purser.yaml body, validates it, computes a diff against the
// live registry, and applies the changes (models + deployments). Quotas are
// not yet persisted (logged + skipped).
func (s *Server) handleConfigApply(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		s.writeError(w, http.StatusBadRequest, "bad_request", "read body: "+err.Error())
		return
	}
	if len(body) == 0 {
		s.writeError(w, http.StatusBadRequest, "bad_request", "empty body: expected purser.yaml")
		return
	}

	cfg, err := config.Load(body)
	if err != nil {
		s.writeError(w, http.StatusBadRequest, "invalid_config", err.Error())
		return
	}

	result, err := s.ApplyClusterConfig(r.Context(), cfg)
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, "apply_failed", err.Error())
		return
	}

	s.log.Info("config apply complete",
		"models_added", result.ModelsAdded,
		"deployments_added", result.DeploymentsAdded,
		"quotas_upserted", result.QuotasUpserted,
		slog.String("cluster", cfg.Cluster.ID),
	)

	s.writeJSON(w, http.StatusOK, map[string]any{"applied": result})
}

// handleConfigDiff handles POST /api/v1/config/diff.
// Dry-run only — no mutations are made. Returns the DiffResult as JSON so
// callers (CI pipelines, operators) can inspect what apply would do.
func (s *Server) handleConfigDiff(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		s.writeError(w, http.StatusBadRequest, "bad_request", "read body: "+err.Error())
		return
	}
	if len(body) == 0 {
		s.writeError(w, http.StatusBadRequest, "bad_request", "empty body: expected purser.yaml")
		return
	}

	cfg, err := config.Load(body)
	if err != nil {
		s.writeError(w, http.StatusBadRequest, "invalid_config", err.Error())
		return
	}

	diff, err := config.Diff(r.Context(), cfg, &registryLister{s.reg})
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, "diff_failed", err.Error())
		return
	}

	// Normalise nil slices to empty so callers get consistent JSON shapes.
	if diff.ModelsToAdd == nil {
		diff.ModelsToAdd = []config.ModelSpec{}
	}
	if diff.ModelsToRemove == nil {
		diff.ModelsToRemove = []string{}
	}
	if diff.DeploymentsToAdd == nil {
		diff.DeploymentsToAdd = []config.DeploySpec{}
	}
	if diff.DeploymentsToRemove == nil {
		diff.DeploymentsToRemove = []string{}
	}
	if diff.QuotasToUpsert == nil {
		diff.QuotasToUpsert = []config.QuotaSpec{}
	}

	s.writeJSON(w, http.StatusOK, map[string]any{
		"models_to_add":         diff.ModelsToAdd,
		"models_to_remove":      diff.ModelsToRemove,
		"deployments_to_add":    diff.DeploymentsToAdd,
		"deployments_to_remove": diff.DeploymentsToRemove,
		"quotas_to_upsert":      diff.QuotasToUpsert,
	})
}

// exportModelSpec converts a registry.Model to a config.ModelSpec.
// Fields that are unknown (no source JSON) are left at their zero value.
func exportModelSpec(m *registry.Model) config.ModelSpec {
	spec := config.ModelSpec{
		ID: m.ID,
	}
	// Best-effort: decode the opaque source blob into a SourceSpec.
	if len(m.Source) > 0 {
		var src struct {
			Type      string `json:"type"`
			Repo      string `json:"repo"`
			BucketURL string `json:"bucket_url"`
			Path      string `json:"path"`
		}
		if err := json.Unmarshal(m.Source, &src); err == nil {
			spec.Source = config.SourceSpec{
				Type:      src.Type,
				Repo:      src.Repo,
				BucketURL: src.BucketURL,
				Path:      src.Path,
			}
		}
	}
	return spec
}

// handleConfigExport handles GET /api/v1/config/export.
// It reads models and deployments from the registry and serialises them as a
// ClusterConfig YAML document. Quotas are not yet stored in the registry; they
// are omitted from the export.
func (s *Server) handleConfigExport(w http.ResponseWriter, r *http.Request) {
	models, err := s.reg.ListModels(r.Context())
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, "list_models_failed", err.Error())
		return
	}

	deps, err := s.reg.ListDeployments(r.Context())
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, "list_deployments_failed", err.Error())
		return
	}

	// Build the desired-state spec from live registry state.
	cfg := config.ClusterConfig{
		APIVersion: "purser/v1",
		Kind:       "ClusterConfig",
		Cluster: config.ClusterSpec{
			ID: s.clusterID,
		},
	}

	for _, m := range models {
		cfg.Models = append(cfg.Models, exportModelSpec(m))
	}

	// Include only non-terminal deployments.
	for _, d := range deps {
		if deploymentTerminal(d.State) {
			continue
		}
		cfg.Deployments = append(cfg.Deployments, config.DeploySpec{
			Model: d.ModelID,
		})
	}

	if cfg.Models == nil {
		cfg.Models = []config.ModelSpec{}
	}
	if cfg.Deployments == nil {
		cfg.Deployments = []config.DeploySpec{}
	}

	out, err := yaml.Marshal(cfg)
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, "marshal_yaml_failed", err.Error())
		return
	}

	w.Header().Set("Content-Type", "application/yaml")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(out)
}
