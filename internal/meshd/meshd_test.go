package meshd

import (
	"os"
	"path/filepath"
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

// ── Container device tests ────────────────────────────────────────────────────

func TestExtractPeersSection_WithPeers(t *testing.T) {
	priv, _ := GenerateKey()
	peer, _ := GenerateKey()
	uapi := RenderUAPI(priv, 51820, []Peer{
		{PublicKey: peer.PublicKey(), Endpoint: "10.0.0.2:51820", AllowedIPs: "100.64.1.0/24"},
	})

	section := extractPeersSection(uapi)

	if strings.HasPrefix(section, "private_key=") {
		t.Error("peer section must not contain private_key")
	}
	if strings.HasPrefix(section, "listen_port=") {
		t.Error("peer section must not contain listen_port")
	}
	if !strings.HasPrefix(section, "replace_peers=true\n") {
		t.Errorf("peer section must start with replace_peers=true, got: %q", section[:min(len(section), 40)])
	}
	if !strings.Contains(section, "public_key=") {
		t.Error("peer section missing public_key")
	}
}

func TestExtractPeersSection_NoPeers(t *testing.T) {
	priv, _ := GenerateKey()
	uapi := RenderUAPI(priv, 51820, nil)

	section := extractPeersSection(uapi)

	if section != "replace_peers=true\n" {
		t.Errorf("empty peer section should be exactly replace_peers=true, got %q", section)
	}
}

func TestContainerDevice_WritesPeersConf(t *testing.T) {
	dir := t.TempDir()
	// Create the mesh sub-dir as LoadOrCreateKey would.
	if err := os.MkdirAll(filepath.Join(dir, "mesh"), 0o700); err != nil {
		t.Fatal(err)
	}

	factory := NewContainerDeviceFactory(dir)
	dev, err := factory("100.64.0.1") // meshAddr ignored by this backend
	if err != nil {
		t.Fatalf("factory: %v", err)
	}
	defer dev.Close()

	priv, _ := GenerateKey()
	peer, _ := GenerateKey()
	uapi := RenderUAPI(priv, 51820, []Peer{
		{PublicKey: peer.PublicKey(), Endpoint: "10.0.0.2:51820", AllowedIPs: "100.64.1.0/24"},
	})

	if err := dev.Configure(uapi); err != nil {
		t.Fatalf("Configure: %v", err)
	}

	peersFile := filepath.Join(dir, "mesh", "peers.conf")
	got, err := os.ReadFile(peersFile)
	if err != nil {
		t.Fatalf("read peers.conf: %v", err)
	}
	content := string(got)

	if strings.Contains(content, "private_key=") {
		t.Error("peers.conf must not contain private_key")
	}
	if !strings.HasPrefix(content, "replace_peers=true\n") {
		t.Errorf("peers.conf must start with replace_peers=true, got: %q", content[:min(len(content), 40)])
	}
	if !strings.Contains(content, "public_key="+peer.PublicKey().Hex()) {
		t.Error("peers.conf missing peer public_key")
	}

	// File must be 0600.
	info, _ := os.Stat(peersFile)
	if info.Mode().Perm() != 0o600 {
		t.Errorf("peers.conf perm = %o, want 0600", info.Mode().Perm())
	}
}

func TestContainerDevice_AtomicOverwrite(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "mesh"), 0o700); err != nil {
		t.Fatal(err)
	}

	factory := NewContainerDeviceFactory(dir)
	dev, _ := factory("")

	priv, _ := GenerateKey()
	peerA, _ := GenerateKey()
	peerB, _ := GenerateKey()

	// First write.
	uapi1 := RenderUAPI(priv, 51820, []Peer{
		{PublicKey: peerA.PublicKey(), Endpoint: "10.0.0.2:51820", AllowedIPs: "100.64.1.0/24"},
	})
	if err := dev.Configure(uapi1); err != nil {
		t.Fatalf("first Configure: %v", err)
	}

	// Second write with different peer — should replace cleanly.
	uapi2 := RenderUAPI(priv, 51820, []Peer{
		{PublicKey: peerB.PublicKey(), Endpoint: "10.0.0.3:51820", AllowedIPs: "100.64.2.0/24"},
	})
	if err := dev.Configure(uapi2); err != nil {
		t.Fatalf("second Configure: %v", err)
	}

	got, _ := os.ReadFile(filepath.Join(dir, "mesh", "peers.conf"))
	content := string(got)
	if strings.Contains(content, peerA.PublicKey().Hex()) {
		t.Error("old peer key still present after overwrite")
	}
	if !strings.Contains(content, peerB.PublicKey().Hex()) {
		t.Error("new peer key missing after overwrite")
	}
}

func TestContainerDevice_NoPeers_ClearsConf(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "mesh"), 0o700); err != nil {
		t.Fatal(err)
	}

	factory := NewContainerDeviceFactory(dir)
	dev, _ := factory("")

	priv, _ := GenerateKey()
	peer, _ := GenerateKey()

	// Write a peer, then configure with no peers.
	dev.Configure(RenderUAPI(priv, 51820, []Peer{ //nolint:errcheck
		{PublicKey: peer.PublicKey(), Endpoint: "10.0.0.2:51820", AllowedIPs: "100.64.1.0/24"},
	}))
	if err := dev.Configure(RenderUAPI(priv, 51820, nil)); err != nil {
		t.Fatalf("Configure (no peers): %v", err)
	}

	got, _ := os.ReadFile(filepath.Join(dir, "mesh", "peers.conf"))
	if string(got) != "replace_peers=true\n" {
		t.Errorf("expected only replace_peers=true after clearing, got %q", string(got))
	}
}

