package aws_sdk_test

import (
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/ecr"
	ecrtypes "github.com/aws/aws-sdk-go-v2/service/ecr/types"
	elbv2 "github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2"
	elbv2types "github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2/types"
	"github.com/aws/aws-sdk-go-v2/service/lambda"
	"github.com/aws/aws-sdk-go-v2/service/route53"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/smithy-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func errCode(t *testing.T, err error) string {
	t.Helper()
	require.Error(t, err)
	var ae smithy.APIError
	require.True(t, errors.As(err, &ae), "expected smithy.APIError, got %v", err)
	return ae.ErrorCode()
}

// TestELBv2_DuplicateNames — CreateLoadBalancer / CreateTargetGroup with a name
// that already exists must be rejected (real ELBv2 behavior), not silently
// create a second resource.
func TestELBv2_DuplicateNames(t *testing.T) {
	c := elbv2Client()
	_, err := c.CreateLoadBalancer(ctx, &elbv2.CreateLoadBalancerInput{
		Name:    aws.String("dup-lb"),
		Subnets: []string{"subnet-aaaa1111", "subnet-bbbb2222"},
	})
	require.NoError(t, err)
	_, err = c.CreateLoadBalancer(ctx, &elbv2.CreateLoadBalancerInput{
		Name:    aws.String("dup-lb"),
		Subnets: []string{"subnet-aaaa1111", "subnet-bbbb2222"},
	})
	assert.Equal(t, "DuplicateLoadBalancerName", errCode(t, err))

	_, err = c.CreateTargetGroup(ctx, &elbv2.CreateTargetGroupInput{
		Name:     aws.String("dup-tg"),
		Protocol: elbv2types.ProtocolEnumHttp,
		Port:     aws.Int32(80),
		VpcId:    aws.String("vpc-12345678"),
	})
	require.NoError(t, err)
	_, err = c.CreateTargetGroup(ctx, &elbv2.CreateTargetGroupInput{
		Name:     aws.String("dup-tg"),
		Protocol: elbv2types.ProtocolEnumHttp,
		Port:     aws.Int32(80),
		VpcId:    aws.String("vpc-12345678"),
	})
	assert.Equal(t, "DuplicateTargetGroupName", errCode(t, err))
}

// TestRoute53_CallerReferenceIdempotency — a reused CallerReference returns
// HostedZoneAlreadyExists, the idempotency key Terraform relies on for retries.
func TestRoute53_CallerReferenceIdempotency(t *testing.T) {
	c := r53Client()
	_, err := c.CreateHostedZone(ctx, &route53.CreateHostedZoneInput{
		Name:            aws.String("audit-example.com"),
		CallerReference: aws.String("audit-ref-1"),
	})
	require.NoError(t, err)
	_, err = c.CreateHostedZone(ctx, &route53.CreateHostedZoneInput{
		Name:            aws.String("audit-example.com"),
		CallerReference: aws.String("audit-ref-1"),
	})
	assert.Equal(t, "HostedZoneAlreadyExists", errCode(t, err))
}

// TestEC2_DeleteSecurityGroupNotFound — DeleteSecurityGroup is not idempotent;
// a missing group returns InvalidGroup.NotFound, not <return>true</return>.
func TestEC2_DeleteSecurityGroupNotFound(t *testing.T) {
	c := ec2Client()
	_, err := c.DeleteSecurityGroup(ctx, &ec2.DeleteSecurityGroupInput{
		GroupId: aws.String("sg-doesnotexist0"),
	})
	assert.Equal(t, "InvalidGroup.NotFound", errCode(t, err))
}

// TestECR_DescribeRepositoriesNotFound — naming a missing repo errors instead of
// silently returning only the found ones.
func TestECR_DescribeRepositoriesNotFound(t *testing.T) {
	c := ecrClient()
	_, err := c.DescribeRepositories(ctx, &ecr.DescribeRepositoriesInput{
		RepositoryNames: []string{"no-such-repo-xyz"},
	})
	assert.Equal(t, "RepositoryNotFoundException", errCode(t, err))
}

// TestECR_UntagResource — tag removal round-trips (the op was unimplemented, so
// terraform tag removal 400'd).
func TestECR_UntagResource(t *testing.T) {
	c := ecrClient()
	repo := "untag-probe-repo"
	out, err := c.CreateRepository(ctx, &ecr.CreateRepositoryInput{
		RepositoryName: aws.String(repo),
		Tags: []ecrtypes.Tag{
			{Key: aws.String("keep"), Value: aws.String("yes")},
			{Key: aws.String("drop"), Value: aws.String("soon")},
		},
	})
	require.NoError(t, err)
	defer c.DeleteRepository(ctx, &ecr.DeleteRepositoryInput{RepositoryName: aws.String(repo), Force: true})
	arn := aws.ToString(out.Repository.RepositoryArn)

	_, err = c.UntagResource(ctx, &ecr.UntagResourceInput{ResourceArn: aws.String(arn), TagKeys: []string{"drop"}})
	require.NoError(t, err)

	tags, err := c.ListTagsForResource(ctx, &ecr.ListTagsForResourceInput{ResourceArn: aws.String(arn)})
	require.NoError(t, err)
	keys := map[string]bool{}
	for _, tg := range tags.Tags {
		keys[aws.ToString(tg.Key)] = true
	}
	assert.True(t, keys["keep"], "kept tag must remain")
	assert.False(t, keys["drop"], "untagged key must be gone")
}

// TestS3_ConditionalPut — If-None-Match:* fails when the object exists;
// If-Match:<etag> fails on a stale etag (optimistic concurrency).
func TestS3_ConditionalPut(t *testing.T) {
	c := s3Client()
	bucket := "cond-put-bucket"
	_, err := c.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: aws.String(bucket)})
	require.NoError(t, err)

	put, err := c.PutObject(ctx, &s3.PutObjectInput{
		Bucket: aws.String(bucket), Key: aws.String("k"), Body: strings.NewReader("v1"),
	})
	require.NoError(t, err)

	// If-None-Match: * must fail now that the object exists.
	_, err = c.PutObject(ctx, &s3.PutObjectInput{
		Bucket: aws.String(bucket), Key: aws.String("k"), Body: strings.NewReader("v2"),
		IfNoneMatch: aws.String("*"),
	})
	assert.Equal(t, "PreconditionFailed", errCode(t, err))

	// If-Match with a stale etag must fail.
	_, err = c.PutObject(ctx, &s3.PutObjectInput{
		Bucket: aws.String(bucket), Key: aws.String("k"), Body: strings.NewReader("v3"),
		IfMatch: aws.String(`"deadbeefdeadbeefdeadbeefdeadbeef"`),
	})
	assert.Equal(t, "PreconditionFailed", errCode(t, err))

	// If-Match with the current etag succeeds.
	_, err = c.PutObject(ctx, &s3.PutObjectInput{
		Bucket: aws.String(bucket), Key: aws.String("k"), Body: strings.NewReader("v4"),
		IfMatch: put.ETag,
	})
	require.NoError(t, err)
}

// TestS3_ConcurrentConditionalPutsHaveOneWinner — S3 evaluates a conditional
// write and applies it as one step, so of many writers that all require the
// key to be absent, exactly one creates it, and the stored object is the one
// that writer sent.
func TestS3_ConcurrentConditionalPutsHaveOneWinner(t *testing.T) {
	c := s3Client()
	bucket := fmt.Sprintf("cond-put-race-%d", time.Now().UnixNano())
	_, err := c.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: aws.String(bucket)})
	require.NoError(t, err)

	// Bodies large enough that a writer spends real time between checking the
	// condition and storing, which is where an unserialized write loses.
	const writers = 32
	var wg sync.WaitGroup
	var mu sync.Mutex
	var winners, refused []string
	for i := range writers {
		wg.Add(1)
		go func(body string) {
			defer wg.Done()
			_, err := c.PutObject(ctx, &s3.PutObjectInput{
				Bucket: aws.String(bucket), Key: aws.String("lock"),
				Body:        strings.NewReader(body + strings.Repeat(".", 256<<10)),
				IfNoneMatch: aws.String("*"),
			})
			mu.Lock()
			defer mu.Unlock()
			if err == nil {
				winners = append(winners, body)
				return
			}
			refused = append(refused, errCode(t, err))
		}(fmt.Sprintf("writer-%02d", i))
	}
	wg.Wait()
	require.Len(t, winners, 1, "exactly one conditional create may succeed")
	for _, code := range refused {
		assert.Equal(t, "PreconditionFailed", code)
	}
	got, err := c.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(bucket), Key: aws.String("lock")})
	require.NoError(t, err)
	defer got.Body.Close()
	body, err := io.ReadAll(got.Body)
	require.NoError(t, err)
	assert.Equal(t, winners[0], strings.TrimRight(string(body), "."), "the stored object is the winner's")
}

// TestLambda_DeleteAliasNotFound — deleting a missing alias errors with
// ResourceNotFoundException instead of a silent 204.
func TestLambda_DeleteAliasNotFound(t *testing.T) {
	c := lambdaClient()
	_, err := c.DeleteAlias(ctx, &lambda.DeleteAliasInput{
		FunctionName: aws.String("no-such-fn-for-alias"),
		Name:         aws.String("prod"),
	})
	assert.Equal(t, "ResourceNotFoundException", errCode(t, err))
}
