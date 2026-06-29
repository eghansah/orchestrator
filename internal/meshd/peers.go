package meshd

import (
	"fmt"
	"sort"
	"strings"

	"github.com/eghansah/orchestrator/pkg/types"
)

// defaultKeepaliveSeconds keeps NAT/stateful-firewall mappings alive so peers
// behind such middleboxes stay reachable. 25s is the WireGuard convention.
const defaultKeepaliveSeconds = 25

// Peer is one WireGuard peer derived from a remote node's published mesh facts.
type Peer struct {
	PublicKey  Key    // remote node's mesh public key
	Endpoint   string // host:port for WireGuard (UDP)
	AllowedIPs string // the remote node's /24 (e.g. "100.64.2.0/24")
}

// DesiredPeers builds the WireGuard peer set from cluster state. It excludes
// selfID and any node that has not yet published a complete set of mesh facts
// (public key, endpoint, and a leader-assigned subnet). The result is sorted by
// public key so callers can compare snapshots for equality.
func DesiredPeers(nodes map[string]types.Node, selfID string) ([]Peer, error) {
	peers := make([]Peer, 0, len(nodes))
	for id, n := range nodes {
		if id == selfID {
			continue
		}
		if n.MeshPubKey == "" || n.MeshEndpoint == "" || n.MeshSubnet == "" {
			continue // node not yet on the mesh
		}
		pk, err := ParseKey(n.MeshPubKey)
		if err != nil {
			return nil, fmt.Errorf("node %s: %w", id, err)
		}
		peers = append(peers, Peer{
			PublicKey:  pk,
			Endpoint:   n.MeshEndpoint,
			AllowedIPs: n.MeshSubnet,
		})
	}
	sort.Slice(peers, func(i, j int) bool {
		return peers[i].PublicKey.String() < peers[j].PublicKey.String()
	})
	return peers, nil
}

// RenderUAPI renders a wireguard-go IpcSet configuration string for the given
// private key, UDP listen port and peer set. Keys are hex-encoded as the UAPI
// requires, and replace_peers=true makes each apply fully declarative — the
// device's peer set becomes exactly peers.
func RenderUAPI(priv Key, listenPort int, peers []Peer) string {
	var b strings.Builder
	fmt.Fprintf(&b, "private_key=%s\n", priv.Hex())
	fmt.Fprintf(&b, "listen_port=%d\n", listenPort)
	b.WriteString("replace_peers=true\n")
	for _, p := range peers {
		fmt.Fprintf(&b, "public_key=%s\n", p.PublicKey.Hex())
		if p.Endpoint != "" {
			fmt.Fprintf(&b, "endpoint=%s\n", p.Endpoint)
		}
		b.WriteString("replace_allowed_ips=true\n")
		fmt.Fprintf(&b, "allowed_ip=%s\n", p.AllowedIPs)
		fmt.Fprintf(&b, "persistent_keepalive_interval=%d\n", defaultKeepaliveSeconds)
	}
	return b.String()
}
