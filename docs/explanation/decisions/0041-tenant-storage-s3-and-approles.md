# ADR 0041: Tenant `storage:` — a declarative S3 bucket, its credential, and a narrow AppRole

- Status: Accepted
- Date: 2026-11-17

## Context

`hgstg` (HG-LIMS staging) needs an S3 bucket its browser uploads to directly
with presigned multipart URLs, and a worker on a tenant node outside AWS that
signs those URLs and re-reads objects to verify them. Nothing in mksrv modelled
a tenant-owned bucket: SES had a per-tenant IAM user (ADR 0038), but there was
no bucket, no way to mirror a credential into `kv/tenants/<id>/…`, and the only
AppRoles were `tenant-<id>` (bound to the `-dev` policy: read-only over the
whole KV subtree, too much for a worker) and `svc-<id>` (Transit only, no KV).

The alternative was to do it once by hand with `aws` and `bao`. That unblocks
faster, but leaves a bucket, an IAM user, a key and an AppRole that exist in no
repo and rotate by memory — the same trap ADR 0038 documents for the hand-made
SES identities. We chose the declarative path.

## Decision

A tenant YAML may carry a `storage:` block (not to be confused with a stack
descriptor's `storage:`, ADR 0014, which is EBS volumes):

```yaml
storage:
  s3:
    bucket: hg-lims-hgstg-data                      # globally unique; validated
    cors_origins: [https://lims.hgstg.cloud-it.click]
    abort_multipart_days: 3                         # default 3
  approles:
    - name: lims-worker                             # → OpenBao role svc-hgstg-lims-worker
      cidrs: [100.64.0.11/32]                       # required
      kv_read: ["storage-*"]                        # default; names under kv/tenants/<id>/
      secret_id_ttl_days: 90                        # default 90
```

### `storage.s3` (Terraform, `infra/root/main.tf`)

Per tenant: `aws_s3_bucket` plus a **fixed** posture that is deliberately not
configurable — Block Public Access (all four), `BucketOwnerEnforced`, SSE-S3,
no versioning, a bucket policy denying `aws:SecureTransport=false`, tags
(`mksrv:env`, `mksrv:tenant`, `tenant`, `env`), and a lifecycle configuration
with **only** `AbortIncompleteMultipartUpload`. No transitions and no
expirations: retention belongs to the tenant application and arrives later as an
explicit change. CORS (`PUT`/`GET`/`HEAD`, all headers, `ExposeHeaders: ETag`,
3600 s) is created only when `cors_origins` is non-empty; without `ETag` exposed
the browser cannot complete a multipart upload.

A dedicated IAM user `mksrv-<env>-<tenant>-s3` — no console — with one access
key and an inline policy: `ListBucket`, `ListBucketMultipartUploads`,
`GetBucketLocation` on the bucket; `PutObject`, `GetObject`,
`AbortMultipartUpload`, `ListMultipartUploadParts` on its objects. **No
`DeleteObject`, no ACLs.** The key is long-lived on purpose: a presigned URL
dies with the STS session that signed it, and the worker needs 12 h URLs.

Output `tenant_storage_s3` (sensitive, by tenant id) carries bucket, region and
the key; the secret therefore lives in Terraform state (encrypted S3 backend),
exactly as the SES users do.

### Mirroring (`mksrv tenant apply`)

`provisionTenantStorage` copies the key to SSM
(`/mksrv/<env>/storage/tenant_<id>_s3_{access_key_id,secret_access_key}`) and to
`kv/tenants/<id>/storage-s3` with exactly two fields, `access_key_id` and
`secret_access_key`. The KV write goes over **stdin** (`bao kv put <path> -`),
never argv — the ssh error text quotes the whole command line. It rewrites only
when the stored value drifted, since KV v2 versions every write. Rotation (every
90 days) means replacing `aws_iam_access_key.tenant_s3["<id>"]` in the
mksrv-managed Terraform workdir, refreshing the outputs, and running
`mksrv tenant apply <id>`; consumers re-read the secret per job, so nothing is
redeployed. No mksrv command wraps the replace step yet.

### `storage.approles` (OpenBao, `provisionOpenBaoTenants`)

Each entry creates policy and AppRole `svc-<tenant>-<name>` (the `svc-` prefix
shared with the Transit-only tier): read-only on `kv/data/tenants/<id>/<name>`
for each `kv_read` entry (the schema forbids anything but `[a-z0-9-]` and an
optional trailing `*`, so a role cannot leave the tenant's own subtree), tokens
1 h / 4 h, and **both** `token_bound_cidrs` and `secret_id_bound_cidrs` set to
the declared CIDRs, `secret_id_ttl` 90 d (aligned with the key rotation),
`secret_id_num_uses=0`. No bootstrap SecretID is minted: mksrv never needs this
credential.

`mksrv-operator`'s policy gains the four sub-paths (`secret-id`,
`secret-id-accessor/{lookup,destroy}`, `role-id`) for every declared role — one
explicit block each, because this OpenBao does not glob inside a path segment
(see `operatorAppRolePolicyHCL`) — and still never the role definition itself.

### Delivery: `mksrv tenant secret-id <id> --approle <name>`

Mints a **wrapped** SecretID for `svc-<id>-<name>` (same flow as `tenant-<id>`
and `--service`), so nothing travels in the clear and the tenant's human
operator needs no OpenBao permissions on the role. `--approle` and `--service`
are mutually exclusive; the name must be declared. Without `--ttl` the role's
90-day default applies.

### Validation (`mksrv validate`)

Bucket names obey S3 rules (reserved prefixes/suffixes, not an IP) and are
unique across tenants; `approles` require the `openbao` stack; names are unique
within a tenant; the resulting role name must not equal another tenant's
`svc-<id>` (tenant `hg-stg` vs tenant `hg` with approle `stg`); each CIDR must
be IPv4 and no wider than /16.

## Consequences

- One reviewed PR/plan replaces unrecorded `aws`/`bao` commands; destroying the
  block removes the bucket only if empty (no `force_destroy`), so data is never
  deleted by a YAML edit.
- Changing `bucket` forces a new bucket (name is immutable): migrate objects
  first.
- The hosting `mksrv-operator` policy grows by four blocks per declared role.
- The block is independent of DNS: a tenant without a route53 zone can use it.
- Not done: versioning/Object Lock (a production decision), SSE-KMS, and a
  command that wraps the 90-day key rotation.
