# `mksrv▊`

**Service stacks as code — deploy anywhere, wired by the mesh.**

`mksrv` is an open-source engine for declaring and operating self-hosted service
stacks on AWS and existing Rocky Linux hosts. The public repository contains the
Go CLI, Terraform engine, stack catalog, schemas, documentation, and synthetic
examples. Real deployment data always lives in a separate private workspace.

> **Implementation status:** milestone **M0** is complete in this repository.
> `version`, `validate`, and `doctor` are functional; infrastructure creation,
> SSH deployment, tenant reconciliation, users, mail, and additional DNS
> providers are intentionally deferred to M1–M6.

## Core boundary

```text
public mksrv engine                      private operator workspace
┌──────────────────────────────┐         ┌───────────────────────────────┐
│ CLI + schemas                │ reads → │ deployment.yaml               │
│ Terraform modules/root       │         │ tenants/*.yaml                │
│ stack descriptors/templates  │         │ tenants/*.users.yaml          │
│ synthetic examples           │         │ secrets.sops.yaml             │
└──────────────────────────────┘         │ mksrv.lock + .mksrv/ state    │
                                         └───────────────────────────────┘
```

The engine repository must never contain real hostnames, account identifiers,
email addresses, routable IP addresses, credentials, Terraform state, or tenant
data.

## M0 quick start

Requirements: Go 1.25 or newer. The fleet host requirement for later milestones
is Rocky Linux 9 with systemd, SELinux enforcing, and Podman Quadlet support.

The CLI is built on `spf13/cobra`; workspace YAML is parsed with
`sigs.k8s.io/yaml` and validated with `santhosh-tekuri/jsonschema/v6`. The IANA
timezone database is embedded via `time/tzdata`. From M1, Terraform is executed
through `hashicorp/terraform-exec` and located or downloaded (version pinned by
`internal/tf.Version`) with `hashicorp/hc-install`.

```bash
make build
./bin/mksrv version
./bin/mksrv validate examples/workspace
./bin/mksrv doctor --workspace examples/workspace
```

Machine-readable output is available on every implemented command:

```bash
./bin/mksrv validate examples/workspace --json
```

Run the local quality gates:

```bash
make test
make lint
make schema-lint
make hygiene
make snapshot
```

`make snapshot` uses GoReleaser when installed and a deterministic local
cross-build fallback otherwise. Release tags always use GoReleaser in GitHub
Actions.

## Implemented commands

```text
mksrv version
mksrv validate [PATH]
mksrv doctor
```

Global flags currently implemented: `--workspace`, `--verbose/-v`,
`--quiet/-q`, `--json`, `--no-color`, `--yes`, and `--allow-downgrade`.
Human diagnostics go to stderr; JSON data goes to stdout.

## Validation performed in M0

The validator loads the four embedded draft 2020-12 schemas and then enforces
cross-file semantics, including:

- every assigned stack exists and supports the host target;
- exactly one fleet host carries `base` and exactly one carries `identity`;
- stack dependencies are present and the catalog is acyclic;
- cloud-only stacks cannot run on existing hosts;
- tenant stacks exist, are tenant-consumable, and are assigned somewhere;
- tenant IDs, filenames, realms, domains, users, CIDR, timezone, and engine
  versions are internally consistent.

The synthetic workspace under `examples/workspace/` is validated by tests and CI.

## Repository map

```text
cmd/mksrv/             thin executable entry point
internal/cli/          command implementations
internal/model/        workspace and stack contracts
internal/workspace/    discovery, loading, schema and semantic validation
internal/engine/       embedded catalog and atomic cache extraction
internal/schema/       embedded schema validation
infra/                 Terraform root and module contracts
stacks/                extensible stack catalog
schemas/               public JSON Schemas
examples/workspace/    fake CI-safe workspace
docs/                  architecture, contracts, roadmap, and ADRs
```

## Engine and workspace versions

Release builds stamp `version`, `commit`, and build date through ldflags. The
same release version identifies the embedded engine. A workspace records its
last successful engine in `mksrv.lock`. Development builds use `dev` and refresh
the engine cache on each extraction.

## Forking the module

The module placeholder is intentionally centralized through the Go module path
and the executable build-info default. A fork should replace the placeholder in
`go.mod`, update Go import paths with `go mod edit -module`, and change the
`modulePath` build default in `cmd/mksrv/main.go`. Release ldflags remain the
source of version, commit, and date values.

## Roadmap

- **M1:** AWS/existing-host infrastructure, state bootstrap, Terraform wrapper,
  `init`, `plan --infra-only`, and `apply --infra-only`.
- **M2:** renderer, SSH bootstrap/deploy, `base`, `identity`, mesh join, status.
- **M3:** tenant lifecycle, realms, per-tenant Headscale, DNS records.
- **M4:** database, files, analytics, and monitoring data-plane stacks.
- **M5:** Keycloak users, SES, and optional inbound mail.
- **M6:** Cloudflare/RFC2136, DNS overrides, secrets UX, polish, Spanish docs.

See `docs/quickstart.md`, `docs/workspace.md`, and
`docs/implementation-status.md`.

## License

Apache License 2.0. Go source files carry SPDX identifiers.
