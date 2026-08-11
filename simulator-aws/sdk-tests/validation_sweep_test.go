package aws_sdk_test

import (
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/iam"
	"github.com/aws/aws-sdk-go-v2/service/lambda"
	lambdatypes "github.com/aws/aws-sdk-go-v2/service/lambda/types"
	"github.com/aws/aws-sdk-go-v2/service/sfn"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestValidationSweep covers the fidelity gaps surfaced by the sweep: the sim
// must reject inputs real AWS rejects.
func TestValidationSweep(t *testing.T) {
	t.Run("SQS_CreateQueue_idempotency", func(t *testing.T) {
		c := sqsClient()
		_, err := c.CreateQueue(ctx, &sqs.CreateQueueInput{
			QueueName: aws.String("vq-idem"), Attributes: map[string]string{"VisibilityTimeout": "30"},
		})
		require.NoError(t, err)
		// Same name, same attributes → idempotent OK.
		_, err = c.CreateQueue(ctx, &sqs.CreateQueueInput{
			QueueName: aws.String("vq-idem"), Attributes: map[string]string{"VisibilityTimeout": "30"},
		})
		require.NoError(t, err)
		// Same name, different attributes → QueueNameExists.
		_, err = c.CreateQueue(ctx, &sqs.CreateQueueInput{
			QueueName: aws.String("vq-idem"), Attributes: map[string]string{"VisibilityTimeout": "60"},
		})
		assert.Equal(t, "QueueNameExists", errCode(t, err))
	})

	t.Run("SQS_SendMessage_sizeLimit", func(t *testing.T) {
		c := sqsClient()
		cq, err := c.CreateQueue(ctx, &sqs.CreateQueueInput{QueueName: aws.String("vq-size")})
		require.NoError(t, err)
		_, err = c.SendMessage(ctx, &sqs.SendMessageInput{
			QueueUrl: cq.QueueUrl, MessageBody: aws.String(strings.Repeat("x", 1048577)),
		})
		assert.Equal(t, "InvalidParameterValue", errCode(t, err))
	})

	t.Run("IAM_CreatePolicy_malformed", func(t *testing.T) {
		c := iamClient()
		_, err := c.CreatePolicy(ctx, &iam.CreatePolicyInput{
			PolicyName: aws.String("vp-bad"), PolicyDocument: aws.String("not json"),
		})
		assert.Equal(t, "MalformedPolicyDocument", errCode(t, err))
	})

	t.Run("SFN_CreateStateMachine_invalidDefinition", func(t *testing.T) {
		c := sfnClient()
		_, err := c.CreateStateMachine(ctx, &sfn.CreateStateMachineInput{
			Name: aws.String("vsm-bad"), Definition: aws.String("{not valid asl"),
			RoleArn: aws.String("arn:aws:iam::123456789012:role/r"),
		})
		assert.Equal(t, "InvalidDefinition", errCode(t, err))
	})

	t.Run("Lambda_CreateFunction_memoryRange", func(t *testing.T) {
		c := lambdaClient()
		_, err := c.CreateFunction(ctx, &lambda.CreateFunctionInput{
			FunctionName: aws.String("vf-mem"),
			Role:         aws.String("arn:aws:iam::123456789012:role/r"),
			Runtime:      lambdatypes.RuntimePython312,
			Handler:      aws.String("app.handler"),
			MemorySize:   aws.Int32(64), // below the 128 minimum
			Code:         &lambdatypes.FunctionCode{ZipFile: lambdaDeploymentZip(t)},
		})
		assert.Equal(t, "InvalidParameterValueException", errCode(t, err))
	})
}
