package meshd

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/eghansah/orchestrator/pkg/types"
)

// reconcileInterval is how often the manager re-derives the peer set from
// cluster state. It mirrors the proxyd/ingressd config writers' cadence.
const reconcileInterval = 2 * time.Second

// Device is the underlying WireGuard device the manager drives. It is an
// interface so the reconcile logic can be unit-tested with a fake, and so the
// userland (netstack) and future kernel-TUN backends are interchangeable.
type Device interface {
	// Configure applies a wireguard-go UAPI configuration string. It must be
	// idempotent: repeated calls with the same config leave the device
	// unchanged, and a new config replaces the peer set wholesale.
	Configure(uapi string) error
	// Close tears the device down.
	Close() error
}

// DeviceFactory creates the WireGuard device once this node's own mesh address
// is known. The address is leader-assigned (see internal/raft/fsm.go) and only
// appears in cluster state after the node registers, so the device cannot be
// built at startup.
type DeviceFactory func(meshAddr string) (Device, error)

// Manager owns this node's mesh identity and keeps its WireGuard peer set in
// sync with the cluster. It does not assign addresses — the leader does that in
// Raft state; the manager only consumes the result. The device is created
// lazily on the first reconcile in which this node's own mesh address is set.
type Manager struct {
	priv       Key
	pub        Key
	endpoint   string // this node's reachable WireGuard endpoint (host:port)
	listenPort int
	newDevice  DeviceFactory

	mu       sync.Mutex
	dev      Device // nil until this node's mesh address is assigned
	lastUAPI string // last config applied, so we skip redundant Configure calls
}

// NewManager loads or creates the node's persistent private key and returns a
// manager. endpoint is the node's externally reachable WireGuard endpoint
// (host:port) published to peers; listenPort is the local UDP port; newDevice
// builds the device once this node's mesh address is assigned.
func NewManager(dataDir, endpoint string, listenPort int, newDevice DeviceFactory) (*Manager, error) {
	priv, err := LoadOrCreateKey(dataDir)
	if err != nil {
		return nil, err
	}
	return &Manager{
		priv:       priv,
		pub:        priv.PublicKey(),
		endpoint:   endpoint,
		listenPort: listenPort,
		newDevice:  newDevice,
	}, nil
}

// PublicKey returns the base64 mesh public key this node publishes to the cluster.
func (m *Manager) PublicKey() string { return m.pub.String() }

// Endpoint returns the WireGuard endpoint (host:port) this node publishes.
func (m *Manager) Endpoint() string { return m.endpoint }

// Reconcile derives the desired peer set from nodes (excluding selfID) and
// applies it to the device only when it differs from the last applied config.
// It is a no-op until this node's own mesh address has been assigned in state,
// at which point it lazily creates the device.
func (m *Manager) Reconcile(nodes map[string]types.Node, selfID string) error {
	self, ok := nodes[selfID]
	if !ok || self.MeshAddr == "" {
		return nil // leader has not assigned this node a mesh address yet
	}

	peers, err := DesiredPeers(nodes, selfID)
	if err != nil {
		return err
	}
	uapi := RenderUAPI(m.priv, m.listenPort, peers)

	m.mu.Lock()
	defer m.mu.Unlock()

	if m.dev == nil {
		dev, err := m.newDevice(self.MeshAddr)
		if err != nil {
			return fmt.Errorf("create wireguard device on %s: %w", self.MeshAddr, err)
		}
		m.dev = dev
		slog.Info("mesh device up", "addr", self.MeshAddr, "subnet", self.MeshSubnet, "listen_port", m.listenPort)
	}

	if uapi == m.lastUAPI {
		return nil
	}
	if err := m.dev.Configure(uapi); err != nil {
		return fmt.Errorf("configure wireguard device: %w", err)
	}
	m.lastUAPI = uapi
	slog.Info("mesh peers reconciled", "peers", len(peers))
	return nil
}

// Run drives Reconcile on a ticker until ctx is cancelled, reading the current
// node set via stateFn each tick. It performs one immediate reconcile up front.
func (m *Manager) Run(ctx context.Context, selfID string, stateFn func() map[string]types.Node) {
	if err := m.Reconcile(stateFn(), selfID); err != nil {
		slog.Warn("initial mesh reconcile failed", "err", err)
	}
	ticker := time.NewTicker(reconcileInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := m.Reconcile(stateFn(), selfID); err != nil {
				slog.Warn("mesh reconcile failed", "err", err)
			}
		}
	}
}

// Close tears down the underlying device, if one was created.
func (m *Manager) Close() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.dev == nil {
		return nil
	}
	return m.dev.Close()
}
