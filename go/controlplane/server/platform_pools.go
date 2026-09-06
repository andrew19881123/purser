// platform_pools.go — REST handlers for node pool CRUD, node assignment, and
// per-team quota management (v0.4 multi-tenant platform).
//
// Endpoints:
//
//	POST   /api/v1/platform/pools                          — create pool
//	GET    /api/v1/platform/pools                          — list pools
//	GET    /api/v1/platform/pools/{id}                     — get pool (+ node_ids)
//	PUT    /api/v1/platform/pools/{id}                     — update pool
//	DELETE /api/v1/platform/pools/{id}                     — delete (409 if has nodes)
//
//	POST   /api/v1/platform/pools/{id}/nodes               — assign node to pool
//	GET    /api/v1/platform/pools/{id}/nodes               — list nodes in pool
//	DELETE /api/v1/platform/pools/{id}/nodes/{nodeId}      — remove node from pool
//
//	PUT    /api/v1/platform/pools/{id}/quotas/{teamId}     — upsert quota
//	GET    /api/v1/platform/pools/{id}/quotas              — list quotas for pool
//	DELETE /api/v1/platform/pools/{id}/quotas/{teamId}     — delete quota
package server

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/purser/purser/go/controlplane/registry"
)

// ─── Pool CRUD ────────────────────────────────────────────────────────────────

// handleCreatePool creates a new node pool.
//
//	POST /api/v1/platform/pools
//	Body: {"name":"…","description":"…","owner_type":"team","owner_id":"…","policy":"exclusive"|"shared"}
//	201 NodePool JSON on success.
func (s *Server) handleCreatePool(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name        string `json:"name"`
		Description string `json:"description"`
		OwnerType   string `json:"owner_type"`
		OwnerID     string `json:"owner_id"`
		Policy      string `json:"policy"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.writeError(w, http.StatusBadRequest, "bad_request", "invalid JSON body: "+err.Error())
		return
	}
	if req.Name == "" {
		s.writeError(w, http.StatusBadRequest, "bad_request", "name is required")
		return
	}
	if req.Policy == "" {
		req.Policy = "exclusive"
	}
	if req.Policy != "exclusive" && req.Policy != "shared" {
		s.writeError(w, http.StatusBadRequest, "bad_request", "policy must be 'exclusive' or 'shared'")
		return
	}
	if req.OwnerType == "" {
		req.OwnerType = "platform"
	}

	p := &registry.NodePool{
		ID:          randHex(8),
		Name:        req.Name,
		Description: req.Description,
		OwnerType:   req.OwnerType,
		OwnerID:     req.OwnerID,
		Policy:      req.Policy,
	}
	if err := s.reg.CreateNodePool(r.Context(), p); err != nil {
		s.writeError(w, http.StatusInternalServerError, "create_pool_failed", err.Error())
		return
	}
	_ = s.reg.AppendAudit(r.Context(), &registry.AuditEntry{
		Actor:  actorFromRequest(r),
		Action: "pool.created",
		Target: p.ID,
	})
	s.writeJSON(w, http.StatusCreated, p)
}

// handleListPools returns all node pools.
//
//	GET /api/v1/platform/pools
//	200 {"pools":[…]}
func (s *Server) handleListPools(w http.ResponseWriter, r *http.Request) {
	pools, err := s.reg.ListNodePools(r.Context())
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, "list_pools_failed", err.Error())
		return
	}
	if pools == nil {
		pools = []*registry.NodePool{}
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"pools": pools})
}

// handleGetPool returns a single pool by ID, including its node_ids.
//
//	GET /api/v1/platform/pools/{id}
//	200 NodePool JSON (with node_ids populated).
func (s *Server) handleGetPool(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	p, err := s.reg.GetNodePool(r.Context(), id)
	if errors.Is(err, registry.ErrNotFound) {
		s.writeError(w, http.StatusNotFound, "not_found", "pool not found")
		return
	}
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, "get_pool_failed", err.Error())
		return
	}
	nodeIDs, err := s.reg.ListNodesInPool(r.Context(), id)
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, "list_pool_nodes_failed", err.Error())
		return
	}
	if nodeIDs == nil {
		nodeIDs = []string{}
	}
	p.NodeIDs = nodeIDs
	s.writeJSON(w, http.StatusOK, p)
}

// handleUpdatePool updates the name, description, or policy of a pool.
//
//	PUT /api/v1/platform/pools/{id}
//	Body: {"name":"…","description":"…","policy":"…"}
//	200 updated NodePool JSON.
func (s *Server) handleUpdatePool(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	p, err := s.reg.GetNodePool(r.Context(), id)
	if errors.Is(err, registry.ErrNotFound) {
		s.writeError(w, http.StatusNotFound, "not_found", "pool not found")
		return
	}
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, "get_pool_failed", err.Error())
		return
	}

	var req struct {
		Name        *string `json:"name"`
		Description *string `json:"description"`
		Policy      *string `json:"policy"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.writeError(w, http.StatusBadRequest, "bad_request", "invalid JSON body: "+err.Error())
		return
	}
	if req.Name != nil {
		p.Name = *req.Name
	}
	if req.Description != nil {
		p.Description = *req.Description
	}
	if req.Policy != nil {
		if *req.Policy != "exclusive" && *req.Policy != "shared" {
			s.writeError(w, http.StatusBadRequest, "bad_request", "policy must be 'exclusive' or 'shared'")
			return
		}
		p.Policy = *req.Policy
	}
	if err := s.reg.UpdateNodePool(r.Context(), p); err != nil {
		s.writeError(w, http.StatusInternalServerError, "update_pool_failed", err.Error())
		return
	}
	_ = s.reg.AppendAudit(r.Context(), &registry.AuditEntry{
		Actor:  actorFromRequest(r),
		Action: "pool.updated",
		Target: id,
	})
	s.writeJSON(w, http.StatusOK, p)
}

// handleDeletePool deletes a pool. Returns 409 if the pool still has assigned
// nodes (remove them first).
//
//	DELETE /api/v1/platform/pools/{id}
//	204 on success.
func (s *Server) handleDeletePool(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, err := s.reg.GetNodePool(r.Context(), id); errors.Is(err, registry.ErrNotFound) {
		s.writeError(w, http.StatusNotFound, "not_found", "pool not found")
		return
	} else if err != nil {
		s.writeError(w, http.StatusInternalServerError, "get_pool_failed", err.Error())
		return
	}

	nodeIDs, err := s.reg.ListNodesInPool(r.Context(), id)
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, "list_pool_nodes_failed", err.Error())
		return
	}
	if len(nodeIDs) > 0 {
		s.writeJSON(w, http.StatusConflict, map[string]any{
			"error":    "pool_has_nodes",
			"message":  "pool still has assigned nodes; remove them before deleting the pool",
			"node_ids": nodeIDs,
		})
		return
	}

	if err := s.reg.DeleteNodePool(r.Context(), id); err != nil {
		s.writeError(w, http.StatusInternalServerError, "delete_pool_failed", err.Error())
		return
	}
	_ = s.reg.AppendAudit(r.Context(), &registry.AuditEntry{
		Actor:  actorFromRequest(r),
		Action: "pool.deleted",
		Target: id,
	})
	w.WriteHeader(http.StatusNoContent)
}

// ─── Node assignment ──────────────────────────────────────────────────────────

// handleAssignNodeToPool assigns a node to a pool. Returns 409 if the node is
// already assigned to a different pool.
//
//	POST /api/v1/platform/pools/{id}/nodes
//	Body: {"node_id":"…"}
//	201 {"pool_id":"…","node_id":"…","message":"…"}
func (s *Server) handleAssignNodeToPool(w http.ResponseWriter, r *http.Request) {
	poolID := r.PathValue("id")
	if _, err := s.reg.GetNodePool(r.Context(), poolID); errors.Is(err, registry.ErrNotFound) {
		s.writeError(w, http.StatusNotFound, "not_found", "pool not found")
		return
	} else if err != nil {
		s.writeError(w, http.StatusInternalServerError, "get_pool_failed", err.Error())
		return
	}

	var req struct {
		NodeID string `json:"node_id"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.NodeID == "" {
		s.writeError(w, http.StatusBadRequest, "bad_request", "node_id is required")
		return
	}

	// Check if the node is already in a different pool.
	existing, err := s.reg.GetNodePool_ByNode(r.Context(), req.NodeID)
	if err != nil && !errors.Is(err, registry.ErrNotFound) {
		s.writeError(w, http.StatusInternalServerError, "check_node_pool_failed", err.Error())
		return
	}
	if err == nil && existing.ID != poolID {
		s.writeJSON(w, http.StatusConflict, map[string]any{
			"error":           "node_already_in_pool",
			"message":         "node is already assigned to a different pool; remove it first",
			"current_pool_id": existing.ID,
		})
		return
	}

	if err := s.reg.AddNodeToPool(r.Context(), req.NodeID, poolID); err != nil {
		s.writeError(w, http.StatusInternalServerError, "assign_node_failed", err.Error())
		return
	}
	_ = s.reg.AppendAudit(r.Context(), &registry.AuditEntry{
		Actor:  actorFromRequest(r),
		Action: "pool.node_assigned",
		Target: poolID,
	})
	s.writeJSON(w, http.StatusCreated, map[string]any{
		"pool_id": poolID,
		"node_id": req.NodeID,
		"message": "node assigned to pool",
	})
}

// handleListPoolNodes returns the node IDs assigned to a pool.
//
//	GET /api/v1/platform/pools/{id}/nodes
//	200 {"pool_id":"…","node_ids":[…]}
func (s *Server) handleListPoolNodes(w http.ResponseWriter, r *http.Request) {
	poolID := r.PathValue("id")
	if _, err := s.reg.GetNodePool(r.Context(), poolID); errors.Is(err, registry.ErrNotFound) {
		s.writeError(w, http.StatusNotFound, "not_found", "pool not found")
		return
	} else if err != nil {
		s.writeError(w, http.StatusInternalServerError, "get_pool_failed", err.Error())
		return
	}

	nodeIDs, err := s.reg.ListNodesInPool(r.Context(), poolID)
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, "list_pool_nodes_failed", err.Error())
		return
	}
	if nodeIDs == nil {
		nodeIDs = []string{}
	}
	s.writeJSON(w, http.StatusOK, map[string]any{
		"pool_id":  poolID,
		"node_ids": nodeIDs,
	})
}

// handleRemoveNodeFromPool removes a node from a pool. Returns 404 if the node
// is not in the specified pool.
//
//	DELETE /api/v1/platform/pools/{id}/nodes/{nodeId}
//	204 on success.
func (s *Server) handleRemoveNodeFromPool(w http.ResponseWriter, r *http.Request) {
	poolID := r.PathValue("id")
	nodeID := r.PathValue("nodeId")

	// Verify pool exists.
	if _, err := s.reg.GetNodePool(r.Context(), poolID); errors.Is(err, registry.ErrNotFound) {
		s.writeError(w, http.StatusNotFound, "not_found", "pool not found")
		return
	} else if err != nil {
		s.writeError(w, http.StatusInternalServerError, "get_pool_failed", err.Error())
		return
	}

	// Verify the node is in this specific pool.
	existingPool, err := s.reg.GetNodePool_ByNode(r.Context(), nodeID)
	if errors.Is(err, registry.ErrNotFound) {
		s.writeError(w, http.StatusNotFound, "not_found", "node is not assigned to any pool")
		return
	}
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, "check_node_pool_failed", err.Error())
		return
	}
	if existingPool.ID != poolID {
		s.writeError(w, http.StatusNotFound, "not_found", "node is not assigned to this pool")
		return
	}

	if err := s.reg.RemoveNodeFromPool(r.Context(), nodeID); err != nil {
		s.writeError(w, http.StatusInternalServerError, "remove_node_failed", err.Error())
		return
	}
	_ = s.reg.AppendAudit(r.Context(), &registry.AuditEntry{
		Actor:  actorFromRequest(r),
		Action: "pool.node_removed",
		Target: poolID,
	})
	w.WriteHeader(http.StatusNoContent)
}

// ─── Pool quotas ──────────────────────────────────────────────────────────────

// handleUpsertPoolQuota inserts or updates per-team quota on a shared pool.
//
//	PUT /api/v1/platform/pools/{id}/quotas/{teamId}
//	Body: {"max_deployments":3,"max_gpu_nodes":6,"priority":10}
//	200 PoolTeamQuota JSON.
func (s *Server) handleUpsertPoolQuota(w http.ResponseWriter, r *http.Request) {
	poolID := r.PathValue("id")
	teamID := r.PathValue("teamId")

	if _, err := s.reg.GetNodePool(r.Context(), poolID); errors.Is(err, registry.ErrNotFound) {
		s.writeError(w, http.StatusNotFound, "not_found", "pool not found")
		return
	} else if err != nil {
		s.writeError(w, http.StatusInternalServerError, "get_pool_failed", err.Error())
		return
	}

	var req struct {
		MaxDeployments int `json:"max_deployments"`
		MaxGPUNodes    int `json:"max_gpu_nodes"`
		Priority       int `json:"priority"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.writeError(w, http.StatusBadRequest, "bad_request", "invalid JSON body: "+err.Error())
		return
	}

	q := &registry.PoolTeamQuota{
		PoolID:         poolID,
		TeamID:         teamID,
		MaxDeployments: req.MaxDeployments,
		MaxGPUNodes:    req.MaxGPUNodes,
		Priority:       req.Priority,
	}
	if err := s.reg.UpsertPoolTeamQuota(r.Context(), q); err != nil {
		s.writeError(w, http.StatusInternalServerError, "upsert_quota_failed", err.Error())
		return
	}
	_ = s.reg.AppendAudit(r.Context(), &registry.AuditEntry{
		Actor:  actorFromRequest(r),
		Action: "pool.quota_upserted",
		Target: poolID,
	})
	s.writeJSON(w, http.StatusOK, q)
}

// handleListPoolQuotas returns all per-team quotas for a pool.
//
//	GET /api/v1/platform/pools/{id}/quotas
//	200 {"pool_id":"…","quotas":[…]}
func (s *Server) handleListPoolQuotas(w http.ResponseWriter, r *http.Request) {
	poolID := r.PathValue("id")
	if _, err := s.reg.GetNodePool(r.Context(), poolID); errors.Is(err, registry.ErrNotFound) {
		s.writeError(w, http.StatusNotFound, "not_found", "pool not found")
		return
	} else if err != nil {
		s.writeError(w, http.StatusInternalServerError, "get_pool_failed", err.Error())
		return
	}

	quotas, err := s.reg.ListPoolTeamQuotas(r.Context(), poolID)
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, "list_quotas_failed", err.Error())
		return
	}
	if quotas == nil {
		quotas = []*registry.PoolTeamQuota{}
	}
	s.writeJSON(w, http.StatusOK, map[string]any{
		"pool_id": poolID,
		"quotas":  quotas,
	})
}

// handleDeletePoolQuota removes the per-team quota from a pool.
//
//	DELETE /api/v1/platform/pools/{id}/quotas/{teamId}
//	204 on success.
func (s *Server) handleDeletePoolQuota(w http.ResponseWriter, r *http.Request) {
	poolID := r.PathValue("id")
	teamID := r.PathValue("teamId")

	if _, err := s.reg.GetNodePool(r.Context(), poolID); errors.Is(err, registry.ErrNotFound) {
		s.writeError(w, http.StatusNotFound, "not_found", "pool not found")
		return
	} else if err != nil {
		s.writeError(w, http.StatusInternalServerError, "get_pool_failed", err.Error())
		return
	}

	if err := s.reg.DeletePoolTeamQuota(r.Context(), poolID, teamID); errors.Is(err, registry.ErrNotFound) {
		s.writeError(w, http.StatusNotFound, "not_found", "quota not found")
		return
	} else if err != nil {
		s.writeError(w, http.StatusInternalServerError, "delete_quota_failed", err.Error())
		return
	}
	_ = s.reg.AppendAudit(r.Context(), &registry.AuditEntry{
		Actor:  actorFromRequest(r),
		Action: "pool.quota_deleted",
		Target: poolID,
	})
	w.WriteHeader(http.StatusNoContent)
}
