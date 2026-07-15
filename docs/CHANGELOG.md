# Changelog

All notable changes to the Orchestrator are documented here.

---

## v0.6.0 — Mesh networking, replicas, system services

### Added
- **WireGuard mesh overlay** — every node gets a `/24` subnet allocation; containers on the `mesh0` bridge receive stable mesh IPs regardless of which node they run on
- **`meshrouterd`** — userspace WireGuard router running inside a rootless network namespace (`--network ns:<path>`), distributed as an embedded OCI image with no host kernel module requirement
- **Replica fan-out** — `ctl run --replicas N` spreads N instances across distinct nodes; the web console shows all instances grouped under the parent workload
- **`*.mesh` DNS zone** — proxyd resolves `<workload>.mesh` to the mesh IPs of all live instances; supports multi-A for round-robin across replicas
- **System services page** — web console shows live status of `ingressd`, `meshrouterd`, and `proxyd` with per-service start/stop controls and Start All / Stop All actions
- **`MeshIP` on actual container state** — agents supplement `nerdctl ps` output with a batch `nerdctl inspect` call to extract each container's mesh IP, which flows through gRPC state sync to the leader's DNS table
- **`GroupName` on workloads** — replica instances carry a group name back to the parent workload name, enabling the DNS aggregator to return a single `*.mesh` record for all replicas

### Changed
- `EnsureMeshNetwork` now accepts an `mtu` parameter and passes `--opt com.docker.network.driver.mtu=1380` to nerdctl so the bridge MTU matches the WireGuard tunnel and avoids silent packet drops
- Reconciler now dispatches `tryFanOut` for workloads with `Replicas > 1` and places children on distinct nodes using `pickNodeExcluding`

### Fixed
- `CreateIngressResponse` proto was missing `system_port` field — referenced by both the server and `ctl ingress` but never declared; added and regenerated

---

## v0.5.0 — Networks, volumes, config reliability

### Added
- **Networks page** — browse nerdctl networks with inspect detail view
- **Volumes page** — browse named volumes with inspect detail view
- **Web UI as tracked build dependency** — `go generate` now rebuilds the frontend; the binary always contains the current UI

### Fixed
- `proxyd` config watcher used inotify on the config file directly; atomic `rename()` from the orchestrator killed the watch silently — switched to parent-directory watching
- `ingressd` had the same atomic-rename problem; switched to mtime polling (inotify cannot cross its bind mount)

---

## v0.4.0 — Pure-Go build tooling, ingressd, containerised ingress

### Added
- **`ingressd`** — HAProxy wrapper running as a rootless managed container; replaces the in-process reverse proxy
- **Built-in OCI registry** — serves embedded container images (`ingressd`, `meshrouterd`) over plain HTTP on localhost so nerdctl can pull without an external registry
- **Pure-Go image assembler** (`cmd/build-image`) — builds scratch-based OCI images from a compiled binary and optional extra directories; replaces `docker build` in release pipelines
- **`dist/` output directory** — all release binaries (orchestrator, ctl, proxyd, ingressd, meshrouterd) land in `dist/`; build tags `with_ingressd_image` and `with_meshrouterd_image` gate embedded image blobs
- **Systemd user unit files** for `ingressd` and `proxyd`
- Digest-based auto-replace for `ingressd` — if the embedded image digest differs from the running container, the container is recreated automatically

### Changed
- Routing model changed from workload-based to container FQDN + port — services reference containers by stable DNS name, not by workload ID
- `go-containerregistry/pkg/registry` replaces the hand-rolled OCI registry implementation

---

## v0.3.0 — Secrets, MFA, proxyd, templates, containers

### Added
- **Secrets management** — `ctl secret create/list/delete`; AES-256-GCM encryption at rest; `--secret ENV=name` flag on `ctl run` and compose stacks; `${ENV_VAR}` substitution backed by an injected `.env` file
- **`proxyd`** — extracted TCP proxy and DNS server into a standalone binary; serves `svc.local.` A records for all registered services; forwards unknown queries to system resolvers
- **TOTP-based MFA** — admin login flow adds a TOTP setup step on first login and verifies a 6-digit code on subsequent logins; `--web-disable-mfa` skips TOTP for development
- **Containers page** — flat list of all running containers across all nodes with live stats
- **Templates page** — save and deploy reusable workload definitions (container or compose)
- **Workflow Builder** — visual drag-and-drop compose stack editor (experimental)
- **Domain TLS management** — CSR generation and download, key regeneration with custom subject fields, PEM certificate import

### Changed
- Compose stack services are now eligible as service proxy backends (previously only single containers)

### Fixed
- Service port routing was broken when a workload had multiple port allocations
- Published ports were not shown on the workload detail page

---

## v0.2.0 — Web console, nodes, registries, domains

### Added
- **Web console** (Cloudscape Design System) with sidebar navigation, login, and session management
- **Node detail page** with CPU/memory/disk utilisation graphs
- **Workload detail page** with live actual state, phase badge, and remove action
- **Domains and HTTPS ingress** — TLS termination at the ingress layer; per-domain cert management
- **User management** — create/disable/delete local users; admin-only actions
- **LDAP authentication** — optional external auth backend configurable at startup
- **Registry management** — add private registries; browse image catalog and tags; pull credentials stored in cluster state
- **`--web-prefix` flag** — serve the console under a URL sub-path (e.g. `/console`)
- **Release Makefile** — cross-compiles static binaries for `linux/amd64` and `linux/arm64`

---

## v0.1.0 — Initial release

### Added
- **Single-binary orchestrator** — every node runs the same binary; Raft leader election via `hashicorp/raft`
- **Rootless container execution** via `nerdctl` exec wrapper; all operations go through `nerdctl` — no direct containerd or OCI runtime calls
- **Compose stacks** as first-class workloads alongside single containers
- **Scheduler** — diffs desired vs actual state; places workloads on healthy nodes via gRPC `PlaceWorkload` RPC
- **Agent** — polls `nerdctl ps` / `nerdctl compose ps` and reports actual state back to the leader
- **gRPC over mTLS** — node-to-node (`NodeService`) and ctl-to-node (`ControlService`) communication
- **Port allocator** — auto-assigns host ports from a configurable pool; manages port mappings per workload
- **`ctl` CLI** — `run`, `stack`, `remove`, `nodes`, `ps` subcommands
- **HTTP ingress proxy** — per-node reverse proxy routes incoming requests to containers by hostname
- **Service discovery** — `svc.local.` DNS via the in-process resolver (later extracted to `proxyd`)
- **Web console login** — Bearer-token auth for API routes; password login via the browser UI
