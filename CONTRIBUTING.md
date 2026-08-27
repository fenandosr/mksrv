# Contributing to mksrv

Thank you for helping build `mksrv`. Keep changes milestone-sized and preserve
the strict separation between the public engine and private deployment data.

## Development workflow

1. Use Go 1.23 or newer.
2. Create a focused branch and add tests with the implementation.
3. Run `make test lint schema-lint hygiene`.
4. Run `make tf-validate` when Terraform is available.
5. Record non-obvious architecture choices in `docs/decisions/`.
6. Keep examples synthetic and use only the documented reserved values.

## Public-repository safety

Never commit deployment workspaces, Terraform state, SOPS files, private keys,
AWS account IDs or ARNs, real domains or email addresses, or public IPs that are
not from the TEST-NET ranges. `scripts/public-hygiene.sh` and gitleaks are both
required CI checks.

## Go conventions

- Every `.go` file starts with `// SPDX-License-Identifier: Apache-2.0`.
- Run `gofmt`; keep `go vet` and staticcheck clean.
- Wrap errors with context and `%w` where callers may inspect them.
- Honor `context.Context`; do not panic outside the executable boundary.
- Machine data belongs on stdout; human diagnostics belong on stderr.
- Never log secret values.

## Milestone discipline

Do not implement later milestone behavior in an earlier milestone merely because
it is convenient. It is fine to create a stable interface or a validation-safe
placeholder, but label it clearly and include acceptance tests for the current
milestone.
