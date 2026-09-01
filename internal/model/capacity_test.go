// SPDX-License-Identifier: Apache-2.0

package model

import "testing"

func TestInstanceRAMMB(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in    string
		mb    int
		known bool
	}{
		{"t4g.nano", 512, true},
		{"t4g.small", 2048, true},
		{"t4g.medium", 4096, true},
		{"t3.large", 8192, true},
		{"m7g.large", 8192, true},
		{"c7g.large", 4096, true},
		{"r6g.large", 16384, true},
		{"x2gd.large", 0, false},
		{"garbage", 0, false},
		{"", 0, false},
	}
	for _, c := range cases {
		mb, known := InstanceRAMMB(c.in)
		if mb != c.mb || known != c.known {
			t.Errorf("InstanceRAMMB(%q) = %d,%v want %d,%v", c.in, mb, known, c.mb, c.known)
		}
	}
}

func TestSwapForStacks(t *testing.T) {
	t.Parallel()
	catalog := map[string]Stack{
		"base":     {Resources: StackResources{MinRAMMB: 512}},
		"identity": {Resources: StackResources{MinRAMMB: 2048}},
		"mail":     {Resources: StackResources{MinRAMMB: 1024}},
		"security": {Resources: StackResources{MinRAMMB: 256}},
	}
	// edge set on t4g.small: 512+2048+1024+512 headroom = 4096, have 2048 -> deficit 2048.
	if got := SwapForStacks([]string{"base", "identity", "mail"}, catalog, "t4g.small"); got != 2048 {
		t.Fatalf("edge swap = %d, want 2048", got)
	}
	// light set on t4g.small: 256+512 = 768 < 2048 -> no swap.
	if got := SwapForStacks([]string{"security"}, catalog, "t4g.small"); got != 0 {
		t.Fatalf("light swap = %d, want 0", got)
	}
	// roomy instance: no deficit.
	if got := SwapForStacks([]string{"base", "identity", "mail"}, catalog, "t4g.large"); got != 0 {
		t.Fatalf("t4g.large swap = %d, want 0", got)
	}
	// unknown instance type falls back to the legacy identity rule.
	if got := SwapForStacks([]string{"identity"}, catalog, "x2gd.large"); got != 2048 {
		t.Fatalf("unknown-type swap = %d, want 2048", got)
	}
	if got := SwapForStacks([]string{"security"}, catalog, "x2gd.large"); got != 0 {
		t.Fatalf("unknown-type no-identity swap = %d, want 0", got)
	}
}
