package proxycfg

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	internraft "github.com/eghansah/orchestrator/internal/raft"
	"github.com/eghansah/orchestrator/pkg/types"
)

// Entry describes one service endpoint — the data proxyd needs to serve DNS
// and (for local entries) bind a TCP listener.
type Entry struct {
	ServiceName   string `json:"service_name"`
	SystemPort    uint32 `json:"system_port"`
	AllocatedPort uint32 `json:"allocated_port"` // loopback port nerdctl bound
	NodeID        string `json:"node_id"`
	NodeDataIP    string `json:"node_data_ip"`
}

// Config is the full contents of <dataDir>/proxy/config.json.
type Config struct {
	LocalNodeID string  `json:"local_node_id"`
	DataIP      string  `json:"data_ip"`
	DNSAddr     string  `json:"dns_addr"`
	Entries     []Entry `json:"entries"`
}

// parseFQDN extracts the workload name from a container FQDN.
// "ecouniversal" → "ecouniversal"; "backend.myapp" → "myapp"
func parseFQDN(fqdn string) string {
	if i := strings.LastIndex(fqdn, "."); i >= 0 {
		return fqdn[i+1:]
	}
	return fqdn
}

// Write atomically rewrites <dataDir>/proxy/config.json from current cluster state.
// Entries are generated for both TCP Services and IngressRules (which have their own SystemPort).
// Entries whose workload is unscheduled (NodeID == "") are omitted.
func Write(dataDir, localNodeID, dataIP, dnsAddr string, state internraft.ClusterState) error {
	cfg := Config{
		LocalNodeID: localNodeID,
		DataIP:      dataIP,
		DNSAddr:     dnsAddr,
	}

	for _, svc := range state.Services {
		wl, ok := findWorkload(state, parseFQDN(svc.ContainerFQDN))
		if !ok || wl.NodeID == "" {
			continue
		}
		node, ok := state.Nodes[wl.NodeID]
		if !ok {
			continue
		}
		cfg.Entries = append(cfg.Entries, Entry{
			ServiceName:   svc.Name,
			SystemPort:    svc.SystemPort,
			AllocatedPort: allocatedPortFor(wl.PortAllocations, svc.ContainerPort),
			NodeID:        wl.NodeID,
			NodeDataIP:    node.DataIP,
		})
	}

	for _, rule := range state.IngressRules {
		if rule.SystemPort == 0 {
			continue
		}
		wl, ok := findWorkload(state, parseFQDN(rule.ContainerFQDN))
		if !ok || wl.NodeID == "" {
			continue
		}
		node, ok := state.Nodes[wl.NodeID]
		if !ok {
			continue
		}
		cfg.Entries = append(cfg.Entries, Entry{
			ServiceName:   rule.ID,
			SystemPort:    rule.SystemPort,
			AllocatedPort: allocatedPortFor(wl.PortAllocations, rule.ContainerPort),
			NodeID:        wl.NodeID,
			NodeDataIP:    node.DataIP,
		})
	}

	dir := filepath.Join(dataDir, "proxy")
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

// ConfigPath returns the canonical path for the config file.
func ConfigPath(dataDir string) string {
	return filepath.Join(dataDir, "proxy", "config.json")
}

func findWorkload(state internraft.ClusterState, name string) (types.Workload, bool) {
	for _, wl := range state.Workloads {
		if wl.Name() == name {
			return wl, true
		}
	}
	return types.Workload{}, false
}

func allocatedPortFor(pas []types.PortAllocation, targetPort uint32) uint32 {
	for _, pa := range pas {
		if pa.ContainerPort == targetPort {
			return pa.AllocatedPort
		}
	}
	return 0
}
