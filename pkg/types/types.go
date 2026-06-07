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
	Name       string
	Image      string
	Command    []string
	Env        []string          // KEY=VALUE pairs
	Ports      []PortMapping
	Volumes    []VolumeMount
	Labels     map[string]string
	Namespace  string            // nerdctl namespace; defaults to "orchestrator"
	SecretRefs map[string]string // env_var_name → secret_name; resolved at placement
}

type ComposeStackSpec struct {
	Name        string
	ComposeYAML string            // inline compose file content
	SecretRefs  map[string]string // env_var_name → secret_name; resolved at placement
	ResolvedEnv []string          // KEY=VALUE pairs injected from secrets at placement; not persisted in Raft
}

type PortMapping struct {
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

// Name returns the stable workload name from its spec.
func (w Workload) Name() string {
	if w.Container != nil {
		return w.Container.Name
	}
	if w.Stack != nil {
		return w.Stack.Name
	}
	return ""
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

// IngressRule is an HTTP/HTTPS routing rule. ContainerFQDN identifies the target
// container (e.g. "ecouniversal" or "backend.myapp") and ContainerPort is the
// port it listens on. SystemPort is auto-assigned from the ingress pool (43000–45767)
// and used by proxyd and ingressd to route traffic — no TCP Service required.
type IngressRule struct {
	ID            string
	DomainID      string // required; host is derived from the linked Domain
	Host          string // matched against Host header; empty = match all
	PathPrefix    string // matched against URL path prefix; empty = "/"
	ContainerFQDN string // target container: "workload" or "service.workload"
	ContainerPort uint32 // port the container listens on
	SystemPort    uint32 // auto-assigned from ingress port pool (43000–45767)
	CreatedAt     time.Time
}

// Service is a named TCP endpoint. ContainerFQDN identifies the target container
// (e.g. "ecouniversal" or "backend.myapp") and ContainerPort is the port it listens
// on. SystemPort is auto-assigned from the service pool (40000–42767); proxyd
// listens on nodeDataIP:SystemPort and forwards to the container.
type Service struct {
	ID            string
	Name          string    // short DNS label, e.g. "api"
	ContainerFQDN string    // target container: "workload" or "service.workload"
	ContainerPort uint32    // port the container listens on
	SystemPort    uint32    // auto-assigned host port
	CreatedAt     time.Time
}

// Domain is a named TLS-enabled virtual host. The ingress uses the stored
// certificate to terminate HTTPS connections whose SNI matches Name.
type Domain struct {
	ID        string
	Name      string    // unique hostname, e.g. "api.example.com"
	TLSCert   string    // PEM-encoded certificate
	TLSKey    string    // PEM-encoded private key
	CSR       string    // PEM-encoded certificate signing request; empty on old records
	Enabled   bool      // when false the ingress ignores this domain's cert
	CreatedAt time.Time
}

// Registry is a configured Docker Registry v2 endpoint.
type Registry struct {
	ID        string
	Name      string // display name, e.g. "internal-harbor"
	URL       string // "https://registry.example.com" (no trailing slash)
	Username  string // empty for unauthenticated registries
	Password  string // stored in Raft, same pattern as Domain.TLSKey
	CreatedAt time.Time
}

// Secret is a named encrypted value stored in cluster state. The EncryptedValue
// field holds AES-256-GCM ciphertext; the plaintext is only materialised on the
// leader at placement time and injected into the container's environment.
type Secret struct {
	ID             string
	Name           string    // unique cluster-wide label
	EncryptedValue []byte    // AES-256-GCM ciphertext produced by pkg/crypto
	CreatedAt      time.Time
}

// WorkloadTemplate is a saved workload definition that can be redeployed.
type WorkloadTemplate struct {
	ID          string
	Name        string
	Description string
	Kind        WorkloadKind
	Container   *ContainerSpec
	Stack       *ComposeStackSpec
	CreatedAt   time.Time
}

// User is an AD-backed web-console account. Authentication is delegated to LDAP;
// the user store is an allowlist of AD usernames that are permitted to log in.
type User struct {
	ID         string
	Username   string    // AD username used for LDAP bind
	Enabled    bool
	CreatedAt  time.Time
	MFASecret  string // base32 TOTP secret; empty = not enrolled
	MFAEnabled bool   // true after user completes first-time TOTP setup
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

// NodeMetrics holds point-in-time resource utilization for a node.
type NodeMetrics struct {
	MemTotalBytes  uint64
	MemUsedBytes   uint64
	DiskTotalBytes uint64
	DiskUsedBytes  uint64
}

// ContainerStats holds runtime resource usage for one container.
type ContainerStats struct {
	WorkloadID    string
	ContainerID   string
	Name          string
	CPUPercent    float64
	MemUsedBytes  uint64
	MemLimitBytes uint64
}

type ActualWorkloadState struct {
	NodeID         string
	Containers     []ActualContainer
	Stacks         []ActualStack
	Metrics        NodeMetrics
	ContainerStats []ContainerStats
	ReportedAt     time.Time
}
