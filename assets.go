// SPDX-License-Identifier: Apache-2.0

// Package engineassets exposes the immutable engine and workspace-template
// files embedded in the mksrv binary.
package engineassets

import "embed"

// FS contains the public engine tree and the synthetic workspace example.
// all:stacks is required so the _shared directory is included.
//
//go:embed infra infra/root/.terraform.lock.hcl all:stacks schemas examples/workspace
var FS embed.FS
