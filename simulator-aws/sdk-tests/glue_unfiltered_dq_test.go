package aws_sdk_test

import (
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/glue"
	gluetypes "github.com/aws/aws-sdk-go-v2/service/glue/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestGlue_UnfilteredMetadata_SDK exercises the Lake Formation credential-vending
// reads (GetUnfilteredTableMetadata / GetUnfilteredPartitionMetadata /
// GetUnfilteredPartitionsMetadata). The simulator vends the full catalog row with
// every column authorized and no Lake Formation registration.
func TestGlue_UnfilteredMetadata_SDK(t *testing.T) {
	c := glueClient()
	db := "glue-unf-db"
	tbl := "glue-unf-tbl"

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

	// GetUnfilteredTableMetadata: full table + all columns authorized.
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

	// GetUnfilteredPartitionMetadata: full partition + all columns authorized.
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

	// GetUnfilteredPartitionsMetadata: one unfiltered partition entry.
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

// TestGlue_DQAnnotations_SDK round-trips the Data Quality statistic/profile
// annotations: BatchPutDataQualityStatisticAnnotation +
// PutDataQualityProfileAnnotation store, ListDataQualityStatisticAnnotations
// reads back, and a bad batch entry surfaces in FailedInclusionAnnotations.
func TestGlue_DQAnnotations_SDK(t *testing.T) {
	c := glueClient()
	profileID := "glue-dq-profile-sdk"
	statisticID := "glue-dq-stat-sdk"

	// BatchPutDataQualityStatisticAnnotation: one good entry, one missing
	// StatisticId which must come back as a FailedInclusionAnnotation.
	batch, err := c.BatchPutDataQualityStatisticAnnotation(ctx, &glue.BatchPutDataQualityStatisticAnnotationInput{
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
	require.NoError(t, err)
	require.Len(t, batch.FailedInclusionAnnotations, 1)
	assert.Equal(t, profileID, aws.ToString(batch.FailedInclusionAnnotations[0].ProfileId))
	assert.NotEmpty(t, aws.ToString(batch.FailedInclusionAnnotations[0].FailureReason))

	// PutDataQualityProfileAnnotation: annotate the whole profile.
	_, err = c.PutDataQualityProfileAnnotation(ctx, &glue.PutDataQualityProfileAnnotationInput{
		ProfileId:           aws.String(profileID),
		InclusionAnnotation: gluetypes.InclusionAnnotationValueInclude,
	})
	require.NoError(t, err)

	// ListDataQualityStatisticAnnotations: read back the stored statistic
	// annotation, filtered by profile.
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

	// Filtering by a non-matching statistic returns nothing.
	none, err := c.ListDataQualityStatisticAnnotations(ctx, &glue.ListDataQualityStatisticAnnotationsInput{
		ProfileId:   aws.String(profileID),
		StatisticId: aws.String("does-not-exist"),
	})
	require.NoError(t, err)
	assert.Empty(t, none.Annotations)
}
