package types

import (
	"crypto/x509"
	"encoding/pem"
	"errors"
	"strings"
	"time"
)

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
	Name             string            `yaml:"name"`
	Image            string            `yaml:"image"`
	Command          []string          `yaml:"command,omitempty"`
	Env              []string          `yaml:"env,omitempty"` // KEY=VALUE pairs
	Ports            []PortMapping     `yaml:"ports,omitempty"`
	Volumes          []VolumeMount     `yaml:"volumes,omitempty"`
	Labels           map[string]string `yaml:"labels,omitempty"`
	Namespace        string            `yaml:"namespace,omitempty"`         // nerdctl namespace; defaults to "orchestrator"
	SecretRefs       map[string]string `yaml:"secret_refs,omitempty"`       // env_var_name → secret_name; resolved at placement
	ConfigRefs       map[string]string `yaml:"config_refs,omitempty"`       // env_var_name → config_name; resolved at placement (plaintext, no OpenBao)
	Replicas         int               `yaml:"replicas,omitempty"`          // desired replica count; 0 or 1 = single instance
	InsecureRegistry bool              `yaml:"insecure_registry,omitempty"` // pass --insecure-registry to nerdctl (plain-HTTP or self-signed registries)
}

type ComposeStackSpec struct {
	Name             string               `yaml:"name"`
	ComposeYAML      string               `yaml:"compose_yaml"`                // inline compose file content
	SecretRefs       map[string]string    `yaml:"secret_refs,omitempty"`       // env_var_name → secret_name; resolved at placement
	ConfigRefs       map[string]string    `yaml:"config_refs,omitempty"`       // env_var_name → config_name; resolved at placement (plaintext, no OpenBao)
	SecretMounts     []ComposeSecretMount `yaml:"secret_mounts,omitempty"`     // per-service file-mounted secrets; resolved at placement
	ResolvedEnv      []string             `yaml:"-"`                           // runtime only; never exported
	Replicas         int                  `yaml:"replicas,omitempty"`          // desired replica count; 0 or 1 = single instance
	InsecureRegistry bool                 `yaml:"insecure_registry,omitempty"` // pass --insecure-registry to nerdctl (plain-HTTP or self-signed registries)
}

// ComposeSecretMount declares that Service should get SecretName mounted as a
// file at Target (default "/run/secrets/<SecretName>" when empty) inside a
// compose stack. Resolved into a ResolvedSecretFile at placement time.
type ComposeSecretMount struct {
	Service    string `yaml:"service"`
	SecretName string `yaml:"secret_name"`
	Target     string `yaml:"target,omitempty"`
	Mode       uint32 `yaml:"mode,omitempty"` // file perm bits; default 0400
}

type PortMapping struct {
	ContainerPort uint32 `yaml:"container_port"`
	Protocol      string `yaml:"protocol"` // "tcp" | "udp"
}

// VolumeType discriminates what VolumeMount.Source refers to. The zero value
// ("") is treated as VolumeTypeBind for backward compatibility with specs
// that predate this field.
type VolumeType string

const (
	VolumeTypeBind   VolumeType = "bind"   // Source is a host path (or named-volume name), same as historical behavior
	VolumeTypeVolume VolumeType = "volume" // Source is an explicit named nerdctl volume
	VolumeTypeSecret VolumeType = "secret" // Source is a secret name, resolved from OpenBao at placement time
)

type VolumeMount struct {
	Type     VolumeType `yaml:"type,omitempty"`
	Source   string     `yaml:"source"` // host path / volume name, or secret name when Type == VolumeTypeSecret
	Target   string     `yaml:"target"` // defaults to "/run/secrets/<Source>" when Type == VolumeTypeSecret and empty
	ReadOnly bool       `yaml:"read_only,omitempty"`
	Mode     uint32     `yaml:"mode,omitempty"` // file perm bits, meaningful only when Type == VolumeTypeSecret; default 0400
}

// EffectiveType returns v.Type, treating the zero value as VolumeTypeBind so
// specs written before this field existed keep their original behavior.
func (v VolumeMount) EffectiveType() VolumeType {
	if v.Type == "" {
		return VolumeTypeBind
	}
	return v.Type
}

// SecretTarget returns v.Target, defaulting to "/run/secrets/<Source>" (the
// Docker Swarm convention) when unset. Only meaningful when
// v.EffectiveType() == VolumeTypeSecret.
func (v VolumeMount) SecretTarget() string {
	if v.Target != "" {
		return v.Target
	}
	return "/run/secrets/" + v.Source
}

// ResolvedSecretFile carries one secret's plaintext, resolved from OpenBao at
// placement time, from the leader to the target node over the placement RPC.
// It lives only on Workload (never on ContainerSpec/ComposeStackSpec, which
// are Raft-persisted) so plaintext never enters the durable log.
type ResolvedSecretFile struct {
	Name      string // secret name (== VolumeMount.Source / ComposeSecretMount.SecretName)
	Target    string // in-container path
	Mode      uint32
	Service   string // compose service name; empty for single containers
	Plaintext string
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
	GroupName       string           // non-empty on replica instances; equals the parent workload name

	// ResolvedSecretFiles carries plaintext for Type==secret VolumeMount/
	// ComposeSecretMount entries, populated at placement time. Present only
	// on the leader→agent placement RPC payload; never Raft-persisted.
	ResolvedSecretFiles []ResolvedSecretFile
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

	// Mesh overlay fields. The node publishes its own MeshPubKey/MeshEndpoint
	// (its WireGuard public key and reachable UDP endpoint); the leader assigns
	// MeshSubnet/MeshAddr from the cluster mesh CIDR and keeps them stable across
	// re-registration. The private key never leaves the node and is never stored
	// here. See docs/mesh-network.md.
	MeshPubKey   string // WireGuard public key (base64)
	MeshEndpoint string // host:port for WireGuard (UDP, port >= 1024)
	MeshSubnet   string // leader-assigned /24 for this node's containers, e.g. "100.64.3.0/24"
	MeshAddr     string // this node's own address on the mesh (first host of MeshSubnet)
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
	StripPrefix   bool   // strip PathPrefix before forwarding to the backend
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
	Name          string // short DNS label, e.g. "api"
	ContainerFQDN string // target container: "workload" or "service.workload"
	ContainerPort uint32 // port the container listens on
	SystemPort    uint32 // auto-assigned host port
	CreatedAt     time.Time
}

// Domain is a named TLS-enabled virtual host. The ingress uses the stored
// certificate to terminate HTTPS connections whose SNI matches Name.
type Domain struct {
	ID        string
	Name      string // unique hostname, e.g. "api.example.com"
	TLSCert   string // PEM-encoded certificate
	TLSKey    string // PEM-encoded private key
	CSR       string // PEM-encoded certificate signing request; empty on old records
	Enabled   bool   // when false the ingress ignores this domain's cert
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

// SecretPathPrefix is prepended to every secret's name to form its BaoPath.
const SecretPathPrefix = "orchestrator/"

// Secret is a named secret stored in the cluster's OpenBao instance.
// BaoPath is the KV v2 path within the configured mount (e.g. "orchestrator/db-pass").
// The plaintext value is fetched from OpenBao at placement time and injected as an env var.
type Secret struct {
	ID        string
	Name      string // unique cluster-wide label
	BaoPath   string // KV v2 path within OpenBaoConfig.Mount
	CreatedAt time.Time
}

// ConfigValue is a named, plaintext, cluster-wide config value stored directly
// in Raft state (no OpenBao involved). Unlike Secret, the value itself lives
// on the struct — config values are never sensitive and are resolved into env
// vars at placement time same as SecretRefs, but with no external round trip.
type ConfigValue struct {
	ID        string
	Name      string // unique cluster-wide label
	Value     string
	CreatedAt time.Time
	UpdatedAt time.Time
}

// OpenBaoConfig holds the connection parameters for the cluster's OpenBao instance.
// All nodes read this from Raft state to resolve secrets at placement time.
//
// Two auth methods are supported: static token (legacy/default, when RoleID
// is empty) and AppRole (when RoleID is set — Token is ignored in that case).
// AppRole credentials are replicated via Raft exactly like Token was before,
// so this doesn't change the cluster's existing trust boundary (every Raft
// member already saw the plaintext OpenBao credential); it only swaps a
// long-lived static token for a short-lived, renewable login.
type OpenBaoConfig struct {
	Address            string // e.g. "https://bao.example.com:8200"
	Token              string // service token scoped to KV read/write on <mount>/data/orchestrator/*; used when RoleID is empty
	RoleID             string // AppRole role_id; when set, AppRole login is used instead of Token
	SecretID           string // AppRole secret_id
	AuthMount          string // AppRole auth backend mount path; defaults to "approle" when empty
	Mount              string // KV v2 mount path; defaults to "secret" when empty
	InsecureSkipVerify bool   // skip TLS certificate verification entirely (testing only)
}

// TrustedCA is a cluster-wide CA certificate that can be used to verify TLS
// connections to internally-signed services. Each entry holds exactly one CA
// certificate (add multiple entries for multiple CAs) and is scoped by the
// two Applies flags to one or both consumers: the OpenBao connection and/or
// container/compose registry image pulls.
type TrustedCA struct {
	ID                  string
	Label               string // display name, e.g. "Internal Corp CA"
	PEM                 string // a single CA certificate in PEM format
	AppliesToOpenBao    bool   // trust this CA when verifying the OpenBao connection
	AppliesToRegistries bool   // trust this CA when verifying container/compose image registries
	CreatedAt           time.Time
}

// ParseCert decodes the CA's PEM block and parses it as an X.509 certificate.
func (ca TrustedCA) ParseCert() (*x509.Certificate, error) {
	block, _ := pem.Decode([]byte(ca.PEM))
	if block == nil {
		return nil, errors.New("no PEM block found")
	}
	return x509.ParseCertificate(block.Bytes)
}

// Expired reports whether the CA certificate is outside its validity window,
// or can't be parsed at all (treated as untrusted rather than silently used).
func (ca TrustedCA) Expired() bool {
	cert, err := ca.ParseCert()
	if err != nil {
		return true
	}
	now := time.Now()
	return now.Before(cert.NotBefore) || now.After(cert.NotAfter)
}

// OpenBaoTrustBundle concatenates the PEM of every non-expired CA flagged
// AppliesToOpenBao, for use as the RootCAs pool verifying the OpenBao
// connection.
func OpenBaoTrustBundle(cas map[string]TrustedCA) string {
	return trustBundle(cas, func(c TrustedCA) bool { return c.AppliesToOpenBao })
}

// RegistryTrustBundle concatenates the PEM of every non-expired CA flagged
// AppliesToRegistries, for use when verifying container/compose image
// registries.
func RegistryTrustBundle(cas map[string]TrustedCA) string {
	return trustBundle(cas, func(c TrustedCA) bool { return c.AppliesToRegistries })
}

func trustBundle(cas map[string]TrustedCA, match func(TrustedCA) bool) string {
	var sb strings.Builder
	for _, c := range cas {
		if !match(c) || c.Expired() {
			continue
		}
		sb.WriteString(c.PEM)
		sb.WriteString("\n")
	}
	return sb.String()
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
	Username   string // AD username used for LDAP bind
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
	MeshIP      string    // container's IP on mesh0; empty if not on mesh or not yet assigned
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
