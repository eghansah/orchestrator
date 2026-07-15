package nerdctl

import (
	"fmt"
	"slices"
	"strings"

	internraft "github.com/eghansah/orchestrator/internal/raft"
	"github.com/eghansah/orchestrator/pkg/types"
	"gopkg.in/yaml.v3"
)

// rewriteComposePorts replaces short-form port declarations in a compose YAML
// with loopback bindings using the FSM-assigned AllocatedPorts, e.g.:
//
//	- "8080:80"  →  - "127.0.0.1:30001:80/tcp"
//	- "80"       →  - "127.0.0.1:30001:80/tcp"
//
// Lines that don't match a known container port are left unchanged.
// Long-form (target:/published:) entries are not rewritten.
func rewriteComposePorts(yaml string, allocations []types.PortAllocation) string {
	if len(allocations) == 0 {
		return yaml
	}
	type binding struct {
		allocated uint32
		proto     string
	}
	// Group allocations by container port as an ordered slice. Multiple services
	// exposing the same container port each get their own entry in the slice.
	byContainer := make(map[uint32][]binding, len(allocations))
	for _, pa := range allocations {
		if pa.AllocatedPort == 0 {
			continue
		}
		proto := pa.Protocol
		if proto == "" {
			proto = "tcp"
		}
		byContainer[pa.ContainerPort] = append(byContainer[pa.ContainerPort], binding{pa.AllocatedPort, proto})
	}

	// counters tracks how many times each container port has been rewritten so
	// far, used to pick the correct binding from the slice above.
	counters := make(map[uint32]int)

	lines := strings.Split(yaml, "\n")
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmed, "- ") {
			continue
		}
		val := strings.TrimSpace(strings.TrimPrefix(trimmed, "- "))
		val = strings.Trim(val, "\"'")

		containerPort, _ := internraft.PortContainerSide(val)
		if containerPort == 0 {
			continue
		}
		bindings := byContainer[containerPort]
		idx := counters[containerPort]
		if idx >= len(bindings) {
			continue
		}
		b := bindings[idx]
		counters[containerPort]++
		indent := line[:len(line)-len(strings.TrimLeft(line, " \t"))]
		lines[i] = fmt.Sprintf(`%s- "127.0.0.1:%d:%d/%s"`, indent, b.allocated, containerPort, b.proto)
	}
	return strings.Join(lines, "\n")
}

// injectMeshNetwork rewrites a compose YAML so that every service is attached
// to networkName (an externally-managed nerdctl network, e.g. "mesh0") in
// addition to whatever networks the service already uses. Services that had no
// explicit networks key are given [default, networkName] so the project's
// implicit default network is preserved. The top-level networks block gains
// networkName marked external:true. Returns the original string unchanged if
// networkName is empty or the YAML cannot be parsed.
func injectMeshNetwork(input, networkName string) string {
	if networkName == "" {
		return input
	}
	var doc map[string]any
	if err := yaml.Unmarshal([]byte(input), &doc); err != nil || doc == nil {
		return input
	}

	// Ensure top-level networks section exists and declares networkName.
	topNets := ensureStringMap(doc, "networks")
	topNets[networkName] = map[string]any{"external": true}

	needsDefault := false
	services, _ := doc["services"].(map[string]any)
	for _, sv := range services {
		svc, ok := sv.(map[string]any)
		if !ok {
			continue
		}
		existing := svcNetworkNames(svc)
		if slices.Contains(existing, networkName) {
			continue // already attached
		}
		if len(existing) == 0 {
			// Service relies on the implicit default — make it explicit so we
			// can attach mesh0 alongside it without dropping it.
			svc["networks"] = []any{"default", networkName}
			needsDefault = true
		} else {
			nets := make([]any, len(existing)+1)
			for i, n := range existing {
				nets[i] = n
			}
			nets[len(existing)] = networkName
			svc["networks"] = nets
		}
	}
	if needsDefault {
		if _, declared := topNets["default"]; !declared {
			topNets["default"] = nil // let compose create it
		}
	}

	out, err := yaml.Marshal(doc)
	if err != nil {
		return input
	}
	return string(out)
}

// ensureStringMap returns the map[string]any at parent[key], creating
// it if absent or if the existing value is not that type.
func ensureStringMap(parent map[string]any, key string) map[string]any {
	if v, ok := parent[key].(map[string]any); ok {
		return v
	}
	m := make(map[string]any)
	parent[key] = m
	return m
}

// svcNetworkNames returns the list of network names declared in a service map.
// Handles both list form ([]any) and map form (map[string]any).
func svcNetworkNames(svc map[string]any) []string {
	v, ok := svc["networks"]
	if !ok {
		return nil
	}
	switch nv := v.(type) {
	case []any:
		names := make([]string, 0, len(nv))
		for _, item := range nv {
			if s, ok := item.(string); ok {
				names = append(names, s)
			}
		}
		return names
	case map[string]any:
		names := make([]string, 0, len(nv))
		for k := range nv {
			names = append(names, k)
		}
		return names
	}
	return nil
}
