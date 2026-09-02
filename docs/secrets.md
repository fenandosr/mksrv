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

## Per-tenant model (M13, not yet implemented)

`kv/tenants/<id>/*` with a `tenant-<id>` policy and a per-tenant AppRole whose
`role_id` / `secret_id` land in SSM. A Transit key `transit/keys/<id>` backs
PII-column encryption. OIDC auth is wired to the tenant's Keycloak realm so team
members log in with their existing identity.
