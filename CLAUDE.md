# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project overview

A usermode-only container orchestrator for Linux. Every node runs the same binary; any node can become the Raft leader (control plane). Managed containers and compose stacks run via **nerdctl in rootless mode** — no root, no privileged system calls, no direct containerd API calls.

## Usermode constraints (non-negotiable)

- All container operations go through `nerdctl` (exec, not SDK). Never call containerd or OCI runtimes directly.
- No `iptables`, no raw sockets, no `CAP_NET_ADMIN`. Networking is rootless (slirp4netns / pasta).
- No ports < 1024.
- Cgroup v2 with user delegation assumed; cgroup v1 not supported.
- `newuidmap`/`newgidmap` must be available on managed nodes for rootless uid mapping.

## Architecture

### Single binary, dual role

One binary (`orchestrator`) acts as both the node agent and (when elected) the Raft leader. There is also a thin CLI client (`ctl`) for human interaction.

```
cmd/
  orchestrator/   # node daemon — agent + embedded Raft peer
  ctl/            # CLI that talks to the local or remote orchestrator gRPC API
internal/
  agent/          # nerdctl wrapper, container/compose lifecycle
  raft/           # Raft consensus, leader election, log application
  scheduler/      # placement decisions (desired → actual state diff)
  state/          # desired state store (applied by Raft), actual state cache
  grpc/           # gRPC server and client, protobuf-generated stubs
  nerdctl/        # thin exec wrapper around the nerdctl binary
proto/            # .proto definitions for node-to-node and ctl-to-node RPCs
pkg/
  types/          # shared domain types (Container, Stack, Node, etc.)
```

### Control flow

1. Nodes discover each other (static config or gossip TBD) and form a Raft cluster.
2. User submits workload via `ctl` → gRPC → leader node.
3. Leader writes desired state to Raft log; all nodes apply the log.
4. Scheduler (runs on leader) diffs desired vs actual state and emits placement decisions.
5. Target node's agent receives placement decision and runs `nerdctl [compose] up/down/pull`.
6. Agent polls `nerdctl ps` / `nerdctl compose ps` and reports actual state back to the cluster.

### Agent protocol

- gRPC over TLS (mTLS between nodes).
- Node-to-node: `NodeService` — heartbeat, state sync, placement RPCs.
- ctl-to-node: `ControlService` — submit workloads, query state, drain nodes.
- Proto files live in `proto/`; generated Go code goes in `internal/grpc/gen/` (committed).

### nerdctl wrapper (`internal/nerdctl`)

Exec-based, not SDK-based. The wrapper:
- Builds `nerdctl` command lines and captures stdout/stderr.
- Parses `nerdctl ps --format json` and `nerdctl compose ps --format json` for actual state.
- Always passes `--namespace <ns>` so the orchestrator's containers are isolated from manual ones.
- Never assumes a specific nerdctl version; commands are feature-detected at startup.

## Common commands

```bash
# Generate protobuf stubs (requires protoc + protoc-gen-go + protoc-gen-go-grpc)
go generate ./proto/...

# Build
go build ./cmd/orchestrator
go build ./cmd/ctl

# Run all tests
go test ./...

# Run a single package's tests
go test ./internal/scheduler/...

# Run a single test
go test ./internal/scheduler/... -run TestPlacementBasic

# Lint (golangci-lint required)
golangci-lint run ./...

# Vet
go vet ./...
```

## Key design decisions

- **No Kubernetes compatibility layer.** This is not a mini-k8s; keep the API surface small.
- **Raft via hashicorp/raft** (or etcd/raft) — do not roll a custom consensus algorithm.
- **State is append-log first.** Desired state mutations go through Raft; reads can be served locally.
- **Actual state is eventually consistent.** Agents poll nerdctl and push updates; the scheduler must tolerate stale actual state.
- **Compose is a first-class citizen**, not an afterthought. `nerdctl compose` commands must be treated equally to single-container commands in the scheduler and state model.
- **Errors from nerdctl are propagated verbatim** to the caller; do not swallow stderr.
