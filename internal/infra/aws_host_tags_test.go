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

// ADR 0046 phase 1: the DLM policies select volumes by tag, so exactly the data volumes (the one
// mounted at /var/lib/mksrv and every dedicated stack volume) carry mksrv:snapshot, and the root
// volume and the instance never do. If this regresses, snapshots stop being taken without an error.
func TestOnlyDataVolumesCarryTheSnapshotTag(t *testing.T) {
	t.Parallel()
	const module = "infra/modules/aws-host/main.tf"
	for header, tier := range map[string]string{
		`resource "aws_ebs_volume" "data" {`:  "var.data_snapshot_tier",
		`resource "aws_ebs_volume" "extra" {`: "each.value.tier",
	} {
		block := resourceBlock(t, module, header)
		if !strings.Contains(block, "local.snapshot_tags_by_tier") || !strings.Contains(block, tier) {
			t.Errorf("%s must merge the tags of its tier (%s) from local.snapshot_tags_by_tier", header, tier)
		}
	}
	if !strings.Contains(resourceBlock(t, "infra/modules/aws-host/main.tf", "locals {"), `"mksrv:snapshot-group" = "${var.env}-${tier}"`) {
		t.Error("the module must set mksrv:snapshot-group = <env>-<tier>, the value the policies target")
	}
	if block := resourceBlock(t, module, `resource "aws_instance" "host" {`); strings.Contains(block, "snapshot_tags") || strings.Contains(block, "mksrv:snapshot") {
		t.Error("aws_instance.host must not carry the snapshot tags: root volumes are not snapshotted")
	}
}

func TestSnapshotPoliciesSelectTheTaggedVolumesOfThisEnvironmentPerTier(t *testing.T) {
	t.Parallel()
	policy := resourceBlock(t, "infra/root/main.tf", `resource "aws_dlm_lifecycle_policy" "volumes" {`)
	for _, want := range []string{
		"for_each           = var.snapshot_enabled ? var.snapshot_tiers : {}", // one policy per tier
		`"mksrv:snapshot-group" = "${local.env}-${each.key}"`,                 // the single tag the module sets
		`resource_types = ["VOLUME"]`,
		"interval      = each.value.rpo_hours",
		"count = each.value.retain",
		"var.snapshot_time_utc",
	} {
		if !strings.Contains(policy, want) {
			t.Errorf("the DLM policy must contain %q", want)
		}
	}
	// DLM's target_tags select a volume that carries ANY of them, so a second tag (mksrv:env was one) makes every
	// policy take every volume of the environment, root volumes included. Seen live: 66 snapshots of 22 volumes.
	inTargets, tags := false, 0
	for _, line := range strings.Split(policy, "\n") {
		trimmed := strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(trimmed, "target_tags = {"):
			inTargets = true
		case inTargets && trimmed == "}":
			inTargets = false
		case inTargets && strings.HasPrefix(trimmed, `"`):
			tags++
		}
	}
	if tags != 1 {
		t.Errorf("target_tags must hold exactly one tag (DLM matches ANY of them), found %d", tags)
	}
	// What the root passes to every host must agree with what the policies target.
	hostCall := resourceBlock(t, "infra/root/main.tf", `module "aws_host" {`)
	for _, re := range []string{
		`data_snapshot_tier\s*=\s*var\.snapshot_enabled \? try\(var\.host_data_tier\[each\.key\], "standard"\) : ""`,
		`snapshot_rpo_hours\s*=\s*\{ for tier, t in var\.snapshot_tiers : tier => t\.rpo_hours \}`,
		`tier = var\.snapshot_enabled \? v\.tier : ""`,
	} {
		if !regexp.MustCompile(re).MatchString(hostCall) {
			t.Errorf("the aws_host module call must match %s", re)
		}
	}
}

// The three tiers and their defaults are part of the contract with the Go side (internal/model
// priorities) and with the snapshot check: adding or renaming one needs all of them.
func TestSnapshotTierDefaults(t *testing.T) {
	t.Parallel()
	raw, err := os.ReadFile(filepath.Join("..", "..", "infra", "root", "variables.tf"))
	if err != nil {
		t.Fatal(err)
	}
	src := string(raw)
	for _, want := range []string{
		"critical = { rpo_hours = 24, retain = 30 }",
		"standard = { rpo_hours = 24, retain = 7 }",
		"low      = { rpo_hours = 24, retain = 2 }",
	} {
		if !strings.Contains(src, want) {
			t.Errorf("snapshot_tiers default must contain %q", want)
		}
	}
}
