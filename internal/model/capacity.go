// SPDX-License-Identifier: Apache-2.0

package model

import "strings"

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
