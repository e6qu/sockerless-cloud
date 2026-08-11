package aws_sdk_test

import (
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	ddbtypes "github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ddbMakeTable creates a simple hash-key table and registers cleanup. Returns
// the table ARN.
func ddbMakeTable(t *testing.T, c *dynamodb.Client, name string) string {
	t.Helper()
	out, err := c.CreateTable(ctx, &dynamodb.CreateTableInput{
		TableName: aws.String(name),
		AttributeDefinitions: []ddbtypes.AttributeDefinition{
			{AttributeName: aws.String("pk"), AttributeType: ddbtypes.ScalarAttributeTypeS},
		},
		KeySchema: []ddbtypes.KeySchemaElement{
			{AttributeName: aws.String("pk"), KeyType: ddbtypes.KeyTypeHash},
		},
		BillingMode: ddbtypes.BillingModePayPerRequest,
	})
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = c.DeleteTable(ctx, &dynamodb.DeleteTableInput{TableName: aws.String(name)}) })
	return aws.ToString(out.TableDescription.TableArn)
}

// TestDDB_BackupLifecycle covers CreateBackup / DescribeBackup / ListBackups /
// DeleteBackup / RestoreTableFromBackup as a faithful round-trip: a backup of a
// populated table restores into a new table that carries the original item.
func TestDDB_BackupLifecycle(t *testing.T) {
	c := ddbClient()
	table := "ddb-bk-src"
	ddbMakeTable(t, c, table)

	_, err := c.PutItem(ctx, &dynamodb.PutItemInput{
		TableName: aws.String(table),
		Item: map[string]ddbtypes.AttributeValue{
			"pk":  &ddbtypes.AttributeValueMemberS{Value: "item1"},
			"val": &ddbtypes.AttributeValueMemberN{Value: "42"},
		},
	})
	require.NoError(t, err)

	cb, err := c.CreateBackup(ctx, &dynamodb.CreateBackupInput{
		TableName:  aws.String(table),
		BackupName: aws.String("daily"),
	})
	require.NoError(t, err)
	require.NotNil(t, cb.BackupDetails)
	backupArn := aws.ToString(cb.BackupDetails.BackupArn)
	assert.Contains(t, backupArn, ":table/"+table+"/backup/")
	assert.Equal(t, ddbtypes.BackupStatusAvailable, cb.BackupDetails.BackupStatus)

	db, err := c.DescribeBackup(ctx, &dynamodb.DescribeBackupInput{BackupArn: aws.String(backupArn)})
	require.NoError(t, err)
	require.NotNil(t, db.BackupDescription)
	require.NotNil(t, db.BackupDescription.SourceTableDetails)
	assert.Equal(t, table, aws.ToString(db.BackupDescription.SourceTableDetails.TableName))

	lb, err := c.ListBackups(ctx, &dynamodb.ListBackupsInput{TableName: aws.String(table)})
	require.NoError(t, err)
	found := false
	for _, s := range lb.BackupSummaries {
		if aws.ToString(s.BackupArn) == backupArn {
			found = true
		}
	}
	assert.True(t, found, "ListBackups must include the created backup")

	// Restore into a new table; the original item must be present.
	restored := "ddb-bk-restored"
	t.Cleanup(func() { _, _ = c.DeleteTable(ctx, &dynamodb.DeleteTableInput{TableName: aws.String(restored)}) })
	rb, err := c.RestoreTableFromBackup(ctx, &dynamodb.RestoreTableFromBackupInput{
		BackupArn:       aws.String(backupArn),
		TargetTableName: aws.String(restored),
	})
	require.NoError(t, err)
	require.NotNil(t, rb.TableDescription)
	assert.Equal(t, restored, aws.ToString(rb.TableDescription.TableName))

	gi, err := c.GetItem(ctx, &dynamodb.GetItemInput{
		TableName: aws.String(restored),
		Key:       map[string]ddbtypes.AttributeValue{"pk": &ddbtypes.AttributeValueMemberS{Value: "item1"}},
	})
	require.NoError(t, err)
	require.NotEmpty(t, gi.Item, "restored table must carry the backed-up item")
	assert.Equal(t, "42", gi.Item["val"].(*ddbtypes.AttributeValueMemberN).Value)

	_, err = c.DeleteBackup(ctx, &dynamodb.DeleteBackupInput{BackupArn: aws.String(backupArn)})
	require.NoError(t, err)
	_, err = c.DescribeBackup(ctx, &dynamodb.DescribeBackupInput{BackupArn: aws.String(backupArn)})
	require.Error(t, err, "deleted backup must DescribeBackup→BackupNotFoundException")
}

// TestDDB_RestoreToPointInTime restores a PITR-enabled table to a new table.
func TestDDB_RestoreToPointInTime(t *testing.T) {
	c := ddbClient()
	table := "ddb-pitr-src"
	ddbMakeTable(t, c, table)

	_, err := c.UpdateContinuousBackups(ctx, &dynamodb.UpdateContinuousBackupsInput{
		TableName: aws.String(table),
		PointInTimeRecoverySpecification: &ddbtypes.PointInTimeRecoverySpecification{
			PointInTimeRecoveryEnabled: aws.Bool(true),
		},
	})
	require.NoError(t, err)

	_, err = c.PutItem(ctx, &dynamodb.PutItemInput{
		TableName: aws.String(table),
		Item:      map[string]ddbtypes.AttributeValue{"pk": &ddbtypes.AttributeValueMemberS{Value: "x"}},
	})
	require.NoError(t, err)

	target := "ddb-pitr-restored"
	t.Cleanup(func() { _, _ = c.DeleteTable(ctx, &dynamodb.DeleteTableInput{TableName: aws.String(target)}) })
	out, err := c.RestoreTableToPointInTime(ctx, &dynamodb.RestoreTableToPointInTimeInput{
		SourceTableName: aws.String(table),
		TargetTableName: aws.String(target),
	})
	require.NoError(t, err)
	require.NotNil(t, out.TableDescription)
	assert.Equal(t, target, aws.ToString(out.TableDescription.TableName))

	gi, err := c.GetItem(ctx, &dynamodb.GetItemInput{
		TableName: aws.String(target),
		Key:       map[string]ddbtypes.AttributeValue{"pk": &ddbtypes.AttributeValueMemberS{Value: "x"}},
	})
	require.NoError(t, err)
	require.NotEmpty(t, gi.Item)
}

// TestDDB_GlobalTables covers CreateGlobalTable / DescribeGlobalTable /
// ListGlobalTables / UpdateGlobalTable and the settings + autoscaling reads.
func TestDDB_GlobalTables(t *testing.T) {
	c := ddbClient()
	table := "ddb-gt"
	ddbMakeTable(t, c, table)

	cg, err := c.CreateGlobalTable(ctx, &dynamodb.CreateGlobalTableInput{
		GlobalTableName: aws.String(table),
		ReplicationGroup: []ddbtypes.Replica{
			{RegionName: aws.String("us-east-1")},
		},
	})
	require.NoError(t, err)
	require.NotNil(t, cg.GlobalTableDescription)
	assert.Len(t, cg.GlobalTableDescription.ReplicationGroup, 1)

	dg, err := c.DescribeGlobalTable(ctx, &dynamodb.DescribeGlobalTableInput{GlobalTableName: aws.String(table)})
	require.NoError(t, err)
	assert.Equal(t, table, aws.ToString(dg.GlobalTableDescription.GlobalTableName))

	lg, err := c.ListGlobalTables(ctx, &dynamodb.ListGlobalTablesInput{})
	require.NoError(t, err)
	names := map[string]bool{}
	for _, g := range lg.GlobalTables {
		names[aws.ToString(g.GlobalTableName)] = true
	}
	assert.True(t, names[table])

	ug, err := c.UpdateGlobalTable(ctx, &dynamodb.UpdateGlobalTableInput{
		GlobalTableName: aws.String(table),
		ReplicaUpdates: []ddbtypes.ReplicaUpdate{
			{Create: &ddbtypes.CreateReplicaAction{RegionName: aws.String("us-west-2")}},
		},
	})
	require.NoError(t, err)
	assert.Len(t, ug.GlobalTableDescription.ReplicationGroup, 2)

	dgs, err := c.DescribeGlobalTableSettings(ctx, &dynamodb.DescribeGlobalTableSettingsInput{
		GlobalTableName: aws.String(table),
	})
	require.NoError(t, err)
	assert.Equal(t, table, aws.ToString(dgs.GlobalTableName))
	assert.Len(t, dgs.ReplicaSettings, 2)

	ugs, err := c.UpdateGlobalTableSettings(ctx, &dynamodb.UpdateGlobalTableSettingsInput{
		GlobalTableName:                          aws.String(table),
		GlobalTableProvisionedWriteCapacityUnits: aws.Int64(10),
	})
	require.NoError(t, err)
	assert.Equal(t, table, aws.ToString(ugs.GlobalTableName))

	uas, err := c.UpdateTableReplicaAutoScaling(ctx, &dynamodb.UpdateTableReplicaAutoScalingInput{
		TableName: aws.String(table),
	})
	require.NoError(t, err)
	require.NotNil(t, uas.TableAutoScalingDescription)
	assert.Equal(t, table, aws.ToString(uas.TableAutoScalingDescription.TableName))

	das, err := c.DescribeTableReplicaAutoScaling(ctx, &dynamodb.DescribeTableReplicaAutoScalingInput{
		TableName: aws.String(table),
	})
	require.NoError(t, err)
	require.NotNil(t, das.TableAutoScalingDescription)
	assert.NotEmpty(t, das.TableAutoScalingDescription.Replicas)
}

// TestDDB_ResourcePolicy covers PutResourcePolicy / GetResourcePolicy /
// DeleteResourcePolicy on a table ARN.
func TestDDB_ResourcePolicy(t *testing.T) {
	c := ddbClient()
	table := "ddb-rp"
	arn := ddbMakeTable(t, c, table)

	policy := `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"AWS":"*"},"Action":"dynamodb:GetItem","Resource":"` + arn + `"}]}`
	pp, err := c.PutResourcePolicy(ctx, &dynamodb.PutResourcePolicyInput{
		ResourceArn: aws.String(arn),
		Policy:      aws.String(policy),
	})
	require.NoError(t, err)
	assert.NotEmpty(t, aws.ToString(pp.RevisionId))

	gp, err := c.GetResourcePolicy(ctx, &dynamodb.GetResourcePolicyInput{ResourceArn: aws.String(arn)})
	require.NoError(t, err)
	assert.JSONEq(t, policy, aws.ToString(gp.Policy))

	_, err = c.DeleteResourcePolicy(ctx, &dynamodb.DeleteResourcePolicyInput{ResourceArn: aws.String(arn)})
	require.NoError(t, err)
	_, err = c.GetResourcePolicy(ctx, &dynamodb.GetResourcePolicyInput{ResourceArn: aws.String(arn)})
	require.Error(t, err, "deleted policy must GetResourcePolicy→PolicyNotFoundException")
}

// TestDDB_KinesisStreaming covers Enable/Describe/Update/Disable Kinesis
// streaming destination.
func TestDDB_KinesisStreaming(t *testing.T) {
	c := ddbClient()
	table := "ddb-kinesis"
	ddbMakeTable(t, c, table)
	streamArn := "arn:aws:kinesis:us-east-1:000000000000:stream/ddb-changes"

	en, err := c.EnableKinesisStreamingDestination(ctx, &dynamodb.EnableKinesisStreamingDestinationInput{
		TableName: aws.String(table),
		StreamArn: aws.String(streamArn),
	})
	require.NoError(t, err)
	assert.Equal(t, table, aws.ToString(en.TableName))

	dk, err := c.DescribeKinesisStreamingDestination(ctx, &dynamodb.DescribeKinesisStreamingDestinationInput{
		TableName: aws.String(table),
	})
	require.NoError(t, err)
	require.Len(t, dk.KinesisDataStreamDestinations, 1)
	assert.Equal(t, streamArn, aws.ToString(dk.KinesisDataStreamDestinations[0].StreamArn))

	uk, err := c.UpdateKinesisStreamingDestination(ctx, &dynamodb.UpdateKinesisStreamingDestinationInput{
		TableName: aws.String(table),
		StreamArn: aws.String(streamArn),
		UpdateKinesisStreamingConfiguration: &ddbtypes.UpdateKinesisStreamingConfiguration{
			ApproximateCreationDateTimePrecision: ddbtypes.ApproximateCreationDateTimePrecisionMillisecond,
		},
	})
	require.NoError(t, err)
	assert.Equal(t, table, aws.ToString(uk.TableName))

	_, err = c.DisableKinesisStreamingDestination(ctx, &dynamodb.DisableKinesisStreamingDestinationInput{
		TableName: aws.String(table),
		StreamArn: aws.String(streamArn),
	})
	require.NoError(t, err)

	dk2, err := c.DescribeKinesisStreamingDestination(ctx, &dynamodb.DescribeKinesisStreamingDestinationInput{
		TableName: aws.String(table),
	})
	require.NoError(t, err)
	assert.Empty(t, dk2.KinesisDataStreamDestinations, "disabled destination must be gone")
}

// TestDDB_ExportImport covers ExportTableToPointInTime / DescribeExport /
// ListExports and ImportTable / DescribeImport / ListImports.
func TestDDB_ExportImport(t *testing.T) {
	c := ddbClient()
	table := "ddb-export"
	arn := ddbMakeTable(t, c, table)

	ex, err := c.ExportTableToPointInTime(ctx, &dynamodb.ExportTableToPointInTimeInput{
		TableArn: aws.String(arn),
		S3Bucket: aws.String("my-export-bucket"),
		S3Prefix: aws.String("exports/ddb"),
	})
	require.NoError(t, err)
	require.NotNil(t, ex.ExportDescription)
	exportArn := aws.ToString(ex.ExportDescription.ExportArn)
	assert.Equal(t, ddbtypes.ExportStatusCompleted, ex.ExportDescription.ExportStatus)

	de, err := c.DescribeExport(ctx, &dynamodb.DescribeExportInput{ExportArn: aws.String(exportArn)})
	require.NoError(t, err)
	assert.Equal(t, arn, aws.ToString(de.ExportDescription.TableArn))

	le, err := c.ListExports(ctx, &dynamodb.ListExportsInput{TableArn: aws.String(arn)})
	require.NoError(t, err)
	require.NotEmpty(t, le.ExportSummaries)

	// Import creates a new destination table.
	importTable := "ddb-imported"
	t.Cleanup(func() { _, _ = c.DeleteTable(ctx, &dynamodb.DeleteTableInput{TableName: aws.String(importTable)}) })
	im, err := c.ImportTable(ctx, &dynamodb.ImportTableInput{
		InputFormat:    ddbtypes.InputFormatDynamodbJson,
		S3BucketSource: &ddbtypes.S3BucketSource{S3Bucket: aws.String("my-import-bucket"), S3KeyPrefix: aws.String("imports/")},
		TableCreationParameters: &ddbtypes.TableCreationParameters{
			TableName: aws.String(importTable),
			AttributeDefinitions: []ddbtypes.AttributeDefinition{
				{AttributeName: aws.String("pk"), AttributeType: ddbtypes.ScalarAttributeTypeS},
			},
			KeySchema: []ddbtypes.KeySchemaElement{
				{AttributeName: aws.String("pk"), KeyType: ddbtypes.KeyTypeHash},
			},
			BillingMode: ddbtypes.BillingModePayPerRequest,
		},
	})
	require.NoError(t, err)
	require.NotNil(t, im.ImportTableDescription)
	importArn := aws.ToString(im.ImportTableDescription.ImportArn)
	importedTableArn := aws.ToString(im.ImportTableDescription.TableArn)

	di, err := c.DescribeImport(ctx, &dynamodb.DescribeImportInput{ImportArn: aws.String(importArn)})
	require.NoError(t, err)
	assert.Equal(t, ddbtypes.ImportStatusCompleted, di.ImportTableDescription.ImportStatus)

	li, err := c.ListImports(ctx, &dynamodb.ListImportsInput{TableArn: aws.String(importedTableArn)})
	require.NoError(t, err)
	require.NotEmpty(t, li.ImportSummaryList)

	// The imported destination table must really exist.
	_, err = c.DescribeTable(ctx, &dynamodb.DescribeTableInput{TableName: aws.String(importTable)})
	require.NoError(t, err)
}

// TestDDB_ContributorInsights covers Update/Describe/List contributor insights.
func TestDDB_ContributorInsights(t *testing.T) {
	c := ddbClient()
	table := "ddb-ci"
	ddbMakeTable(t, c, table)

	_, err := c.UpdateContributorInsights(ctx, &dynamodb.UpdateContributorInsightsInput{
		TableName:                 aws.String(table),
		ContributorInsightsAction: ddbtypes.ContributorInsightsActionEnable,
	})
	require.NoError(t, err)

	dci, err := c.DescribeContributorInsights(ctx, &dynamodb.DescribeContributorInsightsInput{
		TableName: aws.String(table),
	})
	require.NoError(t, err)
	assert.Equal(t, ddbtypes.ContributorInsightsStatusEnabled, dci.ContributorInsightsStatus)

	lci, err := c.ListContributorInsights(ctx, &dynamodb.ListContributorInsightsInput{
		TableName: aws.String(table),
	})
	require.NoError(t, err)
	found := false
	for _, s := range lci.ContributorInsightsSummaries {
		if aws.ToString(s.TableName) == table && s.ContributorInsightsStatus == ddbtypes.ContributorInsightsStatusEnabled {
			found = true
		}
	}
	assert.True(t, found, "ListContributorInsights must report the enabled table")
}

// TestDDB_DescribeEndpoints returns a regional endpoint.
func TestDDB_DescribeEndpoints(t *testing.T) {
	c := ddbClient()
	out, err := c.DescribeEndpoints(ctx, &dynamodb.DescribeEndpointsInput{})
	require.NoError(t, err)
	require.NotEmpty(t, out.Endpoints)
	assert.NotEmpty(t, aws.ToString(out.Endpoints[0].Address))
	assert.Greater(t, out.Endpoints[0].CachePeriodInMinutes, int64(0))
}
