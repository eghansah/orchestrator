package types

import "time"

type WorkloadPhase int32

const (
	PhaseUnspecified WorkloadPhase = iota
	PhasePending
	PhaseScheduled
	PhaseRunning
	PhaseStopped
	PhaseFailed
)

func (p WorkloadPhase) String() string {
	switch p {
	case PhasePending:
		return "pending"
	case PhaseScheduled:
		return "scheduled"
	case PhaseRunning:
		return "running"
	case PhaseStopped:
		return "stopped"
	case PhaseFailed:
		return "failed"
	default:
		return "unknown"
	}
}

type WorkloadKind int

const (
	KindContainer WorkloadKind = iota
	KindStack
)

type ContainerSpec struct {
	Name      string
	Image     string
	Command   []string
	Env       []string // KEY=VALUE pairs
	Ports     []PortMapping
	Volumes   []VolumeMount
	Labels    map[string]string
	Namespace string // nerdctl namespace; defaults to "orchestrator"
}

type ComposeStackSpec struct {
	Name        string
	ComposeYAML string // inline compose file content
}

type PortMapping struct {
	HostPort      uint32
	ContainerPort uint32
	Protocol      string // "tcp" | "udp"
}

type VolumeMount struct {
	Source   string
	Target   string
	ReadOnly bool
}

// PortAllocation records the host port auto-assigned for one container port.
type PortAllocation struct {
	ContainerPort uint32
	AllocatedPort uint32 // bound on the node's dataIP; auto-assigned by the leader
	Protocol      string // "tcp" | "udp"
}

type Workload struct {
	ID              string
	Kind            WorkloadKind
	Container       *ContainerSpec    // non-nil when Kind == KindContainer
	Stack           *ComposeStackSpec // non-nil when Kind == KindStack
	Phase           WorkloadPhase
	NodeID          string // empty = unscheduled
	CreatedAt       time.Time
	PortAllocations []PortAllocation // auto-assigned host ports (containers only)
}

type NodeStatus int32

const (
	NodeStatusUnspecified NodeStatus = iota
	NodeHealthy
	NodeDraining
	NodeUnreachable
)

type NodeResources struct {
	CPUCores    uint32
	MemoryBytes uint64
	DiskBytes   uint64
}

type Node struct {
	ID         string
	Address    string // host:port for gRPC
	Status     NodeStatus
	Resources  NodeResources
	LastSeenAt time.Time
	TLSCert    []byte // DER-encoded self-signed cert for mTLS key pinning
	DataIP     string // routable IP for container traffic (ingress backend)
}

type IngressRule struct {
	ID         string
	Host       string // matched against Host header; empty = match all
	PathPrefix string // matched against URL path prefix; empty = "/"
	WorkloadID string
	Port       uint32    // host port on the target node
	CreatedAt  time.Time
}

// Service is a named TCP endpoint backed by a workload. The system auto-assigns
// a host port in the service pool (40000–42767). Containers resolve the service
// via DNS <name>.svc.local and connect on SystemPort.
type Service struct {
	ID         string
	Name       string    // short DNS label, e.g. "api"
	WorkloadID string
	TargetPort uint32    // container port to proxy to
	SystemPort uint32    // auto-assigned host port
	CreatedAt  time.Time
}

type ActualContainer struct {
	WorkloadID  string
	ContainerID string
	Name        string
	Status      string    // nerdctl status string (e.g. "Up 5 minutes", "Exited (1)")
	StartedAt   time.Time // zero if not running
}

type ActualStack struct {
	WorkloadID string
	Name       string
	Services   []ActualContainer
}

type ActualWorkloadState struct {
	NodeID     string
	Containers []ActualContainer
	Stacks     []ActualStack
	ReportedAt time.Time
}
