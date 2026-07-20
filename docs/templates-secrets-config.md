# Secrets and Config in templates

Workload templates can reference two kinds of externally-stored values instead of baking them into the template as literal text: **Secrets** (sensitive, OpenBao-backed) and **Config** (plaintext, shared, stored directly in cluster state). Both are resolved fresh into environment variables every time a template is deployed — rotating a secret or updating a config value takes effect on the next deploy without editing the template.

## When to use which

| | Secrets | Config |
|---|---|---|
| Use for | passwords, API keys, tokens | database hosts/ports, feature flags, anything non-sensitive |
| Stored in | OpenBao (KV v2) | Raft cluster state, plaintext |
| Value visible after creation | No — write-only | Yes — listable and editable in place |
| Requires OpenBao configured | Yes | No |

If you're unsure, ask: "would it be a problem if this value showed up in `ctl config list`?" If yes, it's a secret.

## Create the values first

Both must exist before a template can reference them.

```bash
ctl secret create --name db-password --value 's3cr3t'
ctl config create --name db-ip --value '10.0.0.5'
ctl config create --name db-port --value '5432'
```

Or use the **Secrets** and **Config** pages in the web console.

## Reference them in a template

On the Templates page, open **New template** (or **Edit** on an existing one). Below the image / compose YAML field you'll find repeatable row editors:

- **Secret refs** — `ENV_VAR → secret name`
- **Config refs** — `ENV_VAR → config value name`
- **Secret mounts** — `service → secret name → target path → mode` (compose stack templates only)

Each secret/config ref row injects one environment variable, resolved at deploy time. Secret mounts are different: instead of an env var, the secret's plaintext is written to a file inside the named service's container, staged through a tmpfs-backed directory on the host so it's never persisted to disk. Leave target blank to default to `/run/secrets/<secret name>`, and mode blank to default to `0400`.

### Container template example

| Env var | Secret ref | Config ref |
|---|---|---|
| `DB_PASSWORD` | `db-password` | |
| `DB_HOST` | | `db-ip` |
| `DB_PORT` | | `db-port` |

Deploying this template runs the container with:

```
DB_PASSWORD=s3cr3t
DB_HOST=10.0.0.5
DB_PORT=5432
```

### Compose stack template example

For a stack, the same refs resolve into env vars usable via standard `${VAR}` substitution in the compose YAML — the compose file itself never contains the actual values:

```yaml
services:
  api:
    image: myregistry/billing-api:latest
    environment:
      - DATABASE_URL=postgres://user:${DB_PASSWORD}@${DB_HOST}:${DB_PORT}/billing
```

Set the same `DB_PASSWORD` / `DB_HOST` / `DB_PORT` refs on the template as in the container example above.

## One-off stack deploys

The **Workloads** page's **Submit Compose Stack** modal has the same three row editors (Secret refs, Config refs, Secret mounts) for deploying a stack directly, without saving it as a template first.

## Equivalent `ctl` commands

The same refs also work from the CLI, for one-off, non-template deploys:

```bash
ctl run --name billing-api \
  --secret DB_PASSWORD=db-password \
  --config DB_HOST=db-ip --config DB_PORT=db-port \
  myregistry/billing-api:latest

ctl stack --name billing-stack -f docker-compose.yml \
  --secret DB_PASSWORD=db-password \
  --config DB_HOST=db-ip --config DB_PORT=db-port
```

## Notes

- Config values with no OpenBao configured at all still resolve fine — config resolution never depends on OpenBao's availability.
- Rotating `db-password` (`ctl secret create` again won't work — use the web console's Secrets page "Edit value", which overwrites the value at the existing OpenBao path) or updating `db-ip` (`ctl config update db-ip --value 10.0.0.6`) takes effect the next time the template is deployed; it does not retroactively affect already-running containers until they're redeployed.
- `ctl export` / `ctl import` (or the web console's Export/Import page) carries `secret_refs`/`config_refs`/`secret_mounts` on templates through as name references. Config *values* export with their real value (safe to round-trip); secret values never do — you'll need to recreate secrets with real values on the target cluster.
