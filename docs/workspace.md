# Workspace contract

A workspace is private deployment data operated by the public `mksrv` binary.
It must not be nested inside the engine repository.

```text
workspace/
├── deployment.yaml
├── tenants/
│   ├── acme.yaml
│   └── acme.users.yaml
├── secrets.sops.yaml       # optional until secret-bearing commands are used
├── mksrv.lock
└── .mksrv/                 # generated files, Terraform data/state, outputs
```

## Discovery precedence

1. Positional path accepted by a command, such as `validate PATH`.
2. Global `--workspace PATH`.
3. `MKSRV_WORKSPACE`.
4. Walk upward from the current directory looking for `deployment.yaml` or
   `.mksrv/`.

## `deployment.yaml`

Declares environment identity, AWS and backend configuration, DNS, public
identity endpoints, and fleet hosts. Each host selects `aws` or `existing` and a
list of catalog stacks. Schema: `schemas/deployment.v1.json`.

## Tenant documents

`tenants/<id>.yaml` declares a client company. `base_domain` is required and all
service FQDNs derive from it. Until M6, a tenant without a DNS override must be
inside the deployment root zone. Schema: `schemas/tenant.v1.json`.

## Users documents

`tenants/<id>.users.yaml` is optional and declarative. M5 reconciliation creates
missing Keycloak users, updates groups and enabled state, and may prune absent
users only with an explicit flag. Schema: `schemas/users.v1.json`.

## Lock file

`mksrv.lock` records the engine version used by the last successful apply. A
newer binary may proceed and updates the lock only after success. An older binary
refuses unless the operator explicitly supplies `--allow-downgrade`.

## Generated data

Everything under `.mksrv/` is disposable or reconstructable except Terraform
state when a local backend is deliberately selected. The normal S3 backend is
private and locking uses DynamoDB. No generated file belongs in the public repo.
