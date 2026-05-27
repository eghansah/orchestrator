# Orchestrator

A usermode-only distributed container runtime for Linux. Every node runs the same binary; a Raft-elected leader owns the control plane while workloads run on any node. No root, no iptables, no privileged daemons — only `nerdctl` in rootless mode.

**Features**

- Schedule containers and Compose stacks across a cluster
- Raft consensus — cluster survives leader failover automatically
- HTTP ingress proxy with host/path routing
- Internal service discovery: `api.svc.local` DNS + TCP proxy
- mTLS between all nodes, bearer-token auth for operators
- Web console at `:7948`

## Quick start (single node)

```bash
# Build
go build -o bin/orchestrator ./cmd/orchestrator
go build -o bin/ctl         ./cmd/ctl

# Bootstrap a single-node cluster
./bin/orchestrator \
  --bootstrap \
  --data-dir ~/.local/share/orchestrator \
  --grpc-addr 0.0.0.0:7946 \
  --raft-addr 127.0.0.1:7947 \
  --ingress-addr 0.0.0.0:8080 \
  --dns-addr :5353 \
  --web-addr :7948
```

On first boot it prints three secrets — save them:

```
*** Join token:            <hex>  ***   ← needed when adding nodes
*** Admin token:           <hex>  ***   ← CLI / API authentication
*** Web console password:  <hex>  ***   ← browser login (username: admin)
```

Verify:

```bash
export ORCHESTRATOR_TOKEN=$(cat ~/.local/share/orchestrator/admin-token)
./bin/ctl status
```

## Multi-node cluster

See **[docs/deploy.md](docs/deploy.md)** for a full step-by-step guide.

## CLI quick reference

```bash
# Workloads
ctl run   --name nginx nginx:alpine -p 80
ctl stack --name myapp -f docker-compose.yml
ctl ps
ctl rm <id>

# Nodes
ctl nodes
ctl drain <node-id>

# HTTP ingress
ctl ingress create --workload <id> --port 80 --host app.example.com
ctl ingress list

# Internal services (DNS + TCP)
ctl service create --name db --workload <id> --port 5432
ctl service list
```

## Documentation

| Document | Contents |
|---|---|
| [docs/deploy.md](docs/deploy.md) | Step-by-step cluster deployment |
| [docs/production.md](docs/production.md) | Full flag reference, security, operations playbook, troubleshooting |

## Requirements

- Linux kernel ≥ 5.11 (cgroup v2 + user delegation)
- `nerdctl` + `containerd` in rootless mode
- `slirp4netns` or `pasta`
- `newuidmap` / `newgidmap` (`uidmap` package)
- Go 1.21+ (build only)
