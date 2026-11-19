# Tenant `storage:` (S3 + AppRoles)

Declared in `tenants/<id>.yaml`; rationale in
[ADR 0041](../explanation/decisions/0041-tenant-storage-s3-and-approles.md).
Schema: `schemas/tenant.v1.json`. Not the stack-descriptor `storage:` of
[Per-stack storage](storage.md).

| Field | Meaning | Default |
|---|---|---|
| `storage.s3.bucket` | S3 bucket name (globally unique) | required |
| `storage.s3.cors_origins` | `https://` origins allowed to `PUT`/`GET`/`HEAD`; none = no CORS | none |
| `storage.s3.abort_multipart_days` | abort incomplete multipart uploads after N days | 3 |
| `storage.approles[].name` | role is `svc-<tenant>-<name>` | required |
| `storage.approles[].cidrs` | bind SecretID and tokens to these IPv4 CIDRs (≤ /16 wide) | required |
| `storage.approles[].kv_read` | secret names (`[a-z0-9_-]`, trailing `*` allowed) under `kv/tenants/<id>/` the role may read, e.g. `database-hgstg_airflow`; an explicit list **replaces** the default, so keep `storage-*` if you still need it | `["storage-*"]` |
| `storage.approles[].secret_id_ttl_days` | lifetime of a minted SecretID | 90 |

## Workflow

```sh
mksrv apply --infra-only          # bucket, IAM user + key (review the plan)
mksrv tenant apply <id>           # mirror key → SSM + kv/tenants/<id>/storage-s3; create AppRole
mksrv tenant secret-id <id> --approle <name> --name <who/where>   # wrapped SecretID
BAO_TOKEN=<wrapping token> bao unwrap                              # recipient, once
```

`kv/tenants/<id>/storage-s3` holds `access_key_id` and `secret_access_key`.

## Fixed posture

Block Public Access (all four), `BucketOwnerEnforced`, SSE-S3, no versioning,
TLS-only bucket policy, lifecycle = abort-incomplete-multipart only. The IAM
user can list/read/write objects and manage multipart uploads; it cannot delete
objects or touch ACLs.

## Rotating the S3 key (every 90 days)

Replace `aws_iam_access_key.tenant_s3["<id>"]` in the mksrv-managed Terraform
workdir (no mksrv command wraps this step yet), then:

```sh
mksrv apply --infra-only          # refresh outputs.json
mksrv tenant apply <id>           # re-mirrors the new key (KV drift check)
```

## Rotating the SecretID

Mint a new one with `mksrv tenant secret-id … --approle`, deliver it, then
`--list` and `--revoke <accessor>` the old one.
