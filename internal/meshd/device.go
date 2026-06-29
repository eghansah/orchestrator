package meshd

import (
	"fmt"
	"log/slog"
	"net/netip"

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
