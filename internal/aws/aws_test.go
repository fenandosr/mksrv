// SPDX-License-Identifier: Apache-2.0

package aws

import "testing"

func TestLocationConstraint(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		"us-east-1":      "",
		"":               "",
		"us-west-2":      "us-west-2",
		"eu-central-1":   "eu-central-1",
		"ap-southeast-2": "ap-southeast-2",
	}
	for region, want := range cases {
		if got := locationConstraint(region); got != want {
			t.Errorf("locationConstraint(%q) = %q, want %q", region, got, want)
		}
	}
}

func TestEnsureBackendRejectsEmptySpec(t *testing.T) {
	t.Parallel()
	clients := &Clients{region: "us-east-1"}
	if _, err := clients.EnsureBackend(t.Context(), BackendSpec{Bucket: "only-bucket"}); err == nil {
		t.Fatal("expected error for missing dynamodb table")
	}
}
