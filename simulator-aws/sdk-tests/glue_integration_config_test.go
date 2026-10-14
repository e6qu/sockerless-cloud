package aws_sdk_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/glue"
	gluetypes "github.com/aws/aws-sdk-go-v2/service/glue/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestGlue_IntegrationTargetNamespace_SDK covers TargetNamespace: taken only
// for an Amazon S3 Tables table bucket target, held to a namespace name,
// defaulted from the integration ID, and fixed once the integration exists.
func TestGlue_IntegrationTargetNamespace_SDK(t *testing.T) {
	t.Parallel()
	c := glueClient()
	const src = "arn:aws:rds:us-east-1:123456789012:db:source"
	const tableBucket = "arn:aws:s3tables:us-east-1:123456789012:bucket/zero-etl-target"
	create := func(name, target string, config *gluetypes.IntegrationConfig) (*glue.CreateIntegrationOutput, error) {
		out, err := c.CreateIntegration(ctx, &glue.CreateIntegrationInput{
			IntegrationName:   aws.String(name),
			SourceArn:         aws.String(src),
			TargetArn:         aws.String(target),
			IntegrationConfig: config,
		})
		if err == nil {
			t.Cleanup(func() {
				_, _ = c.DeleteIntegration(ctx, &glue.DeleteIntegrationInput{IntegrationIdentifier: out.IntegrationArn})
			})
		}
		return out, err
	}

	named, err := create(uniqueName("glue-ns-named"), tableBucket, &gluetypes.IntegrationConfig{TargetNamespace: aws.String("orders_replica")})
	require.NoError(t, err)
	assert.Equal(t, "orders_replica", aws.ToString(named.IntegrationConfig.TargetNamespace))

	defaulted, err := create(uniqueName("glue-ns-default"), tableBucket, nil)
	require.NoError(t, err)
	arn := aws.ToString(defaulted.IntegrationArn)
	id := arn[strings.LastIndex(arn, ":")+1:]
	require.NotNil(t, defaulted.IntegrationConfig)
	assert.Equal(t, "zetl_"+strings.ReplaceAll(id, "-", "_"), aws.ToString(defaulted.IntegrationConfig.TargetNamespace))

	plain, err := create(uniqueName("glue-ns-catalog"), "arn:aws:glue:us-east-1:123456789012:catalog", nil)
	require.NoError(t, err)
	assert.True(t, plain.IntegrationConfig == nil || plain.IntegrationConfig.TargetNamespace == nil,
		"a target other than a table bucket gets no namespace")

	var invalidInput *gluetypes.InvalidInputException
	_, err = create(uniqueName("glue-ns-wrong-target"), "arn:aws:glue:us-east-1:123456789012:catalog",
		&gluetypes.IntegrationConfig{TargetNamespace: aws.String("orders")})
	require.True(t, errors.As(err, &invalidInput), "a namespace for a target other than a table bucket is refused, got %v", err)
	_, err = create(uniqueName("glue-ns-reserved"), tableBucket, &gluetypes.IntegrationConfig{TargetNamespace: aws.String("aws_orders")})
	require.True(t, errors.As(err, &invalidInput), "the reserved aws prefix is refused, got %v", err)
	var validation *gluetypes.ValidationException
	_, err = create(uniqueName("glue-ns-pattern"), tableBucket, &gluetypes.IntegrationConfig{TargetNamespace: aws.String("_Orders")})
	require.True(t, errors.As(err, &validation), "a name outside the pattern is refused, got %v", err)

	_, err = c.ModifyIntegration(ctx, &glue.ModifyIntegrationInput{
		IntegrationIdentifier: named.IntegrationArn,
		IntegrationConfig:     &gluetypes.IntegrationConfig{TargetNamespace: aws.String("orders_other")},
	})
	require.True(t, errors.As(err, &invalidInput), "ModifyIntegration refuses a namespace change, got %v", err)
}

// TestGlue_ModifyIntegrationRefreshInterval_SDK changes an integration's
// RefreshInterval and reads it back, keeping the rest of its configuration.
func TestGlue_ModifyIntegrationRefreshInterval_SDK(t *testing.T) {
	t.Parallel()
	c := glueClient()
	created, err := c.CreateIntegration(ctx, &glue.CreateIntegrationInput{
		IntegrationName: aws.String(uniqueName("glue-refresh")),
		SourceArn:       aws.String("arn:aws:rds:us-east-1:123456789012:db:source"),
		TargetArn:       aws.String("arn:aws:glue:us-east-1:123456789012:catalog"),
		IntegrationConfig: &gluetypes.IntegrationConfig{
			RefreshInterval:  aws.String("60"),
			SourceProperties: map[string]string{"mode": "cdc"},
		},
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = c.DeleteIntegration(ctx, &glue.DeleteIntegrationInput{IntegrationIdentifier: created.IntegrationArn})
	})

	mod, err := c.ModifyIntegration(ctx, &glue.ModifyIntegrationInput{
		IntegrationIdentifier: created.IntegrationArn,
		IntegrationConfig:     &gluetypes.IntegrationConfig{RefreshInterval: aws.String("120")},
	})
	require.NoError(t, err)
	assert.Equal(t, "120", aws.ToString(mod.IntegrationConfig.RefreshInterval))

	desc, err := c.DescribeIntegrations(ctx, &glue.DescribeIntegrationsInput{IntegrationIdentifier: created.IntegrationArn})
	require.NoError(t, err)
	require.Len(t, desc.Integrations, 1)
	config := desc.Integrations[0].IntegrationConfig
	require.NotNil(t, config)
	assert.Equal(t, "120", aws.ToString(config.RefreshInterval))
	assert.Equal(t, map[string]string{"mode": "cdc"}, config.SourceProperties, "members the request omits stay as they were")

	_, err = c.ModifyIntegration(ctx, &glue.ModifyIntegrationInput{
		IntegrationIdentifier: created.IntegrationArn,
		IntegrationConfig:     &gluetypes.IntegrationConfig{SourceProperties: map[string]string{"mode": "full"}},
	})
	var invalidInput *gluetypes.InvalidInputException
	require.True(t, errors.As(err, &invalidInput), "ModifyIntegration changes only RefreshInterval, got %v", err)
}
