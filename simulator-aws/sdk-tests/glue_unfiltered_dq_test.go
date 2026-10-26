package aws_sdk_test

import (
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/glue"
	gluetypes "github.com/aws/aws-sdk-go-v2/service/glue/types"
	"github.com/e6qu/sockerless-cloud/testutil/uniquename"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestGlue_UnfilteredMetadata_SDK covers the AWS Lake Formation
// credential-vending reads; with no Lake Formation registration the full
// catalog row comes back with every column authorized.
func TestGlue_UnfilteredMetadata_SDK(t *testing.T) {
	t.Parallel()
	c := glueClient()
	db := uniquename.New("glue-unf-db")
	tbl := uniquename.New("glue-unf-tbl")

	_, err := c.CreateDatabase(ctx, &glue.CreateDatabaseInput{
		DatabaseInput: &gluetypes.DatabaseInput{Name: aws.String(db)},
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = c.DeleteTable(ctx, &glue.DeleteTableInput{DatabaseName: aws.String(db), Name: aws.String(tbl)})
		_, _ = c.DeleteDatabase(ctx, &glue.DeleteDatabaseInput{Name: aws.String(db)})
	})

	_, err = c.CreateTable(ctx, &glue.CreateTableInput{
		DatabaseName: aws.String(db),
		TableInput: &gluetypes.TableInput{
			Name: aws.String(tbl),
			StorageDescriptor: &gluetypes.StorageDescriptor{
				Location: aws.String("s3://bucket/unf/"),
				Columns: []gluetypes.Column{
					{Name: aws.String("id"), Type: aws.String("int")},
					{Name: aws.String("name"), Type: aws.String("string")},
				},
			},
			PartitionKeys: []gluetypes.Column{{Name: aws.String("dt"), Type: aws.String("string")}},
		},
	})
	require.NoError(t, err)

	_, err = c.CreatePartition(ctx, &glue.CreatePartitionInput{
		DatabaseName: aws.String(db),
		TableName:    aws.String(tbl),
		PartitionInput: &gluetypes.PartitionInput{
			Values:            []string{"2024-01-01"},
			StorageDescriptor: &gluetypes.StorageDescriptor{Location: aws.String("s3://bucket/unf/dt=2024-01-01/")},
		},
	})
	require.NoError(t, err)

	tm, err := c.GetUnfilteredTableMetadata(ctx, &glue.GetUnfilteredTableMetadataInput{
		CatalogId:                aws.String("123456789012"),
		DatabaseName:             aws.String(db),
		Name:                     aws.String(tbl),
		SupportedPermissionTypes: []gluetypes.PermissionType{gluetypes.PermissionTypeColumnPermission},
	})
	require.NoError(t, err)
	require.NotNil(t, tm.Table)
	assert.Equal(t, tbl, aws.ToString(tm.Table.Name))
	assert.ElementsMatch(t, []string{"id", "name"}, tm.AuthorizedColumns)
	assert.False(t, tm.IsRegisteredWithLakeFormation)

	pm, err := c.GetUnfilteredPartitionMetadata(ctx, &glue.GetUnfilteredPartitionMetadataInput{
		CatalogId:                aws.String("123456789012"),
		DatabaseName:             aws.String(db),
		TableName:                aws.String(tbl),
		PartitionValues:          []string{"2024-01-01"},
		SupportedPermissionTypes: []gluetypes.PermissionType{gluetypes.PermissionTypeColumnPermission},
	})
	require.NoError(t, err)
	require.NotNil(t, pm.Partition)
	assert.Equal(t, []string{"2024-01-01"}, pm.Partition.Values)
	assert.ElementsMatch(t, []string{"id", "name"}, pm.AuthorizedColumns)
	assert.False(t, pm.IsRegisteredWithLakeFormation)

	pms, err := c.GetUnfilteredPartitionsMetadata(ctx, &glue.GetUnfilteredPartitionsMetadataInput{
		CatalogId:                aws.String("123456789012"),
		DatabaseName:             aws.String(db),
		TableName:                aws.String(tbl),
		SupportedPermissionTypes: []gluetypes.PermissionType{gluetypes.PermissionTypeColumnPermission},
	})
	require.NoError(t, err)
	require.Len(t, pms.UnfilteredPartitions, 1)
	require.NotNil(t, pms.UnfilteredPartitions[0].Partition)
	assert.Equal(t, []string{"2024-01-01"}, pms.UnfilteredPartitions[0].Partition.Values)
	assert.ElementsMatch(t, []string{"id", "name"}, pms.UnfilteredPartitions[0].AuthorizedColumns)
	assert.False(t, pms.UnfilteredPartitions[0].IsRegisteredWithLakeFormation)
}

// TestGlue_DQAnnotations_SDK expects statistic and profile annotations to be
// stored and listed back, and a bad batch entry in FailedInclusionAnnotations.
func TestGlue_DQAnnotations_SDK(t *testing.T) {
	t.Parallel()
	c := glueClient()
	statisticID := "glue-dq-stat-sdk"

	// An annotation names a profile, so run an evaluation and read the profile it
	// wrote statistics to off the result: a profile nothing issued names nothing.
	_, err := c.StartDataQualityRulesetEvaluationRun(ctx, &glue.StartDataQualityRulesetEvaluationRunInput{
		Role:         aws.String(serviceRole(t, "glue.amazonaws.com")),
		RulesetNames: []string{"glue-dq-annotations-ruleset"},
		DataSource:   &gluetypes.DataSource{},
	})
	require.NoError(t, err)
	results, err := c.ListDataQualityResults(ctx, &glue.ListDataQualityResultsInput{})
	require.NoError(t, err)
	require.NotEmpty(t, results.Results)
	var profileID string
	for _, candidate := range results.Results {
		got, err := c.GetDataQualityResult(ctx, &glue.GetDataQualityResultInput{
			ResultId: candidate.ResultId,
		})
		require.NoError(t, err)
		if aws.ToString(got.RulesetName) == "glue-dq-annotations-ruleset" {
			profileID = aws.ToString(got.ProfileId)
			break
		}
	}
	require.NotEmpty(t, profileID)

	// One entry lacks StatisticId and comes back in FailedInclusionAnnotations.
	batch, err2 := c.BatchPutDataQualityStatisticAnnotation(ctx, &glue.BatchPutDataQualityStatisticAnnotationInput{
		InclusionAnnotations: []gluetypes.DatapointInclusionAnnotation{
			{
				ProfileId:           aws.String(profileID),
				StatisticId:         aws.String(statisticID),
				InclusionAnnotation: gluetypes.InclusionAnnotationValueInclude,
			},
			{
				ProfileId:           aws.String(profileID),
				InclusionAnnotation: gluetypes.InclusionAnnotationValueExclude,
			},
		},
	})
	require.NoError(t, err2)
	require.Len(t, batch.FailedInclusionAnnotations, 1)
	assert.Equal(t, profileID, aws.ToString(batch.FailedInclusionAnnotations[0].ProfileId))
	assert.NotEmpty(t, aws.ToString(batch.FailedInclusionAnnotations[0].FailureReason))

	_, err = c.PutDataQualityProfileAnnotation(ctx, &glue.PutDataQualityProfileAnnotationInput{
		ProfileId:           aws.String(profileID),
		InclusionAnnotation: gluetypes.InclusionAnnotationValueInclude,
	})
	require.NoError(t, err)

	list, err := c.ListDataQualityStatisticAnnotations(ctx, &glue.ListDataQualityStatisticAnnotationsInput{
		ProfileId: aws.String(profileID),
	})
	require.NoError(t, err)
	require.Len(t, list.Annotations, 1)
	a := list.Annotations[0]
	assert.Equal(t, profileID, aws.ToString(a.ProfileId))
	assert.Equal(t, statisticID, aws.ToString(a.StatisticId))
	require.NotNil(t, a.StatisticRecordedOn)
	require.NotNil(t, a.InclusionAnnotation)
	assert.Equal(t, gluetypes.InclusionAnnotationValueInclude, a.InclusionAnnotation.Value)
	require.NotNil(t, a.InclusionAnnotation.LastModifiedOn)

	// An entry naming a profile nothing issued fails alone; the rest of the batch
	// applies.
	unknown, err := c.BatchPutDataQualityStatisticAnnotation(ctx, &glue.BatchPutDataQualityStatisticAnnotationInput{
		InclusionAnnotations: []gluetypes.DatapointInclusionAnnotation{{
			ProfileId:           aws.String("no-such-profile"),
			StatisticId:         aws.String(statisticID),
			InclusionAnnotation: gluetypes.InclusionAnnotationValueInclude,
		}},
	})
	require.NoError(t, err)
	require.Len(t, unknown.FailedInclusionAnnotations, 1)
	assert.Contains(t, aws.ToString(unknown.FailedInclusionAnnotations[0].FailureReason), "no-such-profile")

	// A profile nothing issued cannot be annotated whole or asked for a model: a
	// model trains on statistics the simulator never collects.
	_, err = c.PutDataQualityProfileAnnotation(ctx, &glue.PutDataQualityProfileAnnotationInput{
		ProfileId:           aws.String("no-such-profile"),
		InclusionAnnotation: gluetypes.InclusionAnnotationValueInclude,
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "EntityNotFoundException")

	_, err = c.GetDataQualityModel(ctx, &glue.GetDataQualityModelInput{
		ProfileId: aws.String(profileID), StatisticId: aws.String(statisticID),
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "EntityNotFoundException")

	_, err = c.GetDataQualityModelResult(ctx, &glue.GetDataQualityModelResultInput{
		ProfileId: aws.String(profileID), StatisticId: aws.String(statisticID),
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "EntityNotFoundException")

	none, err := c.ListDataQualityStatisticAnnotations(ctx, &glue.ListDataQualityStatisticAnnotationsInput{
		ProfileId:   aws.String(profileID),
		StatisticId: aws.String("does-not-exist"),
	})
	require.NoError(t, err)
	assert.Empty(t, none.Annotations)
}
