# `mksrv tenant contract`

What one tenant's application sees of the fleet, as JSON
([ADR 0048](../explanation/decisions/0048-tenant-contract.md)).

```bash
mksrv tenant contract <id> --workspace <dir> > <id>.contract.json
```

Offline and read-only: no credential, no connection, nothing written to the
workspace. The output is always JSON and holds no secret. It describes what is
**declared**, not what is deployed.

| Key | Content | Present when |
|---|---|---|
| `contract_version`, `engine`, `tenant` | Format version, binary version, tenant id | always |
| `images` | stack → app → declared image | always |
| `keycloak` | `issuer`, `realm` (creation body), `realm_updates`, `groups`, `admin_group`, `clients[]` with `representation` and `mappers` | always |
| `database` | `name`, `schema`, `global_sql`, `tenant_sql` | stack `database` |
| `openbao` | `kv_mount`, `policies` (HCL), `approles`, `transit_key_args`, `oidc_mount`, `oidc_config_args`, `oidc_roles` | stack `openbao` |
| `secrets` | entry under `kv/tenants/<id>/` → field names; only what mksrv writes | stack `openbao` |
| `cache` | `acl` (the `users.acl` line), `config` (`redis.conf`) | stack `cache` |
| `postgrest` | `unit` (the Quadlet) | PostgREST enabled |
| `storage_s3` | bucket declaration | `storage.s3` |
| `not_covered` | what must still be taken from the repository | always |

## Using it for a local analogue

- Run `global_sql` and then `tenant_sql` with `psql -v ON_ERROR_STOP=1` as a
  superuser, against the `postgres` database. Both use `\gexec`; the second also
  `\connect`. They can be run again.
- Replace the placeholders first: `__PASSWORD__` (the `<id>_login` role, and the
  Redis user), `__AUTH_PASSWORD__` (`<id>_auth`), `__PASSWORD_<role>__` (service
  roles), `__SECRET__` (client secrets), `__PRIVATE_IP__` and `__POSTGRES__` (the
  PostgREST unit).
- The AppRole CIDRs are the real node's: widen them locally.
- `realm_updates` includes the password reset, which the fleet enables together
  with SMTP.
