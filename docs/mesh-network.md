# Mesh Network Design

> A plain-language design for giving every container a real, private address that
> works across nodes — so you can run multiple replicas of a workload on
> different machines and have them talk to each other directly.

## The problem, in one picture

Today, a container can only be reached on the machine it runs on, through a
"front desk" proxy:

```
   Node A                          Node B
 ┌─────────────────┐            ┌─────────────────┐
 │  container web  │            │  container db   │
 │  (localhost)    │            │  (localhost)    │
 │       ▲         │            │       ▲         │
 │       │ proxyd  │  network   │       │ proxyd  │
 │  [front desk]───┼───────────►│  [front desk]   │
 └─────────────────┘            └─────────────────┘
```

A container has no address of its own. It listens on `localhost` (a phone that
only takes calls from inside the same house). Anything outside the machine has
to go through `proxyd` / the ingress proxy, which forwards the call inward.

That works for simple web traffic, but it has a hard limit: **a container can
never tell another container "call me back at this address,"** because it
doesn't *have* an address others can reach. Many real programs need that —
databases that form clusters, queues, anything where copies of the same program
find and talk to each other. Those break behind a front desk.

### What we want instead

Give every container its own private phone number that works from any machine in
the cluster:

```
   Node A                          Node B
 ┌──────────────────┐            ┌──────────────────┐
 │ web  100.64.1.2  │◄──────────►│ db   100.64.2.5  │
 └──────────────────┘  private   └──────────────────┘
                       network
```

Now `web` on Node A can dial `db` on Node B directly at `100.64.2.5`, as if they
were plugged into the same switch — even though they're on different machines.
This private, cluster-wide network is what we'll call the **mesh**.

## The constraint that shapes everything

This project runs **without root / without special privileges**. We can't touch
the machine's real network settings, install firewall rules, or use the usual
admin tools. (See the usermode rules in `CLAUDE.md`.)

So the mesh has to be built entirely in "userland" — using only things a normal,
unprivileged user is allowed to do.

The good news: each machine already runs our containers inside a private sandbox
(a "network namespace") that **we own**. Inside our own sandbox we're allowed to
create network gadgets and set up routing — the container system already does
this to give containers `localhost`. We're just going to add one more gadget to
that same sandbox.

## How it works: an encrypted tunnel between machines

We connect the machines with **WireGuard** — think of it as a secure, private
tunnel (a VPN) between every pair of nodes. We use a pure-software version
(`wireguard-go`) so it needs no special privileges.

Each node gets:

- A **block of addresses** to hand out to its containers. We split one big range,
  `100.64.0.0/10`, into one slice per node:
  - Node A → `100.64.1.x`
  - Node B → `100.64.2.x`
  - Node C → `100.64.3.x`
- A **tunnel** (`wg0`) to every other node.
- A simple rule: *"to reach `100.64.<other node>.x`, send it through the tunnel."*

When `web` (100.64.1.2 on Node A) sends data to `db` (100.64.2.5 on Node B):

1. Node A sees the destination is in Node B's slice → puts it in the tunnel.
2. WireGuard encrypts it and ships it to Node B over the regular network.
3. Node B decrypts it and hands it to `db`.

Neither container knows or cares that a tunnel exists. To them it just looks like
one flat private network.

```
 web 100.64.1.2 ──► "this is for 100.64.2.x" ──► wg0 tunnel ──► Node B ──► db 100.64.2.5
   (Node A)              routing rule          (encrypted)
```

## Choosing the address range (configurable)

`100.64.x` above is just an example — **the range is a setting you choose**, and you
*must* pick one that doesn't clash with the real network your machines already use.

Here's why it matters. The "send it through the tunnel" rule only lives inside the
container sandbox, so it never touches your host's own networking. But if the mesh
range overlaps a real network your containers need to reach (say a database on your
LAN), a container trying to reach that real machine could get sent into the tunnel
by mistake. So the one rule is: **the mesh range must not overlap any real network
your containers talk to.**

Private addresses come in a few reserved blocks. If your network already uses the
common ones, pick a free one:

| Block | Looks like | Notes |
|---|---|---|
| `10.0.0.0/8` | `10.*` | often already used on company/home LANs |
| `192.168.0.0/16` | `192.168.*` | the typical home-router range |
| `172.16.0.0/12` | `172.16.*`–`172.31.*` | often free; good fallback |
| `100.64.0.0/10` | `100.64.*`–`100.127.*` | reserved for exactly this kind of overlay; almost never used locally |

**Default: `100.64.0.0/10`.** It's the safest pick because hardly any real network
uses it (it's the same range Tailscale picks, for the same reason). Override it
(e.g. to `172.20.0.0/16`) if that suits you better.

Two safety rules built into the setting:

1. **One range for the whole cluster.** The leader carves per-node slices out of it,
   so every node must agree. It's set **once when the cluster is created** and stored
   in shared state — not a per-node flag that could drift out of sync.
2. **Refuses to start on a clash.** When a node starts, it compares the chosen range
   against the addresses its own host actually uses. If they overlap, it stops with a
   clear error instead of silently breaking traffic — your protection against the
   `10.*` / `192.*` collision.

(The per-node slice size — `/24`, i.e. 254 containers per node — can also be made
configurable, but the range itself is the setting that matters.)

## How nodes find each other

A node doesn't have a list of peers baked in. We reuse what the cluster already
has: a shared, replicated record of every node (the Raft state — the same place
node certificates already live).

When a node starts, it publishes three small facts about itself into that shared
record:

| Fact | Example | Meaning |
|---|---|---|
| public key | `abc123…` | the tunnel's "lock" others use to send to it |
| endpoint | `10.0.0.7:51820` | where to reach it on the real network |
| address block | `100.64.1.0/24` | the slice of private addresses it owns |

Every node watches that record and keeps its tunnels in sync — if a node joins,
everyone opens a tunnel to it; if one leaves, they drop it. (The private half of
the key never leaves the machine.)

Who hands out the address blocks? The cluster leader, the same way it already
hands out ports today — it just picks the next free `100.64.x` slice when a node
joins. No two nodes ever get the same slice.

## What this gives you: replicas across machines

This is the real goal. Say you want **3 copies of `web`** for redundancy:

1. The scheduler spreads the 3 copies across whatever nodes have room (it already
   decides placement today).
2. Each copy automatically gets its own mesh address: `100.64.1.2`, `100.64.2.9`,
   `100.64.3.4`.
3. The name `web.mesh` is set up to point at **all three** addresses.
4. Anything that connects to `web.mesh` gets sent to one of the live copies.

If a node dies, its copies drop out of `web.mesh` and traffic flows to the
survivors. The copies can also see each other directly — which is what lets
clustered software (databases, queues) actually work.

## What changes for Services and Ingress (and what doesn't)

You already have two ways to expose a container:

- **Service** — a named TCP endpoint.
- **IngressRule** — HTTP/HTTPS routing from the outside world.

**The good news: these stay as concepts. You keep using them the same way.** What
changes is only the plumbing underneath — and it gets *simpler*, because we no
longer need the "front desk" tricks for traffic *inside* the cluster.

| Thing | Before | After |
|---|---|---|
| Where a container listens | `localhost` only | its own mesh address |
| Reaching a container across nodes | front-desk proxy + a juggled "system port" | dial its mesh address directly |
| **Service** | proxy forwards to the container | `name.mesh` points straight at the container(s) |
| **IngressRule** (web traffic) | forwards to localhost, or tunnels to other nodes | dials the container's mesh address directly |
| The juggled "system ports" for internal traffic | needed | no longer needed |

**Traffic from the outside world is untouched.** The ingress (the public front
door for websites) still works exactly as before — it still terminates HTTPS and
routes by hostname. The only difference is that once a request is inside, ingress
reaches the target container by its mesh address instead of through extra hops.

So: **Services and Ingress keep their meaning; the mesh just gives them a
shorter, more direct path** — and unlocks replicas and clustered apps as a bonus.

## External traffic (the outside world coming in)

There's one rule that explains all of this: **the mesh is private to the
cluster — the outside world is not on it.** A browser, a phone, another machine
on your office network, the public internet — none of them can put traffic
directly onto a `100.64.x` address. So traffic from outside can never land on the
mesh by itself. It always comes in through a **front door** — something that sits
on *both* networks at once: reachable from your real network, and able to reach
containers on the mesh.

You already have those front doors: the **ingress** (for websites / HTTP) and
**proxyd** (for plain TCP). The mesh doesn't replace them — it just makes their
job simpler.

```
  Outside world (browsers, office LAN, internet)  ── NOT on the mesh
        │   arrives on a real machine address (e.g. :443)
        ▼
  ┌──────────────── front-door node ───────────────┐
  │  ingress (websites)      or      proxyd (TCP)   │  ← on BOTH networks
  └────────────────────────┬────────────────────────┘
                           │  hands off to the container's mesh address
                           ▼
                 100.64.x container  (on any node)
```

**Websites (HTTP/HTTPS):** a request arrives at the ingress on a real machine,
the ingress checks the security certificate and the web address to decide which
container should answer (exactly as it does today), then passes the request to
that container's mesh address. The only thing that changed is the last step —
it now hands off to a direct mesh address instead of hopping through extra
machinery. A nice bonus falls out of this: because *every* node can now reach
*every* container over the mesh, you can run the front door on all nodes at once
for redundancy — if one front-door machine goes down, another takes over.

**Plain TCP (databases, custom protocols exposed to outsiders):** the request
arrives at proxyd on a real machine address, and proxyd passes it to the
container's mesh address (spreading load across copies if there are several).

A subtlety worth pinning down, since it touches what we said about Services:

| Who's talking | Where it enters | Needs a real public port? |
|---|---|---|
| Container → container (inside the cluster) | nowhere — goes direct on the mesh | No |
| Outsider → a website | the ingress (already owns `:443`) | No new one |
| Outsider → a plain-TCP service | proxyd's listener | **Yes** — outsiders aren't on the mesh, so they need a real address to dial |

In short: **inside the cluster, everything is direct on the mesh with no front
door. From outside, traffic still comes through the front door** — that part
doesn't change — **but the front door's hand-off to the container becomes a
simple, direct mesh address.**

## Rollout — nothing breaks on day one

We don't replace the old system in one go. The mesh is added *alongside* it:

1. **Build the tunnels.** Get two nodes able to reach each other on `100.64.x`.
2. **Put containers on the mesh.** They get a mesh address *in addition to* their
   current `localhost` setup — everything that works today keeps working.
3. **Point Ingress and Services at mesh addresses** instead of the front-desk
   proxy. Same behavior, fewer hops.
4. **Add replicas.** Let a workload ask for N copies; set up `name.mesh` to point
   at all of them.
5. **Clean up.** Once the direct path is trusted, retire the old proxy hops and
   port-juggling for internal traffic.

At every step the cluster keeps running. If the mesh has a problem, the old path
is still there.

## The pieces we'll build

| Piece | What it does |
|---|---|
| Address bookkeeping | leader hands each node a `100.64.x` slice; stored in shared state |
| Node facts | each node publishes its key + endpoint + slice (like it already publishes its certificate) |
| `meshd` (the mesh manager) | on each node: makes the tunnel, keeps peers in sync, hands containers their addresses |
| Container hookup | start containers attached to the mesh so they get a mesh address |
| `name.mesh` lookups | turn a workload name into the addresses of its live copies |

## Honest caveats

- **Speed.** The software tunnel isn't as fast as a hardware network. It's plenty
  for normal app-to-app traffic, but it's not built for moving huge files at full
  wire speed. (If a machine happens to support the faster built-in WireGuard, we
  can use that later with the same code.)
- **Message size.** Wrapping data for the tunnel adds a little overhead, so we
  tell containers to use slightly smaller chunks. Get this wrong and large
  transfers silently stall — it's the most common setup mistake, so we set it up
  front.
- **Machines behind home/office routers.** Nodes need to be reachable on the real
  network for the tunnel to connect. On a private LAN or VPN this just works. For
  nodes scattered across the public internet behind routers that hide them, we'd
  later add a small "relay" node to introduce them. Not needed for the first
  version.

## In one sentence

We give every container its own private address that works across all machines by
running an unprivileged encrypted tunnel between nodes — turning a pile of
separate machines into what looks like one flat private network, so replicas and
clustered apps just work.
