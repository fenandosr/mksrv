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

func TestLeaf(t *testing.T) {
	t.Parallel()
	if got := Leaf("/mksrv/prod/identity/kc_db_password"); got != "kc_db_password" {
		t.Fatalf("Leaf() = %q", got)
	}
}
