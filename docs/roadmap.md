# Roadmap — features not yet implemented

A running list of planned features that are **designed or intended but not yet
built**. Implemented features live in the code and `production.md`; this file is
only for what's still ahead. Keep it current: when a feature ships, remove it
here (and document the shipped behavior in `production.md`).

Status legend: 🟢 designed (doc exists) · 🟡 sketched (notes only) · 🔴 idea (not yet thought through)

---

## 🟢 Mesh network (userland cross-node private network)

Give every container its own cluster-routable private address over an
unprivileged WireGuard overlay, so containers can listen on a real network
instead of `localhost` and reach each other directly across nodes.

- **Design:** [`mesh-network.md`](mesh-network.md)
- **Unlocks:** multiple replicas of a workload spread across nodes; clustered /
  stateful apps that need to advertise their own address.
- **Default address range:** `100.64.0.0/10` (configurable; must not overlap the
  host LAN — this cluster's hosts use `10.*` and `192.168.*`).
- **Build order:**
  1. ✅ `Mesh*` fields on `types.Node` (pubkey, endpoint, subnet, addr) + proto/convert plumbing
     (Node + HeartbeatRequest carry `mesh_pub_key`/`mesh_endpoint`; subnet/addr leader-assigned).
  2. ✅ Leader-side `/24` allocator from the mesh CIDR (`allocMeshSubnet` in `internal/raft/fsm.go`,
     assigned on `cmdRegisterNode`, stable across re-registration).
  3. ◑ `meshd` reconcile loop (`internal/meshd`): persistent keypair + publish pubkey/endpoint via
     self-register/heartbeat; bring up a userland WireGuard device and sync peers from Raft state on a
     2 s loop (`--mesh` flag, off by default). **Done:** keypair, publish, peer sync, netstack-backed
     device (no privileges). **Deferred to step 4:** the real kernel-TUN inside the rootlesskit netns so
     *containers* (not just the orchestrator process) ride the mesh — `meshd.Device` is an interface, so
     this is a second `DeviceFactory` alongside `NewNetstackDevice`.
  4. ◑ Per-node `mesh0` nerdctl network on the node's `/24`; attach containers. **Done:** `meshNetworkLoop`
     waits for the leader-assigned subnet, then `nc.EnsureMeshNetwork` creates/recreates `mesh0`
     (subnet=node's /24, gateway=node's MeshAddr); single-container runs attach via `--network mesh0`
     (`buildRunArgs`); compose stacks attach via `injectMeshNetwork` (rewrites YAML: adds
     `mesh0: {external: true}` to top-level networks, appends `mesh0` to each service — preserving
     `default` when the service had no prior networks key); all paths tested.
     **Remaining (needs a live rootless host — untestable in CI):** the cross-node data path — put
     `wg0` + a `<meshCIDR>→wg0` route inside the RootlessKit netns and forward inbound WireGuard UDP
     into it, so a `mesh0` container on node A reaches node B's containers (the netstack device only
     gives the *orchestrator process* mesh reachability). Approach: a managed WireGuard router
     container (`meshrouterd`, `--network host` = RootlessKit netns) — design + tasks (4b) in
     [`mesh-wg-router.md`](mesh-wg-router.md).
  5. `Replicas` field on `Workload` + scheduler fan-out across nodes.
  6. `*.mesh` (and existing `*.svc.local`) DNS → all live replica mesh IPs.
  7. Retire east-west `ForwardHTTP` tunneling + internal `SystemPort` remaps; point ingress/proxyd at mesh addresses.
- **Open questions:** load-balancing style (multi-A DNS by default vs. optional
  `--vip`); NAT traversal / relay for nodes behind routers (deferred); MTU
  default (~1380); kernel-WireGuard fast path when the module is present.

## 🟡 crond — distributed cron service

DB-backed distributed cron with exactly-once execution via a PostgreSQL unique
constraint. Deferred for later implementation; notes only, no design doc yet.

- **Needs:** design doc; decide whether the schedule store is Raft state or an
  external Postgres; leader-only vs. any-node execution; retry/missed-run policy.

## 🟡 OpenBao secret rotation policies

The Level 1 foundation (token auth, KV v2, orchestrator-mediated injection) has
shipped. What remains is the per-secret rotation policy system:

- **Rotation = per-secret policy (no global default; chosen at `ctl secret create`):**
  | Policy | Behavior | For |
  |---|---|---|
  | `manual` | store new version, report stale set; no auto restart | config / non-critical |
  | `restart` | rolling restart of dependents | secrets where old & new can briefly coexist, or values only read at boot |
  | `cutover` | stop dependents → run self-rotation hook → start them | mutual shared credentials (e.g. DB password) the system can rotate itself |
  | `external` | rotated out-of-band; watch the OpenBao path, restart/reload dependents on change | credentials the user genuinely cannot change |
  - **DB secrets default to `cutover`.** No DB root, but a login user can change its
    **own** password (`ALTER ROLE CURRENT_USER PASSWORD …` / `ALTER USER USER() …`), so
    the hook connects *as the app user* and self-rotates, then updates OpenBao, then
    restarts dependents. The user controls timing → tight window, not a surprise.
  - **Why not the elegant options:** `dynamic` (per-instance leases) and overlapping
    credentials both need `CREATE ROLE`/admin on the DB, which the user lacks — off the
    table. `cutover` is a single shared credential, so a brief window where un-restarted
    apps hold the old password is unavoidable (no zero-downtime overlap without CREATEROLE).
- **Caveats:** self-password-change breaks under external/IAM auth and some managed DBs;
  rolling restart requires the replica work in [`mesh-network.md`](mesh-network.md) — until
  replicas land, `restart`/`cutover` are a brief single-instance blip rather than rolling.
- **Needs:** design doc; depends on replica/mesh work for true rolling restarts.

---

## How to use this file

- Add a feature here the moment it's planned, even as a 🔴 one-liner.
- Link to its design doc once one exists; promote 🔴 → 🟡 → 🟢 as it firms up.
- When it ships: delete the entry and record the real behavior in `production.md`.
