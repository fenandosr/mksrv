// SPDX-License-Identifier: Apache-2.0

package cli

import (
	"strings"
	"testing"
)

func TestRedisACLLine(t *testing.T) {
	t.Parallel()
	got := redisACLLine("bitabit", "p4ss-word_1")
	for _, want := range []string{
		"user bitabit on >p4ss-word_1",
		"resetchannels",
		"~bitabit:* &bitabit:*",
		"+@all -@dangerous -@admin",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("ACL line missing %q: %s", want, got)
		}
	}
}
