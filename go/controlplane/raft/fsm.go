// Package raftcp provides a Raft-consensus layer for the Purser control plane.
//
// It wraps a [registry.Registry] (backed by SQLiteRegistry on each node) and
// replicates every mutating operation through the Raft log so that all cluster
// members converge to the same state. Read operations are served locally
// without going through consensus (eventually-consistent reads; leader-lease
// reads are out of scope for v0.3 MVP).
//
// MVP limitations (v0.3):
//   - Snapshot restore is a no-op (full restore requires a process restart with
//     the backup DB file in place — see the operator guide).
//   - Only the most-used write paths are dispatched through the FSM; remaining
//     operations are stubs that return nil so the cluster stays consistent even
//     if a follower receives an unknown command type.
package raftcp

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"

	"github.com/hashicorp/raft"
	"github.com/purser/purser/go/controlplane/registry"
)

// CommandType identifies the Registry operation to apply to the local SQLite
// store. The iota values must never be reordered — they are serialised into the
// Raft log and must be stable across binary versions.
type CommandType uint8

const (
	CmdCreateModel            CommandType = iota // 0
	CmdDeleteModel                               // 1
	CmdCreateDeployment                          // 2
	CmdUpdateDeploymentState                     // 3
	CmdCreateAPIKey                              // 4
	CmdRevokeAPIKey                              // 5
	CmdRecordInferenceEvent                      // 6
	CmdUpsertPolicy                              // 7
	CmdDeletePolicy                              // 8
	CmdRequestApproval                           // 9
	CmdUpdateApprovalStatus                      // 10
	CmdCreateNode                                // 11
	CmdUpdateNode                                // 12
	CmdDeleteNode                                // 13
	CmdUpsertLink                                // 14
	CmdUpdateModel                               // 15
	CmdCreatePlan                                // 16
	CmdDeletePlan                                // 17
	CmdUpdateDeployment                          // 18
	CmdDeleteDeployment                          // 19
	CmdUpdateAPIKey                              // 20
	CmdDeleteAPIKey                              // 21
	CmdCreateCert                                // 22
	CmdUpdateCert                                // 23
	CmdDeleteCert                                // 24
	CmdAppendAudit                               // 25
	CmdRecordUsage                               // 26
)

// Command is the payload of a single Raft log entry.  Type selects the
// Registry method to call; Data holds the JSON-encoded argument(s).
type Command struct {
	Type CommandType     `json:"t"`
	Data json.RawMessage `json:"d"`
}

// updateDeploymentStateArgs is the payload for CmdUpdateDeploymentState, which
// maps to Registry.UpdateDeployment (the state field is part of the full
// Deployment struct).
type updateDeploymentStateArgs struct {
	ID    string `json:"id"`
	State string `json:"state"`
}

// recordUsageArgs is the payload for CmdRecordUsage.
type recordUsageArgs struct {
	APIKeyID     string `json:"api_key_id"`
	ModelID      string `json:"model_id"`
	InputTokens  int64  `json:"input_tokens"`
	OutputTokens int64  `json:"output_tokens"`
}

// revokeAPIKeyArgs is the payload for CmdRevokeAPIKey (updates Enabled=false).
type revokeAPIKeyArgs struct {
	ID string `json:"id"`
}

// PurserFSM applies Commands from the Raft log to a local Registry instance.
// It implements [raft.FSM].
type PurserFSM struct {
	reg registry.Registry
	log *slog.Logger
}

// NewFSM creates a PurserFSM backed by reg.  If logger is nil, slog.Default()
// is used.
func NewFSM(reg registry.Registry, logger *slog.Logger) *PurserFSM {
	if logger == nil {
		logger = slog.Default()
	}
	return &PurserFSM{reg: reg, log: logger}
}

// Apply is called by the Raft runtime whenever a log entry is committed. It
// must be deterministic: given the same log the same state must result on every
// node.
func (f *PurserFSM) Apply(l *raft.Log) interface{} {
	var cmd Command
	if err := json.Unmarshal(l.Data, &cmd); err != nil {
		f.log.Error("raft fsm: unmarshal command", "index", l.Index, "err", err)
		return err
	}
	ctx := context.Background()
	switch cmd.Type {
	// --- Nodes ---------------------------------------------------------------
	case CmdCreateNode:
		var n registry.Node
		if err := json.Unmarshal(cmd.Data, &n); err != nil {
			return err
		}
		return f.reg.CreateNode(ctx, &n)

	case CmdUpdateNode:
		var n registry.Node
		if err := json.Unmarshal(cmd.Data, &n); err != nil {
			return err
		}
		return f.reg.UpdateNode(ctx, &n)

	case CmdDeleteNode:
		var id string
		if err := json.Unmarshal(cmd.Data, &id); err != nil {
			return err
		}
		return f.reg.DeleteNode(ctx, id)

	case CmdUpsertLink:
		var l registry.Link
		if err := json.Unmarshal(cmd.Data, &l); err != nil {
			return err
		}
		return f.reg.UpsertLink(ctx, &l)

	// --- Models --------------------------------------------------------------
	case CmdCreateModel:
		var m registry.Model
		if err := json.Unmarshal(cmd.Data, &m); err != nil {
			return err
		}
		return f.reg.CreateModel(ctx, &m)

	case CmdUpdateModel:
		var m registry.Model
		if err := json.Unmarshal(cmd.Data, &m); err != nil {
			return err
		}
		return f.reg.UpdateModel(ctx, &m)

	case CmdDeleteModel:
		var id string
		if err := json.Unmarshal(cmd.Data, &id); err != nil {
			return err
		}
		return f.reg.DeleteModel(ctx, id)

	// --- Plans ---------------------------------------------------------------
	case CmdCreatePlan:
		var p registry.Plan
		if err := json.Unmarshal(cmd.Data, &p); err != nil {
			return err
		}
		return f.reg.CreatePlan(ctx, &p)

	case CmdDeletePlan:
		var id string
		if err := json.Unmarshal(cmd.Data, &id); err != nil {
			return err
		}
		return f.reg.DeletePlan(ctx, id)

	// --- Deployments ---------------------------------------------------------
	case CmdCreateDeployment:
		var d registry.Deployment
		if err := json.Unmarshal(cmd.Data, &d); err != nil {
			return err
		}
		return f.reg.CreateDeployment(ctx, &d)

	case CmdUpdateDeployment:
		var d registry.Deployment
		if err := json.Unmarshal(cmd.Data, &d); err != nil {
			return err
		}
		return f.reg.UpdateDeployment(ctx, &d)

	case CmdUpdateDeploymentState:
		var args updateDeploymentStateArgs
		if err := json.Unmarshal(cmd.Data, &args); err != nil {
			return err
		}
		d, err := f.reg.GetDeployment(ctx, args.ID)
		if err != nil {
			return err
		}
		d.State = args.State
		return f.reg.UpdateDeployment(ctx, d)

	case CmdDeleteDeployment:
		var id string
		if err := json.Unmarshal(cmd.Data, &id); err != nil {
			return err
		}
		return f.reg.DeleteDeployment(ctx, id)

	// --- API Keys ------------------------------------------------------------
	case CmdCreateAPIKey:
		var k registry.APIKey
		if err := json.Unmarshal(cmd.Data, &k); err != nil {
			return err
		}
		return f.reg.CreateAPIKey(ctx, &k)

	case CmdUpdateAPIKey:
		var k registry.APIKey
		if err := json.Unmarshal(cmd.Data, &k); err != nil {
			return err
		}
		return f.reg.UpdateAPIKey(ctx, &k)

	case CmdRevokeAPIKey:
		var args revokeAPIKeyArgs
		if err := json.Unmarshal(cmd.Data, &args); err != nil {
			return err
		}
		k, err := f.reg.GetAPIKey(ctx, args.ID)
		if err != nil {
			return err
		}
		k.Enabled = false
		return f.reg.UpdateAPIKey(ctx, k)

	case CmdDeleteAPIKey:
		var id string
		if err := json.Unmarshal(cmd.Data, &id); err != nil {
			return err
		}
		return f.reg.DeleteAPIKey(ctx, id)

	// --- Certs ---------------------------------------------------------------
	case CmdCreateCert:
		var c registry.Cert
		if err := json.Unmarshal(cmd.Data, &c); err != nil {
			return err
		}
		return f.reg.CreateCert(ctx, &c)

	case CmdUpdateCert:
		var c registry.Cert
		if err := json.Unmarshal(cmd.Data, &c); err != nil {
			return err
		}
		return f.reg.UpdateCert(ctx, &c)

	case CmdDeleteCert:
		var serial string
		if err := json.Unmarshal(cmd.Data, &serial); err != nil {
			return err
		}
		return f.reg.DeleteCert(ctx, serial)

	// --- Audit ---------------------------------------------------------------
	case CmdAppendAudit:
		var e registry.AuditEntry
		if err := json.Unmarshal(cmd.Data, &e); err != nil {
			return err
		}
		return f.reg.AppendAudit(ctx, &e)

	// --- Inference events (v0.3 stub) ----------------------------------------
	// registry.InferenceEvent and RecordInferenceEvent are not yet in the
	// registry.Registry interface; these command types are reserved for when
	// that surface lands. They are silently ignored for now so the log stays
	// forward-compatible — a future binary that handles them can replay them.
	case CmdRecordInferenceEvent:
		f.log.Debug("raft fsm: CmdRecordInferenceEvent not yet implemented — ignored", "index", l.Index)
		return nil

	// --- Policies (v0.3 stub) ------------------------------------------------
	// Policy CRUD is not yet in the registry.Registry interface.
	case CmdUpsertPolicy, CmdDeletePolicy:
		f.log.Debug("raft fsm: policy command not yet implemented — ignored", "type", cmd.Type, "index", l.Index)
		return nil

	// --- Approvals (v0.3 stub) -----------------------------------------------
	// Deployment approval workflow is not yet in the registry.Registry interface.
	case CmdRequestApproval, CmdUpdateApprovalStatus:
		f.log.Debug("raft fsm: approval command not yet implemented — ignored", "type", cmd.Type, "index", l.Index)
		return nil

	// --- Usage ---------------------------------------------------------------
	case CmdRecordUsage:
		var args recordUsageArgs
		if err := json.Unmarshal(cmd.Data, &args); err != nil {
			return err
		}
		return f.reg.RecordUsage(ctx, args.APIKeyID, args.ModelID, args.InputTokens, args.OutputTokens)

	default:
		// Unknown command types are silently ignored so a cluster can safely roll
		// a binary upgrade one node at a time without rejecting unknown entries.
		f.log.Warn("raft fsm: unknown command type — ignoring", "type", cmd.Type, "index", l.Index)
		return nil
	}
}

// Snapshot returns a lightweight snapshot handle. For the v0.3 MVP the
// snapshot body is empty — the authoritative state lives in the SQLite file
// and full state transfer requires copying the DB file out-of-band.
func (f *PurserFSM) Snapshot() (raft.FSMSnapshot, error) {
	return &fsmSnapshot{}, nil
}

// Restore is called by the Raft runtime when a snapshot is applied. For the
// v0.3 MVP the operation is a no-op: a full state restore requires restarting
// the node with the backup DB file (see the HA operator guide).  The incoming
// reader is drained and closed so Raft does not block.
func (f *PurserFSM) Restore(rc io.ReadCloser) error {
	defer rc.Close()
	f.log.Warn("raft fsm: snapshot restore is a no-op in v0.3 MVP — restart with backup DB to restore state")
	return nil
}

// fsmSnapshot is the empty snapshot implementation for the v0.3 MVP.
type fsmSnapshot struct{}

// Persist writes zero bytes to the sink and closes it.
func (s *fsmSnapshot) Persist(sink raft.SnapshotSink) error {
	return sink.Close()
}

// Release is a no-op for the empty snapshot.
func (s *fsmSnapshot) Release() {}

// MarshalCommand serialises cmd to JSON, ready to pass to raft.Raft.Apply.
func MarshalCommand(t CommandType, payload interface{}) ([]byte, error) {
	data, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	return json.Marshal(Command{Type: t, Data: json.RawMessage(data)})
}
