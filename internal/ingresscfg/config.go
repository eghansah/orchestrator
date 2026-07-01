package ingresscfg

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	internraft "github.com/eghansah/orchestrator/internal/raft"
	"github.com/eghansah/orchestrator/pkg/types"
)

// Backend describes one ingress route — the data ingressd needs to generate
// an HAProxy frontend ACL + backend stanza.
type Backend struct {
	ID          string `json:"id"`           // ingress rule ID; used to name HAProxy objects
	Host        string `json:"host"`         // virtual host to match; empty = catch-all
	PathPrefix  string `json:"path_prefix"`  // URL path prefix; empty treated as "/"
	StripPrefix bool   `json:"strip_prefix"` // strip path prefix before forwarding
	NodeDataIP  string `json:"node_data_ip"` // data-plane IP of the node running the workload
	SystemPort  uint32 `json:"system_port"`  // proxyd-bound well-known port on that node
	TLSCert     string `json:"tls_cert"`     // PEM cert chain; empty = HTTP-only backend
	TLSKey      string `json:"tls_key"`      // PEM private key
}

// Config is the full contents of <dataDir>/ingress/config.json.
type Config struct {
	Backends []Backend `json:"backends"`
}

// parseFQDN extracts the workload name from a container FQDN.
// "ecouniversal" → "ecouniversal"; "backend.myapp" → "myapp"
func parseFQDN(fqdn string) string {
	if i := strings.LastIndex(fqdn, "."); i >= 0 {
		return fqdn[i+1:]
	}
	return fqdn
}

// Write atomically rewrites <dataDir>/ingress/config.json from current cluster state.
// Rules whose workload is unresolvable or unscheduled are omitted.
// Each IngressRule has its own SystemPort (allocated by the Raft FSM); no TCP Service required.
func Write(dataDir string, state internraft.ClusterState) error {
	var cfg Config

	for _, rule := range state.IngressRules {
		if rule.SystemPort == 0 {
			continue
		}
		host := rule.Host
		var tlsCert, tlsKey string

		if rule.DomainID != "" {
			d, ok := state.Domains[rule.DomainID]
			if !ok || !d.Enabled {
				continue
			}
			host = d.Name
			tlsCert = d.TLSCert
			tlsKey = d.TLSKey
		}

		wl, ok := findWorkload(state, parseFQDN(rule.ContainerFQDN))
		if !ok || wl.NodeID == "" {
			continue
		}

		node, ok := state.Nodes[wl.NodeID]
		if !ok {
			continue
		}

		cfg.Backends = append(cfg.Backends, Backend{
			ID:          rule.ID,
			Host:        host,
			PathPrefix:  rule.PathPrefix,
			StripPrefix: rule.StripPrefix,
			NodeDataIP:  node.DataIP,
			SystemPort:  rule.SystemPort,
			TLSCert:     tlsCert,
			TLSKey:      tlsKey,
		})
	}

	dir := filepath.Join(dataDir, "ingress")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}

	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}

	dst := filepath.Join(dir, "config.json")
	tmp := dst + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, dst)
}

// ConfigPath returns the canonical path for the ingress config file.
func ConfigPath(dataDir string) string {
	return filepath.Join(dataDir, "ingress", "config.json")
}

func findWorkload(state internraft.ClusterState, name string) (types.Workload, bool) {
	for _, wl := range state.Workloads {
		if wl.Name() == name {
			return wl, true
		}
	}
	return types.Workload{}, false
}
