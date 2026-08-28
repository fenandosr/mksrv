// SPDX-License-Identifier: Apache-2.0

// Package aws holds the AWS SDK operations mksrv performs directly, outside
// Terraform: caller-identity diagnostics and Terraform state-backend bootstrap
// (the S3 bucket and DynamoDB lock table that hold Terraform state itself, and
// therefore cannot be managed by that same Terraform).
package aws

import (
	"context"
	"errors"
	"fmt"
	"time"

	awssdk "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	ddbtypes "github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/aws-sdk-go-v2/service/ssm"
	"github.com/aws/aws-sdk-go-v2/service/sts"
	"github.com/aws/smithy-go"
)

// Options selects the shared-config profile and region for a client set.
type Options struct {
	Region  string
	Profile string
}

// Identity is the caller identity reported by STS GetCallerIdentity.
type Identity struct {
	Account string `json:"account"`
	ARN     string `json:"arn"`
	UserID  string `json:"user_id"`
}

// Clients bundles the AWS service clients mksrv uses directly.
type Clients struct {
	region   string
	cfg      awssdk.Config
	sts      *sts.Client
	s3       *s3.Client
	dynamodb *dynamodb.Client
	ssm      *ssm.Client
}

// SSM returns the Systems Manager client.
func (c *Clients) SSM() *ssm.Client { return c.ssm }

// Load resolves AWS configuration from the environment and shared files and
// constructs the client set. It performs no network calls.
func Load(ctx context.Context, opts Options) (*Clients, error) {
	loaders := make([]func(*config.LoadOptions) error, 0, 2)
	if opts.Region != "" {
		loaders = append(loaders, config.WithRegion(opts.Region))
	}
	if opts.Profile != "" {
		loaders = append(loaders, config.WithSharedConfigProfile(opts.Profile))
	}
	cfg, err := config.LoadDefaultConfig(ctx, loaders...)
	if err != nil {
		return nil, fmt.Errorf("load AWS configuration: %w", err)
	}
	if cfg.Region == "" {
		return nil, errors.New("no AWS region configured; set aws.region in deployment.yaml or AWS_REGION")
	}
	return &Clients{
		region:   cfg.Region,
		cfg:      cfg,
		sts:      sts.NewFromConfig(cfg),
		s3:       s3.NewFromConfig(cfg),
		dynamodb: dynamodb.NewFromConfig(cfg),
		ssm:      ssm.NewFromConfig(cfg),
	}, nil
}

// Region reports the resolved region.
func (c *Clients) Region() string { return c.region }

// ExportEnv resolves the current credentials and returns them as the standard
// AWS_* environment variables, so a child process (Terraform) inherits exactly
// the identity mksrv resolved — including credential sources that older
// embedded SDKs, such as Terraform's S3 backend, do not understand on their
// own (e.g. the `login_session` profile written by `aws login`).
func (c *Clients) ExportEnv(ctx context.Context) (map[string]string, error) {
	creds, err := c.cfg.Credentials.Retrieve(ctx)
	if err != nil {
		return nil, fmt.Errorf("retrieve AWS credentials: %w", err)
	}
	env := map[string]string{
		"AWS_ACCESS_KEY_ID":     creds.AccessKeyID,
		"AWS_SECRET_ACCESS_KEY": creds.SecretAccessKey,
		"AWS_REGION":            c.region,
		"AWS_DEFAULT_REGION":    c.region,
	}
	if creds.SessionToken != "" {
		env["AWS_SESSION_TOKEN"] = creds.SessionToken
	}
	return env, nil
}

// WhoAmI calls STS GetCallerIdentity.
func (c *Clients) WhoAmI(ctx context.Context) (Identity, error) {
	out, err := c.sts.GetCallerIdentity(ctx, &sts.GetCallerIdentityInput{})
	if err != nil {
		return Identity{}, fmt.Errorf("get caller identity: %w", err)
	}
	return Identity{
		Account: awssdk.ToString(out.Account),
		ARN:     awssdk.ToString(out.Arn),
		UserID:  awssdk.ToString(out.UserId),
	}, nil
}

// BackendSpec describes the Terraform state backend to ensure.
type BackendSpec struct {
	Bucket        string
	DynamoDBTable string
	Region        string
}

// BackendStatus reports what EnsureBackend found or created.
type BackendStatus struct {
	Bucket        string `json:"bucket"`
	Table         string `json:"table"`
	BucketCreated bool   `json:"bucket_created"`
	TableCreated  bool   `json:"table_created"`
}

// EnsureBackend makes the state bucket and lock table exist, creating each only
// when absent. A created bucket gets versioning, SSE-S3 encryption, and a full
// public-access block. It is safe to call repeatedly.
func (c *Clients) EnsureBackend(ctx context.Context, spec BackendSpec) (BackendStatus, error) {
	status := BackendStatus{Bucket: spec.Bucket, Table: spec.DynamoDBTable}
	if spec.Bucket == "" || spec.DynamoDBTable == "" {
		return status, errors.New("backend spec requires a bucket and a dynamodb table")
	}
	region := spec.Region
	if region == "" {
		region = c.region
	}

	created, err := c.ensureBucket(ctx, spec.Bucket, region)
	if err != nil {
		return status, err
	}
	status.BucketCreated = created

	created, err = c.ensureTable(ctx, spec.DynamoDBTable)
	if err != nil {
		return status, err
	}
	status.TableCreated = created
	return status, nil
}

func (c *Clients) ensureBucket(ctx context.Context, bucket, region string) (bool, error) {
	_, err := c.s3.HeadBucket(ctx, &s3.HeadBucketInput{Bucket: &bucket})
	if err == nil {
		return false, nil
	}
	var notFound *s3types.NotFound
	var noSuchBucket *s3types.NoSuchBucket
	if !errors.As(err, &notFound) && !errors.As(err, &noSuchBucket) && !isNotFoundCode(err) {
		return false, fmt.Errorf("head state bucket %q: %w", bucket, err)
	}

	input := &s3.CreateBucketInput{Bucket: &bucket}
	if constraint := locationConstraint(region); constraint != "" {
		input.CreateBucketConfiguration = &s3types.CreateBucketConfiguration{
			LocationConstraint: s3types.BucketLocationConstraint(constraint),
		}
	}
	if _, err := c.s3.CreateBucket(ctx, input); err != nil {
		return false, fmt.Errorf("create state bucket %q: %w", bucket, err)
	}

	if _, err := c.s3.PutBucketVersioning(ctx, &s3.PutBucketVersioningInput{
		Bucket:                  &bucket,
		VersioningConfiguration: &s3types.VersioningConfiguration{Status: s3types.BucketVersioningStatusEnabled},
	}); err != nil {
		return true, fmt.Errorf("enable versioning on %q: %w", bucket, err)
	}
	if _, err := c.s3.PutBucketEncryption(ctx, &s3.PutBucketEncryptionInput{
		Bucket: &bucket,
		ServerSideEncryptionConfiguration: &s3types.ServerSideEncryptionConfiguration{
			Rules: []s3types.ServerSideEncryptionRule{{
				ApplyServerSideEncryptionByDefault: &s3types.ServerSideEncryptionByDefault{
					SSEAlgorithm: s3types.ServerSideEncryptionAes256,
				},
				BucketKeyEnabled: awssdk.Bool(true),
			}},
		},
	}); err != nil {
		return true, fmt.Errorf("enable encryption on %q: %w", bucket, err)
	}
	if _, err := c.s3.PutPublicAccessBlock(ctx, &s3.PutPublicAccessBlockInput{
		Bucket: &bucket,
		PublicAccessBlockConfiguration: &s3types.PublicAccessBlockConfiguration{
			BlockPublicAcls:       awssdk.Bool(true),
			BlockPublicPolicy:     awssdk.Bool(true),
			IgnorePublicAcls:      awssdk.Bool(true),
			RestrictPublicBuckets: awssdk.Bool(true),
		},
	}); err != nil {
		return true, fmt.Errorf("block public access on %q: %w", bucket, err)
	}
	return true, nil
}

func (c *Clients) ensureTable(ctx context.Context, table string) (bool, error) {
	_, err := c.dynamodb.DescribeTable(ctx, &dynamodb.DescribeTableInput{TableName: &table})
	if err == nil {
		return false, nil
	}
	var notFound *ddbtypes.ResourceNotFoundException
	if !errors.As(err, &notFound) {
		return false, fmt.Errorf("describe lock table %q: %w", table, err)
	}

	if _, err := c.dynamodb.CreateTable(ctx, &dynamodb.CreateTableInput{
		TableName:   &table,
		BillingMode: ddbtypes.BillingModePayPerRequest,
		AttributeDefinitions: []ddbtypes.AttributeDefinition{{
			AttributeName: awssdk.String("LockID"),
			AttributeType: ddbtypes.ScalarAttributeTypeS,
		}},
		KeySchema: []ddbtypes.KeySchemaElement{{
			AttributeName: awssdk.String("LockID"),
			KeyType:       ddbtypes.KeyTypeHash,
		}},
	}); err != nil {
		return false, fmt.Errorf("create lock table %q: %w", table, err)
	}

	waiter := dynamodb.NewTableExistsWaiter(c.dynamodb)
	if err := waiter.Wait(ctx, &dynamodb.DescribeTableInput{TableName: &table}, 2*time.Minute); err != nil {
		return true, fmt.Errorf("wait for lock table %q to become active: %w", table, err)
	}
	return true, nil
}

// locationConstraint returns the S3 CreateBucket location constraint for a
// region, or "" for us-east-1 which must not send one.
func locationConstraint(region string) string {
	if region == "" || region == "us-east-1" {
		return ""
	}
	return region
}

func isNotFoundCode(err error) bool {
	var apiErr smithy.APIError
	if errors.As(err, &apiErr) {
		switch apiErr.ErrorCode() {
		case "NotFound", "NoSuchBucket", "404":
			return true
		}
	}
	return false
}
