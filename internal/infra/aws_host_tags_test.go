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
