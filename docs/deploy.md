# Deployment Guide

This guide walks through deploying a production orchestrator cluster on Linux servers. The example uses three nodes — adjust for your topology.

> For a full flag reference, security details, and operations playbook see [production.md](production.md).

---

## 1. Prepare each node

### OS and kernel

Requires Linux kernel ≥ 5.11 with cgroup v2 and user namespace delegation enabled. Verify:

```bash
# cgroup v2
stat -f -c '%T' /sys/fs/cgroup
# → tmpfs means v2 is active

# User namespace delegation (Fedora/RHEL — path may differ on Ubuntu)
cat /sys/fs/cgroup/user.slice/user-$(id -u).slice/cgroup.controllers
# Should include: cpu memory pids
```

If delegation is missing, add it as root (once, survives reboots):

```bash
mkdir -p /etc/systemd/system/user@.service.d
cat > /etc/systemd/system/user@.service.d/delegate.conf <<'EOF'
[Service]
Delegate=cpu cpuset io memory pids
EOF
systemctl daemon-reload
```

### Install rootless nerdctl (run as the service user, not root)

```bash
# Install uidmap (provides newuidmap/newgidmap)
# Fedora/RHEL:
sudo dnf install -y shadow-utils slirp4netns
# Ubuntu/Debian:
sudo apt-get install -y uidmap slirp4netns

# Enable lingering so the user's systemd session survives logout
sudo loginctl enable-linger $USER

# Install containerd rootless + nerdctl
curl -sSL https://raw.githubusercontent.com/containerd/nerdctl/main/extras/rootless/containerd-rootless-setuptool.sh | bash

# Verify
nerdctl run --rm hello-world
```

---

## 2. Build and distribute binaries

On any machine with Go 1.21+:

```bash
git clone https://github.com/eghansah/orchestrator
cd orchestrator
go build -o bin/orchestrator ./cmd/orchestrator
go build -o bin/ctl         ./cmd/ctl
```

Copy to each node:

```bash
for NODE in node1 node2 node3; do
  ssh $NODE mkdir -p ~/bin
  scp bin/orchestrator bin/ctl $NODE:~/bin/
done
```

---

## 3. Configure firewall

Open these ports between all cluster nodes (and from operator machines to node1):

| Port | Protocol | Purpose |
|---|---|---|
| 7946 | TCP | gRPC — ctl and node-to-node RPC |
| 7947 | TCP | Raft consensus |
| 7948 | TCP | Web console (restrict to trusted networks) |
| 8080 | TCP | HTTP ingress (public-facing) |

Example with `firewall-cmd` (Fedora/RHEL):

```bash
sudo firewall-cmd --permanent --add-port=7946-7948/tcp
sudo firewall-cmd --permanent --add-port=8080/tcp
sudo firewall-cmd --reload
```

Example with `ufw` (Ubuntu):

```bash
sudo ufw allow 7946:7948/tcp
sudo ufw allow 8080/tcp
```

---

## 4. Bootstrap the first node

On **node1** (replace `192.168.1.10` with node1's actual IP):

```bash
~/bin/orchestrator \
  --bootstrap \
  --node-id node1 \
  --grpc-addr 192.168.1.10:7946 \
  --raft-addr 192.168.1.10:7947 \
  --data-dir ~/.local/share/orchestrator \
  --ingress-addr 0.0.0.0:8080 \
  --dns-addr :5353 \
  --web-addr :7948
```

It prints three secrets on first boot — **save all three**:

```
*** Join token:            a3f9c2d1e4b8071f               ***
*** Admin token:           1b4fe11674167f1a9c72a78dfb374b ***
*** Web console password:  e4c80cbf01f6f53f8a9c86cecd889b ***
```

The secrets are also written to `~/.local/share/orchestrator/`:

```
join-token       ← share with operators adding new nodes
admin-token      ← distribute to operators using ctl
web-password     ← browser login (username: admin)
```

Verify node1 is healthy:

```bash
export ORCHESTRATOR_TOKEN=$(cat ~/.local/share/orchestrator/admin-token)
~/bin/ctl --server 192.168.1.10:7946 status
```

---

## 5. Join additional nodes

On **node2** (replace addresses and token with your values):

```bash
~/bin/orchestrator \
  --node-id node2 \
  --grpc-addr 192.168.1.11:7946 \
  --raft-addr 192.168.1.11:7947 \
  --data-dir ~/.local/share/orchestrator \
  --join 192.168.1.10:7946 \
  --join-token a3f9c2d1e4b8071f \
  --ingress-addr 0.0.0.0:8080 \
  --dns-addr :5353 \
  --web-addr :7948
```

Repeat for node3 (with `--node-id node3`, `--grpc-addr 192.168.1.12:7946`, `--raft-addr 192.168.1.12:7947`).

Verify all nodes appear:

```bash
~/bin/ctl --server 192.168.1.10:7946 nodes
```

Expected output:

```
ID      ADDRESS               STATUS   CPUS
node1   192.168.1.10:7946     healthy  4
node2   192.168.1.11:7946     healthy  4
node3   192.168.1.12:7946     healthy  4
```

---

## 6. Run as a systemd user service

Create `~/.config/systemd/user/orchestrator.service` on **each node**. The bootstrap node needs `--bootstrap` only on the very first start — remove it after the Raft log is seeded.

**node1** (first start with `--bootstrap`; remove it after):

```ini
[Unit]
Description=Orchestrator node daemon
After=network-online.target
Wants=network-online.target

[Service]
ExecStart=%h/bin/orchestrator \
  --node-id node1 \
  --grpc-addr 192.168.1.10:7946 \
  --raft-addr 192.168.1.10:7947 \
  --data-dir %h/.local/share/orchestrator \
  --ingress-addr 0.0.0.0:8080 \
  --dns-addr :5353 \
  --web-addr :7948
Restart=on-failure
RestartSec=5s

[Install]
WantedBy=default.target
```

**node2 and node3** (replace addresses):

```ini
[Unit]
Description=Orchestrator node daemon
After=network-online.target
Wants=network-online.target

[Service]
ExecStart=%h/bin/orchestrator \
  --node-id node2 \
  --grpc-addr 192.168.1.11:7946 \
  --raft-addr 192.168.1.11:7947 \
  --data-dir %h/.local/share/orchestrator \
  --join 192.168.1.10:7946 \
  --ingress-addr 0.0.0.0:8080 \
  --dns-addr :5353 \
  --web-addr :7948
Restart=on-failure
RestartSec=5s

[Install]
WantedBy=default.target
```

> **Note:** `--join-token` is intentionally omitted here because the token is already saved in `~/.local/share/orchestrator/join-token` from the initial join. The daemon loads it automatically on restart.

Enable and start on each node:

```bash
systemctl --user daemon-reload
systemctl --user enable --now orchestrator

# Follow logs
journalctl --user -u orchestrator -f
```

---

## 7. Configure ctl on operator machines

```bash
# Save the admin token (picked up automatically)
mkdir -p ~/.config/orchestrator
scp node1:~/.local/share/orchestrator/admin-token ~/.config/orchestrator/token
chmod 600 ~/.config/orchestrator/token

# Point ctl at the cluster (any node works; leader is discovered automatically)
export ORCHESTRATOR_SERVER=192.168.1.10:7946

# Verify
ctl --server $ORCHESTRATOR_SERVER status
```

---

## 8. Smoke test

```bash
SERVER=192.168.1.10:7946

# Run a test container
ctl --server $SERVER run --name smoke nginx:alpine -p 80

# Wait for running phase
ctl --server $SERVER ps

# Create an ingress rule
WORKLOAD_ID=$(ctl --server $SERVER ps | grep smoke | awk '{print $1}')
ctl --server $SERVER ingress create --workload $WORKLOAD_ID --port 80

# Hit the ingress on any node
curl -s -o /dev/null -w "%{http_code}" http://192.168.1.10:8080/
# → 200

# Clean up
ctl --server $SERVER rm $WORKLOAD_ID
```

---

## 9. Access the web console

Open `http://192.168.1.10:7948` in a browser. Log in with:

- **Username:** `admin`
- **Password:** contents of `~/.local/share/orchestrator/web-password` on node1

> Recommend placing the web console and gRPC port (7948, 7946) behind a TLS-terminating reverse proxy or restricting them to a management VLAN. The ingress proxy (8080) is the only port that needs to be publicly reachable.

---

## Upgrading

Rolling upgrade — no downtime:

```bash
# Rebuild on your build machine
go build -o bin/orchestrator ./cmd/orchestrator
go build -o bin/ctl         ./cmd/ctl

# Update each non-leader node first
for NODE in node2 node3; do
  scp bin/orchestrator $NODE:~/bin/
  ssh $NODE systemctl --user restart orchestrator
  sleep 5
done

# Then the leader (triggers re-election, usually < 2 s)
scp bin/orchestrator node1:~/bin/
ssh node1 systemctl --user restart orchestrator

# Verify
ctl --server $SERVER status
```
