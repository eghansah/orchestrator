package proxycfg

import (
	"encoding/json"
	"log/slog"
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

// MeshEntry maps a workload name (or replica group name) to the mesh IPs of
// all its live container instances across all nodes.
type MeshEntry struct {
	Name    string   `json:"name"`     // workload name, e.g. "web"
	MeshIPs []string `json:"mesh_ips"` // one IP per live container instance
}

// Config is the full contents of <dataDir>/proxy/config.json.
type Config struct {
	LocalNodeID  string       `json:"local_node_id"`
	DataIP       string       `json:"data_ip"`
	DNSAddr      string       `json:"dns_addr"`
	Entries      []Entry      `json:"entries"`
	MeshEntries  []MeshEntry  `json:"mesh_entries,omitempty"`
}

// parseFQDN extracts the workload name from a container FQDN.
// "ecouniversal" → "ecouniversal"; "backend.myapp" → "myapp"
func parseFQDN(fqdn string) string {
	if i := strings.LastIndex(fqdn, "."); i >= 0 {
		return fqdn[i+1:]
	}
	return fqdn
}

// BuildMeshEntries aggregates mesh IPs for each workload (or replica group) from
// all nodes' actual state. The returned slice is ready to write into Config.MeshEntries.
func BuildMeshEntries(allStates map[string]types.ActualWorkloadState, raftState internraft.ClusterState) []MeshEntry {
	// Map group-name (or workload name for non-replicated) → []mesh IPs.
	byName := map[string][]string{}
	for _, nodeState := range allStates {
		for _, c := range nodeState.Containers {
			if c.MeshIP == "" {
				continue
			}
			name := groupNameFor(c.WorkloadID, raftState)
			if name == "" {
				continue
			}
			byName[name] = append(byName[name], c.MeshIP)
		}
	}
	entries := make([]MeshEntry, 0, len(byName))
	for name, ips := range byName {
		entries = append(entries, MeshEntry{Name: name, MeshIPs: ips})
	}
	return entries
}

// groupNameFor returns the DNS name to use for a workload in mesh DNS. Replicas
// use their GroupName (which equals the parent workload name); singletons use
// their own workload name.
func groupNameFor(workloadID string, state internraft.ClusterState) string {
	wl, ok := state.Workloads[workloadID]
	if !ok {
		return ""
	}
	if wl.GroupName != "" {
		return wl.GroupName
	}
	return wl.Name()
}

// Write atomically rewrites <dataDir>/proxy/config.json from current cluster state.
// Entries are generated for both TCP Services and IngressRules (which have their own SystemPort).
// Entries whose workload is unscheduled (NodeID == "") are omitted.
func Write(dataDir, localNodeID, dataIP, dnsAddr string, state internraft.ClusterState) error {
	return WriteWithMesh(dataDir, localNodeID, dataIP, dnsAddr, state, nil)
}

// WriteWithMesh is like Write but also includes mesh DNS entries.
func WriteWithMesh(dataDir, localNodeID, dataIP, dnsAddr string, state internraft.ClusterState, meshEntries []MeshEntry) error {
	cfg := Config{
		LocalNodeID: localNodeID,
		DataIP:      dataIP,
		DNSAddr:     dnsAddr,
		MeshEntries: meshEntries,
	}

	for _, svc := range state.Services {
		wlName := parseFQDN(svc.ContainerFQDN)
		wl, ok := findWorkload(state, wlName)
		if !ok {
			slog.Warn("proxycfg: skipping service — workload not found",
				"service", svc.Name, "fqdn", svc.ContainerFQDN, "resolved_name", wlName)
			continue
		}
		if wl.NodeID == "" {
			slog.Warn("proxycfg: skipping service — workload not scheduled",
				"service", svc.Name, "workload", wlName)
			continue
		}
		node, ok := state.Nodes[wl.NodeID]
		if !ok {
			slog.Warn("proxycfg: skipping service — node not found",
				"service", svc.Name, "node_id", wl.NodeID)
			continue
		}
		allocPort := allocatedPortFor(wl.PortAllocations, svc.ContainerPort)
		if allocPort == 0 {
			slog.Warn("proxycfg: skipping service — no allocated port for container port",
				"service", svc.Name, "container_port", svc.ContainerPort)
			continue
		}
		slog.Info("proxycfg: writing service entry",
			"service", svc.Name, "system_port", svc.SystemPort,
			"container_port", svc.ContainerPort, "allocated_port", allocPort,
			"node", wl.NodeID, "node_ip", node.DataIP)
		cfg.Entries = append(cfg.Entries, Entry{
			ServiceName:   svc.Name,
			SystemPort:    svc.SystemPort,
			AllocatedPort: allocPort,
			NodeID:        wl.NodeID,
			NodeDataIP:    node.DataIP,
		})
	}

	for _, rule := range state.IngressRules {
		if rule.SystemPort == 0 {
			slog.Warn("proxycfg: skipping ingress rule — no system port assigned", "rule_id", rule.ID)
			continue
		}
		wlName := parseFQDN(rule.ContainerFQDN)
		wl, ok := findWorkload(state, wlName)
		if !ok {
			slog.Warn("proxycfg: skipping ingress rule — workload not found",
				"rule_id", rule.ID, "fqdn", rule.ContainerFQDN, "resolved_name", wlName)
			continue
		}
		if wl.NodeID == "" {
			slog.Warn("proxycfg: skipping ingress rule — workload not scheduled",
				"rule_id", rule.ID, "workload", wlName)
			continue
		}
		node, ok := state.Nodes[wl.NodeID]
		if !ok {
			slog.Warn("proxycfg: skipping ingress rule — node not found",
				"rule_id", rule.ID, "node_id", wl.NodeID)
			continue
		}
		allocPort := allocatedPortFor(wl.PortAllocations, rule.ContainerPort)
		if allocPort == 0 {
			slog.Warn("proxycfg: skipping ingress rule — no allocated port for container port",
				"rule_id", rule.ID, "container_port", rule.ContainerPort)
			continue
		}
		slog.Info("proxycfg: writing ingress entry",
			"rule_id", rule.ID, "system_port", rule.SystemPort,
			"container_port", rule.ContainerPort, "allocated_port", allocPort,
			"node", wl.NodeID, "node_ip", node.DataIP)
		cfg.Entries = append(cfg.Entries, Entry{
			ServiceName:   rule.ID,
			SystemPort:    rule.SystemPort,
			AllocatedPort: allocPort,
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
