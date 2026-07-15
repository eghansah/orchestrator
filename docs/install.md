# Air-Gapped Install

This guide is for orchestrator nodes with **no internet access at all**. If your target machines can reach the internet and you're comfortable installing rootless nerdctl yourself, follow [deploy.md](deploy.md) §1 directly instead — it's simpler and doesn't need the larger offline bundle described here.

Everything a target node needs — including the rootless container runtime itself (containerd, runc, CNI plugins, RootlessKit) — is fetched once on a machine that *does* have internet, bundled into a single tarball, and transferred to the air-gapped node. `install.sh` never makes a network call, and it never runs as root or invokes `sudo` — it refuses to run at all if launched as root. Everything it does is confined to the invoking user's own home directory and `systemd --user` scope.

## 1. On a build machine (has internet)

```bash
make offline-release
```

This produces `dist/orchestrator-offline-<version>-linux-{amd64,arm64}.tar.gz` and `dist/checksums-offline.sha256`. Each tarball bundles:

- `orchestrator`, `ctl`, `proxyd` — the same binaries as the regular `make release` tarball
- `nerdctl-full-linux-<arch>.tar.gz` — a vendored copy of the upstream [nerdctl-full](https://github.com/containerd/nerdctl/releases) release (containerd, runc, CNI plugins, RootlessKit, `containerd-rootless-setuptool.sh`)
- `install.sh` and this doc
- `README.md`, `deploy.md`, `production.md`

The vendored nerdctl version and its checksum are pinned in [`mk/nerdctl-checksums.mk`](../mk/nerdctl-checksums.mk) — check that file to see exactly what shipped, independent of anything printed at build time. A maintainer bumps `NERDCTL_VERSION` in the `Makefile` and updates that checksum file together whenever the vendored version changes.

## 2. Transfer

Copy the tarball plus `checksums-offline.sha256` to each target node by whatever offline means you have (USB drive, internal file share, etc). Verify on arrival before extracting:

```bash
sha256sum -c checksums-offline.sha256
```

## 3. On each air-gapped node

```bash
tar xzf orchestrator-offline-<version>-linux-<arch>.tar.gz
cd orchestrator-offline-<version>-linux-<arch>
./install.sh [flags]
```

`install.sh` auto-detects that the vendored `nerdctl-full-linux-*.tar.gz` sits next to it and installs from that bundle — it does not need or attempt any network access.

### Flags

| Flag | Purpose |
|---|---|
| `--bootstrap` | Bootstrap a brand-new cluster (use once, on the first node) |
| `--join HOST:PORT` | Join an existing cluster through this node's gRPC address |
| `--join-token TOKEN` | Cluster join secret |
| `--node-id`, `--grpc-addr`, `--raft-addr`, `--web-addr`, `--ingress-addr`, `--ingress-tls-addr`, `--dns-addr`, `--data-dir`, `--admin-token`, `--namespace` | Map 1:1 to the orchestrator flags in [production.md's flag reference](production.md#flag-reference) — see there for full semantics |
| `--nerdctl PATH` | Use this nerdctl binary instead of the vendored/auto-detected one |
| `--dry-run` | Print what would happen without doing it |
| `--skip-runtime-setup` | Don't touch the container runtime; assume nerdctl already works |

Run `./install.sh --help` for the full list.

## 4. Multi-node walkthrough

Same three-node topology as [deploy.md](deploy.md) §4-6, expressed as `install.sh` invocations instead of manual build+scp+hand-edited-unit-file steps.

**node1** (192.168.1.10):

```bash
./install.sh --bootstrap --node-id node1 --grpc-addr 192.168.1.10:7946 --raft-addr 192.168.1.10:7947
```

The install prints the join token (also saved to `<data-dir>/join-token`) and, at the end, the exact command to run on the next node.

**node2, node3** (repeat with each node's own address):

```bash
./install.sh --node-id node2 --grpc-addr 192.168.1.11:7946 --raft-addr 192.168.1.11:7947 \
  --join 192.168.1.10:7946 --join-token <token-from-node1>
```

Each run is idempotent — re-running `install.sh` with the same flags is safe and only restarts services whose configuration actually changed.

## Out of scope

`install.sh` sets up the rootless container runtime and the orchestrator/proxyd systemd services only, and does so entirely as an unprivileged user — it never runs as root and never shells out to `sudo`. A couple of one-time host settings are still genuinely root-only; `install.sh` only *checks* for them and, if missing, fails (or warns) with the exact command an administrator needs to run once, out-of-band:

- Cgroup v2 delegation (`cpu`, `memory`, `pids` controllers on the user slice) — required, `install.sh` fails with the `sudo` command to hand to an administrator if missing
- Lingering (`loginctl enable-linger`) so services survive logout — optional; `install.sh` warns but continues if it's not enabled
- Firewall rules — [deploy.md §3](deploy.md#3-configure-firewall)
- Host HAProxy for ports 80/443 — [deploy.md §9](deploy.md#9-deploy-proxyd-and-ingressd) (stays root-owned and manual, consistent with this project's usermode-only design)
- `ingressd` running directly on the host instead of as an auto-deployed container — see [docs/ingressd.service](ingressd.service) and [production.md](production.md)

## Troubleshooting

**"newuidmap not found" / "no /etc/subuid entry"**

These must come from your OS's own trusted package (`shadow-utils` on Fedora/RHEL, `uidmap` on Debian/Ubuntu) or an existing `/etc/subuid`/`/etc/subgid` entry. `install.sh` deliberately does not install packages — stage the package through your normal air-gapped provisioning process (internal mirror, pre-baked image, etc.) and re-run.

**"cgroup controllers ... are not delegated"**

`install.sh` cannot fix this itself — delegating cgroup controllers to a user slice is a root-only, host-wide systemd change. Have an administrator run the `sudo` commands printed in the error (writes `/etc/systemd/system/user@.service.d/delegate.conf` and reloads systemd), then re-run `install.sh`.

**`containerd-rootless-setuptool.sh install` fails**

Check `journalctl --user -u containerd -n 50`. Common causes: `XDG_RUNTIME_DIR` not writable (usually means lingering isn't enabled yet, or you're in an `su`/`sudo` shell rather than a real login session for the target user), or cgroup v2 delegation missing (see above).

**Which mode did install.sh use?**

It prints `mode: offline` or `mode: online` as its first line of output.

**Re-running safely**

`install.sh` is idempotent: preflight checks, the runtime bundle extraction, and systemd unit installation are all skipped or no-ops when already satisfied. Re-run any time with the same or updated flags.
