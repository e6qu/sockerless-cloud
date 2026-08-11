package aws_sdk_test

import (
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sts"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func stsClient() *sts.Client {
	return sts.NewFromConfig(sdkConfig(), func(o *sts.Options) {
		o.BaseEndpoint = aws.String(baseURL)
	})
}

func TestSTS_GetCallerIdentity(t *testing.T) {
	client := stsClient()
	out, err := client.GetCallerIdentity(ctx, &sts.GetCallerIdentityInput{})
	require.NoError(t, err)
	assert.NotEmpty(t, *out.Account)
	assert.NotEmpty(t, *out.Arn)
	assert.NotEmpty(t, *out.UserId)
}
