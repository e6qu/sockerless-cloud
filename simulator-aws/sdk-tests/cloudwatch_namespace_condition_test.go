package aws_sdk_test

import (
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatch"
	cwtypes "github.com/aws/aws-sdk-go-v2/service/cloudwatch/types"
	"github.com/aws/aws-sdk-go-v2/service/iam"
	"github.com/e6qu/sockerless-cloud/testutil/uniquename"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestCloudWatch_PutMetricDataIsScopedByItsNamespaceCondition: PutMetricData's
// only resource type, "dataset", is one no request names, so a policy scopes
// the action with the cloudwatch:namespace condition key, as the AWS service
// reference lists for it. The request context must carry that key, or the
// allow never matches.
func TestCloudWatch_PutMetricDataIsScopedByItsNamespaceCondition(t *testing.T) {
	t.Parallel()
	iamc := iamClient()
	user := uniquename.New("cw-namespace-writer")

	_, err := iamc.CreateUser(ctx, &iam.CreateUserInput{UserName: aws.String(user)})
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = iamc.DeleteUser(ctx, &iam.DeleteUserInput{UserName: aws.String(user)}) })

	// The policy AWS documents: every namespace refused but one.
	_, err = iamc.PutUserPolicy(ctx, &iam.PutUserPolicyInput{
		UserName:   aws.String(user),
		PolicyName: aws.String("one-namespace"),
		PolicyDocument: aws.String(`{"Version":"2012-10-17","Statement":[{
			"Effect":"Allow","Action":"cloudwatch:PutMetricData","Resource":"*",
			"Condition":{"StringEquals":{"cloudwatch:namespace":"Permitted"}}}]}`),
	})
	require.NoError(t, err)
	key, err := iamc.CreateAccessKey(ctx, &iam.CreateAccessKeyInput{UserName: aws.String(user)})
	require.NoError(t, err)

	restricted := cloudwatch.NewFromConfig(aws.Config{Region: "us-east-1",
		Credentials: credentials.NewStaticCredentialsProvider(
			aws.ToString(key.AccessKey.AccessKeyId), aws.ToString(key.AccessKey.SecretAccessKey), "")},
		func(o *cloudwatch.Options) { o.BaseEndpoint = aws.String(baseURL) })

	datum := []cwtypes.MetricDatum{{
		MetricName: aws.String("Requests"),
		Timestamp:  aws.Time(time.Now()),
		Value:      aws.Float64(1),
	}}

	_, err = restricted.PutMetricData(ctx, &cloudwatch.PutMetricDataInput{
		Namespace: aws.String("Permitted"), MetricData: datum})
	assert.NoError(t, err,
		"the policy allows this namespace, so the condition key must be in the context for it to match")

	_, err = restricted.PutMetricData(ctx, &cloudwatch.PutMetricDataInput{
		Namespace: aws.String("Other"), MetricData: datum})
	require.Error(t, err, "a namespace the condition does not name is refused")
	assert.Contains(t, err.Error(), "not authorized")
}
