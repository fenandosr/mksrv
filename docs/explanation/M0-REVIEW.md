# M0 review packet

Date: 2026-08-27

This repository stops at M0. No M1 infrastructure or cloud mutation behavior is
implemented.

## Acceptance evidence

| M0 criterion | Result | Local evidence |
|---|---|---|
| Repository layout and Go module | Pass | Required roots and package boundaries exist |
| CLI skeleton | Pass | `version`, `validate`, and `doctor` execute |
| JSON mode | Pass | All three implemented commands emit one JSON document on stdout |
| Embedded engine | Pass | `infra`, `stacks`, and `schemas` extract atomically to a versioned cache |
| Four schemas | Pass | JSON parsing test sees exactly four draft 2020-12 schemas |
| Example workspace | Pass | 2 hosts, 1 tenant, 2 users, 7 catalog stacks |
| Schema + semantic validation | Pass | Positive and negative automated cases |
| Unit tests | Pass | `go test ./...` and `go test -race ./...` |
| Go static checks | Partial local / configured CI | `go vet ./...` passed; staticcheck is configured but unavailable locally |
| Public data hygiene | Pass locally | Custom scanner passed; gitleaks configured but unavailable locally |
| Snapshot builds | Pass through offline fallback | Linux amd64/arm64, macOS amd64/arm64, Windows amd64, engine tar, checksums |
| GoReleaser snapshot | Configured / not locally executed | Tool unavailable locally; CI uses GoReleaser v2 |
| Terraform fmt/validate | Configured / not locally executed | Terraform unavailable locally; CI validates every root/module |
| GitHub Actions | Authored / not remotely executed | CI and tagged release workflows are checked in |

## Review commands

```bash
make test
make lint
make schema-lint
make hygiene
make build
./bin/mksrv validate examples/workspace --json
./bin/mksrv doctor --workspace examples/workspace --json
make snapshot
(cd dist && sha256sum -c checksums.txt)
```

## M1 entry gate — closed

The temporary M0 command/YAML/schema adapters have been replaced with Cobra,
`sigs.k8s.io/yaml`, and `santhosh-tekuri/jsonschema/v6`. The existing behavioral
tests passed unchanged and served as the compatibility gate. ADR 0007 supersedes
ADR 0006. AWS, Terraform execution, and SSH work (M1+) may now proceed.

The binary also embeds `time/tzdata` so `-trimpath` release builds validate
workspace timezones on hosts without system zoneinfo.
