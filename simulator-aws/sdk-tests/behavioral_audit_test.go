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
	"github.com/e6qu/sockerless-cloud/testutil/uniquename"
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

// TestELBv2_DuplicateNames expects CreateLoadBalancer and CreateTargetGroup to
// reject an existing name rather than create a second resource.
func TestELBv2_DuplicateNames(t *testing.T) {
	t.Parallel()
	tgName := uniquename.New("dup-tg")
	lbName := uniquename.New("dup-lb")
	c := elbv2Client()
	_, subnets := elbv2FidVPCSubnets(t, "10.166.0.0/16", "10.166.1.0/24", "10.166.2.0/24")
	created, err := c.CreateLoadBalancer(ctx, &elbv2.CreateLoadBalancerInput{
		Name:    aws.String(lbName),
		Subnets: subnets,
	})
	require.NoError(t, err)
	// "If you attempt to create multiple load balancers with the same settings,
	// each call succeeds"; differing settings under the name are a duplicate.
	again, err := c.CreateLoadBalancer(ctx, &elbv2.CreateLoadBalancerInput{
		Name:    aws.String(lbName),
		Subnets: subnets,
	})
	require.NoError(t, err)
	assert.Equal(t, aws.ToString(created.LoadBalancers[0].LoadBalancerArn), aws.ToString(again.LoadBalancers[0].LoadBalancerArn))
	_, err = c.CreateLoadBalancer(ctx, &elbv2.CreateLoadBalancerInput{
		Name:    aws.String(lbName),
		Subnets: subnets,
		Tags:    []elbv2types.Tag{{Key: aws.String("env"), Value: aws.String("other")}},
	})
	assert.Equal(t, "DuplicateLoadBalancerName", errCode(t, err))

	_, err = c.CreateTargetGroup(ctx, &elbv2.CreateTargetGroupInput{
		Name:     aws.String(tgName),
		Protocol: elbv2types.ProtocolEnumHttp,
		Port:     aws.Int32(80),
		VpcId:    aws.String("vpc-12345678"),
	})
	require.NoError(t, err)
	_, err = c.CreateTargetGroup(ctx, &elbv2.CreateTargetGroupInput{
		Name:     aws.String(tgName),
		Protocol: elbv2types.ProtocolEnumHttp,
		Port:     aws.Int32(80),
		VpcId:    aws.String("vpc-12345678"),
	})
	assert.Equal(t, "DuplicateTargetGroupName", errCode(t, err))
}

// TestRoute53_CallerReferenceIdempotency expects a reused CallerReference to
// return HostedZoneAlreadyExists, the idempotency key Terraform retries on.
func TestRoute53_CallerReferenceIdempotency(t *testing.T) {
	t.Parallel()
	callerRef := uniquename.New("audit-ref")
	c := r53Client()
	_, err := c.CreateHostedZone(ctx, &route53.CreateHostedZoneInput{
		Name:            aws.String("audit-example.com"),
		CallerReference: aws.String(callerRef),
	})
	require.NoError(t, err)
	_, err = c.CreateHostedZone(ctx, &route53.CreateHostedZoneInput{
		Name:            aws.String("audit-example.com"),
		CallerReference: aws.String(callerRef),
	})
	assert.Equal(t, "HostedZoneAlreadyExists", errCode(t, err))
}

// TestEC2_DeleteSecurityGroupNotFound expects InvalidGroup.NotFound for a
// missing group: DeleteSecurityGroup is not idempotent.
func TestEC2_DeleteSecurityGroupNotFound(t *testing.T) {
	t.Parallel()
	c := ec2Client()
	_, err := c.DeleteSecurityGroup(ctx, &ec2.DeleteSecurityGroupInput{
		GroupId: aws.String("sg-doesnotexist0"),
	})
	assert.Equal(t, "InvalidGroup.NotFound", errCode(t, err))
}

// TestECR_DescribeRepositoriesNotFound expects naming a missing repository to
// fail rather than return only the found ones.
func TestECR_DescribeRepositoriesNotFound(t *testing.T) {
	t.Parallel()
	c := ecrClient()
	_, err := c.DescribeRepositories(ctx, &ecr.DescribeRepositoriesInput{
		RepositoryNames: []string{"no-such-repo-xyz"},
	})
	assert.Equal(t, "RepositoryNotFoundException", errCode(t, err))
}

func TestECR_UntagResource(t *testing.T) {
	t.Parallel()
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

// TestS3_ConditionalPut expects If-None-Match:* to fail when the object exists
// and If-Match:<etag> to fail on a stale etag.
func TestS3_ConditionalPut(t *testing.T) {
	t.Parallel()
	c := s3Client()
	bucket := "cond-put-bucket"
	_, err := c.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: aws.String(bucket)})
	require.NoError(t, err)

	put, err := c.PutObject(ctx, &s3.PutObjectInput{
		Bucket: aws.String(bucket), Key: aws.String("k"), Body: strings.NewReader("v1"),
	})
	require.NoError(t, err)

	_, err = c.PutObject(ctx, &s3.PutObjectInput{
		Bucket: aws.String(bucket), Key: aws.String("k"), Body: strings.NewReader("v2"),
		IfNoneMatch: aws.String("*"),
	})
	assert.Equal(t, "PreconditionFailed", errCode(t, err))

	_, err = c.PutObject(ctx, &s3.PutObjectInput{
		Bucket: aws.String(bucket), Key: aws.String("k"), Body: strings.NewReader("v3"),
		IfMatch: aws.String(`"deadbeefdeadbeefdeadbeefdeadbeef"`),
	})
	assert.Equal(t, "PreconditionFailed", errCode(t, err))

	_, err = c.PutObject(ctx, &s3.PutObjectInput{
		Bucket: aws.String(bucket), Key: aws.String("k"), Body: strings.NewReader("v4"),
		IfMatch: put.ETag,
	})
	require.NoError(t, err)
}

// TestS3_ConcurrentConditionalPutsHaveOneWinner: Amazon S3 evaluates and applies
// a conditional write as one step, so of many writers requiring the key to be
// absent exactly one creates it, and the stored object is that writer's.
func TestS3_ConcurrentConditionalPutsHaveOneWinner(t *testing.T) {
	t.Parallel()
	c := s3Client()
	bucket := fmt.Sprintf("cond-put-race-%d", time.Now().UnixNano())
	_, err := c.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: aws.String(bucket)})
	require.NoError(t, err)

	// Bodies large enough that a writer spends real time between checking the
	// condition and storing, where an unserialized write loses.
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

// TestLambda_DeleteAliasNotFound expects ResourceNotFoundException for a
// missing alias rather than a 204.
func TestLambda_DeleteAliasNotFound(t *testing.T) {
	t.Parallel()
	c := lambdaClient()
	_, err := c.DeleteAlias(ctx, &lambda.DeleteAliasInput{
		FunctionName: aws.String("no-such-fn-for-alias"),
		Name:         aws.String("prod"),
	})
	assert.Equal(t, "ResourceNotFoundException", errCode(t, err))
}
