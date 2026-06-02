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
	cmdApplyIngress                  // add or update an ingress rule
	cmdRemoveIngress                 // remove an ingress rule
	cmdApplyService                  // add or update a service
	cmdRemoveService                 // remove a service
	cmdApplyDomain                   // add or update a domain (TLS cert store)
	cmdRemoveDomain                  // remove a domain
	cmdApplyUser                     // add or update a user account (AD allowlist entry)
	cmdRemoveUser                    // remove a user account
	cmdApplyRegistry                 // add or update a container registry
	cmdRemoveRegistry                // remove a container registry
)

type command struct {
	Type cmdType         `json:"t"`
	Data json.RawMessage `json:"d"`
}

const (
	portPoolStart    uint32 = 30000
	portPoolEnd      uint32 = 32767
	svcPortPoolStart uint32 = 40000
	svcPortPoolEnd   uint32 = 42767
)

// ClusterState is the desired-state view maintained by the FSM.
type ClusterState struct {
	Workloads       map[string]types.Workload    `json:"workloads"`
	Nodes           map[string]types.Node        `json:"nodes"`
	IngressRules    map[string]types.IngressRule `json:"ingress_rules"`
	Services        map[string]types.Service     `json:"services"`
	Domains         map[string]types.Domain      `json:"domains"`
	Users           map[string]types.User        `json:"users"`
	Registries      map[string]types.Registry    `json:"registries"`
	NextPort        uint32                       `json:"next_port"`         // container port pool
	NextServicePort uint32                       `json:"next_service_port"` // service port pool
}

func newClusterState() ClusterState {
	return ClusterState{
		Workloads:       make(map[string]types.Workload),
		Nodes:           make(map[string]types.Node),
		IngressRules:    make(map[string]types.IngressRule),
		Services:        make(map[string]types.Service),
		Domains:         make(map[string]types.Domain),
		Users:           make(map[string]types.User),
		Registries:      make(map[string]types.Registry),
		NextPort:        portPoolStart,
		NextServicePort: svcPortPoolStart,
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
		// Auto-assign host ports for any container port not yet allocated.
		if wl.Kind == types.KindContainer && wl.Container != nil {
			allocated := make(map[uint32]bool)
			for _, pa := range wl.PortAllocations {
				allocated[pa.ContainerPort] = true
			}
			for _, pm := range wl.Container.Ports {
				if allocated[pm.ContainerPort] {
					continue
				}
				if f.state.NextPort == 0 {
					f.state.NextPort = portPoolStart
				}
				if f.state.NextPort > portPoolEnd {
					f.state.NextPort = portPoolStart // wrap (shouldn't happen in practice)
				}
				wl.PortAllocations = append(wl.PortAllocations, types.PortAllocation{
					ContainerPort: pm.ContainerPort,
					AllocatedPort: f.state.NextPort,
					Protocol:      pm.Protocol,
				})
				f.state.NextPort++
			}
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

	case cmdApplyIngress:
		var rule types.IngressRule
		if err := json.Unmarshal(cmd.Data, &rule); err != nil {
			return err
		}
		if f.state.IngressRules == nil {
			f.state.IngressRules = make(map[string]types.IngressRule)
		}
		f.state.IngressRules[rule.ID] = rule

	case cmdRemoveIngress:
		var id string
		if err := json.Unmarshal(cmd.Data, &id); err != nil {
			return err
		}
		delete(f.state.IngressRules, id)

	case cmdApplyService:
		var svc types.Service
		if err := json.Unmarshal(cmd.Data, &svc); err != nil {
			return err
		}
		if f.state.Services == nil {
			f.state.Services = make(map[string]types.Service)
		}
		// Auto-assign system port on first creation (SystemPort == 0).
		if svc.SystemPort == 0 {
			if f.state.NextServicePort == 0 {
				f.state.NextServicePort = svcPortPoolStart
			}
			if f.state.NextServicePort > svcPortPoolEnd {
				f.state.NextServicePort = svcPortPoolStart
			}
			svc.SystemPort = f.state.NextServicePort
			f.state.NextServicePort++
		}
		f.state.Services[svc.ID] = svc

	case cmdRemoveService:
		var id string
		if err := json.Unmarshal(cmd.Data, &id); err != nil {
			return err
		}
		delete(f.state.Services, id)

	case cmdApplyDomain:
		var d types.Domain
		if err := json.Unmarshal(cmd.Data, &d); err != nil {
			return err
		}
		if f.state.Domains == nil {
			f.state.Domains = make(map[string]types.Domain)
		}
		for _, existing := range f.state.Domains {
			if existing.Name == d.Name && existing.ID != d.ID {
				return fmt.Errorf("domain name %q already exists", d.Name)
			}
		}
		f.state.Domains[d.ID] = d

	case cmdRemoveDomain:
		var id string
		if err := json.Unmarshal(cmd.Data, &id); err != nil {
			return err
		}
		delete(f.state.Domains, id)

	case cmdApplyUser:
		var u types.User
		if err := json.Unmarshal(cmd.Data, &u); err != nil {
			return err
		}
		if f.state.Users == nil {
			f.state.Users = make(map[string]types.User)
		}
		for _, existing := range f.state.Users {
			if existing.Username == u.Username && existing.ID != u.ID {
				return fmt.Errorf("username %q already exists", u.Username)
			}
		}
		f.state.Users[u.ID] = u

	case cmdRemoveUser:
		var id string
		if err := json.Unmarshal(cmd.Data, &id); err != nil {
			return err
		}
		delete(f.state.Users, id)

	case cmdApplyRegistry:
		var r types.Registry
		if err := json.Unmarshal(cmd.Data, &r); err != nil {
			return err
		}
		if f.state.Registries == nil {
			f.state.Registries = make(map[string]types.Registry)
		}
		for _, existing := range f.state.Registries {
			if existing.Name == r.Name && existing.ID != r.ID {
				return fmt.Errorf("registry name %q already exists", r.Name)
			}
		}
		f.state.Registries[r.ID] = r

	case cmdRemoveRegistry:
		var id string
		if err := json.Unmarshal(cmd.Data, &id); err != nil {
			return err
		}
		delete(f.state.Registries, id)
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
		Workloads:       f.state.Workloads,
		Nodes:           f.state.Nodes,
		IngressRules:    f.state.IngressRules,
		Services:        f.state.Services,
		Domains:         f.state.Domains,
		Users:           f.state.Users,
		NextPort:        f.state.NextPort,
		NextServicePort: f.state.NextServicePort,
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
