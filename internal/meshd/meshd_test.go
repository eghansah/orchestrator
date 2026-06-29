package meshd

import (
	"strings"
	"testing"

	"github.com/eghansah/orchestrator/pkg/types"
)

func TestKeyRoundTrip(t *testing.T) {
	k, err := GenerateKey()
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	// Clamping invariants WireGuard requires of a private key.
	if k[0]&7 != 0 {
		t.Errorf("low 3 bits of byte 0 not cleared: %08b", k[0])
	}
	if k[31]&0xC0 != 0x40 {
		t.Errorf("top 2 bits of byte 31 not set to 01: %08b", k[31])
	}
	parsed, err := ParseKey(k.String())
	if err != nil {
		t.Fatalf("ParseKey: %v", err)
	}
	if parsed != k {
		t.Error("round-trip through base64 changed the key")
	}
	if len(k.Hex()) != 64 {
		t.Errorf("hex length = %d, want 64", len(k.Hex()))
	}
}

func TestPublicKeyDeterministic(t *testing.T) {
	k, _ := GenerateKey()
	pub1, pub2 := k.PublicKey(), k.PublicKey()
	if pub1 != pub2 {
		t.Error("public key derivation is not deterministic")
	}
	if pub1 == k {
		t.Error("public key equals private key")
	}
}

func TestParseKeyRejectsBadInput(t *testing.T) {
	if _, err := ParseKey("not base64!!!"); err == nil {
		t.Error("expected error for invalid base64")
	}
	if _, err := ParseKey("YWJj"); err == nil { // decodes to 3 bytes
		t.Error("expected error for wrong-length key")
	}
}

// meshNode is a small helper to build a node with mesh facts set.
func meshNode(id, pub, endpoint, subnet, addr string) types.Node {
	return types.Node{ID: id, MeshPubKey: pub, MeshEndpoint: endpoint, MeshSubnet: subnet, MeshAddr: addr}
}

func TestDesiredPeers_ExcludesSelfAndIncomplete(t *testing.T) {
	pubA := mustPub(t)
	pubB := mustPub(t)
	nodes := map[string]types.Node{
		"a": meshNode("a", pubA, "10.0.0.1:51820", "100.64.0.0/24", "100.64.0.1"),
		"b": meshNode("b", pubB, "10.0.0.2:51820", "100.64.1.0/24", "100.64.1.1"),
		// incomplete: no endpoint yet → excluded
		"c": meshNode("c", mustPub(t), "", "100.64.2.0/24", "100.64.2.1"),
	}
	peers, err := DesiredPeers(nodes, "a")
	if err != nil {
		t.Fatalf("DesiredPeers: %v", err)
	}
	if len(peers) != 1 {
		t.Fatalf("got %d peers, want 1 (self excluded, incomplete excluded)", len(peers))
	}
	if peers[0].AllowedIPs != "100.64.1.0/24" {
		t.Errorf("peer allowedIPs = %q, want 100.64.1.0/24", peers[0].AllowedIPs)
	}
}

func TestDesiredPeers_SortedAndRejectsBadKey(t *testing.T) {
	nodes := map[string]types.Node{
		"bad": meshNode("bad", "!!!notakey!!!", "10.0.0.9:51820", "100.64.9.0/24", "100.64.9.1"),
	}
	if _, err := DesiredPeers(nodes, "self"); err == nil {
		t.Error("expected error for unparseable peer key")
	}
}

func TestRenderUAPI(t *testing.T) {
	priv, _ := GenerateKey()
	peerKey, _ := GenerateKey()
	peers := []Peer{{PublicKey: peerKey.PublicKey(), Endpoint: "10.0.0.2:51820", AllowedIPs: "100.64.1.0/24"}}
	uapi := RenderUAPI(priv, 51820, peers)

	for _, want := range []string{
		"private_key=" + priv.Hex(),
		"listen_port=51820",
		"replace_peers=true",
		"public_key=" + peerKey.PublicKey().Hex(),
		"endpoint=10.0.0.2:51820",
		"allowed_ip=100.64.1.0/24",
		"persistent_keepalive_interval=25",
	} {
		if !strings.Contains(uapi, want) {
			t.Errorf("UAPI missing %q in:\n%s", want, uapi)
		}
	}
}

// fakeDevice records Configure calls and counts how many times it was built.
type fakeDevice struct {
	configs []string
	closed  bool
}

func (d *fakeDevice) Configure(uapi string) error { d.configs = append(d.configs, uapi); return nil }
func (d *fakeDevice) Close() error                { d.closed = true; return nil }

func TestManagerReconcile_LazyDeviceAndDedup(t *testing.T) {
	dir := t.TempDir()
	dev := &fakeDevice{}
	built := 0
	m, err := NewManager(dir, "10.0.0.1:51820", 51820, func(addr string) (Device, error) {
		built++
		if addr != "100.64.0.1" {
			t.Errorf("device built with addr %q, want 100.64.0.1", addr)
		}
		return dev, nil
	})
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}

	// Before this node has a mesh address assigned: no device, no config.
	nodes := map[string]types.Node{"self": {ID: "self"}}
	if err := m.Reconcile(nodes, "self"); err != nil {
		t.Fatalf("reconcile (unassigned): %v", err)
	}
	if built != 0 {
		t.Fatalf("device built before address assigned")
	}

	// Address assigned + one peer present → device created and configured once.
	nodes["self"] = meshNode("self", m.PublicKey(), "10.0.0.1:51820", "100.64.0.0/24", "100.64.0.1")
	nodes["peer"] = meshNode("peer", mustPub(t), "10.0.0.2:51820", "100.64.1.0/24", "100.64.1.1")
	if err := m.Reconcile(nodes, "self"); err != nil {
		t.Fatalf("reconcile (assigned): %v", err)
	}
	if built != 1 {
		t.Fatalf("device built %d times, want 1", built)
	}
	if len(dev.configs) != 1 {
		t.Fatalf("Configure called %d times, want 1", len(dev.configs))
	}

	// Re-reconcile with identical state → no redundant Configure.
	if err := m.Reconcile(nodes, "self"); err != nil {
		t.Fatalf("reconcile (idempotent): %v", err)
	}
	if len(dev.configs) != 1 {
		t.Errorf("Configure called again for unchanged state: %d", len(dev.configs))
	}

	// A new peer changes the desired set → Configure again.
	nodes["peer2"] = meshNode("peer2", mustPub(t), "10.0.0.3:51820", "100.64.2.0/24", "100.64.2.1")
	if err := m.Reconcile(nodes, "self"); err != nil {
		t.Fatalf("reconcile (new peer): %v", err)
	}
	if len(dev.configs) != 2 {
		t.Errorf("Configure called %d times after new peer, want 2", len(dev.configs))
	}

	if err := m.Close(); err != nil || !dev.closed {
		t.Errorf("Close did not tear down device (err=%v closed=%v)", err, dev.closed)
	}
}

func TestLoadOrCreateKey_Persists(t *testing.T) {
	dir := t.TempDir()
	k1, err := LoadOrCreateKey(dir)
	if err != nil {
		t.Fatalf("first LoadOrCreateKey: %v", err)
	}
	k2, err := LoadOrCreateKey(dir)
	if err != nil {
		t.Fatalf("second LoadOrCreateKey: %v", err)
	}
	if k1 != k2 {
		t.Error("key not persisted across calls")
	}
}

func mustPub(t *testing.T) string {
	t.Helper()
	k, err := GenerateKey()
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	return k.PublicKey().String()
}
