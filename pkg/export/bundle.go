// Package export provides the portable ClusterExport bundle format used to
// migrate configuration between orchestrator instances.
package export

import (
	"fmt"
	"time"

	"gopkg.in/yaml.v3"

	internraft "github.com/eghansah/orchestrator/internal/raft"
	"github.com/eghansah/orchestrator/pkg/types"
)

const apiVersion = "orchestrator/v1"

// Bundle is the top-level export document.
type Bundle struct {
	APIVersion   string          `yaml:"api_version"`
	ExportedAt   time.Time       `yaml:"exported_at"`
	Workloads    []WorkloadEntry  `yaml:"workloads,omitempty"`
	Domains      []DomainEntry    `yaml:"domains,omitempty"`
	IngressRules []IngressEntry   `yaml:"ingress_rules,omitempty"`
	Services     []ServiceEntry   `yaml:"services,omitempty"`
	Secrets      []SecretEntry    `yaml:"secrets,omitempty"`
	ConfigValues []ConfigEntry    `yaml:"config_values,omitempty"`
	Registries   []RegistryEntry  `yaml:"registries,omitempty"`
	Templates    []TemplateEntry  `yaml:"templates,omitempty"`
}

type WorkloadEntry struct {
	Kind      string                  `yaml:"kind"` // "container" | "stack"
	Container *types.ContainerSpec    `yaml:"container,omitempty"`
	Stack     *types.ComposeStackSpec `yaml:"stack,omitempty"`
}

type DomainEntry struct {
	Name    string `yaml:"name"`
	TLSCert string `yaml:"tls_cert,omitempty"`
	TLSKey  string `yaml:"tls_key,omitempty"`
	CSR     string `yaml:"csr,omitempty"`
	Enabled bool   `yaml:"enabled"`
}

type IngressEntry struct {
	DomainName    string `yaml:"domain_name"`               // resolved to domain ID on import
	Host          string `yaml:"host,omitempty"`
	PathPrefix    string `yaml:"path_prefix,omitempty"`
	StripPrefix   bool   `yaml:"strip_prefix,omitempty"`
	ContainerFQDN string `yaml:"container_fqdn"`
	ContainerPort uint32 `yaml:"container_port"`
}

type ServiceEntry struct {
	Name          string `yaml:"name"`
	ContainerFQDN string `yaml:"container_fqdn"`
	ContainerPort uint32 `yaml:"container_port"`
}

type SecretEntry struct {
	Name    string `yaml:"name"`
	BaoPath string `yaml:"bao_path,omitempty"`
}

// ConfigEntry carries the real value — unlike SecretEntry, config values
// aren't sensitive, so export/import can fully round-trip them.
type ConfigEntry struct {
	Name  string `yaml:"name"`
	Value string `yaml:"value"`
}

type RegistryEntry struct {
	Name     string `yaml:"name"`
	URL      string `yaml:"url"`
	Username string `yaml:"username,omitempty"`
	Password string `yaml:"password,omitempty"`
}

type TemplateEntry struct {
	Name        string                  `yaml:"name"`
	Description string                  `yaml:"description,omitempty"`
	Kind        string                  `yaml:"kind"` // "container" | "stack"
	Container   *types.ContainerSpec    `yaml:"container,omitempty"`
	Stack       *types.ComposeStackSpec `yaml:"stack,omitempty"`
}

// FromState builds an export bundle from a cluster state snapshot.
// Runtime-only data (NodeID, port allocations, mesh addresses) is excluded.
func FromState(state internraft.ClusterState) Bundle {
	b := Bundle{
		APIVersion: apiVersion,
		ExportedAt: time.Now().UTC(),
	}

	for _, wl := range state.Workloads {
		e := WorkloadEntry{}
		switch {
		case wl.Container != nil:
			e.Kind = "container"
			spec := *wl.Container
			e.Container = &spec
		case wl.Stack != nil:
			e.Kind = "stack"
			spec := *wl.Stack
			spec.ResolvedEnv = nil // strip runtime secret values
			e.Stack = &spec
		}
		b.Workloads = append(b.Workloads, e)
	}

	for _, d := range state.Domains {
		b.Domains = append(b.Domains, DomainEntry{
			Name:    d.Name,
			TLSCert: d.TLSCert,
			TLSKey:  d.TLSKey,
			CSR:     d.CSR,
			Enabled: d.Enabled,
		})
	}

	// Build ID → name map for ingress cross-references.
	domainNames := make(map[string]string)
	for _, d := range state.Domains {
		domainNames[d.ID] = d.Name
	}
	for _, r := range state.IngressRules {
		b.IngressRules = append(b.IngressRules, IngressEntry{
			DomainName:    domainNames[r.DomainID],
			Host:          r.Host,
			PathPrefix:    r.PathPrefix,
			StripPrefix:   r.StripPrefix,
			ContainerFQDN: r.ContainerFQDN,
			ContainerPort: r.ContainerPort,
		})
	}

	for _, svc := range state.Services {
		b.Services = append(b.Services, ServiceEntry{
			Name:          svc.Name,
			ContainerFQDN: svc.ContainerFQDN,
			ContainerPort: svc.ContainerPort,
		})
	}

	for _, sec := range state.Secrets {
		b.Secrets = append(b.Secrets, SecretEntry{
			Name:    sec.Name,
			BaoPath: sec.BaoPath,
		})
	}

	for _, cv := range state.ConfigValues {
		b.ConfigValues = append(b.ConfigValues, ConfigEntry{
			Name:  cv.Name,
			Value: cv.Value,
		})
	}

	for _, reg := range state.Registries {
		b.Registries = append(b.Registries, RegistryEntry{
			Name:     reg.Name,
			URL:      reg.URL,
			Username: reg.Username,
			Password: reg.Password,
		})
	}

	for _, t := range state.Templates {
		e := TemplateEntry{Name: t.Name, Description: t.Description}
		switch {
		case t.Container != nil:
			e.Kind = "container"
			spec := *t.Container
			e.Container = &spec
		case t.Stack != nil:
			e.Kind = "stack"
			spec := *t.Stack
			e.Stack = &spec
		}
		b.Templates = append(b.Templates, e)
	}

	return b
}

// Marshal serializes the bundle to YAML.
func (b *Bundle) Marshal() ([]byte, error) {
	return yaml.Marshal(b)
}

// Unmarshal parses a YAML-encoded bundle. Returns an error if the api_version
// is missing or does not match.
func Unmarshal(data []byte) (*Bundle, error) {
	var b Bundle
	if err := yaml.Unmarshal(data, &b); err != nil {
		return nil, fmt.Errorf("parse bundle: %w", err)
	}
	if b.APIVersion != apiVersion {
		return nil, fmt.Errorf("unsupported api_version %q (expected %q)", b.APIVersion, apiVersion)
	}
	return &b, nil
}
