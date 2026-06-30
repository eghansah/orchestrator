package meshd

import (
	"fmt"
	"log/slog"
	"net/netip"
	"os"
	"path/filepath"
	"strings"

	"golang.zx2c4.com/wireguard/conn"
	"golang.zx2c4.com/wireguard/device"
	"golang.zx2c4.com/wireguard/tun/netstack"
)

// DefaultMTU is the tunnel MTU. WireGuard's encapsulation overhead (up to 80
// bytes over IPv4/UDP) means the inner MTU must sit below the path MTU or large
// transfers stall — the single most common setup mistake. 1380 is a safe value
// for a 1500-byte underlay. See docs/mesh-network.md ("Honest caveats").
const DefaultMTU = 1380

// netstackDevice is a userland WireGuard device backed by wireguard-go's gVisor
// netstack. It needs no elevated privileges and no /dev/net/tun, which suits the
// project's rootless constraint: the orchestrator process itself gets a presence
// on the mesh (so ingress/proxyd can dial mesh addresses).
//
// NOTE: netstack keeps the mesh inside this process's userland stack. Giving
// *containers* a mesh address instead requires a real TUN inside the rootlesskit
// network namespace; that is the concern of step 4 (attach containers) and will
// add a kernel-TUN DeviceFactory alongside this one. See docs/roadmap.md.
type netstackDevice struct {
	dev *device.Device
}

// NewNetstackDevice creates and starts a userland WireGuard device bound to
// meshAddr (this node's own mesh address). The returned device is Up; peers are
// applied separately via Configure.
func NewNetstackDevice(meshAddr string, mtu int) (Device, error) {
	addr, err := netip.ParseAddr(meshAddr)
	if err != nil {
		return nil, fmt.Errorf("parse mesh address %q: %w", meshAddr, err)
	}
	tunDev, _, err := netstack.CreateNetTUN([]netip.Addr{addr}, nil, mtu)
	if err != nil {
		return nil, fmt.Errorf("create netstack tun: %w", err)
	}
	logger := device.NewLogger(device.LogLevelError, "meshd: ")
	dev := device.NewDevice(tunDev, conn.NewDefaultBind(), logger)
	if err := dev.Up(); err != nil {
		dev.Close()
		return nil, fmt.Errorf("bring up wireguard device: %w", err)
	}
	slog.Debug("netstack wireguard device created", "addr", meshAddr, "mtu", mtu)
	return &netstackDevice{dev: dev}, nil
}

func (d *netstackDevice) Configure(uapi string) error {
	return d.dev.IpcSet(uapi)
}

func (d *netstackDevice) Close() error {
	d.dev.Close()
	return nil
}

// ── Container device (file-writing backend for meshrouterd) ──────────────────

// containerDevice implements Device by writing the WireGuard peer section to a
// file that the meshrouterd container mtime-polls and feeds to its own
// device.IpcSet. It owns no actual WireGuard device in this process — the
// container is the data plane. Configure is the only meaningful method; Close
// is a no-op.
type containerDevice struct {
	peersFile string // <dataDir>/mesh/peers.conf
}

// NewContainerDeviceFactory returns a DeviceFactory that writes peer
// configuration to <dataDir>/mesh/peers.conf for the meshrouterd container.
// The factory ignores the meshAddr argument (the container receives it as a
// CLI flag); the caller is responsible for ensuring the mesh dir exists (it is
// created by LoadOrCreateKey which is called earlier).
func NewContainerDeviceFactory(dataDir string) DeviceFactory {
	return func(_ string) (Device, error) {
		return &containerDevice{
			peersFile: filepath.Join(dataDir, "mesh", "peers.conf"),
		}, nil
	}
}

// Configure extracts the peer section from uapi (everything from
// "replace_peers=true" onward) and writes it atomically to peers.conf.
// meshrouterd reloads on mtime change and applies it with device.IpcSet.
func (d *containerDevice) Configure(uapi string) error {
	peers := extractPeersSection(uapi)
	return atomicWriteFile(d.peersFile, []byte(peers), 0o600)
}

func (d *containerDevice) Close() error { return nil }

// extractPeersSection returns the portion of a UAPI string beginning at
// "replace_peers=true\n". This is the declaration meshrouterd feeds to
// device.IpcSet to replace its peer set wholesale. If the marker is absent
// (no peers yet), the result is just "replace_peers=true\n" so the container
// still clears any stale peers.
func extractPeersSection(uapi string) string {
	const marker = "replace_peers=true\n"
	if idx := strings.Index(uapi, marker); idx >= 0 {
		return uapi[idx:]
	}
	return marker
}

// atomicWriteFile writes data to path by writing a sibling .tmp file and then
// renaming it into place, so readers never see a partial write.
func atomicWriteFile(path string, data []byte, perm os.FileMode) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, perm); err != nil {
		return fmt.Errorf("write %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("rename to %s: %w", path, err)
	}
	return nil
}
