package nerdctl

import (
	"fmt"
	"strings"

	internraft "github.com/eghansah/orchestrator/internal/raft"
	"github.com/eghansah/orchestrator/pkg/types"
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
