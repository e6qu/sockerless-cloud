package aws_sdk_test

import (
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/ecs"
	ecstypes "github.com/aws/aws-sdk-go-v2/service/ecs/types"
	"github.com/aws/aws-sdk-go-v2/service/iam"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Each test writes a policy whose condition tests one key the gate builds for
// an action, and drives a request the condition allows and one it refuses
// through the SDK. A key missing from the gate's context would refuse both.

func keyConfig(akid, secret string) aws.Config {
	return aws.Config{Region: "us-east-1", Credentials: credentials.NewStaticCredentialsProvider(akid, secret, "")}
}

// TestSQS_UntagQueueTagKeysScopeTheGrant covers aws:TagKeys on an untagging
// request, which names the keys it removes: a policy lets a caller remove the
// team tag and no other.
func TestSQS_UntagQueueTagKeysScopeTheGrant(t *testing.T) {
	admin := sqsClient()
	created, err := admin.CreateQueue(ctx, &sqs.CreateQueueInput{
		QueueName: aws.String(uniqueName("untag-keys")),
		Tags:      map[string]string{"team": "platform", "owner": "ops"},
	})
	require.NoError(t, err)
	queue := created.QueueUrl

	akid, secret := restrictedCredential(t, "sqs-untag-team",
		`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"sqs:UntagQueue","Resource":"*",
		  "Condition":{"ForAllValues:StringEquals":{"aws:TagKeys":["team"]},"Null":{"aws:TagKeys":"false"}}}]}`)
	restricted := sqs.NewFromConfig(keyConfig(akid, secret),
		func(o *sqs.Options) { o.BaseEndpoint = aws.String(baseURL) })

	_, err = restricted.UntagQueue(ctx, &sqs.UntagQueueInput{QueueUrl: queue, TagKeys: []string{"owner"}})
	require.Error(t, err, "removing a tag the grant does not name is refused")
	assert.Contains(t, err.Error(), "AccessDenied")

	_, err = restricted.UntagQueue(ctx, &sqs.UntagQueueInput{QueueUrl: queue, TagKeys: []string{"team"}})
	require.NoError(t, err, "removing the team tag is what the grant allows")

	tags, err := admin.ListQueueTags(ctx, &sqs.ListQueueTagsInput{QueueUrl: queue})
	require.NoError(t, err)
	assert.Equal(t, map[string]string{"owner": "ops"}, tags.Tags)
}

// TestECS_RegisterTaskDefinitionSizeScopesTheGrant covers ecs:task-cpu and
// ecs:task-memory on RegisterTaskDefinition, which states the size outright: a
// policy lets a caller register small task definitions only.
func TestECS_RegisterTaskDefinitionSizeScopesTheGrant(t *testing.T) {
	akid, secret := restrictedCredential(t, "ecs-small-definitions",
		`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"ecs:RegisterTaskDefinition","Resource":"*",
		  "Condition":{"StringEquals":{"ecs:task-cpu":"256","ecs:task-memory":"512"}}}]}`)
	restricted := ecs.NewFromConfig(keyConfig(akid, secret),
		func(o *ecs.Options) { o.BaseEndpoint = aws.String(baseURL) })

	register := func(cpu, memory string) error {
		_, err := restricted.RegisterTaskDefinition(ctx, &ecs.RegisterTaskDefinitionInput{
			Family: aws.String(uniqueName("sized")),
			Cpu:    aws.String(cpu), Memory: aws.String(memory),
			RequiresCompatibilities: []ecstypes.Compatibility{ecstypes.CompatibilityFargate},
			NetworkMode:             ecstypes.NetworkModeAwsvpc,
			ContainerDefinitions: []ecstypes.ContainerDefinition{{
				Name:  aws.String("app"),
				Image: aws.String("public.ecr.aws/docker/library/busybox:latest"),
			}},
		})
		return err
	}

	err := register("1024", "2048")
	require.Error(t, err, "a definition larger than the grant allows is refused")
	assert.Contains(t, err.Error(), "not authorized")
	require.NoError(t, register("256", "512"), "the size the grant names is allowed")
}

// TestIAM_PermissionsBoundaryOfTheNamedRoleScopesTheGrant covers
// iam:PermissionsBoundary on a request that attaches no boundary: it is the
// boundary the named role already carries, which is how an administrator lets
// a delegate edit only the roles that stay inside the boundary.
func TestIAM_PermissionsBoundaryOfTheNamedRoleScopesTheGrant(t *testing.T) {
	admin := iamClient()
	trust := `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"Service":"lambda.amazonaws.com"},"Action":"sts:AssumeRole"}]}`
	boundary, err := admin.CreatePolicy(ctx, &iam.CreatePolicyInput{
		PolicyName:     aws.String(uniqueName("boundary")),
		PolicyDocument: aws.String(`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"s3:GetObject","Resource":"*"}]}`),
	})
	require.NoError(t, err)
	boundaryArn := aws.ToString(boundary.Policy.Arn)

	bounded, unbounded := uniqueName("bounded"), uniqueName("unbounded")
	_, err = admin.CreateRole(ctx, &iam.CreateRoleInput{RoleName: aws.String(bounded),
		AssumeRolePolicyDocument: aws.String(trust), PermissionsBoundary: aws.String(boundaryArn)})
	require.NoError(t, err)
	_, err = admin.CreateRole(ctx, &iam.CreateRoleInput{RoleName: aws.String(unbounded),
		AssumeRolePolicyDocument: aws.String(trust)})
	require.NoError(t, err)

	akid, secret := restrictedCredential(t, "iam-bounded-editor",
		`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"iam:PutRolePolicy","Resource":"*",
		  "Condition":{"StringEquals":{"iam:PermissionsBoundary":"`+boundaryArn+`"}}}]}`)
	restricted := iamClientWithCreds(akid, secret)

	put := func(role string) error {
		_, err := restricted.PutRolePolicy(ctx, &iam.PutRolePolicyInput{
			RoleName: aws.String(role), PolicyName: aws.String("inline"),
			PolicyDocument: aws.String(`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"s3:GetObject","Resource":"*"}]}`),
		})
		return err
	}
	err = put(unbounded)
	require.Error(t, err, "a role without the boundary is outside the grant")
	assert.Contains(t, err.Error(), "AccessDenied")
	require.NoError(t, put(bounded), "a role carrying the boundary is what the grant allows")
}

// TestS3_ObjectTaggedOnPutScopesReads covers s3:ExistingObjectTag/<key> on an
// object whose tags arrived with the PutObject that wrote it, in the
// x-amz-tagging header: a policy lets a caller read objects tagged public only.
func TestS3_ObjectTaggedOnPutScopesReads(t *testing.T) {
	admin := s3Client()
	bucket := strings.ToLower(uniqueName("tagged-on-put"))
	_, err := admin.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: aws.String(bucket)})
	require.NoError(t, err)
	for key, tagging := range map[string]string{"open": "visibility=public", "closed": "visibility=private"} {
		_, err = admin.PutObject(ctx, &s3.PutObjectInput{Bucket: aws.String(bucket), Key: aws.String(key),
			Body: strings.NewReader(key), Tagging: aws.String(tagging)})
		require.NoError(t, err)
	}
	tags, err := admin.GetObjectTagging(ctx, &s3.GetObjectTaggingInput{Bucket: aws.String(bucket), Key: aws.String("open")})
	require.NoError(t, err)
	require.Len(t, tags.TagSet, 1)
	assert.Equal(t, "visibility", aws.ToString(tags.TagSet[0].Key))

	akid, secret := restrictedCredential(t, "s3-public-reader",
		`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"s3:GetObject","Resource":"*",
		  "Condition":{"StringEquals":{"s3:ExistingObjectTag/visibility":"public"}}}]}`)
	restricted := s3.NewFromConfig(keyConfig(akid, secret), func(o *s3.Options) {
		o.BaseEndpoint = aws.String(baseURL)
		o.UsePathStyle = true
	})

	_, err = restricted.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(bucket), Key: aws.String("closed")})
	require.Error(t, err, "an object tagged private is outside the grant")
	assert.Contains(t, err.Error(), "AccessDenied")
	out, err := restricted.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(bucket), Key: aws.String("open")})
	require.NoError(t, err, "an object tagged public is what the grant allows")
	_ = out.Body.Close()
}
