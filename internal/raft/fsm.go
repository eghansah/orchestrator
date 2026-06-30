package raft

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/netip"
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
	cmdApplyTemplate                 // add or update a workload template
	cmdRemoveTemplate                // remove a workload template
	cmdApplySecret                   // add or update a secret
	cmdRemoveSecret                  // remove a secret
	cmdSetOpenBaoConfig              // store OpenBao connection config
)

type command struct {
	Type cmdType         `json:"t"`
	Data json.RawMessage `json:"d"`
}

const (
	portPoolStart        uint32 = 30000
	portPoolEnd          uint32 = 32767
	svcPortPoolStart     uint32 = 40000
	svcPortPoolEnd       uint32 = 42767
	ingressPortPoolStart uint32 = 43000
	ingressPortPoolEnd   uint32 = 45767
)

// defaultMeshCIDR is the cluster-wide mesh address range from which the leader
// carves a /24 per node. 100.64.0.0/10 (RFC 6598, carrier-grade NAT) is chosen
// because it almost never collides with real LANs. Configurable per cluster via
// ClusterState.MeshCIDR. See docs/mesh-network.md.
const defaultMeshCIDR = "100.64.0.0/10"

// ClusterState is the desired-state view maintained by the FSM.
type ClusterState struct {
	Workloads       map[string]types.Workload    `json:"workloads"`
	Nodes           map[string]types.Node        `json:"nodes"`
	IngressRules    map[string]types.IngressRule `json:"ingress_rules"`
	Services        map[string]types.Service     `json:"services"`
	Domains         map[string]types.Domain      `json:"domains"`
	Users           map[string]types.User        `json:"users"`
	Registries      map[string]types.Registry         `json:"registries"`
	Templates       map[string]types.WorkloadTemplate `json:"templates"`
	Secrets         map[string]types.Secret           `json:"secrets"`
	OpenBaoConfig   *types.OpenBaoConfig              `json:"openbao_config,omitempty"`
	NextPort        uint32                            `json:"next_port"`         // container port pool
	NextServicePort uint32                            `json:"next_service_port"` // service port pool
	MeshCIDR        string                            `json:"mesh_cidr"`         // cluster mesh range; leader carves a /24 per node
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
		Templates:       make(map[string]types.WorkloadTemplate),
		Secrets:         make(map[string]types.Secret),
		MeshCIDR:        defaultMeshCIDR,
	}
}

// scanFreePort returns the lowest port in [start, end] not present in inUse,
// or 0 if the range is fully exhausted.
func scanFreePort(inUse map[uint32]bool, start, end uint32) uint32 {
	for p := start; p <= end; p++ {
		if !inUse[p] {
			return p
		}
	}
	return 0
}

// allocMeshSubnet returns the lowest /24 within cidr that is not present in
// inUse (keyed by canonical "a.b.c.0/24" string), together with that subnet's
// first host address (".1") to use as the node's mesh address. cidr must be an
// IPv4 prefix of /24 or larger. Returns an error if cidr is invalid or every
// /24 is taken.
func allocMeshSubnet(cidr string, inUse map[string]bool) (subnet, addr string, err error) {
	p, err := netip.ParsePrefix(cidr)
	if err != nil {
		return "", "", fmt.Errorf("parse mesh CIDR %q: %w", cidr, err)
	}
	p = p.Masked()
	if !p.Addr().Is4() {
		return "", "", fmt.Errorf("mesh CIDR %q must be IPv4", cidr)
	}
	if p.Bits() > 24 {
		return "", "", fmt.Errorf("mesh CIDR %q must be /24 or larger", cidr)
	}
	blocks := 1 << (24 - p.Bits())
	cur := p.Addr() // network address, already aligned to <=/24
	for range blocks {
		sn := netip.PrefixFrom(cur, 24).String()
		if !inUse[sn] {
			return sn, cur.Next().String(), nil
		}
		cur = nextSlash24(cur)
	}
	return "", "", fmt.Errorf("mesh CIDR %q exhausted: no free /24", cidr)
}

// nextSlash24 returns the address 256 higher than a (the base of the next /24).
func nextSlash24(a netip.Addr) netip.Addr {
	b := a.As4()
	v := uint32(b[0])<<24 | uint32(b[1])<<16 | uint32(b[2])<<8 | uint32(b[3])
	v += 256
	return netip.AddrFrom4([4]byte{byte(v >> 24), byte(v >> 16), byte(v >> 8), byte(v)})
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
		// Auto-assign host ports, scanning the full pool so freed ports are reused.
		if wl.Kind == types.KindContainer && wl.Container != nil {
			inUse := make(map[uint32]bool)
			for id, existing := range f.state.Workloads {
				if id == wl.ID {
					continue // exclude self (update case)
				}
				for _, pa := range existing.PortAllocations {
					inUse[pa.AllocatedPort] = true
				}
			}
			assigned := make(map[uint32]bool)
			for _, pa := range wl.PortAllocations {
				assigned[pa.ContainerPort] = true
				inUse[pa.AllocatedPort] = true
			}
			for _, pm := range wl.Container.Ports {
				if assigned[pm.ContainerPort] {
					continue
				}
				port := scanFreePort(inUse, portPoolStart, portPoolEnd)
				if port == 0 {
					return fmt.Errorf("container port pool exhausted")
				}
				inUse[port] = true
				wl.PortAllocations = append(wl.PortAllocations, types.PortAllocation{
					ContainerPort: pm.ContainerPort,
					AllocatedPort: port,
					Protocol:      pm.Protocol,
				})
			}
		}
		// For stacks, parse container ports from the compose YAML and auto-assign
		// AllocatedPorts from the pool — same logic as container workloads.
		// The agent rewrites the YAML at deploy time to bind each container port
		// to 127.0.0.1:AllocatedPort so nerdctl uses the orchestrator-managed ports.
		if wl.Kind == types.KindStack && wl.Stack != nil {
			inUse := make(map[uint32]bool)
			for id, existing := range f.state.Workloads {
				if id == wl.ID {
					continue
				}
				for _, pa := range existing.PortAllocations {
					inUse[pa.AllocatedPort] = true
				}
			}
			// Count how many allocations already exist per container port.
			existing := make(map[uint32]int)
			for _, pa := range wl.PortAllocations {
				existing[pa.ContainerPort]++
				inUse[pa.AllocatedPort] = true
			}
			// Allocate one host port per port declaration. Each occurrence of a
			// container port across different services gets its own loopback binding.
			seen := make(map[uint32]int)
			for _, cp := range parseComposeContainerPorts(wl.Stack.ComposeYAML) {
				seen[cp.ContainerPort]++
				if seen[cp.ContainerPort] <= existing[cp.ContainerPort] {
					continue // this occurrence already has an allocation
				}
				port := scanFreePort(inUse, portPoolStart, portPoolEnd)
				if port == 0 {
					return fmt.Errorf("container port pool exhausted")
				}
				inUse[port] = true
				wl.PortAllocations = append(wl.PortAllocations, types.PortAllocation{
					ContainerPort: cp.ContainerPort,
					AllocatedPort: port,
					Protocol:      cp.Protocol,
				})
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
		// Assign (or preserve) the leader-controlled mesh subnet. The subnet must
		// stay stable across re-registration, so an existing assignment always wins
		// over whatever the (re-)registering node sent. Node-provided fields
		// (MeshPubKey/MeshEndpoint) come from n and are kept as-is.
		if existing, ok := f.state.Nodes[n.ID]; ok && existing.MeshSubnet != "" {
			n.MeshSubnet = existing.MeshSubnet
			n.MeshAddr = existing.MeshAddr
		} else if n.MeshSubnet == "" {
			inUse := make(map[string]bool)
			for id, other := range f.state.Nodes {
				if id != n.ID && other.MeshSubnet != "" {
					inUse[other.MeshSubnet] = true
				}
			}
			cidr := f.state.MeshCIDR
			if cidr == "" {
				cidr = defaultMeshCIDR
			}
			subnet, addr, err := allocMeshSubnet(cidr, inUse)
			if err != nil {
				return fmt.Errorf("allocate mesh subnet for node %s: %w", n.ID, err)
			}
			n.MeshSubnet = subnet
			n.MeshAddr = addr
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
		slog.Info("fsm: applying ingress rule",
			"id", rule.ID, "fqdn", rule.ContainerFQDN, "container_port", rule.ContainerPort, "system_port", rule.SystemPort)
		// Auto-assign system port from the ingress pool on first creation.
		if rule.SystemPort == 0 {
			inUse := make(map[uint32]bool)
			for _, r := range f.state.IngressRules {
				if r.SystemPort > 0 {
					inUse[r.SystemPort] = true
				}
			}
			port := scanFreePort(inUse, ingressPortPoolStart, ingressPortPoolEnd)
			if port == 0 {
				return fmt.Errorf("ingress port pool exhausted")
			}
			rule.SystemPort = port
		}
		slog.Info("fsm: ingress rule committed", "id", rule.ID, "system_port", rule.SystemPort)
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
		slog.Info("fsm: applying service",
			"id", svc.ID, "name", svc.Name, "fqdn", svc.ContainerFQDN, "container_port", svc.ContainerPort, "system_port", svc.SystemPort)
		// Auto-assign system port on first creation (SystemPort == 0).
		if svc.SystemPort == 0 {
			inUse := make(map[uint32]bool)
			for _, existing := range f.state.Services {
				if existing.SystemPort > 0 {
					inUse[existing.SystemPort] = true
				}
			}
			port := scanFreePort(inUse, svcPortPoolStart, svcPortPoolEnd)
			if port == 0 {
				return fmt.Errorf("service port pool exhausted")
			}
			svc.SystemPort = port
		}
		slog.Info("fsm: service committed", "id", svc.ID, "name", svc.Name, "system_port", svc.SystemPort)
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

	case cmdApplyTemplate:
		var t types.WorkloadTemplate
		if err := json.Unmarshal(cmd.Data, &t); err != nil {
			return err
		}
		if f.state.Templates == nil {
			f.state.Templates = make(map[string]types.WorkloadTemplate)
		}
		for _, existing := range f.state.Templates {
			if existing.Name == t.Name && existing.ID != t.ID {
				return fmt.Errorf("template name %q already exists", t.Name)
			}
		}
		f.state.Templates[t.ID] = t

	case cmdRemoveTemplate:
		var id string
		if err := json.Unmarshal(cmd.Data, &id); err != nil {
			return err
		}
		delete(f.state.Templates, id)

	case cmdApplySecret:
		var s types.Secret
		if err := json.Unmarshal(cmd.Data, &s); err != nil {
			return err
		}
		if f.state.Secrets == nil {
			f.state.Secrets = make(map[string]types.Secret)
		}
		for _, existing := range f.state.Secrets {
			if existing.Name == s.Name && existing.ID != s.ID {
				return fmt.Errorf("secret name %q already exists", s.Name)
			}
		}
		f.state.Secrets[s.ID] = s

	case cmdRemoveSecret:
		var id string
		if err := json.Unmarshal(cmd.Data, &id); err != nil {
			return err
		}
		delete(f.state.Secrets, id)

	case cmdSetOpenBaoConfig:
		var cfg types.OpenBaoConfig
		if err := json.Unmarshal(cmd.Data, &cfg); err != nil {
			return err
		}
		f.state.OpenBaoConfig = &cfg
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
	return f.state
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
