package aws_sdk_test

import (
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ecs"
	ecstypes "github.com/aws/aws-sdk-go-v2/service/ecs/types"
	"github.com/aws/aws-sdk-go-v2/service/secretsmanager"
	smtypes "github.com/aws/aws-sdk-go-v2/service/secretsmanager/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestECS_ListTaskDefinitions_SortAndStatus covers issue #630.
func TestECS_ListTaskDefinitions_SortAndStatus(t *testing.T) {
	c := ecsClient()
	fam := "ltd-probe-630"
	var arns []string
	for i := 0; i < 3; i++ {
		reg, err := c.RegisterTaskDefinition(ctx, &ecs.RegisterTaskDefinitionInput{
			Family:                  aws.String(fam),
			RequiresCompatibilities: []ecstypes.Compatibility{ecstypes.CompatibilityFargate},
			NetworkMode:             ecstypes.NetworkModeAwsvpc,
			Cpu:                     aws.String("256"), Memory: aws.String("512"),
			ContainerDefinitions: []ecstypes.ContainerDefinition{{Name: aws.String("a"), Image: aws.String("nginx"), Essential: aws.Bool(true)}},
		})
		require.NoError(t, err)
		arns = append(arns, *reg.TaskDefinition.TaskDefinitionArn)
	}
	asc, err := c.ListTaskDefinitions(ctx, &ecs.ListTaskDefinitionsInput{FamilyPrefix: aws.String(fam), Status: ecstypes.TaskDefinitionStatusActive, Sort: ecstypes.SortOrderAsc})
	require.NoError(t, err)
	desc, err := c.ListTaskDefinitions(ctx, &ecs.ListTaskDefinitionsInput{FamilyPrefix: aws.String(fam), Status: ecstypes.TaskDefinitionStatusActive, Sort: ecstypes.SortOrderDesc})
	require.NoError(t, err)
	// sort: DESC must reverse ASC.
	require.Len(t, asc.TaskDefinitionArns, 3)
	require.Len(t, desc.TaskDefinitionArns, 3)
	for i := range asc.TaskDefinitionArns {
		assert.Equal(t, asc.TaskDefinitionArns[i], desc.TaskDefinitionArns[len(desc.TaskDefinitionArns)-1-i], "DESC should reverse ASC")
	}
	assert.Equal(t, arns[0], asc.TaskDefinitionArns[0])  // rev1 first ASC
	assert.Equal(t, arns[2], desc.TaskDefinitionArns[0]) // rev3 first DESC

	// status: a deregistered (INACTIVE) revision must drop out of the ACTIVE list.
	_, err = c.DeregisterTaskDefinition(ctx, &ecs.DeregisterTaskDefinitionInput{TaskDefinition: aws.String(arns[0])})
	require.NoError(t, err)
	active, err := c.ListTaskDefinitions(ctx, &ecs.ListTaskDefinitionsInput{FamilyPrefix: aws.String(fam), Status: ecstypes.TaskDefinitionStatusActive})
	require.NoError(t, err)
	assert.NotContains(t, active.TaskDefinitionArns, arns[0], "deregistered rev must not appear in ACTIVE list")
	assert.Contains(t, active.TaskDefinitionArns, arns[1])
}

// TestSM_ListSecrets_TagKeyFilter covers issue #629.
func TestSM_ListSecrets_TagKeyFilter(t *testing.T) {
	c := smClient()
	_, err := c.CreateSecret(ctx, &secretsmanager.CreateSecretInput{
		Name: aws.String("sm629-a"), SecretString: aws.String("x"),
		Tags: []smtypes.Tag{{Key: aws.String("sm629:workspace-id"), Value: aws.String("w")}},
	})
	require.NoError(t, err)
	_, err = c.CreateSecret(ctx, &secretsmanager.CreateSecretInput{
		Name: aws.String("sm629-b"), SecretString: aws.String("y"),
		Tags: []smtypes.Tag{{Key: aws.String("sm629-other"), Value: aws.String("z")}},
	})
	require.NoError(t, err)
	// tag-key matching no secret → empty.
	none, err := c.ListSecrets(ctx, &secretsmanager.ListSecretsInput{
		Filters: []smtypes.Filter{{Key: smtypes.FilterNameStringTypeTagKey, Values: []string{"sm629-no-such-key"}}},
	})
	require.NoError(t, err)
	assert.Empty(t, none.SecretList, "a tag-key matching nothing must return []")
	// tag-key matching secret a → only a.
	one, err := c.ListSecrets(ctx, &secretsmanager.ListSecretsInput{
		Filters: []smtypes.Filter{{Key: smtypes.FilterNameStringTypeTagKey, Values: []string{"sm629:workspace-id"}}},
	})
	require.NoError(t, err)
	names := make([]string, 0)
	for _, s := range one.SecretList {
		names = append(names, *s.Name)
	}
	assert.Contains(t, names, "sm629-a")
	assert.NotContains(t, names, "sm629-b")
}
