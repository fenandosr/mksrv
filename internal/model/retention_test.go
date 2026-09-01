// SPDX-License-Identifier: Apache-2.0

package model

import "testing"

func TestRetentionResolved(t *testing.T) {
	t.Parallel()
	// nil -> defaults
	d := (*RetentionConfig)(nil).Resolved()
	if d.MetricsDays != 15 || d.LogsDays != 14 || d.MetricsGBPerDay != 0.3 || d.LogsGBPerDay != 2 {
		t.Fatalf("nil defaults wrong: %+v", d)
	}
	// partial override keeps the rest at default
	r := &RetentionConfig{MetricsDays: 30, LogsGBPerDay: 5}
	got := r.Resolved()
	if got.MetricsDays != 30 || got.LogsDays != 14 || got.MetricsGBPerDay != 0.3 || got.LogsGBPerDay != 5 {
		t.Fatalf("partial override wrong: %+v", got)
	}
	if got.LogsHours() != 336 {
		t.Fatalf("LogsHours = %d, want 336", got.LogsHours())
	}
	if (RetentionConfig{LogsDays: 21}).LogsHours() != 504 {
		t.Fatal("LogsHours 21d")
	}
}
