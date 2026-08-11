package aws_sdk_test

import (
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/acm"
	acmtypes "github.com/aws/aws-sdk-go-v2/service/acm/types"
	"github.com/stretchr/testify/require"
)

// TestACM_ResourceTagging covers the cross-resource tagging API AWS Certificate
// Manager exposes alongside the certificate-scoped AddTagsToCertificate family:
// TagResource / ListTagsForResource / UntagResource addressed by ARN.
func TestACM_ResourceTagging(t *testing.T) {
	c := acmClient()

	requested, err := c.RequestCertificate(ctx, &acm.RequestCertificateInput{
		DomainName:       aws.String("tagging.sdk.example.com"),
		ValidationMethod: acmtypes.ValidationMethodDns,
	})
	require.NoError(t, err)
	arn := aws.ToString(requested.CertificateArn)
	require.NotEmpty(t, arn)
	defer func() {
		_, _ = c.DeleteCertificate(ctx, &acm.DeleteCertificateInput{CertificateArn: aws.String(arn)})
	}()

	_, err = c.TagResource(ctx, &acm.TagResourceInput{
		ResourceArn: aws.String(arn),
		Tags: []acmtypes.Tag{
			{Key: aws.String("env"), Value: aws.String("prod")},
			{Key: aws.String("owner"), Value: aws.String("platform")},
		},
	})
	require.NoError(t, err)

	listed, err := c.ListTagsForResource(ctx, &acm.ListTagsForResourceInput{ResourceArn: aws.String(arn)})
	require.NoError(t, err)
	got := map[string]string{}
	for _, tag := range listed.Tags {
		got[aws.ToString(tag.Key)] = aws.ToString(tag.Value)
	}
	require.Equal(t, map[string]string{"env": "prod", "owner": "platform"}, got)

	_, err = c.UntagResource(ctx, &acm.UntagResourceInput{
		ResourceArn: aws.String(arn),
		TagKeys:     []string{"owner"},
	})
	require.NoError(t, err)

	listed, err = c.ListTagsForResource(ctx, &acm.ListTagsForResourceInput{ResourceArn: aws.String(arn)})
	require.NoError(t, err)
	require.Len(t, listed.Tags, 1)
	require.Equal(t, "env", aws.ToString(listed.Tags[0].Key))

	// An ARN that names no ACM resource is a ResourceNotFoundException, not a
	// silently-empty answer.
	_, err = c.ListTagsForResource(ctx, &acm.ListTagsForResourceInput{
		ResourceArn: aws.String("arn:aws:acm:us-east-1:000000000000:certificate/does-not-exist"),
	})
	require.Error(t, err)
}
