package aws_cli_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestGlue_CatalogCRUD_CLI exercises create/get/get-catalogs/update/delete-catalog.
func TestGlue_CatalogCRUD_CLI(t *testing.T) {
	const name = "glue-cli-catalog"
	runCLI(t, awsCLI("glue", "create-catalog",
		"--name", name,
		"--catalog-input", `{"Description":"cli catalog","Parameters":{"k":"v"}}`,
	))
	t.Cleanup(func() {
		runCLIIgnore(awsCLI("glue", "delete-catalog", "--catalog-id", name))
	})

	out := runCLI(t, awsCLI("glue", "get-catalog", "--catalog-id", name))
	var get struct {
		Catalog struct {
			Name        string `json:"Name"`
			ResourceArn string `json:"ResourceArn"`
			Description string `json:"Description"`
		} `json:"Catalog"`
	}
	parseJSON(t, out, &get)
	assert.Equal(t, name, get.Catalog.Name)
	assert.NotEmpty(t, get.Catalog.ResourceArn)
	assert.Equal(t, "cli catalog", get.Catalog.Description)

	runCLI(t, awsCLI("glue", "update-catalog",
		"--catalog-id", name,
		"--catalog-input", `{"Description":"updated"}`,
	))
	out = runCLI(t, awsCLI("glue", "get-catalog", "--catalog-id", name))
	parseJSON(t, out, &get)
	assert.Equal(t, "updated", get.Catalog.Description)

	out = runCLI(t, awsCLI("glue", "get-catalogs"))
	var list struct {
		CatalogList []struct {
			Name string `json:"Name"`
		} `json:"CatalogList"`
	}
	parseJSON(t, out, &list)
	found := false
	for _, c := range list.CatalogList {
		if c.Name == name {
			found = true
		}
	}
	assert.True(t, found)
}

// TestGlue_TableOptimizerCRUD_CLI exercises create/get/update/batch-get/
// list-table-optimizer-runs/delete on a table optimizer.
func TestGlue_TableOptimizerCRUD_CLI(t *testing.T) {
	const db = "glue-cli-opt-db"
	const tbl = "glue-cli-opt-tbl"
	const cat = "123456789012"

	runCLI(t, awsCLI("glue", "create-database", "--database-input", `{"Name":"`+db+`"}`))
	t.Cleanup(func() { runCLIIgnore(awsCLI("glue", "delete-database", "--name", db)) })
	runCLI(t, awsCLI("glue", "create-table", "--database-name", db, "--table-input", `{"Name":"`+tbl+`"}`))

	runCLI(t, awsCLI("glue", "create-table-optimizer",
		"--catalog-id", cat,
		"--database-name", db,
		"--table-name", tbl,
		"--type", "compaction",
		"--table-optimizer-configuration", `{"enabled":true,"roleArn":"arn:aws:iam::123456789012:role/opt"}`,
	))
	t.Cleanup(func() {
		runCLIIgnore(awsCLI("glue", "delete-table-optimizer",
			"--catalog-id", cat, "--database-name", db, "--table-name", tbl, "--type", "compaction"))
	})

	out := runCLI(t, awsCLI("glue", "get-table-optimizer",
		"--catalog-id", cat, "--database-name", db, "--table-name", tbl, "--type", "compaction"))
	var get struct {
		TableName      string `json:"TableName"`
		TableOptimizer struct {
			Type          string `json:"type"`
			Configuration struct {
				Enabled bool `json:"enabled"`
			} `json:"configuration"`
		} `json:"TableOptimizer"`
	}
	parseJSON(t, out, &get)
	assert.Equal(t, tbl, get.TableName)
	assert.Equal(t, "compaction", get.TableOptimizer.Type)
	assert.True(t, get.TableOptimizer.Configuration.Enabled)

	runCLI(t, awsCLI("glue", "update-table-optimizer",
		"--catalog-id", cat, "--database-name", db, "--table-name", tbl, "--type", "compaction",
		"--table-optimizer-configuration", `{"enabled":false,"roleArn":"arn:aws:iam::123456789012:role/opt"}`,
	))
	out = runCLI(t, awsCLI("glue", "get-table-optimizer",
		"--catalog-id", cat, "--database-name", db, "--table-name", tbl, "--type", "compaction"))
	parseJSON(t, out, &get)
	assert.False(t, get.TableOptimizer.Configuration.Enabled)

	out = runCLI(t, awsCLI("glue", "list-table-optimizer-runs",
		"--catalog-id", cat, "--database-name", db, "--table-name", tbl, "--type", "compaction"))
	var runs struct {
		TableName string `json:"TableName"`
	}
	parseJSON(t, out, &runs)
	assert.Equal(t, tbl, runs.TableName)

	out = runCLI(t, awsCLI("glue", "batch-get-table-optimizer",
		"--entries", `[{"catalogId":"`+cat+`","databaseName":"`+db+`","tableName":"`+tbl+`","type":"compaction"},{"catalogId":"`+cat+`","databaseName":"`+db+`","tableName":"missing","type":"compaction"}]`))
	var batch struct {
		TableOptimizers []map[string]any `json:"TableOptimizers"`
		Failures        []map[string]any `json:"Failures"`
	}
	parseJSON(t, out, &batch)
	assert.Len(t, batch.TableOptimizers, 1)
	assert.Len(t, batch.Failures, 1)
}

// TestGlue_BatchGetFamilies_CLI exercises batch-get-crawlers/jobs/triggers/
// workflows/custom-entity-types, batch-delete-connection and batch-update-partition.
func TestGlue_BatchGetFamilies_CLI(t *testing.T) {
	const crawler = "glue-cli-batch-crawler"
	runCLI(t, awsCLI("glue", "create-crawler",
		"--name", crawler, "--role", "arn:aws:iam::123456789012:role/glue", "--targets", `{}`))
	t.Cleanup(func() { runCLIIgnore(awsCLI("glue", "delete-crawler", "--name", crawler)) })

	out := runCLI(t, awsCLI("glue", "batch-get-crawlers", "--crawler-names", crawler, "no-such-crawler"))
	var gc struct {
		Crawlers         []map[string]any `json:"Crawlers"`
		CrawlersNotFound []string         `json:"CrawlersNotFound"`
	}
	parseJSON(t, out, &gc)
	assert.Len(t, gc.Crawlers, 1)
	assert.Equal(t, []string{"no-such-crawler"}, gc.CrawlersNotFound)

	const job = "glue-cli-batch-job"
	runCLI(t, awsCLI("glue", "create-job",
		"--name", job, "--role", "arn:aws:iam::123456789012:role/glue", "--command", `{"Name":"glueetl"}`))
	t.Cleanup(func() { runCLIIgnore(awsCLI("glue", "delete-job", "--job-name", job)) })

	out = runCLI(t, awsCLI("glue", "batch-get-jobs", "--job-names", job, "no-such-job"))
	var gj struct {
		Jobs         []map[string]any `json:"Jobs"`
		JobsNotFound []string         `json:"JobsNotFound"`
	}
	parseJSON(t, out, &gj)
	assert.Len(t, gj.Jobs, 1)
	assert.Equal(t, []string{"no-such-job"}, gj.JobsNotFound)

	const trigger = "glue-cli-batch-trigger"
	runCLI(t, awsCLI("glue", "create-trigger",
		"--name", trigger, "--type", "ON_DEMAND", "--actions", `[{"JobName":"`+job+`"}]`))
	t.Cleanup(func() { runCLIIgnore(awsCLI("glue", "delete-trigger", "--name", trigger)) })

	out = runCLI(t, awsCLI("glue", "batch-get-triggers", "--trigger-names", trigger, "no-such-trigger"))
	var gt struct {
		Triggers         []map[string]any `json:"Triggers"`
		TriggersNotFound []string         `json:"TriggersNotFound"`
	}
	parseJSON(t, out, &gt)
	assert.Len(t, gt.Triggers, 1)
	assert.Equal(t, []string{"no-such-trigger"}, gt.TriggersNotFound)

	const workflow = "glue-cli-batch-workflow"
	runCLI(t, awsCLI("glue", "create-workflow", "--name", workflow))
	t.Cleanup(func() { runCLIIgnore(awsCLI("glue", "delete-workflow", "--name", workflow)) })

	out = runCLI(t, awsCLI("glue", "batch-get-workflows", "--names", workflow, "no-such-workflow"))
	var gw struct {
		Workflows        []map[string]any `json:"Workflows"`
		MissingWorkflows []string         `json:"MissingWorkflows"`
	}
	parseJSON(t, out, &gw)
	assert.Len(t, gw.Workflows, 1)
	assert.Equal(t, []string{"no-such-workflow"}, gw.MissingWorkflows)

	out = runCLI(t, awsCLI("glue", "batch-get-custom-entity-types", "--names", "no-such-entity"))
	var gce struct {
		CustomEntityTypesNotFound []string `json:"CustomEntityTypesNotFound"`
	}
	parseJSON(t, out, &gce)
	assert.Equal(t, []string{"no-such-entity"}, gce.CustomEntityTypesNotFound)

	const conn = "glue-cli-batch-conn"
	runCLI(t, awsCLI("glue", "create-connection",
		"--connection-input", `{"Name":"`+conn+`","ConnectionType":"JDBC","ConnectionProperties":{"JDBC_CONNECTION_URL":"jdbc:mysql://h/db"}}`))
	out = runCLI(t, awsCLI("glue", "batch-delete-connection", "--connection-name-list", conn, "no-such-conn"))
	var bdc struct {
		Succeeded []string                  `json:"Succeeded"`
		Errors    map[string]map[string]any `json:"Errors"`
	}
	parseJSON(t, out, &bdc)
	assert.Equal(t, []string{conn}, bdc.Succeeded)
	assert.Contains(t, bdc.Errors, "no-such-conn")

	const db = "glue-cli-batch-db"
	const tbl = "glue-cli-batch-tbl"
	runCLI(t, awsCLI("glue", "create-database", "--database-input", `{"Name":"`+db+`"}`))
	t.Cleanup(func() { runCLIIgnore(awsCLI("glue", "delete-database", "--name", db)) })
	runCLI(t, awsCLI("glue", "create-table", "--database-name", db,
		"--table-input", `{"Name":"`+tbl+`","PartitionKeys":[{"Name":"dt","Type":"string"}]}`))
	runCLI(t, awsCLI("glue", "create-partition", "--database-name", db, "--table-name", tbl,
		"--partition-input", `{"Values":["2024"]}`))

	out = runCLI(t, awsCLI("glue", "batch-update-partition", "--database-name", db, "--table-name", tbl,
		"--entries", `[{"PartitionValueList":["2024"],"PartitionInput":{"Values":["2024"],"Parameters":{"u":"y"}}},{"PartitionValueList":["9999"],"PartitionInput":{"Values":["9999"]}}]`))
	var bup struct {
		Errors []map[string]any `json:"Errors"`
	}
	parseJSON(t, out, &bup)
	assert.Len(t, bup.Errors, 1)
}

// TestGlue_IntegrationCRUD_CLI exercises the zero-ETL integration family plus
// resource/table properties.
func TestGlue_IntegrationCRUD_CLI(t *testing.T) {
	const name = "glue-cli-integration"
	const src = "arn:aws:rds:us-east-1:123456789012:db:source"
	const tgt = "arn:aws:glue:us-east-1:123456789012:catalog"

	out := runCLI(t, awsCLI("glue", "create-integration",
		"--integration-name", name, "--source-arn", src, "--target-arn", tgt,
		"--description", "zero-etl"))
	var created struct {
		IntegrationName string `json:"IntegrationName"`
		IntegrationArn  string `json:"IntegrationArn"`
	}
	parseJSON(t, out, &created)
	assert.Equal(t, name, created.IntegrationName)
	require.NotEmpty(t, created.IntegrationArn)
	integArn := created.IntegrationArn
	t.Cleanup(func() {
		runCLIIgnore(awsCLI("glue", "delete-integration", "--integration-identifier", integArn))
	})

	out = runCLI(t, awsCLI("glue", "describe-integrations", "--integration-identifier", integArn))
	var desc struct {
		Integrations []struct {
			IntegrationName string `json:"IntegrationName"`
		} `json:"Integrations"`
	}
	parseJSON(t, out, &desc)
	require.Len(t, desc.Integrations, 1)
	assert.Equal(t, name, desc.Integrations[0].IntegrationName)

	out = runCLI(t, awsCLI("glue", "modify-integration",
		"--integration-identifier", integArn, "--description", "modified"))
	var mod struct {
		Description string `json:"Description"`
	}
	parseJSON(t, out, &mod)
	assert.Equal(t, "modified", mod.Description)

	out = runCLI(t, awsCLI("glue", "describe-inbound-integrations", "--target-arn", tgt))
	var inbound struct {
		InboundIntegrations []struct {
			IntegrationArn string `json:"IntegrationArn"`
		} `json:"InboundIntegrations"`
	}
	parseJSON(t, out, &inbound)
	foundInbound := false
	for _, ii := range inbound.InboundIntegrations {
		if ii.IntegrationArn == integArn {
			foundInbound = true
		}
	}
	assert.True(t, foundInbound)

	const resArn = "arn:aws:glue:us-east-1:123456789012:connection/src-conn"
	out = runCLI(t, awsCLI("glue", "create-integration-resource-property",
		"--resource-arn", resArn,
		"--source-processing-properties", `{"RoleArn":"arn:aws:iam::123456789012:role/src"}`))
	var crp struct {
		ResourcePropertyArn string `json:"ResourcePropertyArn"`
	}
	parseJSON(t, out, &crp)
	assert.NotEmpty(t, crp.ResourcePropertyArn)
	t.Cleanup(func() {
		runCLIIgnore(awsCLI("glue", "delete-integration-resource-property", "--resource-arn", resArn))
	})

	out = runCLI(t, awsCLI("glue", "get-integration-resource-property", "--resource-arn", resArn))
	var grp struct {
		SourceProcessingProperties struct {
			RoleArn string `json:"RoleArn"`
		} `json:"SourceProcessingProperties"`
	}
	parseJSON(t, out, &grp)
	assert.Equal(t, "arn:aws:iam::123456789012:role/src", grp.SourceProcessingProperties.RoleArn)

	runCLI(t, awsCLI("glue", "update-integration-resource-property",
		"--resource-arn", resArn,
		"--target-processing-properties", `{"RoleArn":"arn:aws:iam::123456789012:role/tgt"}`))

	out = runCLI(t, awsCLI("glue", "list-integration-resource-properties"))
	var lrp struct {
		IntegrationResourcePropertyList []struct {
			ResourceArn string `json:"ResourceArn"`
		} `json:"IntegrationResourcePropertyList"`
	}
	parseJSON(t, out, &lrp)
	foundProp := false
	for _, p := range lrp.IntegrationResourcePropertyList {
		if p.ResourceArn == resArn {
			foundProp = true
		}
	}
	assert.True(t, foundProp)

	const tableResArn = "arn:aws:glue:us-east-1:123456789012:table/target-tbl"
	runCLI(t, awsCLI("glue", "create-integration-table-properties",
		"--resource-arn", tableResArn, "--table-name", "orders",
		"--source-table-config", `{"PrimaryKey":["id"]}`))
	t.Cleanup(func() {
		runCLIIgnore(awsCLI("glue", "delete-integration-table-properties",
			"--resource-arn", tableResArn, "--table-name", "orders"))
	})

	out = runCLI(t, awsCLI("glue", "get-integration-table-properties",
		"--resource-arn", tableResArn, "--table-name", "orders"))
	var gtp struct {
		TableName         string `json:"TableName"`
		SourceTableConfig struct {
			PrimaryKey []string `json:"PrimaryKey"`
		} `json:"SourceTableConfig"`
	}
	parseJSON(t, out, &gtp)
	assert.Equal(t, "orders", gtp.TableName)
	assert.Equal(t, []string{"id"}, gtp.SourceTableConfig.PrimaryKey)

	runCLI(t, awsCLI("glue", "update-integration-table-properties",
		"--resource-arn", tableResArn, "--table-name", "orders",
		"--target-table-config", `{"TargetTableName":"orders_replica"}`))
}
