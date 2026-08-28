// SPDX-License-Identifier: Apache-2.0

// Package secrets resolves the /mksrv/{env}/... references declared by stack
// descriptors. Runtime values live in AWS SSM Parameter Store as SecureString
// parameters; EnsureRandom generates one on first use.
package secrets

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"

	awssdk "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ssm"
	ssmtypes "github.com/aws/aws-sdk-go-v2/service/ssm/types"
)

// SSMAPI is the subset of the SSM client the resolver uses.
type SSMAPI interface {
	GetParameter(context.Context, *ssm.GetParameterInput, ...func(*ssm.Options)) (*ssm.GetParameterOutput, error)
	PutParameter(context.Context, *ssm.PutParameterInput, ...func(*ssm.Options)) (*ssm.PutParameterOutput, error)
}

// Resolver reads and creates SSM parameters for one environment.
type Resolver struct {
	api SSMAPI
	env string
}

// NewResolver binds a resolver to an SSM client and environment name.
func NewResolver(api SSMAPI, env string) *Resolver {
	return &Resolver{api: api, env: env}
}

// Expand replaces the {env} placeholder in a reference.
func (r *Resolver) Expand(ref string) string {
	return strings.ReplaceAll(ref, "{env}", r.env)
}

// Leaf is the last path segment of a reference, used to name derived artifacts
// such as podman secrets.
func Leaf(ref string) string {
	ref = strings.TrimRight(ref, "/")
	if i := strings.LastIndex(ref, "/"); i >= 0 {
		return ref[i+1:]
	}
	return ref
}

// Get returns the decrypted value of an existing parameter.
func (r *Resolver) Get(ctx context.Context, ref string) (string, error) {
	name := r.Expand(ref)
	out, err := r.api.GetParameter(ctx, &ssm.GetParameterInput{
		Name:           awssdk.String(name),
		WithDecryption: awssdk.Bool(true),
	})
	if err != nil {
		return "", fmt.Errorf("get parameter %s: %w", name, err)
	}
	if out.Parameter == nil || out.Parameter.Value == nil {
		return "", fmt.Errorf("parameter %s has no value", name)
	}
	return *out.Parameter.Value, nil
}

// EnsureRandom returns the value of ref, generating and storing a URL-safe
// random string of at least nbytes of entropy when the parameter is absent.
func (r *Resolver) EnsureRandom(ctx context.Context, ref string, nbytes int) (string, error) {
	value, err := r.Get(ctx, ref)
	if err == nil {
		return value, nil
	}
	var notFound *ssmtypes.ParameterNotFound
	if !errors.As(err, &notFound) {
		return "", err
	}
	if nbytes < 16 {
		nbytes = 16
	}
	raw := make([]byte, nbytes)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("generate secret: %w", err)
	}
	generated := base64.RawURLEncoding.EncodeToString(raw)

	name := r.Expand(ref)
	if _, err := r.api.PutParameter(ctx, &ssm.PutParameterInput{
		Name:      awssdk.String(name),
		Value:     awssdk.String(generated),
		Type:      ssmtypes.ParameterTypeSecureString,
		Overwrite: awssdk.Bool(false),
		Tier:      ssmtypes.ParameterTierStandard,
	}); err != nil {
		return "", fmt.Errorf("create parameter %s: %w", name, err)
	}
	return generated, nil
}
