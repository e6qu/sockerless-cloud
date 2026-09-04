package aws_sdk_test

import (
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatch"
	cwtypes "github.com/aws/aws-sdk-go-v2/service/cloudwatch/types"
	"github.com/aws/aws-sdk-go-v2/service/iam"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestCloudWatch_PutMetricDataIsScopedByItsNamespaceCondition covers the way
// AWS scopes an action whose resource the request never names.
//
// cloudwatch:PutMetricData declares no resource a caller can name — its only
// resource type is "dataset", which no request identifies — so a policy scopes
// it with the cloudwatch:namespace condition key instead, which is what AWS's
// own service reference lists against the action. The simulator populated only
// the global aws: keys, so that condition was evaluated against a context that
// did not contain the key it tests: the allow never matched and the policy
// denied the very writes it was written to permit.
func TestCloudWatch_PutMetricDataIsScopedByItsNamespaceCondition(t *testing.T) {
	iamc := iamClient()
	user := "cw-namespace-writer"

	_, err := iamc.CreateUser(ctx, &iam.CreateUserInput{UserName: aws.String(user)})
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = iamc.DeleteUser(ctx, &iam.DeleteUserInput{UserName: aws.String(user)}) })

	// The policy AWS documents for this: any namespace is refused but one.
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

	// The namespace the condition names is allowed.
	_, err = restricted.PutMetricData(ctx, &cloudwatch.PutMetricDataInput{
		Namespace: aws.String("Permitted"), MetricData: datum})
	assert.NoError(t, err,
		"the policy allows this namespace, so the condition key must be in the context for it to match")

	// Any other namespace is not.
	_, err = restricted.PutMetricData(ctx, &cloudwatch.PutMetricDataInput{
		Namespace: aws.String("Other"), MetricData: datum})
	require.Error(t, err, "a namespace the condition does not name is refused")
	assert.Contains(t, err.Error(), "not authorized")
}
