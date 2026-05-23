package raft

import (
	"encoding/json"
	"fmt"
	"io"
	"sync"

	"github.com/hashicorp/raft"

	"github.com/eghansah/orchestrator/pkg/types"
)

type cmdType uint8

const (
	cmdApplyWorkload  cmdType = iota // add or update a workload in desired state
	cmdRemoveWorkload                // remove a workload
	cmdRegisterNode                  // node joined or updated
	cmdAssignWorkload                // scheduler assigned workload to a node
)

type command struct {
	Type cmdType         `json:"t"`
	Data json.RawMessage `json:"d"`
}

// ClusterState is the desired-state view maintained by the FSM.
type ClusterState struct {
	Workloads map[string]types.Workload `json:"workloads"`
	Nodes     map[string]types.Node     `json:"nodes"`
}

func newClusterState() ClusterState {
	return ClusterState{
		Workloads: make(map[string]types.Workload),
		Nodes:     make(map[string]types.Node),
	}
}

type fsm struct {
	mu    sync.RWMutex
	state ClusterState
}

func newFSM() *fsm {
	return &fsm{state: newClusterState()}
}

// Apply is called by the Raft library once a log entry is committed.
func (f *fsm) Apply(l *raft.Log) any {
	var cmd command
	if err := json.Unmarshal(l.Data, &cmd); err != nil {
		return fmt.Errorf("unmarshal command: %w", err)
	}

	f.mu.Lock()
	defer f.mu.Unlock()

	switch cmd.Type {
	case cmdApplyWorkload:
		var wl types.Workload
		if err := json.Unmarshal(cmd.Data, &wl); err != nil {
			return err
		}
		f.state.Workloads[wl.ID] = wl

	case cmdRemoveWorkload:
		var id string
		if err := json.Unmarshal(cmd.Data, &id); err != nil {
			return err
		}
		delete(f.state.Workloads, id)

	case cmdRegisterNode:
		var n types.Node
		if err := json.Unmarshal(cmd.Data, &n); err != nil {
			return err
		}
		f.state.Nodes[n.ID] = n

	case cmdAssignWorkload:
		var payload struct {
			WorkloadID string `json:"wid"`
			NodeID     string `json:"nid"`
		}
		if err := json.Unmarshal(cmd.Data, &payload); err != nil {
			return err
		}
		if wl, ok := f.state.Workloads[payload.WorkloadID]; ok {
			wl.NodeID = payload.NodeID
			wl.Phase = types.PhaseScheduled
			f.state.Workloads[payload.WorkloadID] = wl
		}
	}
	return nil
}

func (f *fsm) Snapshot() (raft.FSMSnapshot, error) {
	f.mu.RLock()
	defer f.mu.RUnlock()
	data, err := json.Marshal(f.state)
	if err != nil {
		return nil, err
	}
	return &fsmSnapshot{data: data}, nil
}

func (f *fsm) Restore(rc io.ReadCloser) error {
	defer rc.Close()
	var state ClusterState
	if err := json.NewDecoder(rc).Decode(&state); err != nil {
		return err
	}
	f.mu.Lock()
	f.state = state
	f.mu.Unlock()
	return nil
}

// State returns a shallow copy of the current cluster state.
func (f *fsm) State() ClusterState {
	f.mu.RLock()
	defer f.mu.RUnlock()
	return ClusterState{
		Workloads: f.state.Workloads,
		Nodes:     f.state.Nodes,
	}
}

type fsmSnapshot struct {
	data []byte
}

func (s *fsmSnapshot) Persist(sink raft.SnapshotSink) error {
	if _, err := sink.Write(s.data); err != nil {
		_ = sink.Cancel()
		return err
	}
	return sink.Close()
}

func (s *fsmSnapshot) Release() {}
