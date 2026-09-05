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

// Put writes value to ref as a SecureString, overwriting any existing value.
// Use it for computed configuration (not random secrets).
func (r *Resolver) Put(ctx context.Context, ref, value string) error {
	name := r.Expand(ref)
	_, err := r.api.PutParameter(ctx, &ssm.PutParameterInput{
		Name:      awssdk.String(name),
		Value:     awssdk.String(value),
		Type:      ssmtypes.ParameterTypeSecureString,
		Overwrite: awssdk.Bool(true),
		Tier:      ssmtypes.ParameterTierStandard,
	})
	if err != nil {
		return fmt.Errorf("put parameter %s: %w", name, err)
	}
	return nil
}

// EnsureString returns ref if it exists, otherwise stores value and returns it.
func (r *Resolver) EnsureString(ctx context.Context, ref, value string) (string, error) {
	if existing, err := r.Get(ctx, ref); err == nil {
		return existing, nil
	}
	if err := r.Put(ctx, ref, value); err != nil {
		return "", err
	}
	return value, nil
}

// EnsureRandom returns the value of ref, generating and storing a random
// alphanumeric string of at least nbytes of entropy when the parameter is
// absent.
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
	generated, err := randomAlphanumeric(base64.RawURLEncoding.EncodedLen(nbytes))
	if err != nil {
		return "", fmt.Errorf("generate secret: %w", err)
	}

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

// randomAlphanumeric returns an n-character string over [A-Za-z0-9]. That set
// is safe unquoted in shell args (`redis-cli -a`, `PGPASSWORD=`), connection
// URLs (`scheme://user:pw@host`), Redis aclfile `>pw` directives, and
// Keycloak's SMTP config alike — base64url (the previous encoding) could emit
// a leading `-`, which getopt-based tools parse as a flag.
func randomAlphanumeric(n int) (string, error) {
	const alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789"
	out := make([]byte, 0, n)
	buf := make([]byte, n)
	for len(out) < n {
		if _, err := rand.Read(buf); err != nil {
			return "", err
		}
		for _, b := range buf {
			if b >= 248 { // reject the top 8 of 256 so 62 divides evenly (no modulo bias)
				continue
			}
			out = append(out, alphabet[b%62])
			if len(out) == n {
				break
			}
		}
	}
	return string(out), nil
}
