package server

import (
	"net/http"

	raftcp "github.com/purser/purser/go/controlplane/raft"
)

// RaftNode is the small surface of *raftcp.Node that the server needs.
// Declared as an interface here so tests can supply a stub without starting a
// real Raft node.
type RaftNode interface {
	IsLeader() bool
	LeaderAddr() string
	Stats() map[string]string
	State() string
}

// handleClusterStatus returns the Raft cluster membership status.
//
// In standalone (single-node) mode — i.e. when no Raft node is configured —
// the response indicates that this process is always the leader:
//
//	{"mode":"standalone","is_leader":true}
//
// In Raft mode the response includes the Raft state, the leader address, and
// the full stats map returned by hashicorp/raft:
//
//	{"mode":"raft","is_leader":true,"leader":"10.0.0.1:7000","state":"Leader","stats":{...}}
//
// This endpoint is accessible without authentication so that load-balancers and
// orchestrators (e.g. Kubernetes liveness probes) can determine which replica
// is the active leader without needing an API key.
func (s *Server) handleClusterStatus(w http.ResponseWriter, r *http.Request) {
	if s.raftNode == nil {
		s.writeJSON(w, http.StatusOK, map[string]interface{}{
			"mode":      "standalone",
			"is_leader": true,
		})
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]interface{}{
		"mode":      "raft",
		"is_leader": s.raftNode.IsLeader(),
		"leader":    s.raftNode.LeaderAddr(),
		"state":     s.raftNode.State(),
		"stats":     s.raftNode.Stats(),
	})
}

// compile-time assertion: *raftcp.Node satisfies RaftNode.
var _ RaftNode = (*raftcp.Node)(nil)
