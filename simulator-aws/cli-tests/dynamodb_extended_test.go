package aws_cli_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ddbCLICreateTableArn creates a simple hash-key table via the CLI and registers
// cleanup. Returns the table ARN.
func ddbCLICreateTableArn(t *testing.T, table string) string {
	t.Helper()
	out := runCLI(t, awsCLI("dynamodb", "create-table",
		"--table-name", table,
		"--attribute-definitions", "AttributeName=pk,AttributeType=S",
		"--key-schema", "AttributeName=pk,KeyType=HASH",
		"--billing-mode", "PAY_PER_REQUEST",
		"--output", "json"))
	t.Cleanup(func() { _ = awsCLI("dynamodb", "delete-table", "--table-name", table).Run() })
	var desc struct {
		TableDescription struct {
			TableArn string `json:"TableArn"`
		} `json:"TableDescription"`
	}
	parseJSON(t, out, &desc)
	return desc.TableDescription.TableArn
}

func TestDynamoDBCLI_BackupLifecycle(t *testing.T) {
	table := "cli-ddb-bk"
	ddbCLICreateTableArn(t, table)

	out := runCLI(t, awsCLI("dynamodb", "create-backup",
		"--table-name", table, "--backup-name", "nightly", "--output", "json"))
	var cb struct {
		BackupDetails struct {
			BackupArn    string `json:"BackupArn"`
			BackupStatus string `json:"BackupStatus"`
		} `json:"BackupDetails"`
	}
	parseJSON(t, out, &cb)
	require.Contains(t, cb.BackupDetails.BackupArn, ":table/"+table+"/backup/")
	assert.Equal(t, "AVAILABLE", cb.BackupDetails.BackupStatus)
	arn := cb.BackupDetails.BackupArn

	dbOut := runCLI(t, awsCLI("dynamodb", "describe-backup", "--backup-arn", arn, "--output", "json"))
	var db struct {
		BackupDescription struct {
			SourceTableDetails struct {
				TableName string `json:"TableName"`
			} `json:"SourceTableDetails"`
		} `json:"BackupDescription"`
	}
	parseJSON(t, dbOut, &db)
	assert.Equal(t, table, db.BackupDescription.SourceTableDetails.TableName)

	lbOut := runCLI(t, awsCLI("dynamodb", "list-backups", "--table-name", table, "--output", "json"))
	var lb struct {
		BackupSummaries []struct {
			BackupArn string `json:"BackupArn"`
		} `json:"BackupSummaries"`
	}
	parseJSON(t, lbOut, &lb)
	require.NotEmpty(t, lb.BackupSummaries)

	restored := "cli-ddb-bk-restored"
	t.Cleanup(func() { _ = awsCLI("dynamodb", "delete-table", "--table-name", restored).Run() })
	rbOut := runCLI(t, awsCLI("dynamodb", "restore-table-from-backup",
		"--backup-arn", arn, "--target-table-name", restored, "--output", "json"))
	var rb struct {
		TableDescription struct {
			TableName string `json:"TableName"`
		} `json:"TableDescription"`
	}
	parseJSON(t, rbOut, &rb)
	assert.Equal(t, restored, rb.TableDescription.TableName)

	runCLI(t, awsCLI("dynamodb", "delete-backup", "--backup-arn", arn, "--output", "json"))
}

func TestDynamoDBCLI_RestoreTableToPointInTime(t *testing.T) {
	table := "cli-ddb-pitr"
	ddbCLICreateTableArn(t, table)
	runCLI(t, awsCLI("dynamodb", "update-continuous-backups",
		"--table-name", table,
		"--point-in-time-recovery-specification", "PointInTimeRecoveryEnabled=true",
		"--output", "json"))

	target := "cli-ddb-pitr-restored"
	t.Cleanup(func() { _ = awsCLI("dynamodb", "delete-table", "--table-name", target).Run() })
	out := runCLI(t, awsCLI("dynamodb", "restore-table-to-point-in-time",
		"--source-table-name", table, "--target-table-name", target, "--output", "json"))
	var r struct {
		TableDescription struct {
			TableName string `json:"TableName"`
		} `json:"TableDescription"`
	}
	parseJSON(t, out, &r)
	assert.Equal(t, target, r.TableDescription.TableName)
}

func TestDynamoDBCLI_GlobalTables(t *testing.T) {
	table := "cli-ddb-gt"
	ddbCLICreateTableArn(t, table)

	runCLI(t, awsCLI("dynamodb", "create-global-table",
		"--global-table-name", table,
		"--replication-group", "RegionName=us-east-1",
		"--output", "json"))

	dgOut := runCLI(t, awsCLI("dynamodb", "describe-global-table", "--global-table-name", table, "--output", "json"))
	var dg struct {
		GlobalTableDescription struct {
			GlobalTableName  string `json:"GlobalTableName"`
			ReplicationGroup []any  `json:"ReplicationGroup"`
		} `json:"GlobalTableDescription"`
	}
	parseJSON(t, dgOut, &dg)
	assert.Equal(t, table, dg.GlobalTableDescription.GlobalTableName)
	require.Len(t, dg.GlobalTableDescription.ReplicationGroup, 1)

	lgOut := runCLI(t, awsCLI("dynamodb", "list-global-tables", "--output", "json"))
	var lg struct {
		GlobalTables []struct {
			GlobalTableName string `json:"GlobalTableName"`
		} `json:"GlobalTables"`
	}
	parseJSON(t, lgOut, &lg)
	found := false
	for _, g := range lg.GlobalTables {
		if g.GlobalTableName == table {
			found = true
		}
	}
	assert.True(t, found)

	ugOut := runCLI(t, awsCLI("dynamodb", "update-global-table",
		"--global-table-name", table,
		"--replica-updates", "Create={RegionName=us-west-2}",
		"--output", "json"))
	var ug struct {
		GlobalTableDescription struct {
			ReplicationGroup []any `json:"ReplicationGroup"`
		} `json:"GlobalTableDescription"`
	}
	parseJSON(t, ugOut, &ug)
	assert.Len(t, ug.GlobalTableDescription.ReplicationGroup, 2)

	dgsOut := runCLI(t, awsCLI("dynamodb", "describe-global-table-settings", "--global-table-name", table, "--output", "json"))
	var dgs struct {
		GlobalTableName string `json:"GlobalTableName"`
		ReplicaSettings []any  `json:"ReplicaSettings"`
	}
	parseJSON(t, dgsOut, &dgs)
	assert.Equal(t, table, dgs.GlobalTableName)

	runCLI(t, awsCLI("dynamodb", "update-global-table-settings",
		"--global-table-name", table,
		"--global-table-provisioned-write-capacity-units", "10",
		"--output", "json"))

	runCLI(t, awsCLI("dynamodb", "update-table-replica-auto-scaling",
		"--table-name", table, "--output", "json"))

	dasOut := runCLI(t, awsCLI("dynamodb", "describe-table-replica-auto-scaling", "--table-name", table, "--output", "json"))
	var das struct {
		TableAutoScalingDescription struct {
			TableName string `json:"TableName"`
		} `json:"TableAutoScalingDescription"`
	}
	parseJSON(t, dasOut, &das)
	assert.Equal(t, table, das.TableAutoScalingDescription.TableName)
}

func TestDynamoDBCLI_ResourcePolicy(t *testing.T) {
	table := "cli-ddb-rp"
	arn := ddbCLICreateTableArn(t, table)
	policy := `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"AWS":"*"},"Action":"dynamodb:GetItem","Resource":"` + arn + `"}]}`

	runCLI(t, awsCLI("dynamodb", "put-resource-policy",
		"--resource-arn", arn, "--policy", policy, "--output", "json"))

	gpOut := runCLI(t, awsCLI("dynamodb", "get-resource-policy", "--resource-arn", arn, "--output", "json"))
	var gp struct {
		Policy string `json:"Policy"`
	}
	parseJSON(t, gpOut, &gp)
	assert.JSONEq(t, policy, gp.Policy)

	runCLI(t, awsCLI("dynamodb", "delete-resource-policy", "--resource-arn", arn, "--output", "json"))
	runCLIExpectError(t, awsCLI("dynamodb", "get-resource-policy", "--resource-arn", arn, "--output", "json"))
}

func TestDynamoDBCLI_KinesisStreaming(t *testing.T) {
	table := "cli-ddb-kinesis"
	ddbCLICreateTableArn(t, table)
	streamArn := "arn:aws:kinesis:us-east-1:000000000000:stream/cli-ddb-changes"

	runCLI(t, awsCLI("dynamodb", "enable-kinesis-streaming-destination",
		"--table-name", table, "--stream-arn", streamArn, "--output", "json"))

	dkOut := runCLI(t, awsCLI("dynamodb", "describe-kinesis-streaming-destination", "--table-name", table, "--output", "json"))
	var dk struct {
		KinesisDataStreamDestinations []struct {
			StreamArn string `json:"StreamArn"`
		} `json:"KinesisDataStreamDestinations"`
	}
	parseJSON(t, dkOut, &dk)
	require.Len(t, dk.KinesisDataStreamDestinations, 1)
	assert.Equal(t, streamArn, dk.KinesisDataStreamDestinations[0].StreamArn)

	runCLI(t, awsCLI("dynamodb", "update-kinesis-streaming-destination",
		"--table-name", table, "--stream-arn", streamArn,
		"--update-kinesis-streaming-configuration", "ApproximateCreationDateTimePrecision=MILLISECOND",
		"--output", "json"))

	runCLI(t, awsCLI("dynamodb", "disable-kinesis-streaming-destination",
		"--table-name", table, "--stream-arn", streamArn, "--output", "json"))
}

func TestDynamoDBCLI_ExportImport(t *testing.T) {
	table := "cli-ddb-export"
	arn := ddbCLICreateTableArn(t, table)

	exOut := runCLI(t, awsCLI("dynamodb", "export-table-to-point-in-time",
		"--table-arn", arn, "--s3-bucket", "cli-export-bucket", "--s3-prefix", "exports/", "--output", "json"))
	var ex struct {
		ExportDescription struct {
			ExportArn    string `json:"ExportArn"`
			ExportStatus string `json:"ExportStatus"`
		} `json:"ExportDescription"`
	}
	parseJSON(t, exOut, &ex)
	assert.Equal(t, "COMPLETED", ex.ExportDescription.ExportStatus)

	runCLI(t, awsCLI("dynamodb", "describe-export", "--export-arn", ex.ExportDescription.ExportArn, "--output", "json"))

	leOut := runCLI(t, awsCLI("dynamodb", "list-exports", "--table-arn", arn, "--output", "json"))
	var le struct {
		ExportSummaries []any `json:"ExportSummaries"`
	}
	parseJSON(t, leOut, &le)
	require.NotEmpty(t, le.ExportSummaries)

	importTable := "cli-ddb-imported"
	t.Cleanup(func() { _ = awsCLI("dynamodb", "delete-table", "--table-name", importTable).Run() })
	imOut := runCLI(t, awsCLI("dynamodb", "import-table",
		"--input-format", "DYNAMODB_JSON",
		"--s3-bucket-source", "S3Bucket=cli-import-bucket,S3KeyPrefix=imports/",
		"--table-creation-parameters",
		"TableName="+importTable+",AttributeDefinitions=[{AttributeName=pk,AttributeType=S}],KeySchema=[{AttributeName=pk,KeyType=HASH}],BillingMode=PAY_PER_REQUEST",
		"--output", "json"))
	var im struct {
		ImportTableDescription struct {
			ImportArn    string `json:"ImportArn"`
			ImportStatus string `json:"ImportStatus"`
			TableArn     string `json:"TableArn"`
		} `json:"ImportTableDescription"`
	}
	parseJSON(t, imOut, &im)
	assert.Equal(t, "COMPLETED", im.ImportTableDescription.ImportStatus)

	runCLI(t, awsCLI("dynamodb", "describe-import", "--import-arn", im.ImportTableDescription.ImportArn, "--output", "json"))

	liOut := runCLI(t, awsCLI("dynamodb", "list-imports", "--table-arn", im.ImportTableDescription.TableArn, "--output", "json"))
	var li struct {
		ImportSummaryList []any `json:"ImportSummaryList"`
	}
	parseJSON(t, liOut, &li)
	require.NotEmpty(t, li.ImportSummaryList)
}

func TestDynamoDBCLI_ContributorInsights(t *testing.T) {
	table := "cli-ddb-ci"
	ddbCLICreateTableArn(t, table)

	runCLI(t, awsCLI("dynamodb", "update-contributor-insights",
		"--table-name", table, "--contributor-insights-action", "ENABLE", "--output", "json"))

	dciOut := runCLI(t, awsCLI("dynamodb", "describe-contributor-insights", "--table-name", table, "--output", "json"))
	var dci struct {
		ContributorInsightsStatus string `json:"ContributorInsightsStatus"`
	}
	parseJSON(t, dciOut, &dci)
	assert.Equal(t, "ENABLED", dci.ContributorInsightsStatus)

	lciOut := runCLI(t, awsCLI("dynamodb", "list-contributor-insights", "--table-name", table, "--output", "json"))
	var lci struct {
		ContributorInsightsSummaries []struct {
			TableName                 string `json:"TableName"`
			ContributorInsightsStatus string `json:"ContributorInsightsStatus"`
		} `json:"ContributorInsightsSummaries"`
	}
	parseJSON(t, lciOut, &lci)
	found := false
	for _, s := range lci.ContributorInsightsSummaries {
		if s.TableName == table && s.ContributorInsightsStatus == "ENABLED" {
			found = true
		}
	}
	assert.True(t, found)
}

func TestDynamoDBCLI_DescribeEndpoints(t *testing.T) {
	out := runCLI(t, awsCLI("dynamodb", "describe-endpoints", "--output", "json"))
	var resp struct {
		Endpoints []struct {
			Address              string `json:"Address"`
			CachePeriodInMinutes int64  `json:"CachePeriodInMinutes"`
		} `json:"Endpoints"`
	}
	parseJSON(t, out, &resp)
	require.NotEmpty(t, resp.Endpoints)
	assert.NotEmpty(t, resp.Endpoints[0].Address)
	assert.Greater(t, resp.Endpoints[0].CachePeriodInMinutes, int64(0))
}
