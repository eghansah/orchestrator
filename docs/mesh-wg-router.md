# Mesh WireGuard Router (`meshrouterd`) — Design

> How the mesh's cross-node data path is built without the orchestrator ever
> entering a network namespace by hand. The orchestrator stays in control; a
> small managed container carries the actual WireGuard tunnel. Read
> [`mesh-network.md`](mesh-network.md) first for the why; this doc is the how for
> the container approach (option B).

## The problem this solves

A container on `mesh0` (node A) gets a `100.64.x` address, but it can't yet reach
a container on node B. For that, `wg0` and the route `meshCIDR → wg0` must live
**inside the RootlessKit network namespace** — the same namespace where the
`mesh0` bridge lives and where containers actually route — and inbound WireGuard
UDP must be forwarded from the host into that namespace.

The orchestrator process runs in the **host** netns, outside RootlessKit. The two
hard parts of doing this from the orchestrator directly are (1) entering the
RootlessKit userns+netns from Go and (2) forwarding inbound UDP into it. Both are
things **nerdctl/RootlessKit already automate** for containers. So we let a
container do it.

```
            host netns                         RootlessKit netns
 ┌───────────────────────────┐      ┌────────────────────────────────────┐
 │ orchestrator (meshd)      │      │  mesh0 bridge  100.64.A.1/24        │
 │  - owns WG keypair        │      │     ├── web   100.64.A.2            │
 │  - derives peers from Raft│ file │     └── db    100.64.A.3            │
 │  - writes peers config ───┼─────▶│  meshrouterd (container)           │
 │                           │  -v  │     wg0  +  route meshCIDR→wg0      │
 └───────────────────────────┘      └──────────────┬─────────────────────┘
        host :51820/udp ───────(RootlessKit -p)────┘   encrypted to peers
```

## Components

| Piece | Where | Role |
|---|---|---|
| `internal/meshd` (`Manager`) | orchestrator process | Owns the keypair, derives the peer set from Raft, renders config. **Unchanged in spirit** — only its `Device` backend changes (see below). |
| `meshrouterd` | managed container, `--network host` (= RootlessKit netns) | Holds `wg0`, the `meshCIDR → wg0` route, and the WireGuard UDP listener. Reloads peers from a watched file. |
| peers config file | `<dataDir>/mesh/peers.conf` (bind-mounted) | The dynamic contract between the two. meshd writes; `meshrouterd` reloads. |
| `reconcileMeshRouter` | orchestrator process | Supervises the container (pull, run, replace on change) — modeled exactly on `reconcileIngressd`. |

`meshrouterd` is a userspace WireGuard runner: it imports the same
`golang.zx2c4.com/wireguard` device + `internal/meshd` rendering already in the
tree, creates a **real** `wg0` TUN (`tun.CreateTUN`), assigns the interface,
installs the route via netlink, and feeds peers to the device over its UAPI.
No kernel WireGuard module is required.

## Why a container (recap of the trade-off)

The two hardest, most failure-prone parts of the hand-rolled `setns` approach are
solved here by the platform:

- **Netns entry:** a rootless container with `--network host` *is* the RootlessKit
  netns. nerdctl/RootlessKit handle all userns/netns entry. No `setns`, no
  `nsenter` helper, no child-PID discovery.
- **Inbound UDP:** `-p 51820:51820/udp` makes RootlessKit's port driver forward
  host UDP into the netns. No RootlessKit API calls.

It also fits the established pattern: `ingressd`, `proxyd`, and the local registry
are already separate, config-file-driven components the orchestrator supervises.
`meshrouterd` is the same shape — meshd writes a config file (like `proxycfg` /
`ingresscfg`), the container watches and reloads it.

## How the container is run

### Why not `--network host -p`

Inspection of the nerdctl v2.3.1 OCI hook (`pkg/ocihook/ocihook.go`) shows that
port forwarding (`exposePortsRootless` → rootlesskit port API) is only wired up
when `opts.cni != nil`. For `--network host` the hook takes the `NOP` branch
(`nettype.Host`), so `opts.cni` is never set and `-p` flags are silently ignored.
**`-p 51820:51820/udp` with `--network host` does nothing in rootless nerdctl.**

### Correct approach: `--network ns:<path>` + direct rootlesskit port API call

`nerdctl run --network ns:<path>` is the supported way to place a container in an
arbitrary existing network namespace. The RootlessKit netns path is always at
`$XDG_RUNTIME_DIR/containerd-rootless/netns` (confirmed present on this host).
Using this instead of `--network host` puts meshrouterd in the same netns where
`mesh0` lives — identical semantics, but nerdctl actually honours it.

For inbound WireGuard UDP, meshd calls the rootlesskit API socket directly
(`$XDG_RUNTIME_DIR/containerd-rootless/api.sock`, HTTP over Unix) to register
the port forward — the same call nerdctl makes for bridge containers, just
invoked by meshd rather than by the CNI hook. The running rootlesskit on this
host uses the `builtin` driver with `protos: [tcp, tcp4, tcp6, udp, udp4, udp6]`
— UDP is supported.

`reconcileMeshRouter` (mirrors `reconcileIngressd`) pulls the embedded image and
runs:

```
nerdctl run -d --name meshrouterd \
  --restart always \
  --network ns:$XDG_RUNTIME_DIR/containerd-rootless/netns \   # explicit RootlessKit netns
  --cap-add NET_ADMIN \                  # namespaced cap; create TUN + routes
  --device /dev/net/tun \
  --insecure-registry \
  -v <dataDir>/mesh:/data/mesh \         # private key + peers.conf, watched
  <registry>/meshrouterd:<version> \
  --mesh-addr 100.64.A.1 --mesh-cidr 100.64.0.0/10 \
  --listen-port 51820 --config /data/mesh/peers.conf \
  --private-key-file /data/mesh/private.key
```

Immediately after starting the container, `reconcileMeshRouter` calls the
rootlesskit API to add the UDP port forward:

```
POST http+unix://$XDG_RUNTIME_DIR/containerd-rootless/api.sock/v1/ports
{"proto":"udp","parentIP":"","parentPort":51820,"childIP":"","childPort":51820}
```

On container removal the corresponding DELETE call removes it.

Static facts (mesh address, CIDR, listen port, netns path) are **run args** —
they change only when the leader re-slots the node, which triggers a container
replace. The dynamic peer set is the **watched file**, reloaded in place without
restarting.

Replacement detection reuses the `reconcileIngressd` mechanism: persist a small
`<dataDir>/system/meshrouterd.json` recording the image digest + run args; replace
the container when the embedded image digest or any static arg changes.

## The config contract

**Static, passed as args/files (rarely change):**
- `--private-key-file` → `<dataDir>/mesh/private.key` (0600, bind-mounted). The
  key never leaves the node; the container shares the node's trust boundary.
- `--mesh-addr` → the node's own mesh address (the `mesh0` gateway, `.1`).
- `--mesh-cidr` → the cluster mesh CIDR (default `100.64.0.0/10`).
- `--listen-port` → WireGuard UDP port (≥1024).

**Dynamic, watched file `peers.conf` (changes as nodes join/leave):**
A wireguard-go UAPI fragment — exactly what `meshd.RenderUAPI` already produces for
the peer section (per peer: `public_key`, `endpoint`, `allowed_ip = <remote /24>`,
`persistent_keepalive_interval`). On change, `meshrouterd` re-applies it with
`device.IpcSet` (declarative `replace_peers=true`), so peer churn never drops the
tunnel.

**Reload mechanism: mtime polling, not inotify.** Per the known gotcha that
ingressd already hit, inotify can't cross the container's bind mount, and atomic
tmp+rename writes break file-level watches. meshd writes `peers.conf` atomically;
`meshrouterd` polls its mtime (exactly as ingressd polls its config) and reloads
on change.

## Routing model

- `mesh0` bridge owns the node's `/24`; gateway `.1` = the node's `MeshAddr`.
  Local-`/24` traffic is directly connected and never touches `wg0`.
- `wg0` is an **unnumbered** tunnel. One route installed in the RootlessKit netns:
  `meshCIDR (/10) → wg0`. Longest-prefix match means the connected local `/24`
  wins for local traffic; everything else in the `/10` goes to `wg0`.
- Per-peer cryptokey routing: each peer's `allowed_ip` is that node's `/24`, so
  WireGuard sends a packet for `100.64.B.x` to node B and accepts return traffic
  only from B's `/24`.
- `meshrouterd` enables `net.ipv4.ip_forward=1` **in the RootlessKit netns**
  (namespaced sysctl, settable rootless) so the bridge↔`wg0` hand-off forwards.

Worked path — A's `web` (100.64.A.2) → B's `db` (100.64.B.3):
bridge → route `meshCIDR→wg0` (B.3 ∉ local /24) → WG encrypt (peer B `allowed_ip`
`100.64.B.0/24` matches) → host UDP → node B → B's `wg0` → B routes B.3 as
connected on its bridge → `db`.

## Change to `internal/meshd`

The `Manager` and its reconcile loop stay. We add a third `Device` backend
alongside `NewNetstackDevice` (process-only) and the future kernel-TUN: a
**file-writing device** whose `Configure(uapi)` writes the peer section to
`<dataDir>/mesh/peers.conf` atomically. A `--mesh-backend=container|netstack` flag
(default `container` on Linux rootless) selects it. With `container`, meshd:

1. Loads/publishes the keypair (already done — step 3).
2. Writes `private.key` for the bind mount (0600).
3. On each reconcile, renders the peer UAPI and writes `peers.conf` if changed.

So meshd's job becomes "publish identity + maintain the peers file"; the container
owns the data plane. The netstack backend remains useful for giving the
*orchestrator process itself* mesh reachability (e.g. ingress/proxyd dialing mesh
addresses, step 7) and for tests.

## Build & packaging

Mirror `ingressd`:
- New `cmd/meshrouterd` binary, built static (`CGO_ENABLED=0`).
- Image built by the existing `cmd/build-image` pure-Go builder into an embedded
  tar (`localregistry.MeshrouterdTar`), tagged `meshrouterd:<version>`, served by
  the built-in local registry and pulled with `--insecure-registry`.
- Digest-based replace, same as ingressd (`ingressd:<version>` tag + manifest
  digest comparison in `reconcileIngressd`).

## Security considerations

- `--cap-add NET_ADMIN` + `--device /dev/net/tun`: namespaced within the
  RootlessKit userns — **no host root**, consistent with the usermode constraint.
- The private key lives in a `0600` file on a bind mount readable only by the node
  user and the container. It never enters Raft, env vars (which show in
  `inspect`), or logs.
- The container is on `--network host` (RootlessKit netns), so it shares the
  containers' network view — intended, since it *is* their router. It runs no
  listener other than the WireGuard UDP port.

## Open questions — validate on a live rootless host

1. ~~**`-p …/udp` with `--network host` (rootless).**~~ **Resolved (source
   inspection).** nerdctl's OCI hook skips `exposePortsRootless` for
   `--network host` (NOP branch, `opts.cni == nil`). `-p` is silently ignored.
   Fix: use `--network ns:<rlk-netns-path>` + direct rootlesskit API call
   (see "How the container is run" above).

2. ~~**`net.ipv4.ip_forward` in the RootlessKit netns.**~~ **Resolved (code
   analysis).** RootlessKit creates the netns owned by the user's uid mapping;
   `ip_forward` is per-netns and writable by a process with `CAP_NET_ADMIN` in
   that netns. `--cap-add NET_ADMIN` grants the namespaced capability inside
   meshrouterd's userns. The code already treats the write as advisory:
   `slog.Warn(…)` on failure, not fatal — so a host that pre-enables ip_forward
   continues without error. Live check:
   ```
   nsenter --net=$XDG_RUNTIME_DIR/containerd-rootless/netns \
     cat /proc/sys/net/ipv4/ip_forward
   ```
   Expected: `1` after meshrouterd starts (or already `1` if the host enables it).

3. ~~**MTU propagation.**~~ **Fixed in code.** `EnsureMeshNetwork` now passes
   `--opt com.docker.network.driver.mtu=1380` when creating the `mesh0` bridge
   so container MTU matches the WireGuard tunnel MTU (1380). meshrouterd creates
   `wg0` at the same MTU via `--mtu 1380` (default). Live check:
   ```
   # On the node, after mesh0 is created:
   ip link show mesh0           # should show mtu 1380
   ip link show wg0             # should show mtu 1380
   # Cross-node large-frame test (replace IPs):
   ping -c 5 -s 1350 <peer-mesh-ip>   # should succeed without fragmentation
   ```

4. **`/dev/net/tun` availability rootless.** Live check only. If `/dev/net/tun`
   is absent or not group-accessible the meshrouterd container will fail to
   start with "operation not permitted". Validation:
   ```
   ls -la /dev/net/tun          # expect crw-rw-rw- or crw-rw---- with group tun
   # If crw-rw---- and user not in tun group:
   sudo usermod -aG tun $USER   # then re-login
   ```
   Once the container starts, confirm:
   ```
   nerdctl --namespace orchestrator inspect meshrouterd \
     --format '{{.State.Status}}'   # should be "running"
   ip link show wg0                  # should appear in RootlessKit netns
   ```

5. ~~**Container restart / key persistence.**~~ **Resolved (code analysis).**
   `meshd.LoadOrCreateKey` writes `<dataDir>/mesh/private.key` on first run and
   reads it on every subsequent start. The meshrouterd container bind-mounts
   the same file at `/data/mesh/private.key`. On container restart via
   `--restart always`, meshrouterd reads the unchanged key → same WireGuard
   public key → peers already have the public key in their allowed-IPs tables →
   sessions resume after a normal re-handshake (no storm). The orchestrator
   daemon also reads the same key so its self-registration always advertises the
   stable public key.

## Rollout / coexistence

Additive, like the rest of the mesh work. With `--mesh` off (default), nothing
changes. With `--mesh` on and backend `container`, containers gain mesh
reachability *in addition to* their existing loopback + proxy path; the east-west
`ForwardHTTP`/`SystemPort` retirement (step 7) only happens once the mesh path is
trusted.

## Tasks (step 4b)

1. `internal/meshd`: file-writing `Device` backend + `--mesh-backend` flag; write
   `private.key` and atomic `peers.conf`.
2. `cmd/meshrouterd`: TUN bring-up, interface addr, `meshCIDR→wg0` route, ip_forward,
   UAPI peer apply, mtime-poll reload of `peers.conf`.
3. Image: build pipeline + embedded tar + local-registry serving.
4. `reconcileMeshRouter` in `cmd/orchestrator/main.go` (model on `reconcileIngressd`):
   - run with `--network ns:$XDG_RUNTIME_DIR/containerd-rootless/netns`
   - after start: POST to rootlesskit API socket to register UDP 51820
   - on removal: DELETE the same port entry
5. Validate remaining open questions (2–5) on a real node; iterate.
6. Then revisit step 5 (replicas) and step 6 (`*.mesh` DNS) on top of a working path.
