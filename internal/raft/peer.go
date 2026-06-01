package raft

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"time"

	"github.com/hashicorp/raft"
	raftboltdb "github.com/hashicorp/raft-boltdb/v2"

	"github.com/eghansah/orchestrator/pkg/types"
)

const applyTimeout = 5 * time.Second

// Config holds the parameters needed to start a Raft peer.
type Config struct {
	NodeID    string
	BindAddr  string // host:port for Raft TCP transport (must be ≥1024 in usermode)
	DataDir   string // persistent storage: raft.db, snapshots/
	Bootstrap bool   // set true only for the very first node in a new cluster
}

// Peer wraps a hashicorp/raft instance and exposes the cluster's desired state.
type Peer struct {
	raft   *raft.Raft
	fsm    *fsm
	config Config
}

// NewPeer creates and starts a Raft peer. All operations are usermode-safe
// (TCP transport, boltdb storage, file-based snapshots — no root required).
func NewPeer(cfg Config) (*Peer, error) {
	if err := os.MkdirAll(cfg.DataDir, 0o700); err != nil {
		return nil, fmt.Errorf("create data dir: %w", err)
	}

	rc := raft.DefaultConfig()
	rc.LocalID = raft.ServerID(cfg.NodeID)

	boltPath := filepath.Join(cfg.DataDir, "raft.db")
	boltStore, err := raftboltdb.NewBoltStore(boltPath)
	if err != nil {
		return nil, fmt.Errorf("open bolt store: %w", err)
	}

	snapshotDir := filepath.Join(cfg.DataDir, "snapshots")
	snapStore, err := raft.NewFileSnapshotStore(snapshotDir, 3, os.Stderr)
	if err != nil {
		return nil, fmt.Errorf("open snapshot store: %w", err)
	}

	addr, err := net.ResolveTCPAddr("tcp", cfg.BindAddr)
	if err != nil {
		return nil, fmt.Errorf("resolve bind addr: %w", err)
	}
	transport, err := raft.NewTCPTransport(cfg.BindAddr, addr, 3, 10*time.Second, os.Stderr)
	if err != nil {
		return nil, fmt.Errorf("create TCP transport: %w", err)
	}

	f := newFSM()
	r, err := raft.NewRaft(rc, f, boltStore, boltStore, snapStore, transport)
	if err != nil {
		return nil, fmt.Errorf("create raft: %w", err)
	}

	if cfg.Bootstrap {
		configuration := raft.Configuration{
			Servers: []raft.Server{
				{ID: raft.ServerID(cfg.NodeID), Address: raft.ServerAddress(cfg.BindAddr)},
			},
		}
		if fut := r.BootstrapCluster(configuration); fut.Error() != nil {
			return nil, fmt.Errorf("bootstrap cluster: %w", fut.Error())
		}
	}

	return &Peer{raft: r, fsm: f, config: cfg}, nil
}

func (p *Peer) IsLeader() bool {
	return p.raft.State() == raft.Leader
}

// LeaderAddr returns the Raft transport address of the current leader.
func (p *Peer) LeaderAddr() string {
	addr, _ := p.raft.LeaderWithID()
	return string(addr)
}

// LeaderID returns the node ID of the current Raft leader.
func (p *Peer) LeaderID() string {
	_, id := p.raft.LeaderWithID()
	return string(id)
}

// State returns the current desired cluster state as seen by this node.
func (p *Peer) State() ClusterState {
	return p.fsm.State()
}

// AddVoter adds a new peer to the cluster. Must be called on the leader.
func (p *Peer) AddVoter(nodeID, addr string) error {
	f := p.raft.AddVoter(raft.ServerID(nodeID), raft.ServerAddress(addr), 0, applyTimeout)
	return f.Error()
}

// ApplyWorkload writes a workload into the Raft log.
func (p *Peer) ApplyWorkload(wl types.Workload) error {
	return p.apply(cmdApplyWorkload, wl)
}

// RemoveWorkload removes a workload from the Raft log.
func (p *Peer) RemoveWorkload(workloadID string) error {
	return p.apply(cmdRemoveWorkload, workloadID)
}

// RegisterNode adds or updates a node record in the Raft log.
func (p *Peer) RegisterNode(n types.Node) error {
	return p.apply(cmdRegisterNode, n)
}

// AssignWorkload records a scheduler placement decision in the Raft log.
func (p *Peer) AssignWorkload(workloadID, nodeID string) error {
	payload := struct {
		WorkloadID string `json:"wid"`
		NodeID     string `json:"nid"`
	}{WorkloadID: workloadID, NodeID: nodeID}
	return p.apply(cmdAssignWorkload, payload)
}

// ApplyIngress writes an ingress rule into the Raft log.
func (p *Peer) ApplyIngress(rule types.IngressRule) error {
	return p.apply(cmdApplyIngress, rule)
}

// RemoveIngress removes an ingress rule from the Raft log.
func (p *Peer) RemoveIngress(ruleID string) error {
	return p.apply(cmdRemoveIngress, ruleID)
}

// ApplyService writes a service into the Raft log.
func (p *Peer) ApplyService(svc types.Service) error {
	return p.apply(cmdApplyService, svc)
}

// RemoveService removes a service from the Raft log.
func (p *Peer) RemoveService(serviceID string) error {
	return p.apply(cmdRemoveService, serviceID)
}

// ApplyDomain writes a domain (TLS cert store entry) into the Raft log.
func (p *Peer) ApplyDomain(d types.Domain) error {
	return p.apply(cmdApplyDomain, d)
}

// RemoveDomain removes a domain from the Raft log.
func (p *Peer) RemoveDomain(domainID string) error {
	return p.apply(cmdRemoveDomain, domainID)
}

// ApplyUser writes a user account (AD allowlist entry) into the Raft log.
func (p *Peer) ApplyUser(u types.User) error {
	return p.apply(cmdApplyUser, u)
}

// RemoveUser removes a user account from the Raft log.
func (p *Peer) RemoveUser(userID string) error {
	return p.apply(cmdRemoveUser, userID)
}

func (p *Peer) apply(t cmdType, payload any) error {
	if !p.IsLeader() {
		return fmt.Errorf("not the leader")
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	cmd, err := json.Marshal(command{Type: t, Data: data})
	if err != nil {
		return err
	}
	return p.raft.Apply(cmd, applyTimeout).Error()
}
