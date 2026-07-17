# OpenBao setup

Secrets are stored in OpenBao (Vault-compatible) as KV v2 values. The orchestrator is a pure client of an existing OpenBao instance — it never calls OpenBao's `sys/mounts` or `sys/auth` APIs, so a few things must be created manually before you configure the connection on this page.

## Where orchestrator secrets live

Every secret the orchestrator creates is written to `<mount>/data/orchestrator/<secret_name>` (and `<mount>/metadata/orchestrator/<secret_name>` for KV v2 metadata), where `<mount>` is whatever you set as the KV mount below — it defaults to `secret` if left blank. The `orchestrator/` prefix is fixed; every secret this cluster creates lands under that one subtree, no matter what mount you point it at.

## 1. Enable the KV v2 secrets engine

In OpenBao's dev mode, `secret/` is already enabled as a KV v2 engine. In a real deployment it usually isn't, so enable one at whatever mount path you plan to use:

```
bao secrets enable -path=secret -version=2 kv
```

## 2. Choose an auth method: AppRole (recommended) or static token

### AppRole

```
bao auth enable approle

bao policy write orchestrator - <<EOF
path "secret/data/orchestrator/*"     { capabilities = ["create", "read", "update"] }
path "secret/metadata/orchestrator/*" { capabilities = ["delete"] }
EOF

bao write auth/approle/role/orchestrator token_policies="orchestrator" secret_id_ttl=0 token_ttl=1h token_max_ttl=4h

bao read auth/approle/role/orchestrator/role-id
bao write -f auth/approle/role/orchestrator/secret-id
```

Enter the resulting `role_id` and `secret_id` (plus the address, mount, and auth mount if not the default `approle`) in the connection form. The orchestrator logs in and renews its token automatically — only the current cluster leader ever performs a login.

### Static token (legacy)

Alternatively, mint a token scoped to the same policy shown above and paste it directly into the connection form's Token field. This still works and requires no other setup, but the token is long-lived and doesn't rotate on its own.

## 3. Policy requirements

Whichever auth method you use, the identity must be able to:

- `create`, `update`, `read` on `<mount>/data/orchestrator/*`
- `delete` on `<mount>/metadata/orchestrator/*`

"Save & test" on the connection form checks exactly these capabilities before the config is persisted, so a missing policy grant fails immediately here rather than silently later when a secret is created or placed.

## Individual secrets

Nothing about individual secret values needs manual setup in OpenBao — once the mount and auth are in place, use "Create secret" on this page (or `ctl secret create`) to write values.
