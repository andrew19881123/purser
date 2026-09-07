// config_snapshot.go — CP→DP config snapshot push mechanism (v0.5).
//
// The Data Plane must be able to operate autonomously when the Control Plane is
// temporarily unreachable.  To support this, the CP periodically pushes a
// DataPlaneConfigSnapshot to every registered Data Plane; the snapshot is stored
// in the registry so the DP can pull it at any time via
// GET /api/v1/platform/dataplanes/{id}/config.
//
// A background goroutine (startConfigSnapshotPusher) refreshes all active DPs
// every 30 seconds.  An operator or CI pipeline can also trigger an immediate
// refresh via POST /api/v1/platform/dataplanes/{id}/config/refresh.
package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/purser/purser/go/controlplane/registry"
	purserv1 "github.com/purser/purser/go/gen/purser/v1"
)

// ─── Snapshot builder ─────────────────────────────────────────────────────────

// BuildDataPlaneSnapshot constructs the configuration snapshot for a specific
// Data Plane.  The snapshot contains everything the DP needs to operate
// autonomously if the CP is temporarily unreachable.
//
// Contents:
//   - routing_table: active deployment → endpoint mapping for deployments whose
//     host or engine nodes are assigned to this DP
//   - auth_bundle:   SHA-256 hashes of enabled API keys + role/tenant/quota
//   - policy_bundle: names of enabled OPA policies for this DP
//   - generated_at:  snapshot timestamp
//
// This method is exported so it can be called from tests.
func (s *Server) BuildDataPlaneSnapshot(ctx context.Context, dpID string) (*registry.DataPlaneConfigSnapshot, error) {
	snapshot := &registry.DataPlaneConfigSnapshot{
		GeneratedAt:  time.Now().UTC(),
		RoutingTable: map[string]any{},
		AuthBundle:   map[string]any{},
		PolicyBundle: []string{},
	}

	// 1. Collect the node IDs assigned to this DP.
	nodes, err := s.reg.ListNodesByDataPlane(ctx, dpID)
	if err != nil {
		return nil, fmt.Errorf("list nodes for DP %s: %w", dpID, err)
	}
	nodeIDs := make(map[string]bool, len(nodes))
	for _, n := range nodes {
		nodeIDs[n.ID] = true
	}

	// 2. Routing table: active deployments that run on at least one node of this DP.
	// We reuse the deploymentOccupiesNode helper (defined in server.go) which
	// decodes host_node_id and engines[].node_id from the Detail JSON blob —
	// no plan fetch required.
	deployments, err := s.reg.ListDeployments(ctx)
	if err != nil {
		return nil, fmt.Errorf("list deployments: %w", err)
	}
	activeState := purserv1.DeploymentState_DEPLOYMENT_STATE_ACTIVE.String()
	for _, d := range deployments {
		if d.State != activeState {
			continue
		}
		// Only include if at least one DP node hosts this deployment.
		hasNode := false
		for nodeID := range nodeIDs {
			if deploymentOccupiesNode(d, nodeID) {
				hasNode = true
				break
			}
		}
		if !hasNode {
			continue
		}
		// Extract endpoint and quantization from the Detail JSON blob.
		var detail struct {
			Endpoint     string `json:"endpoint"`
			Quantization string `json:"quantization"`
		}
		if len(d.Detail) > 0 {
			_ = json.Unmarshal(d.Detail, &detail)
		}
		snapshot.RoutingTable[d.ModelID] = map[string]any{
			"deployment_id": d.ID,
			"state":         d.State,
			"endpoint":      detail.Endpoint,
			"quantization":  detail.Quantization,
		}
	}

	// 3. Auth bundle: enabled API keys reachable by this DP.
	// Future: scope to team pools assigned to this DP.
	keys, err := s.reg.ListAPIKeys(ctx)
	if err != nil {
		return nil, fmt.Errorf("list api keys: %w", err)
	}
	for _, k := range keys {
		if !k.Enabled {
			continue
		}
		snapshot.AuthBundle[k.KeyHash] = map[string]any{
			"role":   k.Role,
			"tenant": k.Tenant,
			"quota":  k.Quota,
		}
	}

	// 4. Policy bundle: enabled OPA policy names.
	policies, err := s.reg.ListPolicies(ctx)
	if err != nil {
		return nil, fmt.Errorf("list policies: %w", err)
	}
	for _, p := range policies {
		if p.Enabled {
			snapshot.PolicyBundle = append(snapshot.PolicyBundle, p.Name)
		}
	}

	return snapshot, nil
}

// PushConfigSnapshot rebuilds the snapshot for dpID and stores it in the
// registry.  The DP gateway can then pull it via
// GET /api/v1/platform/dataplanes/{id}/config.
//
// This method is exported so it can be called from tests.
func (s *Server) PushConfigSnapshot(ctx context.Context, dpID string) error {
	snapshot, err := s.BuildDataPlaneSnapshot(ctx, dpID)
	if err != nil {
		return fmt.Errorf("build snapshot for DP %s: %w", dpID, err)
	}
	return s.reg.UpdateDataPlaneConfigSnapshot(ctx, dpID, snapshot)
}

// ─── Background pusher ────────────────────────────────────────────────────────

// startConfigSnapshotPusher runs a background loop that refreshes the config
// snapshot for all registered, non-offline Data Planes every 30 seconds.
//
// The DP can also trigger an immediate refresh by calling
// POST /api/v1/platform/dataplanes/{id}/config/refresh.
func (s *Server) startConfigSnapshotPusher(ctx context.Context) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			dps, err := s.reg.ListDataPlanes(ctx)
			if err != nil {
				s.log.Warn("config snapshot pusher: list DPs failed", "err", err)
				continue
			}
			for _, dp := range dps {
				if dp.Status == "offline" {
					continue
				}
				if err := s.PushConfigSnapshot(ctx, dp.ID); err != nil {
					s.log.Warn("config snapshot push failed",
						"dp_id", dp.ID, "err", err)
				}
			}
		}
	}
}

// ─── HTTP handler ─────────────────────────────────────────────────────────────

// handleRefreshDataPlaneConfig triggers an immediate config snapshot rebuild and
// store for the named Data Plane.
//
//	POST /api/v1/platform/dataplanes/{id}/config/refresh
//	200: {"message":"config snapshot refreshed","dataplane_id":"…","refreshed_at":"…"}
//	404: when the DP does not exist
func (s *Server) handleRefreshDataPlaneConfig(w http.ResponseWriter, r *http.Request) {
	dpID := r.PathValue("id")
	if err := s.PushConfigSnapshot(r.Context(), dpID); err != nil {
		if errors.Is(err, registry.ErrNotFound) {
			s.writeError(w, http.StatusNotFound, "not_found", "data plane not found")
			return
		}
		s.writeError(w, http.StatusInternalServerError, "refresh_failed", err.Error())
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{
		"message":      "config snapshot refreshed",
		"dataplane_id": dpID,
		"refreshed_at": time.Now().UTC().Format(time.RFC3339),
	})
}
