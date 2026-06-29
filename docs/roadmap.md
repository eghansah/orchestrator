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
  4. Per-node `mesh0` nerdctl network on the node's `/24`; attach containers (incl. the kernel-TUN-in-netns from step 3).
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

## 🟡 OpenBao-backed secrets (audit + rotation + access control)

Replace the join-token-derived AES scheme (`pkg/crypto/secrets.go`, encrypted
`Secret.EncryptedValue` in Raft) with OpenBao as the secret store of record, to
get a real audit trail, value rotation, and access control — while keeping the
existing **define-globally / reference-by-name-in-templates** authoring model.

- **Goals driving it:** audit, rotation, access control (not just "fix the weak key").
- **Authoring model (unchanged):** secrets are global `Secret` objects; specs and
  `WorkloadTemplate`s reference them by name via `SecretRefs` (`env_var → secret_name`,
  `types.go:49,55`). OpenBao changes only where the *value* is stored, not how it's
  referenced.
- **Decisions landed:**
  - **Store-of-record**, not Transit — KV v2 (versioned) + policies + an audit device.
    (Transit only gives crypto-op audit + key rotation; no per-secret ACL or value versioning.)
  - **Level 1 — orchestrator-mediated injection.** The orchestrator authenticates to
    OpenBao (reuse the node mTLS identity via cert auth), resolves `SecretRefs` at
    placement, and injects. *Not* Level 2 (workload-direct fetch) — that conflicts with
    the "just reference it in the template and it appears" experience the user wants.
  - `types.Secret` becomes a **reference** (name → OpenBao path, optional pinned version)
    instead of carrying `EncryptedValue` — also removes ciphertext-forever-in-the-Raft-log.
  - Path layout: `secret/data/cluster/<name>`, `secret/data/workload/<workload>/<name>`.
  - Track, per running workload, the **secret version it was injected with** (KV v2
    version numbers) → compute the "stale set" of dependents on rotation from `SecretRefs`.
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
  - App-level **reconnect-on-auth-failure** (re-read credential + retry) is the gold
    standard where achievable — self-heals with no orchestrator restart; pushes the
    `external`/`restart` cases toward near-zero downtime.
- **Caveats:** self-password-change breaks under external/IAM auth and some managed DBs
  (test against the real DB); OpenBao seal/unseal in usermode (auto-unseal via transit/KMS
  vs. manual); HA (integrated Raft on ≥3 nodes, reachable over the mesh); `disable_mlock`
  in rootless (secrets may swap); keep the join-token scheme as a fallback when OpenBao
  isn't configured.
- **Depends on:** rolling-restart machinery overlaps with the replica work in
  [`mesh-network.md`](mesh-network.md) — until replicas land, `restart`/`cutover` are a
  brief single-instance blip rather than a true rolling update.
- **Needs:** design doc.

---

## How to use this file

- Add a feature here the moment it's planned, even as a 🔴 one-liner.
- Link to its design doc once one exists; promote 🔴 → 🟡 → 🟢 as it firms up.
- When it ships: delete the entry and record the real behavior in `production.md`.
