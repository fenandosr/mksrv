// SPDX-License-Identifier: Apache-2.0

package model

import (
	"sort"
	"strings"
)

// burstableRAMMB maps a t-family size to MiB. Sizes large and up follow the
// general-purpose (m) sizing in familyRAMMB.
var burstableRAMMB = map[string]int{
	"nano": 512, "micro": 1024, "small": 2048, "medium": 4096,
}

// familyRAMMB maps "<family-letter>.<size>" to MiB for the non-burstable
// families mksrv operators pick.
var familyRAMMB = map[string]int{
	// m7g / m6g / m6i / m5 — 4 GiB per vCPU
	"m.medium": 4096, "m.large": 8192, "m.xlarge": 16384, "m.2xlarge": 32768, "m.4xlarge": 65536, "m.8xlarge": 131072,
	// c7g / c6g / c6i / c5 — 2 GiB per vCPU
	"c.large": 4096, "c.xlarge": 8192, "c.2xlarge": 16384, "c.4xlarge": 32768, "c.8xlarge": 65536,
	// r7g / r6g / r6i / r5 — 8 GiB per vCPU
	"r.large": 16384, "r.xlarge": 32768, "r.2xlarge": 65536, "r.4xlarge": 131072,
}

// suggestedGenerations are the instance families offered as completions: the
// generations operators actually pick, by their letter family. The SIZES come
// from the RAM tables above, so a size mksrv can plan capacity for is the only
// kind suggested. It is a suggestion list, not a whitelist: any other type that
// matches the schema's pattern is still accepted.
var suggestedGenerations = []string{
	"t3", "t3a", "t4g",
	"m5", "m6g", "m6i", "m7g", "m7i",
	"c5", "c6g", "c6i", "c7g",
	"r5", "r6g", "r6i", "r7g",
}

// SuggestedInstanceTypes returns the instance types to offer as completions
// (e.g. "t4g.small", "m7g.large"), sorted. Every entry is one InstanceRAMMB
// knows, so mksrv can size swap and warn about overcommit for it.
func SuggestedInstanceTypes() []string {
	var out []string
	for _, gen := range suggestedGenerations {
		var sizes []string
		if gen[0] == 't' {
			for size := range burstableRAMMB {
				sizes = append(sizes, size)
			}
			// t3 and t4g also come in large and up, sized like the m family.
			for key := range familyRAMMB {
				if fam, size, _ := strings.Cut(key, "."); fam == "m" && size != "medium" {
					sizes = append(sizes, size)
				}
			}
		} else {
			for key := range familyRAMMB {
				if fam, size, _ := strings.Cut(key, "."); fam == gen[:1] {
					sizes = append(sizes, size)
				}
			}
		}
		for _, size := range sizes {
			if t := gen + "." + size; knownInstanceType(t) {
				out = append(out, t)
			}
		}
	}
	sort.Strings(out)
	return out
}

func knownInstanceType(t string) bool { _, ok := InstanceRAMMB(t); return ok }

// InstanceRAMMB returns an EC2 instance type's memory in MiB and whether the
// type is known to mksrv.
func InstanceRAMMB(instanceType string) (int, bool) {
	family, size, ok := strings.Cut(instanceType, ".")
	if !ok || family == "" {
		return 0, false
	}
	if strings.HasPrefix(family, "t") {
		if mb, ok := burstableRAMMB[size]; ok {
			return mb, true
		}
		if mb, ok := familyRAMMB["m."+size]; ok {
			return mb, true
		}
		return 0, false
	}
	if mb, ok := familyRAMMB[family[:1]+"."+size]; ok {
		return mb, true
	}
	return 0, false
}

// swapOSHeadroomMB is reserved for the OS and page cache on top of the stacks' hints.
const swapOSHeadroomMB = 512

// SwapForStacks derives the swapfile size (MiB) for a host: the deficit between
// (sum of the stacks' min_ram_mb + OS headroom) and instance RAM, rounded up to
// 512 and capped at min(instanceRAM, 4096). Returns 0 when there is no deficit.
// Falls back to the legacy rule (2048 when identity is present, else 0) when the
// instance type is unknown.
func SwapForStacks(stacks []string, catalog map[string]Stack, instanceType string) int {
	have, known := InstanceRAMMB(instanceType)
	if !known {
		for _, s := range stacks {
			if s == "identity" {
				return 2048
			}
		}
		return 0
	}
	want := swapOSHeadroomMB
	for _, s := range stacks {
		want += catalog[s].Resources.MinRAMMB
	}
	deficit := want - have
	if deficit <= 0 {
		return 0
	}
	swap := ((deficit + 511) / 512) * 512
	maxSwap := have
	if maxSwap > 4096 {
		maxSwap = 4096
	}
	if swap > maxSwap {
		swap = maxSwap
	}
	return swap
}
