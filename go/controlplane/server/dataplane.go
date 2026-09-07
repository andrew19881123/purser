// dataplane.go — REST handlers for the DataPlane entity (v0.5 CP/DP
// architectural separation).
//
// Endpoints:
//
//	POST   /api/v1/platform/dataplanes                         — register DP, returns join token
//	GET    /api/v1/platform/dataplanes                         — list DPs
//	GET    /api/v1/platform/dataplanes/{id}                    — get DP
//	PUT    /api/v1/platform/dataplanes/{id}                    — update DP
//	DELETE /api/v1/platform/dataplanes/{id}                    — delete DP
//
//	POST   /api/v1/platform/dataplanes/{id}/heartbeat          — DP reports health
//	GET    /api/v1/platform/dataplanes/{id}/config             — DP pulls config snapshot
//	PUT    /api/v1/platform/dataplanes/{id}/config             — operator pushes config snapshot
//
//	POST   /api/v1/platform/dataplanes/{id}/nodes/{nodeId}     — assign node to DP
//	DELETE /api/v1/platform/dataplanes/{id}/nodes/{nodeId}     — unassign node from DP
//	GET    /api/v1/platform/dataplanes/{id}/nodes              — list nodes in DP
package server

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/purser/purser/go/controlplane/registry"
)

// ─── Create / List / Get / Update / Delete ────────────────────────────────────

// handleCreateDataPlane registers a new Data Plane.
//
//	POST /api/v1/platform/dataplanes
//	Body: {"name":"…","description":"…","tier":"production","gateway_url":"https://…"}
//	201: {"dataplane": <DataPlane>, "join_token": "dp_…"}  (token shown once)
func (s *Server) handleCreateDataPlane(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name        string `json:"name"`
		Description string `json:"description"`
		Tier        string `json:"tier"`
		GatewayURL  string `json:"gateway_url"`
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

	dp := &registry.DataPlane{
		Name:        req.Name,
		Description: req.Description,
		Tier:        req.Tier,
		GatewayURL:  req.GatewayURL,
	}
	joinToken, err := s.reg.CreateDataPlane(r.Context(), dp)
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, "create_dataplane_failed", err.Error())
		return
	}
	_ = s.reg.AppendAudit(r.Context(), &registry.AuditEntry{
		Actor:  actorFromRequest(r),
		Action: "dataplane.created",
		Target: dp.ID,
	})
	s.writeJSON(w, http.StatusCreated, map[string]any{
		"dataplane":  dp,
		"join_token": joinToken,
	})
}

// handleListDataPlanes returns all registered data planes.
//
//	GET /api/v1/platform/dataplanes
//	200: {"dataplanes": […]}
func (s *Server) handleListDataPlanes(w http.ResponseWriter, r *http.Request) {
	dps, err := s.reg.ListDataPlanes(r.Context())
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, "list_dataplanes_failed", err.Error())
		return
	}
	if dps == nil {
		dps = []*registry.DataPlane{}
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"dataplanes": dps})
}

// handleGetDataPlane returns one data plane by id.
//
//	GET /api/v1/platform/dataplanes/{id}
//	200: <DataPlane JSON>
func (s *Server) handleGetDataPlane(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	dp, err := s.reg.GetDataPlane(r.Context(), id)
	if errors.Is(err, registry.ErrNotFound) {
		s.writeError(w, http.StatusNotFound, "not_found", "data plane not found")
		return
	}
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, "get_dataplane_failed", err.Error())
		return
	}
	s.writeJSON(w, http.StatusOK, dp)
}

// handleUpdateDataPlane replaces the mutable fields of a data plane.
//
//	PUT /api/v1/platform/dataplanes/{id}
//	Body: {"name":"…","description":"…","tier":"…","gateway_url":"…","status":"…"}
//	200: <DataPlane JSON>
func (s *Server) handleUpdateDataPlane(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	dp, err := s.reg.GetDataPlane(r.Context(), id)
	if errors.Is(err, registry.ErrNotFound) {
		s.writeError(w, http.StatusNotFound, "not_found", "data plane not found")
		return
	}
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, "get_dataplane_failed", err.Error())
		return
	}

	var req struct {
		Name        *string `json:"name"`
		Description *string `json:"description"`
		Tier        *string `json:"tier"`
		GatewayURL  *string `json:"gateway_url"`
		Status      *string `json:"status"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.writeError(w, http.StatusBadRequest, "bad_request", "invalid JSON body: "+err.Error())
		return
	}
	if req.Name != nil {
		dp.Name = *req.Name
	}
	if req.Description != nil {
		dp.Description = *req.Description
	}
	if req.Tier != nil {
		dp.Tier = *req.Tier
	}
	if req.GatewayURL != nil {
		dp.GatewayURL = *req.GatewayURL
	}
	if req.Status != nil {
		dp.Status = *req.Status
	}

	if err := s.reg.UpdateDataPlane(r.Context(), dp); err != nil {
		s.writeError(w, http.StatusInternalServerError, "update_dataplane_failed", err.Error())
		return
	}
	_ = s.reg.AppendAudit(r.Context(), &registry.AuditEntry{
		Actor:  actorFromRequest(r),
		Action: "dataplane.updated",
		Target: dp.ID,
	})
	s.writeJSON(w, http.StatusOK, dp)
}

// handleDeleteDataPlane removes a data plane by id.
//
//	DELETE /api/v1/platform/dataplanes/{id}
//	204 on success.
func (s *Server) handleDeleteDataPlane(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := s.reg.DeleteDataPlane(r.Context(), id); errors.Is(err, registry.ErrNotFound) {
		s.writeError(w, http.StatusNotFound, "not_found", "data plane not found")
		return
	} else if err != nil {
		s.writeError(w, http.StatusInternalServerError, "delete_dataplane_failed", err.Error())
		return
	}
	_ = s.reg.AppendAudit(r.Context(), &registry.AuditEntry{
		Actor:  actorFromRequest(r),
		Action: "dataplane.deleted",
		Target: id,
	})
	w.WriteHeader(http.StatusNoContent)
}

// ─── Heartbeat ────────────────────────────────────────────────────────────────

// handleDataPlaneHeartbeat processes a health report from a DP gateway.
//
//	POST /api/v1/platform/dataplanes/{id}/heartbeat
//	Body: {"status":"active","node_count":3,"active_models":["llama3-8b"]}
//	204 on success.
func (s *Server) handleDataPlaneHeartbeat(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var req struct {
		Status       string   `json:"status"`
		NodeCount    int      `json:"node_count"`
		ActiveModels []string `json:"active_models"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, 1<<16)
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.writeError(w, http.StatusBadRequest, "bad_request", "invalid JSON body: "+err.Error())
		return
	}
	if req.Status == "" {
		req.Status = "active"
	}
	hb := &registry.DataPlaneHeartbeat{
		DataPlaneID:  id,
		Status:       req.Status,
		NodeCount:    req.NodeCount,
		ActiveModels: req.ActiveModels,
	}
	if err := s.reg.RecordDataPlaneHeartbeat(r.Context(), hb); errors.Is(err, registry.ErrNotFound) {
		s.writeError(w, http.StatusNotFound, "not_found", "data plane not found")
		return
	} else if err != nil {
		s.writeError(w, http.StatusInternalServerError, "heartbeat_failed", err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ─── Config snapshot ──────────────────────────────────────────────────────────

// handleGetDataPlaneConfig returns the current config snapshot for a DP.
//
//	GET /api/v1/platform/dataplanes/{id}/config
//	Supports Bearer token auth from the DP gateway itself (ValidateDataPlaneToken).
//	200: <DataPlaneConfigSnapshot JSON>
func (s *Server) handleGetDataPlaneConfig(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	snap, err := s.reg.GetDataPlaneConfigSnapshot(r.Context(), id)
	if errors.Is(err, registry.ErrNotFound) {
		s.writeError(w, http.StatusNotFound, "not_found", "data plane not found")
		return
	}
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, "get_config_failed", err.Error())
		return
	}
	s.writeJSON(w, http.StatusOK, snap)
}

// handlePutDataPlaneConfig replaces the config snapshot for a DP.
//
//	PUT /api/v1/platform/dataplanes/{id}/config
//	Body: {"routing_table":{…},"auth_bundle":{…},"policy_bundle":[…]}
//	200: <DataPlaneConfigSnapshot JSON>
func (s *Server) handlePutDataPlaneConfig(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	// Verify the DP exists.
	if _, err := s.reg.GetDataPlane(r.Context(), id); errors.Is(err, registry.ErrNotFound) {
		s.writeError(w, http.StatusNotFound, "not_found", "data plane not found")
		return
	} else if err != nil {
		s.writeError(w, http.StatusInternalServerError, "get_dataplane_failed", err.Error())
		return
	}

	var snap registry.DataPlaneConfigSnapshot
	r.Body = http.MaxBytesReader(w, r.Body, 4<<20)
	if err := json.NewDecoder(r.Body).Decode(&snap); err != nil {
		s.writeError(w, http.StatusBadRequest, "bad_request", "invalid JSON body: "+err.Error())
		return
	}
	if err := s.reg.UpdateDataPlaneConfigSnapshot(r.Context(), id, &snap); err != nil {
		s.writeError(w, http.StatusInternalServerError, "update_config_failed", err.Error())
		return
	}
	_ = s.reg.AppendAudit(r.Context(), &registry.AuditEntry{
		Actor:  actorFromRequest(r),
		Action: "dataplane.config_updated",
		Target: id,
	})
	s.writeJSON(w, http.StatusOK, snap)
}

// ─── Node assignment ──────────────────────────────────────────────────────────

// handleAssignNodeToDataPlane assigns a fleet node to this data plane.
//
//	POST /api/v1/platform/dataplanes/{id}/nodes/{nodeId}
//	204 on success.
func (s *Server) handleAssignNodeToDataPlane(w http.ResponseWriter, r *http.Request) {
	dpID := r.PathValue("id")
	nodeID := r.PathValue("nodeId")

	// Verify the DP exists.
	if _, err := s.reg.GetDataPlane(r.Context(), dpID); errors.Is(err, registry.ErrNotFound) {
		s.writeError(w, http.StatusNotFound, "not_found", "data plane not found")
		return
	} else if err != nil {
		s.writeError(w, http.StatusInternalServerError, "get_dataplane_failed", err.Error())
		return
	}

	if err := s.reg.AssignNodeToDataPlane(r.Context(), nodeID, dpID); errors.Is(err, registry.ErrNotFound) {
		s.writeError(w, http.StatusNotFound, "not_found", "node not found")
		return
	} else if err != nil {
		s.writeError(w, http.StatusInternalServerError, "assign_node_failed", err.Error())
		return
	}
	_ = s.reg.AppendAudit(r.Context(), &registry.AuditEntry{
		Actor:  actorFromRequest(r),
		Action: "dataplane.node_assigned",
		Target: dpID + "/" + nodeID,
	})
	w.WriteHeader(http.StatusNoContent)
}

// handleUnassignNodeFromDataPlane removes a node from this data plane.
//
//	DELETE /api/v1/platform/dataplanes/{id}/nodes/{nodeId}
//	204 on success.
func (s *Server) handleUnassignNodeFromDataPlane(w http.ResponseWriter, r *http.Request) {
	dpID := r.PathValue("id")
	nodeID := r.PathValue("nodeId")

	if err := s.reg.AssignNodeToDataPlane(r.Context(), nodeID, ""); errors.Is(err, registry.ErrNotFound) {
		s.writeError(w, http.StatusNotFound, "not_found", "node not found")
		return
	} else if err != nil {
		s.writeError(w, http.StatusInternalServerError, "unassign_node_failed", err.Error())
		return
	}
	_ = s.reg.AppendAudit(r.Context(), &registry.AuditEntry{
		Actor:  actorFromRequest(r),
		Action: "dataplane.node_unassigned",
		Target: dpID + "/" + nodeID,
	})
	w.WriteHeader(http.StatusNoContent)
}

// handleListDataPlaneNodes returns nodes belonging to this data plane.
//
//	GET /api/v1/platform/dataplanes/{id}/nodes
//	200: {"nodes":[…]}
func (s *Server) handleListDataPlaneNodes(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	// Verify the DP exists.
	if _, err := s.reg.GetDataPlane(r.Context(), id); errors.Is(err, registry.ErrNotFound) {
		s.writeError(w, http.StatusNotFound, "not_found", "data plane not found")
		return
	} else if err != nil {
		s.writeError(w, http.StatusInternalServerError, "get_dataplane_failed", err.Error())
		return
	}

	nodes, err := s.reg.ListNodesByDataPlane(r.Context(), id)
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, "list_nodes_failed", err.Error())
		return
	}
	if nodes == nil {
		nodes = []*registry.Node{}
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"nodes": nodes})
}
