package raft

import (
	"strconv"
	"strings"

	"github.com/eghansah/orchestrator/pkg/types"
)

// parseComposePortAllocations extracts host→container port mappings from a
// compose YAML string and returns them as PortAllocation entries so that
// Service objects can route to stack containers via the service proxy.
//
// Only the short-form port syntax is supported:
//
//	"HOST:CONTAINER[/proto]"
//	"IP:HOST:CONTAINER[/proto]"
//
// Container-only entries (no host port) are skipped because the host port
// is unknown until nerdctl runs. Port ranges are not supported.
func parseComposePortAllocations(yaml string) []types.PortAllocation {
	var out []types.PortAllocation
	for _, line := range strings.Split(yaml, "\n") {
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmed, "- ") {
			continue
		}
		val := strings.TrimSpace(strings.TrimPrefix(trimmed, "- "))
		val = strings.Trim(val, "\"'")

		pa := parseComposePortEntry(val)
		if pa == nil {
			continue
		}
		// Skip duplicates (same host+container pair from multiple services).
		dup := false
		for _, e := range out {
			if e.AllocatedPort == pa.AllocatedPort && e.ContainerPort == pa.ContainerPort {
				dup = true
				break
			}
		}
		if !dup {
			out = append(out, *pa)
		}
	}
	return out
}

// parseComposePortEntry parses a single short-form compose port string.
// Returns nil if the string is not a valid host:container mapping.
func parseComposePortEntry(s string) *types.PortAllocation {
	proto := "tcp"
	// Strip optional /proto suffix — must appear after the last colon segment.
	if idx := strings.LastIndex(s, "/"); idx > strings.LastIndex(s, ":") {
		proto = strings.ToLower(s[idx+1:])
		s = s[:idx]
	}

	parts := strings.Split(s, ":")
	var hostStr, containerStr string
	switch len(parts) {
	case 1:
		return nil // container port only; host port is unknown
	case 2:
		// HOST:CONTAINER or IP:CONTAINER (no host port).
		// If parts[0] fails to parse as a number it's an IP without a host port.
		hostStr, containerStr = parts[0], parts[1]
	case 3:
		// IP:HOST:CONTAINER
		hostStr, containerStr = parts[1], parts[2]
	default:
		return nil
	}

	host, err := strconv.ParseUint(hostStr, 10, 32)
	if err != nil || host == 0 {
		return nil
	}
	container, err := strconv.ParseUint(containerStr, 10, 32)
	if err != nil || container == 0 {
		return nil
	}

	return &types.PortAllocation{
		AllocatedPort: uint32(host),
		ContainerPort: uint32(container),
		Protocol:      proto,
	}
}
