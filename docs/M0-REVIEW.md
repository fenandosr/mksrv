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

## Required decision before M1

ADR 0006 is temporary. Before implementing AWS, Terraform execution, or SSH,
replace the M0 dependency-free command/YAML/schema adapters with Cobra,
`sigs.k8s.io/yaml`, and `santhosh-tekuri/jsonschema/v6`, then retain the existing
behavioral tests as the compatibility gate.
