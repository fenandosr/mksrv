// SPDX-License-Identifier: Apache-2.0

package cli

import "testing"

func TestThemeName(t *testing.T) {
	t.Parallel()
	if got := themeName("bitabit"); got != "mksrv-bitabit" {
		t.Fatalf("themeName(bitabit) = %q", got)
	}
}
