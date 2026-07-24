package webui

import (
	"fmt"
	"net"
)

// checkMeshCIDROverlap warns if the cluster's configured mesh overlay CIDR
// overlaps a subnet already live on one of this node's real network
// interfaces. The mesh CIDR defaults to RFC 6598 space (100.64.0.0/10)
// specifically because it "almost never collides with real LANs" (see
// internal/raft/fsm.go), but some cloud providers hand out private/VPC
// addresses from that exact range. When that happens, a per-node mesh /24
// carved from the pool can be numerically identical to the host's real
// underlay subnet: containers get a mesh gateway address that looks like the
// host's real router but is actually an unrelated, disconnected nerdctl
// bridge, and traffic to real hosts on that network silently times out
// instead of reaching them.
func checkMeshCIDROverlap(meshCIDR string) []string {
	if meshCIDR == "" {
		return nil
	}
	_, meshNet, err := net.ParseCIDR(meshCIDR)
	if err != nil {
		return nil
	}

	ifaces, err := net.Interfaces()
	if err != nil {
		return nil
	}

	var warnings []string
	for _, iface := range ifaces {
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, addr := range addrs {
			ipNet, ok := addr.(*net.IPNet)
			if !ok || ipNet.IP.To4() == nil || ipNet.IP.IsLoopback() {
				continue
			}
			if !cidrsOverlap(meshNet, ipNet) {
				continue
			}
			warnings = append(warnings, fmt.Sprintf(
				"This node's network interface %q is on %s, which overlaps the cluster's mesh overlay CIDR "+
					"(%s). Mesh subnets are carved from that range and handed to containers as their own "+
					"private addresses; if a carved /24 collides with this interface's real subnet, containers "+
					"get a mesh gateway address that looks identical to but is disconnected from the host's "+
					"real router, and traffic to real hosts on that network can silently time out instead of "+
					"reaching them. Set the cluster's mesh CIDR to a range that doesn't overlap any node's "+
					"real network.",
				iface.Name, ipNet.String(), meshCIDR,
			))
		}
	}
	return warnings
}

// cidrsOverlap reports whether a and b share any address. CIDR-aligned
// blocks never partially overlap — if they intersect at all, one is a subset
// of the other — so checking each network's base address against the other
// is sufficient.
func cidrsOverlap(a, b *net.IPNet) bool {
	return a.Contains(b.IP) || b.Contains(a.IP)
}
