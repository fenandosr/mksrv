// SPDX-License-Identifier: Apache-2.0

package secrets

import (
	"context"
	"testing"

	awssdk "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ssm"
	ssmtypes "github.com/aws/aws-sdk-go-v2/service/ssm/types"
)

type fakeSSM struct {
	store map[string]string
	puts  int
}

func (f *fakeSSM) GetParameter(_ context.Context, in *ssm.GetParameterInput, _ ...func(*ssm.Options)) (*ssm.GetParameterOutput, error) {
	value, ok := f.store[*in.Name]
	if !ok {
		return nil, &ssmtypes.ParameterNotFound{}
	}
	return &ssm.GetParameterOutput{Parameter: &ssmtypes.Parameter{Value: awssdk.String(value)}}, nil
}

func (f *fakeSSM) PutParameter(_ context.Context, in *ssm.PutParameterInput, _ ...func(*ssm.Options)) (*ssm.PutParameterOutput, error) {
	if f.store == nil {
		f.store = map[string]string{}
	}
	f.store[*in.Name] = *in.Value
	f.puts++
	return &ssm.PutParameterOutput{}, nil
}

func TestEnsureRandomCreatesOnceThenReads(t *testing.T) {
	t.Parallel()
	api := &fakeSSM{store: map[string]string{}}
	r := NewResolver(api, "prod")

	first, err := r.EnsureRandom(context.Background(), "/mksrv/{env}/identity/kc_db_password", 32)
	if err != nil {
		t.Fatalf("EnsureRandom() error = %v", err)
	}
	if len(first) < 20 {
		t.Fatalf("generated value too short: %q", first)
	}
	for _, c := range first {
		if !((c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9')) {
			t.Fatalf("generated value has a non-alphanumeric char %q: %q", c, first)
		}
	}
	if _, ok := api.store["/mksrv/prod/identity/kc_db_password"]; !ok {
		t.Fatal("parameter not stored under expanded name")
	}

	second, err := r.EnsureRandom(context.Background(), "/mksrv/{env}/identity/kc_db_password", 32)
	if err != nil {
		t.Fatal(err)
	}
	if second != first || api.puts != 1 {
		t.Fatalf("EnsureRandom not idempotent: puts=%d first=%q second=%q", api.puts, first, second)
	}
}

func TestIsNotFound(t *testing.T) {
	t.Parallel()
	api := &fakeSSM{store: map[string]string{}}
	r := NewResolver(api, "prod")

	_, err := r.Get(context.Background(), "/mksrv/{env}/does/not/exist")
	if err == nil {
		t.Fatal("Get() on a missing parameter returned no error")
	}
	if !IsNotFound(err) {
		t.Fatalf("IsNotFound(%v) = false, want true", err)
	}

	if err := r.Put(context.Background(), "/mksrv/{env}/present", "v"); err != nil {
		t.Fatal(err)
	}
	_, err = r.Get(context.Background(), "/mksrv/{env}/present")
	if err != nil {
		t.Fatalf("Get() on a present parameter error = %v", err)
	}
	if IsNotFound(err) {
		t.Fatal("IsNotFound(nil) = true, want false")
	}
}

func TestRandomAlphanumeric(t *testing.T) {
	t.Parallel()
	for _, n := range []int{1, 16, 43, 100} {
		got, err := randomAlphanumeric(n)
		if err != nil {
			t.Fatalf("randomAlphanumeric(%d) error = %v", n, err)
		}
		if len(got) != n {
			t.Fatalf("randomAlphanumeric(%d) len = %d", n, len(got))
		}
		for _, c := range got {
			if !((c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9')) {
				t.Fatalf("non-alphanumeric %q in %q", c, got)
			}
		}
	}
	// Two consecutive draws must differ (would fail ~1 in 62^43).
	a, _ := randomAlphanumeric(43)
	b, _ := randomAlphanumeric(43)
	if a == b {
		t.Fatal("two draws produced the same value")
	}
}

func TestLeaf(t *testing.T) {
	t.Parallel()
	if got := Leaf("/mksrv/prod/identity/kc_db_password"); got != "kc_db_password" {
		t.Fatalf("Leaf() = %q", got)
	}
}
