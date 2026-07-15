package raft

import (
	"strconv"
	"strings"

	"github.com/eghansah/orchestrator/pkg/types"
)

// parseComposeContainerPorts scans a compose YAML string for port declarations
// and returns one PortAllocation per unique container port with AllocatedPort=0.
// AllocatedPort is filled in by the FSM from the port pool, the same way
// container workloads are handled.
//
// Both short-form strings and bare integers are supported:
//
//	"80"                → ContainerPort 80
//	"80/tcp"            → ContainerPort 80, Protocol tcp
//	"8080:80"           → ContainerPort 80  (host side ignored; FSM reassigns)
//	"8080:80/tcp"       → ContainerPort 80
//	"127.0.0.1:8080:80" → ContainerPort 80
//
// Long-form (target:/published: mapping) and port ranges are not supported.
// parseComposeContainerPorts returns one PortAllocation per port declaration
// line. It does NOT deduplicate across services — two services each exposing
// port 80 produce two entries so each gets its own loopback binding.
func parseComposeContainerPorts(yaml string) []types.PortAllocation {
	var out []types.PortAllocation
	for _, line := range strings.Split(yaml, "\n") {
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmed, "- ") {
			continue
		}
		val := strings.TrimSpace(strings.TrimPrefix(trimmed, "- "))
		val = strings.Trim(val, "\"'")

		containerPort, proto := PortContainerSide(val)
		if containerPort == 0 {
			continue
		}
		out = append(out, types.PortAllocation{
			ContainerPort: containerPort,
			Protocol:      proto,
		})
	}
	return out
}

// PortContainerSide extracts the container port and protocol from a short-form
// compose port string. Returns (0, "") when the string is not a port entry.
// Exported so the nerdctl package can reuse the parsing logic when rewriting
// compose files at deployment time.
func PortContainerSide(s string) (containerPort uint32, proto string) {
	proto = "tcp"
	if idx := strings.LastIndex(s, "/"); idx > strings.LastIndex(s, ":") {
		proto = strings.ToLower(s[idx+1:])
		s = s[:idx]
	}
	// Container port is always the last colon-separated segment.
	parts := strings.Split(s, ":")
	port, err := strconv.ParseUint(parts[len(parts)-1], 10, 32)
	if err != nil || port == 0 {
		return 0, ""
	}
	return uint32(port), proto
}
