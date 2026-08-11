package aws_sdk_test

import (
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ecs"
	ecstypes "github.com/aws/aws-sdk-go-v2/service/ecs/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestECS_DescribeCapacityProviders covers the read-back for capacity
// providers — the built-in FARGATE/FARGATE_SPOT always resolve ACTIVE,
// and an unknown name comes back as a failure rather than a phantom ACTIVE.
func TestECS_DescribeCapacityProviders(t *testing.T) {
	c := ecsClient()
	out, err := c.DescribeCapacityProviders(ctx, &ecs.DescribeCapacityProvidersInput{
		CapacityProviders: []string{"FARGATE", "FARGATE_SPOT"},
	})
	require.NoError(t, err)
	require.Len(t, out.CapacityProviders, 2)
	for _, cp := range out.CapacityProviders {
		assert.Equal(t, ecstypes.CapacityProviderStatusActive, cp.Status)
		assert.Contains(t, []string{"FARGATE", "FARGATE_SPOT"}, aws.ToString(cp.Name))
	}

	missing, err := c.DescribeCapacityProviders(ctx, &ecs.DescribeCapacityProvidersInput{
		CapacityProviders: []string{"no-such-provider"},
	})
	require.NoError(t, err)
	assert.Empty(t, missing.CapacityProviders)
	require.Len(t, missing.Failures, 1)
	assert.Equal(t, "MISSING", aws.ToString(missing.Failures[0].Reason))
}

// TestECS_ListTaskDefinitionFamilies covers the family aggregation:
// after registering a task definition its family is listed, honouring
// familyPrefix.
func TestECS_ListTaskDefinitionFamilies(t *testing.T) {
	c := ecsClient()
	family := "readcomplete-family"
	_, err := c.RegisterTaskDefinition(ctx, &ecs.RegisterTaskDefinitionInput{
		Family: aws.String(family),
		ContainerDefinitions: []ecstypes.ContainerDefinition{{
			Name:   aws.String("app"),
			Image:  aws.String("public.ecr.aws/docker/library/busybox:latest"),
			Memory: aws.Int32(128),
		}},
	})
	require.NoError(t, err)

	all, err := c.ListTaskDefinitionFamilies(ctx, &ecs.ListTaskDefinitionFamiliesInput{})
	require.NoError(t, err)
	assert.Contains(t, all.Families, family)

	filtered, err := c.ListTaskDefinitionFamilies(ctx, &ecs.ListTaskDefinitionFamiliesInput{
		FamilyPrefix: aws.String("readcomplete-"),
	})
	require.NoError(t, err)
	assert.Contains(t, filtered.Families, family)

	none, err := c.ListTaskDefinitionFamilies(ctx, &ecs.ListTaskDefinitionFamiliesInput{
		FamilyPrefix: aws.String("zzz-no-match-"),
	})
	require.NoError(t, err)
	assert.NotContains(t, none.Families, family)
}
