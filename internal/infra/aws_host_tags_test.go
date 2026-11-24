// SPDX-License-Identifier: Apache-2.0

package infra

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// instanceBlock returns the text of resource "aws_instance" "host" in the aws-host module.
func instanceBlock(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "infra", "modules", "aws-host", "main.tf"))
	if err != nil {
		t.Fatal(err)
	}
	src := string(raw)
	start := strings.Index(src, `resource "aws_instance" "host" {`)
	if start < 0 {
		t.Fatal(`resource "aws_instance" "host" not found`)
	}
	end := strings.Index(src[start:], "\n}\n")
	if end < 0 {
		t.Fatal("end of the aws_instance block not found")
	}
	return src[start : start+end]
}

// The instance must not tag the volumes attached to it. `volume_tags` tags every one of them,
// data volumes included, and those tag themselves (aws_ebs_volume.extra); the two resources then
// overwrote each other's Name and mksrv:volume tags on every apply, and a tag-based snapshot
// policy (ADR 0046) would have silently lost its volumes. The root volume is tagged through
// root_block_device, and volume_tags is ignored so that an instance that once had it does not
// plan to delete those tags from every attached volume.
func TestInstanceDoesNotManageTheTagsOfItsDataVolumes(t *testing.T) {
	t.Parallel()
	block := instanceBlock(t)

	if regexp.MustCompile(`(?m)^\s*volume_tags\s*=`).MatchString(block) {
		t.Fatal("aws_instance.host must not set volume_tags: it fights aws_ebs_volume's own tags (see ADR 0046)")
	}
	if !regexp.MustCompile(`ignore_changes\s*=\s*\[[^\]]*\bvolume_tags\b`).MatchString(block) {
		t.Error("aws_instance.host must list volume_tags in lifecycle.ignore_changes, or an apply strips shared tags from the data volumes")
	}
	root := regexp.MustCompile(`(?s)root_block_device\s*\{.*?\n  \}`).FindString(block)
	if root == "" {
		t.Fatal("root_block_device not found")
	}
	if !regexp.MustCompile(`(?m)^\s*tags\s*=`).MatchString(root) {
		t.Error("the root volume must be tagged from root_block_device")
	}
}

// resourceBlock returns the text of the named resource in a Terraform file, up to the closing
// brace at column 0.
func resourceBlock(t *testing.T, relPath, header string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", relPath))
	if err != nil {
		t.Fatal(err)
	}
	src := string(raw)
	start := strings.Index(src, header)
	if start < 0 {
		t.Fatalf("%s not found in %s", header, relPath)
	}
	end := strings.Index(src[start:], "\n}\n")
	if end < 0 {
		t.Fatalf("end of %s not found", header)
	}
	return src[start : start+end]
}

// ADR 0046 phase 1: the DLM policy selects volumes by tag, so exactly the data volumes (the one
// mounted at /var/lib/mksrv and every dedicated stack volume) carry mksrv:snapshot, and the root
// volume and the instance never do. If this regresses, snapshots stop being taken without an error.
func TestOnlyDataVolumesCarryTheSnapshotTag(t *testing.T) {
	t.Parallel()
	const module = "infra/modules/aws-host/main.tf"
	for _, header := range []string{`resource "aws_ebs_volume" "data" {`, `resource "aws_ebs_volume" "extra" {`} {
		if block := resourceBlock(t, module, header); !strings.Contains(block, "local.snapshot_tags") {
			t.Errorf("%s must merge local.snapshot_tags into its tags", header)
		}
	}
	for _, header := range []string{`resource "aws_instance" "host" {`} {
		if block := resourceBlock(t, module, header); strings.Contains(block, "snapshot_tags") || strings.Contains(block, "mksrv:snapshot") {
			t.Errorf("%s must not carry the snapshot tag: root volumes are not snapshotted", header)
		}
	}
}

func TestSnapshotPolicySelectsTheTaggedVolumesOfThisEnvironment(t *testing.T) {
	t.Parallel()
	policy := resourceBlock(t, "infra/root/main.tf", `resource "aws_dlm_lifecycle_policy" "volumes" {`)
	for _, want := range []string{
		`"mksrv:snapshot" = "daily"`, // the tag the module sets
		`"mksrv:env"      = local.env`,
		`resource_types = ["VOLUME"]`,
		"retain_rule",
		"var.snapshot_retention_count",
		"var.snapshot_time_utc",
	} {
		if !strings.Contains(policy, want) {
			t.Errorf("the DLM policy must contain %q", want)
		}
	}
	// The value the root passes to every host must be the one the policy targets.
	hostCall := resourceBlock(t, "infra/root/main.tf", `module "aws_host" {`)
	if !regexp.MustCompile(`snapshot_tag\s*=\s*var\.snapshot_enabled \? "daily" : ""`).MatchString(hostCall) {
		t.Error(`the aws_host module call must pass snapshot_tag = var.snapshot_enabled ? "daily" : ""`)
	}
}
