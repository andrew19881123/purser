package raftcp

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"time"

	"github.com/hashicorp/raft"
	raftboltdb "github.com/hashicorp/raft-boltdb/v2"
)

// Config holds the runtime configuration for a Raft node. All fields may be
// populated from environment variables by [ConfigFromEnv].
type Config struct {
	// NodeID must be unique across the cluster — typically the machine hostname
	// or a UUID persisted between restarts.
	NodeID string

	// BindAddr is the "host:port" address on which the Raft TCP transport
	// listens.  Other cluster members must be able to reach this address.
	// Default: ":7000".
	BindAddr string

	// DataDir is the directory used to store Raft state (BoltDB log store,
	// BoltDB stable store, and file-based snapshot store). It must be distinct
	// from the SQLite DB directory.  Default: same directory as the SQLite DB
	// with a "raft-data" subdirectory.
	DataDir string

	// Bootstrap must be true on the very first node of a new cluster.  All
	// subsequent nodes (or restart of an existing node) must set this to false —
	// bootstrapping a running cluster corrupts it.
	Bootstrap bool

	// JoinAddrs is the list of existing cluster member addresses to contact when
	// joining an existing cluster (not used when Bootstrap is true).
	JoinAddrs []string
}

// Node wraps a [raft.Raft] instance and exposes the small surface that the
// control-plane server needs: leader check, leader address, and cluster stats.
type Node struct {
	r      *raft.Raft
	config Config
}

// NewNode creates and starts a Raft node with the given FSM.  It creates the
// DataDir if it does not already exist.
func NewNode(cfg Config, fsm raft.FSM) (*Node, error) {
	if err := os.MkdirAll(cfg.DataDir, 0o700); err != nil {
		return nil, fmt.Errorf("raft: create data dir %s: %w", cfg.DataDir, err)
	}

	raftCfg := raft.DefaultConfig()
	raftCfg.LocalID = raft.ServerID(cfg.NodeID)
	raftCfg.HeartbeatTimeout = 500 * time.Millisecond
	raftCfg.ElectionTimeout = 500 * time.Millisecond
	raftCfg.CommitTimeout = 50 * time.Millisecond

	// TCP transport — peers communicate over this address.
	addr, err := net.ResolveTCPAddr("tcp", cfg.BindAddr)
	if err != nil {
		return nil, fmt.Errorf("raft: resolve bind addr %s: %w", cfg.BindAddr, err)
	}
	transport, err := raft.NewTCPTransport(cfg.BindAddr, addr, 3, 10*time.Second, os.Stderr)
	if err != nil {
		return nil, fmt.Errorf("raft: create TCP transport: %w", err)
	}

	// Persistent BoltDB log store.
	logStore, err := raftboltdb.NewBoltStore(filepath.Join(cfg.DataDir, "raft-log.bolt"))
	if err != nil {
		return nil, fmt.Errorf("raft: create log store: %w", err)
	}

	// Persistent BoltDB stable store (node metadata, current term, voted-for).
	stableStore, err := raftboltdb.NewBoltStore(filepath.Join(cfg.DataDir, "raft-stable.bolt"))
	if err != nil {
		return nil, fmt.Errorf("raft: create stable store: %w", err)
	}

	// File-based snapshot store (retain last 2 snapshots).
	snapshotStore, err := raft.NewFileSnapshotStore(cfg.DataDir, 2, os.Stderr)
	if err != nil {
		return nil, fmt.Errorf("raft: create snapshot store: %w", err)
	}

	r, err := raft.NewRaft(raftCfg, fsm, logStore, stableStore, snapshotStore, transport)
	if err != nil {
		return nil, fmt.Errorf("raft: create raft: %w", err)
	}

	if cfg.Bootstrap {
		configuration := raft.Configuration{
			Servers: []raft.Server{
				{
					ID:      raft.ServerID(cfg.NodeID),
					Address: raft.ServerAddress(cfg.BindAddr),
				},
			},
		}
		future := r.BootstrapCluster(configuration)
		if err := future.Error(); err != nil && err != raft.ErrCantBootstrap {
			return nil, fmt.Errorf("raft: bootstrap cluster: %w", err)
		}
	}

	return &Node{r: r, config: cfg}, nil
}

// NewInmemNode creates a Raft node using an in-memory transport — useful for
// unit tests that must not bind a real TCP port.
func NewInmemNode(nodeID string, fsm raft.FSM) (*Node, *raft.InmemTransport, error) {
	cfg := raft.DefaultConfig()
	cfg.LocalID = raft.ServerID(nodeID)
	cfg.HeartbeatTimeout = 50 * time.Millisecond
	cfg.ElectionTimeout = 50 * time.Millisecond
	cfg.LeaderLeaseTimeout = 40 * time.Millisecond // must be < HeartbeatTimeout
	cfg.CommitTimeout = 5 * time.Millisecond
	// Speed up leader election in tests by shrinking the snapshot interval.
	cfg.SnapshotThreshold = 2048
	cfg.TrailingLogs = 1024

	addr, transport := raft.NewInmemTransport(raft.ServerAddress(nodeID))
	_ = addr

	logStore := raft.NewInmemStore()
	stableStore := raft.NewInmemStore()
	snapshotStore := raft.NewInmemSnapshotStore()

	r, err := raft.NewRaft(cfg, fsm, logStore, stableStore, snapshotStore, transport)
	if err != nil {
		return nil, nil, fmt.Errorf("raft inmem: create raft: %w", err)
	}

	// Bootstrap as a single-node cluster.
	configuration := raft.Configuration{
		Servers: []raft.Server{
			{
				Suffrage: raft.Voter,
				ID:       raft.ServerID(nodeID),
				Address:  raft.ServerAddress(nodeID),
			},
		},
	}
	future := r.BootstrapCluster(configuration)
	if err := future.Error(); err != nil {
		return nil, nil, fmt.Errorf("raft inmem: bootstrap: %w", err)
	}

	return &Node{r: r, config: Config{NodeID: nodeID, Bootstrap: true}}, transport, nil
}

// IsLeader returns true when this node is the current Raft leader.
func (n *Node) IsLeader() bool {
	return n.r.State() == raft.Leader
}

// LeaderAddr returns the network address of the current cluster leader.  It
// returns an empty string when the leader is unknown (e.g. during an election).
func (n *Node) LeaderAddr() string {
	addr, _ := n.r.LeaderWithID()
	return string(addr)
}

// Stats returns a map of Raft cluster statistics (term, commit index, last
// log, fsm_pending, etc.) as reported by hashicorp/raft.  The map is safe to
// JSON-encode directly.
func (n *Node) Stats() map[string]string {
	return n.r.Stats()
}

// State returns the string representation of this node's Raft state
// ("Follower", "Candidate", "Leader", "Shutdown").
func (n *Node) State() string {
	return n.r.State().String()
}

// Shutdown stops the Raft node gracefully.
func (n *Node) Shutdown() error {
	future := n.r.Shutdown()
	return future.Error()
}

// Raft exposes the underlying [raft.Raft] for callers that need direct access
// (e.g. for AddVoter / RemoveServer cluster membership operations).
func (n *Node) Raft() *raft.Raft {
	return n.r
}
