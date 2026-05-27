# Production Operations Guide

## Overview

The orchestrator is a usermode-only distributed container runtime. Every node runs the same binary. The cluster elects a Raft leader that owns the control plane; workloads run on any node including the leader. There are no privileged daemons, no iptables rules, and no root requirements at runtime.

**What it does:**
- Schedules containers and Compose stacks across a cluster via `nerdctl` (rootless)
- Maintains desired state in a replicated Raft log — cluster survives leader failover
- Routes inbound HTTP traffic to containers via a built-in ingress proxy
- Provides named TCP service endpoints with DNS resolution (`api.svc.local`)
- Cross-node container traffic tunnels through mTLS gRPC — nothing is exposed on the LAN except the gRPC port and the ingress proxy

**What it does not do:**
- No Kubernetes compatibility layer
- No image building
- No pod networking / CNI — containers communicate via the service discovery layer
- No persistent volume management

---

## Prerequisites

Every node needs:

| Requirement | Notes |
|---|---|
| Linux kernel ≥ 5.11 | cgroup v2 + user delegation required |
| `nerdctl` in rootless mode | Installed per-user, not system-wide |
| `containerd` rootless | Via `containerd-rootless-setuptool.sh` |
| `slirp4netns` or `pasta` | Network backend for rootless containers |
| `newuidmap` / `newgidmap` | Usually in `uidmap` package |
| Go 1.21+ | Build-time only |

**Install rootless nerdctl on each node:**
```bash
# Install containerd rootless (as the service user, not root)
curl -sSL https://raw.githubusercontent.com/containerd/nerdctl/main/extras/rootless/containerd-rootless-setuptool.sh | bash

# Verify
nerdctl run --rm hello-world
```

---

## Building

```bash
git clone https://github.com/eghansah/orchestrator
cd orchestrator

go build -o bin/orchestrator ./cmd/orchestrator
go build -o bin/ctl ./cmd/ctl

# Copy binaries to each node
scp bin/orchestrator user@node2:~/bin/
scp bin/ctl          user@node2:~/bin/
```

---

## Single-node quickstart

```bash
./bin/orchestrator \
  --bootstrap \
  --data-dir /var/lib/orchestrator \
  --grpc-addr 0.0.0.0:7946 \
  --raft-addr 0.0.0.0:7947 \
  --ingress-addr 0.0.0.0:8080 \
  --dns-addr :5353 \
  --web-addr :7948
```

The node bootstraps a single-node Raft cluster, generates a self-signed TLS identity, and prints a join token:

```
*** Join token: a3f9c2d1e4b8071f ***
Joining nodes require: --join-token a3f9c2d1e4b8071f
```

Save this token — it is also written to `<data-dir>/join-token` for recovery.

Verify the node is up:
```bash
./bin/ctl status
```

---

## Multi-node cluster

### Starting the first node (bootstrap)

```bash
./bin/orchestrator \
  --bootstrap \
  --node-id node1 \
  --grpc-addr 192.168.1.10:7946 \
  --raft-addr 192.168.1.10:7947 \
  --data-dir /var/lib/orchestrator \
  --ingress-addr 0.0.0.0:8080 \
  --dns-addr :5353
```

Note the join token from the output (or read it from `/var/lib/orchestrator/join-token`).

### Adding subsequent nodes

On each additional node, run without `--bootstrap`, pointing `--join` at the first node's gRPC address:

```bash
./bin/orchestrator \
  --node-id node2 \
  --grpc-addr 192.168.1.11:7946 \
  --raft-addr 192.168.1.11:7947 \
  --data-dir /var/lib/orchestrator \
  --join 192.168.1.10:7946 \
  --join-token a3f9c2d1e4b8071f \
  --ingress-addr 0.0.0.0:8080 \
  --dns-addr :5353
```

The joining node heartbeats the bootstrap node, which adds it as a Raft voter and registers it in the cluster state. Repeat for every additional node.

Verify all nodes appear:
```bash
./bin/ctl nodes
```

### Leader failover

Leadership is automatic. If the leader node goes down, the remaining nodes elect a new leader within seconds (Raft default timeout). There is no manual intervention required. Workloads already running on surviving nodes continue running; workloads that were on the failed node stay in `scheduled` phase until you drain/reschedule them or the node rejoins.

---

## Running as a systemd service

Create `/etc/systemd/user/orchestrator.service` on each node (runs as the service user, no root):

```ini
[Unit]
Description=Orchestrator node daemon
After=network-online.target
Wants=network-online.target

[Service]
ExecStart=/home/deploy/bin/orchestrator \
  --node-id %H \
  --grpc-addr %i:7946 \
  --raft-addr %i:7947 \
  --data-dir %h/.local/share/orchestrator \
  --join FIRST_NODE_IP:7946 \
  --join-token TOKEN \
  --ingress-addr 0.0.0.0:8080 \
  --dns-addr :5353
Restart=on-failure
RestartSec=5s

[Install]
WantedBy=default.target
```

Replace `FIRST_NODE_IP` and `TOKEN`. For the bootstrap node, add `--bootstrap` on the first start only and then remove it (the Raft log persists across restarts).

```bash
systemctl --user daemon-reload
systemctl --user enable --now orchestrator

# Follow logs
journalctl --user -u orchestrator -f
```

---

## Flag reference

| Flag | Default | Description |
|---|---|---|
| `--node-id` | hostname | Unique identifier for this node |
| `--grpc-addr` | `127.0.0.1:7946` | gRPC listen address (set to `0.0.0.0:7946` to accept cluster traffic) |
| `--raft-addr` | `127.0.0.1:7947` | Raft TCP transport address (must be reachable by all nodes) |
| `--data-dir` | `~/.local/share/orchestrator` | Persistent storage: Raft log, BoltDB, snapshots, TLS cert |
| `--bootstrap` | false | Bootstrap a brand-new cluster (use exactly once on the first node) |
| `--join` | — | gRPC address of an existing node to join through |
| `--join-token` | — | Cluster secret; loaded from `<data-dir>/join-token` if omitted |
| `--admin-token` | auto-generated | Bearer token for ControlService RPCs and web console; loaded from `<data-dir>/admin-token` if omitted |
| `--ingress-addr` | `:8080` | HTTP ingress proxy listen address; empty to disable |
| `--dns-addr` | disabled | DNS listen address for `svc.local` zone, e.g. `:5353` |
| `--web-addr` | `:7948` | Web console listen address; empty to disable |
| `--data-addr` | auto-detected | Routable IP used as the ingress/service backend address |
| `--nerdctl` | `nerdctl` | Path to the nerdctl binary |
| `--namespace` | `orchestrator` | nerdctl namespace for managed containers |

---

## Ports

| Port | Protocol | Purpose |
|---|---|---|
| 7946 | TCP (gRPC/mTLS) | ctl-to-node and node-to-node RPC |
| 7947 | TCP | Raft consensus transport |
| 7948 | HTTP | Web console |
| 8080 | HTTP | Ingress proxy (inbound user traffic) |
| 5353 | UDP+TCP | DNS server for `svc.local` (optional) |
| 30000–32767 | TCP/UDP | Auto-assigned container host ports (loopback only) |
| 40000–42767 | TCP | Auto-assigned service endpoint ports |

All container ports bind on `127.0.0.1` only — they are not reachable from the network. External access goes through the ingress proxy (HTTP) or the service layer (TCP).

---

## Security

### Admin token (ControlService + web console)

All `ctl` commands and web console API calls require a bearer token. On first bootstrap the token is auto-generated, printed to stdout, and saved to `<data-dir>/admin-token`:

```
*** Admin token: a3f9c2d1e4b8071f2c4e6a8b0d3f5e7c ***
Store this in ~/.config/orchestrator/token or use: --token a3f9c2d1e4b8071f2c4e6a8b0d3f5e7c
```

**Distribute the token to operators:**
```bash
# Save for ctl use (picked up automatically)
mkdir -p ~/.config/orchestrator
cat /var/lib/orchestrator/admin-token > ~/.config/orchestrator/token
chmod 600 ~/.config/orchestrator/token

# Or set per-session
export ORCHESTRATOR_TOKEN=$(cat /var/lib/orchestrator/admin-token)

# Or pass explicitly
ctl --token <token> ps
```

**Token rotation:** replace `<data-dir>/admin-token` with a new random value and restart the orchestrator. All connected clients need to update their stored token. Currently a single token is supported — all operators share it.

### mTLS between nodes

Every node generates a self-signed ECDSA P-256 certificate on first start, stored in `<data-dir>/tls.{crt,key}`. All node-to-node gRPC calls use mutual TLS with cert pinning — each node's cert DER is stored in the Raft state after the first heartbeat, and subsequent connections are verified against it.

The `ctl` client uses TLS without cert verification (it is a local admin tool). For remote `ctl` access, put a TLS-terminating reverse proxy in front of the gRPC port or use SSH tunnelling.

### Join token

The join token is a 128-bit random hex string generated on bootstrap and written to `<data-dir>/join-token`. Every incoming heartbeat from a new node is checked against this token. Nodes that present the wrong token are rejected with `Unauthenticated`.

If the token file is lost: stop all nodes, delete all `<data-dir>/join-token` files, restart the bootstrap node (it regenerates and prints a new token), then rejoin all other nodes with the new token. The Raft log and workload state are preserved.

### Firewall rules

Only the following ports need to be reachable between cluster nodes:
- `7946/tcp` — gRPC (mTLS-protected)
- `7947/tcp` — Raft

The ingress proxy (`8080`) and web console (`7948`) should be behind a load balancer or firewall that restricts access as appropriate for your environment. The DNS port (`5353`) only needs to be reachable from containers on the same host.

---

## CLI reference

All commands default to `localhost:7946`. Use `--server HOST:PORT` to point at a remote node.

```bash
ctl [--server HOST:PORT] COMMAND [flags]
```

### Workloads

```bash
# Run a container (host port is auto-assigned from 30000–32767)
ctl run --name nginx nginx:alpine -p 80

# Run with environment variables and a volume
ctl run --name api myimage:latest \
  -p 3000 \
  -e DATABASE_URL=postgres://db:5432/app \
  -v /data/uploads:/app/uploads

# Deploy a Compose stack
ctl stack --name myapp -f docker-compose.yml

# List all workloads
ctl ps

# Filter by phase
ctl ps --phase running

# Show full cluster state
ctl status

# Remove a workload
ctl rm WORKLOAD_ID [WORKLOAD_ID...]
```

### Nodes

```bash
# List nodes
ctl nodes

# Mark a node as draining (stops new placements)
ctl drain NODE_ID
```

### Ingress (HTTP routing)

The ingress proxy matches requests by `Host` header and URL path prefix, longest-prefix-wins.

```bash
# Route all traffic to a workload
ctl ingress create --workload WORKLOAD_ID --port 80

# Route by host
ctl ingress create --workload WORKLOAD_ID --port 3000 --host api.example.com

# Route by path prefix
ctl ingress create --workload WORKLOAD_ID --port 8080 --path /app

# List rules
ctl ingress list

# Delete a rule
ctl ingress delete RULE_ID
```

After creating an ingress rule, send traffic to any node's ingress address (e.g. `http://192.168.1.10:8080`). The proxy will route to the correct container even if it is running on a different node — cross-node requests tunnel through mTLS gRPC automatically.

### Service discovery (TCP + DNS)

Services provide stable named endpoints for TCP traffic (databases, caches, internal APIs). The system auto-assigns a port in the 40000–42767 range.

```bash
# Expose a workload's port as a named service
ctl service create --name db --workload WORKLOAD_ID --port 5432
# → created service abc123ef (system port 40000)

# List services
ctl service list

# Delete a service
ctl service delete SERVICE_ID
```

From inside any container (glibc-based), the service is reachable by name:
```bash
# DNS resolves api.svc.local to the node's data IP
psql -h db.svc.local -p 40000 mydb

# Or query the SRV record to discover the port dynamically
dig _db._tcp.svc.local SRV
```

For musl/Alpine containers where `options port:N` is not supported, connect directly using the node's data IP and the system port shown in `ctl service list`.

---

## Web console

The web console is available at `http://<node>:7948`. It provides:
- Cluster overview (leader, node count, workload phases)
- Workload list with port allocation details
- Node list with resource info
- Ingress rule management (create, delete)
- Service management (create, delete)

The console reads state from the local node; it shows the same view as `ctl status`.

---

## Operations playbook

### Adding a node mid-cluster

Start the new node with `--join` pointing at any existing node (not just the leader — any node will report the current leader address back). No intervention needed on existing nodes.

### Removing a node

1. Drain the node to stop new placements: `ctl drain NODE_ID`
2. Manually reschedule any workloads that were on it: `ctl rm` the old workload IDs and resubmit
3. Stop the orchestrator process on the removed node
4. The node will eventually appear as `unreachable` in `ctl nodes`

There is currently no automated rescheduling on node failure — the scheduler places workloads on demand; it does not re-converge after a node loss.

### Restarting a node

Just restart the binary with the same flags (no `--bootstrap`). The node replays its Raft log, reconnects to the cluster, and resumes participating within a few seconds. Containers that were running before the restart are not affected (they continue running under `nerdctl`/`containerd`).

### Upgrading the binary

Rolling upgrade is safe:
1. Rebuild the binary
2. On each non-leader node: stop orchestrator, replace binary, restart
3. On the leader: stop orchestrator (triggers leader election), replace binary, restart
4. Verify with `ctl status`

The Raft log and BoltDB store are forward-compatible as long as the command types and state schema have not changed.

### Backup and restore

The full cluster state is in the Raft log. Back up `<data-dir>/raft/` on every node. The most important files are `raft.db` (BoltDB) and the `snapshots/` directory.

To restore a node from backup: stop the process, replace `<data-dir>/raft/` with the backup, restart. If restoring the entire cluster from a snapshot, restore on all nodes simultaneously to avoid log divergence.

---

## Troubleshooting

**Node shows as `unreachable` but is running**

Check that `--grpc-addr` is set to a routable address (not `127.0.0.1`) and that port 7946 is reachable between nodes.

**Workload stuck in `scheduled` phase**

The leader sent a placement RPC to the agent but did not get a success response. Check the target node's logs for nerdctl errors. Common causes: image pull failure, port already in use, invalid compose YAML.

**`not the leader` error from ctl**

The node you are talking to is not currently the leader. Use `ctl status` to find the current leader's address, then: `ctl --server LEADER_ADDR:7946 ...`. Alternatively, any command that hits a non-leader will report the leader address in the error message.

**Ingress returns 502 Bad Gateway**

- `no matching ingress rule` — no rule matches the Host header + path. Check `ctl ingress list`.
- `workload not scheduled` — the target workload has no `node_id`. It may be in `pending` or `failed` phase.
- `no allocated port for container port` — the port in the ingress rule does not match any port the container declared with `-p`.

**DNS not resolving `*.svc.local`**

Confirm `--dns-addr` is set on the node and the container was started after DNS was enabled (DNS flags are injected at container start time — existing containers need to be restarted). Verify with:
```bash
nerdctl --namespace orchestrator exec CONTAINER_NAME -- cat /etc/resolv.conf
# Should contain: nameserver <dataIP> and options port:5353
```

**Join token rejected**

The joining node's `--join-token` does not match the cluster's stored token. Read the correct token from `<data-dir>/join-token` on the bootstrap node.
