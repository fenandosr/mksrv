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
		"~bitabit:*",
		"+@all -@dangerous -@admin",
		// Kombu's own bookkeeping keys (unprefixable, see celeryBookkeepingKeys) —
		// granted to every tenant so a Celery worker doesn't crash with
		// redis.exceptions.NoPermissionError on its restore_visible() lock.
		"~unacked", "~unacked_index", "~unacked_mutex", "~_kombu.binding.*", "~*.reply.celery.pidbox*",
		// Channels are unscoped -- Celery/Kombu's pidbox pub/sub kept
		// intermittently failing even with a specific channel grant.
		"&*",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("ACL line missing %q: %s", want, got)
		}
	}
	if strings.Contains(got, "&bitabit:*") {
		t.Fatalf("channels should be unscoped (&*), not tenant-prefixed:\n%s", got)
	}
}
