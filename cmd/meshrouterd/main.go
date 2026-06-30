// meshrouterd is the managed WireGuard router container for the orchestrator
// mesh overlay (step 4b). It runs inside the rootless RootlessKit network
// namespace (via `nerdctl run --network ns:<path>`) so it shares the mesh0
// bridge. It creates wg0, installs the mesh CIDR route, and keeps the peer set
// in sync by mtime-polling a peers.conf file written atomically by meshd.
//
// See docs/mesh-wg-router.md for design rationale.
//
//go:build linux

package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/vishvananda/netlink"
	"golang.zx2c4.com/wireguard/conn"
	"golang.zx2c4.com/wireguard/device"
	wgtun "golang.zx2c4.com/wireguard/tun"

	"github.com/eghansah/orchestrator/internal/meshd"
)

const pollInterval = 2 * time.Second

func main() {
	var (
		meshAddr       = flag.String("mesh-addr", "", "node's own mesh gateway IP (e.g. 100.64.3.1); used for logging/validation")
		meshCIDR       = flag.String("mesh-cidr", "100.64.0.0/10", "full mesh CIDR; route added via wg0")
		listenPort     = flag.Int("listen-port", 51820, "WireGuard UDP listen port (≥1024, host-forwarded by rootlesskit)")
		peersConf      = flag.String("config", "/data/mesh/peers.conf", "path to peers.conf; mtime-polled for peer updates")
		privateKeyFile = flag.String("private-key-file", "/data/mesh/private.key", "path to base64 WireGuard private key file")
		tunName        = flag.String("tun", "wg0", "TUN interface name")
		mtu            = flag.Int("mtu", meshd.DefaultMTU, "TUN MTU (default 1380, safe for 1500-byte underlay)")
	)
	flag.Parse()

	if *meshAddr == "" {
		fmt.Fprintln(os.Stderr, "usage: meshrouterd --mesh-addr <ip> [options]")
		os.Exit(1)
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()

	if err := run(ctx, *tunName, *meshAddr, *meshCIDR, *listenPort, *mtu, *privateKeyFile, *peersConf); err != nil {
		slog.Error("meshrouterd fatal", "err", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, tunName, meshAddr, meshCIDR string, listenPort, mtu int, privKeyPath, peersPath string) error {
	// Enable IP forwarding so the kernel forwards packets between mesh0 and wg0
	// inside this netns. This sysctl is namespaced; no host-root required.
	if err := os.WriteFile("/proc/sys/net/ipv4/ip_forward", []byte("1"), 0o644); err != nil {
		slog.Warn("could not enable ip_forward (may already be on)", "err", err)
	}

	// Load the node's WireGuard private key.
	raw, err := os.ReadFile(privKeyPath)
	if err != nil {
		return fmt.Errorf("read private key %s: %w", privKeyPath, err)
	}
	priv, err := meshd.ParseKey(strings.TrimSpace(string(raw)))
	if err != nil {
		return fmt.Errorf("parse private key: %w", err)
	}

	// Create the TUN interface (requires /dev/net/tun and NET_ADMIN cap).
	tunDev, err := wgtun.CreateTUN(tunName, mtu)
	if err != nil {
		return fmt.Errorf("create TUN %s: %w", tunName, err)
	}
	defer tunDev.Close()

	// Bring the interface up and install the mesh CIDR route.
	if err := setupInterface(tunName, meshCIDR); err != nil {
		return fmt.Errorf("setup interface: %w", err)
	}

	// Create the WireGuard device on top of the TUN.
	logger := device.NewLogger(device.LogLevelError, "meshrouterd: ")
	wgDev := device.NewDevice(tunDev, conn.NewDefaultBind(), logger)
	defer wgDev.Close()

	// Configure private key and listen port. The peer set is applied separately
	// from the peers.conf file so it can be updated without restarting.
	bootUAPI := fmt.Sprintf("private_key=%s\nlisten_port=%d\n", priv.Hex(), listenPort)
	if err := wgDev.IpcSet(bootUAPI); err != nil {
		return fmt.Errorf("set private key / listen port: %w", err)
	}
	if err := wgDev.Up(); err != nil {
		return fmt.Errorf("bring up WireGuard device: %w", err)
	}
	slog.Info("meshrouterd up", "tun", tunName, "mesh-addr", meshAddr, "cidr", meshCIDR, "port", listenPort, "mtu", mtu)

	// Apply initial peers immediately, then poll for changes.
	applyPeers(wgDev, peersPath)
	go pollPeers(ctx, wgDev, peersPath)

	<-ctx.Done()
	slog.Info("meshrouterd shutting down")
	return nil
}

// setupInterface brings up tunName and adds a route for meshCIDR pointing to it.
// Both operations are performed via netlink inside the current network namespace,
// which is the RootlessKit netns when run via --network ns:<path>.
func setupInterface(tunName, meshCIDR string) error {
	link, err := netlink.LinkByName(tunName)
	if err != nil {
		return fmt.Errorf("get link %s: %w", tunName, err)
	}
	if err := netlink.LinkSetUp(link); err != nil {
		return fmt.Errorf("link set %s up: %w", tunName, err)
	}
	_, dst, err := net.ParseCIDR(meshCIDR)
	if err != nil {
		return fmt.Errorf("parse mesh cidr %q: %w", meshCIDR, err)
	}
	route := &netlink.Route{LinkIndex: link.Attrs().Index, Dst: dst}
	if err := netlink.RouteAdd(route); err != nil {
		return fmt.Errorf("add route %s dev %s: %w", meshCIDR, tunName, err)
	}
	slog.Debug("interface ready", "tun", tunName, "route", meshCIDR)
	return nil
}

// applyPeers reads peersPath and feeds it to the WireGuard device via IpcSet.
// The file contains only the peer section (replace_peers=true + peer entries)
// as written by meshd's containerDevice. Missing file is silently skipped.
func applyPeers(dev *device.Device, peersPath string) {
	data, err := os.ReadFile(peersPath)
	if err != nil {
		if !os.IsNotExist(err) {
			slog.Warn("read peers.conf", "path", peersPath, "err", err)
		}
		return
	}
	if err := dev.IpcSet(string(data)); err != nil {
		slog.Warn("apply peers.conf", "path", peersPath, "err", err)
		return
	}
	slog.Debug("peers applied", "path", peersPath)
}

// pollPeers watches peersPath for mtime changes and re-applies the peer set on
// each change. It uses mtime polling (not inotify) because the file is written
// via atomic rename by meshd — inotify on the file descriptor would not fire
// across a rename. This mirrors the approach used by ingressd and proxyd.
func pollPeers(ctx context.Context, dev *device.Device, peersPath string) {
	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()
	var lastMtime time.Time
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			info, err := os.Stat(peersPath)
			if err != nil {
				continue
			}
			if !info.ModTime().After(lastMtime) {
				continue
			}
			lastMtime = info.ModTime()
			applyPeers(dev, peersPath)
		}
	}
}
