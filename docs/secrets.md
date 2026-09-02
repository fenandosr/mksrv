# Secrets (OpenBao)

The `openbao` stack runs an OpenBao cluster on Integrated Storage (Raft) with
AWS KMS auto-unseal. It is `kind: cluster` — assign it to an odd number of hosts
(≥ 3), the same ones that carry `postgres` in the distributed profile.

```yaml
# deployment.yaml
hosts:
  core1: { provider: aws, stacks: [postgres, openbao] }
  core2: { provider: aws, stacks: [postgres, openbao] }
  core3: { provider: aws, stacks: [postgres, openbao] }
```

## Bring-up

```
mksrv apply                 # creates the KMS key + grant, deploys the 3 nodes
mksrv openbao bootstrap      # init, store recovery material, enable KV v2 + AppRole
mksrv openbao status         # seal state / raft roles / engines
```

`bootstrap` is idempotent: on an already-initialised cluster it re-checks the
seal state, the Raft quorum, and the enabled engines.

## Auto-unseal

Each `openbao` host's instance profile carries a `kms:Encrypt`/`Decrypt` grant on
`alias/mksrv-<env>-openbao`. Nodes unseal themselves on every boot — there is no
Shamir key ceremony. `bao operator init` still produces **recovery keys** (5
shares, threshold 3) used for recovery mode and regenerating a root token.

## Operator material (SSM, `SecureString`)

| Path | Content |
|---|---|
| `/mksrv/<env>/openbao/recovery_keys` | JSON array of base64 recovery key shares |
| `/mksrv/<env>/openbao/root_token` | initial root token |

Both are written by `mksrv openbao bootstrap` and are **never** pushed to hosts
as podman secrets. Read them with the AWS CLI:

```
aws ssm get-parameter --with-decryption --name /mksrv/prod/openbao/root_token \
  --query Parameter.Value --output text
```

## Manual access

The API is reachable on port 8200 over the tailnet (and `127.0.0.1` on each
node). From a node:

```
sudo podman exec -it mksrv-openbao sh
export BAO_ADDR=http://127.0.0.1:8200
bao login <root-token-from-ssm>
bao secrets list        # kv/  (v2)
bao auth list           # approle/
```

## Recovery

- **Lost root token:** `bao operator generate-root` with a threshold of recovery
  key shares.
- **Sealed cluster (KMS unavailable):** `bao operator unseal -recovery` with the
  recovery keys.
- **Lost quorum:** restore from a Raft snapshot (`bao operator raft snapshot
  restore`); snapshots are an operator responsibility until M13.

## Per-tenant secrets

A tenant opts in by listing `openbao` in its `stacks:` (like `cache` or
`database`):

```yaml
# tenants/acme.yaml
stacks: [database, monitor, cache, openbao]
```

`mksrv tenant apply <id>` then reconciles, idempotently:

- a policy **`tenant-<id>`** granting access to `kv/tenants/<id>/*` and the
  tenant's own Transit key, and nothing else;
- an **AppRole `tenant-<id>`** bound to that policy (`token_ttl 1h`,
  `token_max_ttl 4h`) — for services;
- the AppRole's RoleID and SecretID in SSM;
- a **Transit key `transit/keys/<id>`** (`aes256-gcm96`, non-exportable,
  auto-rotated every 90 days) for PII-column encryption;
- an **OIDC auth mount `oidc-<id>/`** wired to the tenant's Keycloak realm — for
  humans.

| Path | Content |
|---|---|
| `/mksrv/<env>/openbao/approle_<id>_role_id` | AppRole RoleID (stable) |
| `/mksrv/<env>/openbao/approle_<id>_secret_id` | AppRole SecretID (written once; re-running `tenant apply` never rotates it) |

A tenant service authenticates and reads its own secrets:

```
bao write auth/approle/login role_id=<role_id> secret_id=<secret_id>   # -> token
BAO_TOKEN=<token> bao kv put  kv/tenants/acme/db  url=postgres://...
BAO_TOKEN=<token> bao kv get  kv/tenants/acme/db
# kv/tenants/<other>/* -> 403
```

## Encrypting PII columns (Transit)

The Transit engine is encryption-as-a-service: the key never leaves OpenBao, the
app sends plaintext and stores the returned `vault:v1:…` ciphertext in the
column.

```
# encrypt (plaintext must be base64)
BAO_TOKEN=<token> bao write transit/encrypt/acme \
  plaintext=$(printf 'jane@acme.example.com' | base64)
# -> ciphertext = vault:v1:xxxx   (store this in the DB)

# decrypt
BAO_TOKEN=<token> bao write transit/decrypt/acme ciphertext='vault:v1:xxxx'
# -> plaintext (base64)
```

For high-volume columns use envelope encryption: `bao write
transit/datakey/plaintext/acme` returns a one-off data key (plaintext + wrapped);
encrypt rows locally with the plaintext key, store the wrapped key, discard the
plaintext. Key rotation (`transit/keys/acme/rotate`, or the 90-day auto-rotate)
re-keys new writes; `transit/rewrap/acme` upgrades old ciphertexts.

## Human login (OIDC)

`mksrv tenant apply` adds a confidential `openbao` client to the tenant's
Keycloak realm and an `oidc-<id>/` auth mount on the cluster. Any member of the
realm logs in and gets the `tenant-<id>` policy — the realm is the isolation
boundary:

```
export BAO_ADDR=http://<node-tailnet-ip>:8200
bao login -method=oidc -path=oidc-<id>
# opens the browser to Keycloak, returns on http://localhost:8250/oidc/callback
bao kv get kv/tenants/<id>/db
```

Refining access by realm group (a subset of the team gets write, the rest read)
is a later enhancement; today every realm member gets the full tenant policy.

Still forthcoming: consumer stacks (`database` / `postgrest`) reading their
credentials from `kv/tenants/<id>/…` instead of raw SSM (M16).
