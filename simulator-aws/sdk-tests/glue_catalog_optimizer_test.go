package aws_sdk_test

import (
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/glue"
	gluetypes "github.com/aws/aws-sdk-go-v2/service/glue/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestGlue_Catalog_SDK exercises the Glue multi-catalog CRUD: Create / Get /
// GetCatalogs / Update / Delete.
func TestGlue_Catalog_SDK(t *testing.T) {
	c := glueClient()
	const name = "glue-sdk-catalog"

	_, err := c.CreateCatalog(ctx, &glue.CreateCatalogInput{
		Name: aws.String(name),
		CatalogInput: &gluetypes.CatalogInput{
			Description: aws.String("sdk catalog"),
			Parameters:  map[string]string{"k": "v"},
		},
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = c.DeleteCatalog(ctx, &glue.DeleteCatalogInput{CatalogId: aws.String(name)})
	})

	get, err := c.GetCatalog(ctx, &glue.GetCatalogInput{CatalogId: aws.String(name)})
	require.NoError(t, err)
	require.NotNil(t, get.Catalog)
	assert.Equal(t, name, aws.ToString(get.Catalog.Name))
	assert.NotEmpty(t, aws.ToString(get.Catalog.ResourceArn))
	assert.Equal(t, "sdk catalog", aws.ToString(get.Catalog.Description))

	_, err = c.UpdateCatalog(ctx, &glue.UpdateCatalogInput{
		CatalogId: aws.String(name),
		CatalogInput: &gluetypes.CatalogInput{
			Description: aws.String("updated"),
		},
	})
	require.NoError(t, err)
	get2, err := c.GetCatalog(ctx, &glue.GetCatalogInput{CatalogId: aws.String(name)})
	require.NoError(t, err)
	assert.Equal(t, "updated", aws.ToString(get2.Catalog.Description))

	list, err := c.GetCatalogs(ctx, &glue.GetCatalogsInput{})
	require.NoError(t, err)
	found := false
	for _, cat := range list.CatalogList {
		if aws.ToString(cat.Name) == name {
			found = true
		}
	}
	assert.True(t, found)

	_, err = c.DeleteCatalog(ctx, &glue.DeleteCatalogInput{CatalogId: aws.String(name)})
	require.NoError(t, err)
}

// TestGlue_TableOptimizer_SDK exercises Create / Get / Update / BatchGet /
// ListTableOptimizerRuns / Delete on a table optimizer attached to a table.
func TestGlue_TableOptimizer_SDK(t *testing.T) {
	c := glueClient()
	const db = "glue-sdk-opt-db"
	const tbl = "glue-sdk-opt-tbl"

	_, err := c.CreateDatabase(ctx, &glue.CreateDatabaseInput{
		DatabaseInput: &gluetypes.DatabaseInput{Name: aws.String(db)},
	})
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = c.DeleteDatabase(ctx, &glue.DeleteDatabaseInput{Name: aws.String(db)}) })

	_, err = c.CreateTable(ctx, &glue.CreateTableInput{
		DatabaseName: aws.String(db),
		TableInput:   &gluetypes.TableInput{Name: aws.String(tbl)},
	})
	require.NoError(t, err)

	_, err = c.CreateTableOptimizer(ctx, &glue.CreateTableOptimizerInput{
		CatalogId:    aws.String("123456789012"),
		DatabaseName: aws.String(db),
		TableName:    aws.String(tbl),
		Type:         gluetypes.TableOptimizerTypeCompaction,
		TableOptimizerConfiguration: &gluetypes.TableOptimizerConfiguration{
			Enabled: aws.Bool(true),
			RoleArn: aws.String("arn:aws:iam::123456789012:role/glue-opt"),
		},
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = c.DeleteTableOptimizer(ctx, &glue.DeleteTableOptimizerInput{
			CatalogId:    aws.String("123456789012"),
			DatabaseName: aws.String(db),
			TableName:    aws.String(tbl),
			Type:         gluetypes.TableOptimizerTypeCompaction,
		})
	})

	get, err := c.GetTableOptimizer(ctx, &glue.GetTableOptimizerInput{
		CatalogId:    aws.String("123456789012"),
		DatabaseName: aws.String(db),
		TableName:    aws.String(tbl),
		Type:         gluetypes.TableOptimizerTypeCompaction,
	})
	require.NoError(t, err)
	require.NotNil(t, get.TableOptimizer)
	assert.Equal(t, gluetypes.TableOptimizerTypeCompaction, get.TableOptimizer.Type)
	require.NotNil(t, get.TableOptimizer.Configuration)
	assert.Equal(t, true, aws.ToBool(get.TableOptimizer.Configuration.Enabled))

	_, err = c.UpdateTableOptimizer(ctx, &glue.UpdateTableOptimizerInput{
		CatalogId:    aws.String("123456789012"),
		DatabaseName: aws.String(db),
		TableName:    aws.String(tbl),
		Type:         gluetypes.TableOptimizerTypeCompaction,
		TableOptimizerConfiguration: &gluetypes.TableOptimizerConfiguration{
			Enabled: aws.Bool(false),
			RoleArn: aws.String("arn:aws:iam::123456789012:role/glue-opt"),
		},
	})
	require.NoError(t, err)
	get2, err := c.GetTableOptimizer(ctx, &glue.GetTableOptimizerInput{
		CatalogId:    aws.String("123456789012"),
		DatabaseName: aws.String(db),
		TableName:    aws.String(tbl),
		Type:         gluetypes.TableOptimizerTypeCompaction,
	})
	require.NoError(t, err)
	assert.Equal(t, false, aws.ToBool(get2.TableOptimizer.Configuration.Enabled))

	runs, err := c.ListTableOptimizerRuns(ctx, &glue.ListTableOptimizerRunsInput{
		CatalogId:    aws.String("123456789012"),
		DatabaseName: aws.String(db),
		TableName:    aws.String(tbl),
		Type:         gluetypes.TableOptimizerTypeCompaction,
	})
	require.NoError(t, err)
	assert.Equal(t, tbl, aws.ToString(runs.TableName))

	batch, err := c.BatchGetTableOptimizer(ctx, &glue.BatchGetTableOptimizerInput{
		Entries: []gluetypes.BatchGetTableOptimizerEntry{
			{
				CatalogId:    aws.String("123456789012"),
				DatabaseName: aws.String(db),
				TableName:    aws.String(tbl),
				Type:         gluetypes.TableOptimizerTypeCompaction,
			},
			{
				CatalogId:    aws.String("123456789012"),
				DatabaseName: aws.String(db),
				TableName:    aws.String("missing-table"),
				Type:         gluetypes.TableOptimizerTypeCompaction,
			},
		},
	})
	require.NoError(t, err)
	assert.Len(t, batch.TableOptimizers, 1)
	assert.Len(t, batch.Failures, 1)
}

// TestGlue_BatchGet_SDK exercises BatchGetCrawlers / BatchGetJobs /
// BatchGetTriggers / BatchGetWorkflows / BatchGetCustomEntityTypes /
// BatchDeleteConnection / BatchUpdatePartition against pre-created resources.
func TestGlue_BatchGet_SDK(t *testing.T) {
	c := glueClient()

	// Crawler.
	const crawler = "glue-sdk-batch-crawler"
	_, err := c.CreateCrawler(ctx, &glue.CreateCrawlerInput{
		Name:    aws.String(crawler),
		Role:    aws.String("arn:aws:iam::123456789012:role/glue"),
		Targets: &gluetypes.CrawlerTargets{},
	})
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = c.DeleteCrawler(ctx, &glue.DeleteCrawlerInput{Name: aws.String(crawler)}) })

	gc, err := c.BatchGetCrawlers(ctx, &glue.BatchGetCrawlersInput{
		CrawlerNames: []string{crawler, "no-such-crawler"},
	})
	require.NoError(t, err)
	assert.Len(t, gc.Crawlers, 1)
	assert.Equal(t, []string{"no-such-crawler"}, gc.CrawlersNotFound)

	// Job.
	const job = "glue-sdk-batch-job"
	_, err = c.CreateJob(ctx, &glue.CreateJobInput{
		Name:    aws.String(job),
		Role:    aws.String("arn:aws:iam::123456789012:role/glue"),
		Command: &gluetypes.JobCommand{Name: aws.String("glueetl")},
	})
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = c.DeleteJob(ctx, &glue.DeleteJobInput{JobName: aws.String(job)}) })

	gj, err := c.BatchGetJobs(ctx, &glue.BatchGetJobsInput{JobNames: []string{job, "no-such-job"}})
	require.NoError(t, err)
	assert.Len(t, gj.Jobs, 1)
	assert.Equal(t, []string{"no-such-job"}, gj.JobsNotFound)

	// Trigger.
	const trigger = "glue-sdk-batch-trigger"
	_, err = c.CreateTrigger(ctx, &glue.CreateTriggerInput{
		Name: aws.String(trigger),
		Type: gluetypes.TriggerTypeOnDemand,
		Actions: []gluetypes.Action{
			{JobName: aws.String(job)},
		},
	})
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = c.DeleteTrigger(ctx, &glue.DeleteTriggerInput{Name: aws.String(trigger)}) })

	gt, err := c.BatchGetTriggers(ctx, &glue.BatchGetTriggersInput{
		TriggerNames: []string{trigger, "no-such-trigger"},
	})
	require.NoError(t, err)
	assert.Len(t, gt.Triggers, 1)
	assert.Equal(t, []string{"no-such-trigger"}, gt.TriggersNotFound)

	// Workflow.
	const workflow = "glue-sdk-batch-workflow"
	_, err = c.CreateWorkflow(ctx, &glue.CreateWorkflowInput{Name: aws.String(workflow)})
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = c.DeleteWorkflow(ctx, &glue.DeleteWorkflowInput{Name: aws.String(workflow)}) })

	gw, err := c.BatchGetWorkflows(ctx, &glue.BatchGetWorkflowsInput{
		Names: []string{workflow, "no-such-workflow"},
	})
	require.NoError(t, err)
	assert.Len(t, gw.Workflows, 1)
	assert.Equal(t, []string{"no-such-workflow"}, gw.MissingWorkflows)

	// Custom entity types (none created — all reported missing).
	gce, err := c.BatchGetCustomEntityTypes(ctx, &glue.BatchGetCustomEntityTypesInput{
		Names: []string{"no-such-entity"},
	})
	require.NoError(t, err)
	assert.Equal(t, []string{"no-such-entity"}, gce.CustomEntityTypesNotFound)

	// BatchDeleteConnection.
	const conn = "glue-sdk-batch-conn"
	_, err = c.CreateConnection(ctx, &glue.CreateConnectionInput{
		ConnectionInput: &gluetypes.ConnectionInput{
			Name:                 aws.String(conn),
			ConnectionType:       gluetypes.ConnectionTypeJdbc,
			ConnectionProperties: map[string]string{"JDBC_CONNECTION_URL": "jdbc:mysql://h/db"},
		},
	})
	require.NoError(t, err)
	bdc, err := c.BatchDeleteConnection(ctx, &glue.BatchDeleteConnectionInput{
		ConnectionNameList: []string{conn, "no-such-conn"},
	})
	require.NoError(t, err)
	assert.Equal(t, []string{conn}, bdc.Succeeded)
	assert.Contains(t, bdc.Errors, "no-such-conn")

	// BatchUpdatePartition on a partitioned table.
	const db = "glue-sdk-batch-db"
	const tbl = "glue-sdk-batch-tbl"
	_, err = c.CreateDatabase(ctx, &glue.CreateDatabaseInput{
		DatabaseInput: &gluetypes.DatabaseInput{Name: aws.String(db)},
	})
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = c.DeleteDatabase(ctx, &glue.DeleteDatabaseInput{Name: aws.String(db)}) })
	_, err = c.CreateTable(ctx, &glue.CreateTableInput{
		DatabaseName: aws.String(db),
		TableInput: &gluetypes.TableInput{
			Name:          aws.String(tbl),
			PartitionKeys: []gluetypes.Column{{Name: aws.String("dt"), Type: aws.String("string")}},
		},
	})
	require.NoError(t, err)
	_, err = c.CreatePartition(ctx, &glue.CreatePartitionInput{
		DatabaseName:   aws.String(db),
		TableName:      aws.String(tbl),
		PartitionInput: &gluetypes.PartitionInput{Values: []string{"2024"}},
	})
	require.NoError(t, err)

	bup, err := c.BatchUpdatePartition(ctx, &glue.BatchUpdatePartitionInput{
		DatabaseName: aws.String(db),
		TableName:    aws.String(tbl),
		Entries: []gluetypes.BatchUpdatePartitionRequestEntry{
			{
				PartitionValueList: []string{"2024"},
				PartitionInput: &gluetypes.PartitionInput{
					Values:     []string{"2024"},
					Parameters: map[string]string{"updated": "yes"},
				},
			},
			{
				PartitionValueList: []string{"9999"},
				PartitionInput:     &gluetypes.PartitionInput{Values: []string{"9999"}},
			},
		},
	})
	require.NoError(t, err)
	assert.Len(t, bup.Errors, 1)
}

// TestGlue_Integration_SDK exercises the zero-ETL Integration family plus
// resource/table properties round-trips.
func TestGlue_Integration_SDK(t *testing.T) {
	c := glueClient()
	const name = "glue-sdk-integration"
	const src = "arn:aws:rds:us-east-1:123456789012:db:source"
	const tgt = "arn:aws:glue:us-east-1:123456789012:catalog"

	created, err := c.CreateIntegration(ctx, &glue.CreateIntegrationInput{
		IntegrationName: aws.String(name),
		SourceArn:       aws.String(src),
		TargetArn:       aws.String(tgt),
		Description:     aws.String("zero-etl"),
	})
	require.NoError(t, err)
	assert.Equal(t, name, aws.ToString(created.IntegrationName))
	assert.NotEmpty(t, aws.ToString(created.IntegrationArn))
	integArn := aws.ToString(created.IntegrationArn)
	t.Cleanup(func() {
		_, _ = c.DeleteIntegration(ctx, &glue.DeleteIntegrationInput{
			IntegrationIdentifier: aws.String(integArn),
		})
	})

	desc, err := c.DescribeIntegrations(ctx, &glue.DescribeIntegrationsInput{
		IntegrationIdentifier: aws.String(integArn),
	})
	require.NoError(t, err)
	require.Len(t, desc.Integrations, 1)
	assert.Equal(t, name, aws.ToString(desc.Integrations[0].IntegrationName))

	mod, err := c.ModifyIntegration(ctx, &glue.ModifyIntegrationInput{
		IntegrationIdentifier: aws.String(integArn),
		Description:           aws.String("modified"),
	})
	require.NoError(t, err)
	assert.Equal(t, "modified", aws.ToString(mod.Description))

	inbound, err := c.DescribeInboundIntegrations(ctx, &glue.DescribeInboundIntegrationsInput{
		TargetArn: aws.String(tgt),
	})
	require.NoError(t, err)
	foundInbound := false
	for _, ii := range inbound.InboundIntegrations {
		if aws.ToString(ii.IntegrationArn) == integArn {
			foundInbound = true
		}
	}
	assert.True(t, foundInbound)

	// Resource properties.
	const resArn = "arn:aws:glue:us-east-1:123456789012:connection/src-conn"
	crp, err := c.CreateIntegrationResourceProperty(ctx, &glue.CreateIntegrationResourcePropertyInput{
		ResourceArn: aws.String(resArn),
		SourceProcessingProperties: &gluetypes.SourceProcessingProperties{
			RoleArn: aws.String("arn:aws:iam::123456789012:role/src"),
		},
	})
	require.NoError(t, err)
	assert.NotEmpty(t, aws.ToString(crp.ResourcePropertyArn))
	t.Cleanup(func() {
		_, _ = c.DeleteIntegrationResourceProperty(ctx, &glue.DeleteIntegrationResourcePropertyInput{
			ResourceArn: aws.String(resArn),
		})
	})

	grp, err := c.GetIntegrationResourceProperty(ctx, &glue.GetIntegrationResourcePropertyInput{
		ResourceArn: aws.String(resArn),
	})
	require.NoError(t, err)
	require.NotNil(t, grp.SourceProcessingProperties)
	assert.Equal(t, "arn:aws:iam::123456789012:role/src", aws.ToString(grp.SourceProcessingProperties.RoleArn))

	_, err = c.UpdateIntegrationResourceProperty(ctx, &glue.UpdateIntegrationResourcePropertyInput{
		ResourceArn: aws.String(resArn),
		TargetProcessingProperties: &gluetypes.TargetProcessingProperties{
			RoleArn: aws.String("arn:aws:iam::123456789012:role/tgt"),
		},
	})
	require.NoError(t, err)

	lrp, err := c.ListIntegrationResourceProperties(ctx, &glue.ListIntegrationResourcePropertiesInput{})
	require.NoError(t, err)
	foundProp := false
	for _, p := range lrp.IntegrationResourcePropertyList {
		if aws.ToString(p.ResourceArn) == resArn {
			foundProp = true
		}
	}
	assert.True(t, foundProp)

	// Table properties.
	const tableResArn = "arn:aws:glue:us-east-1:123456789012:table/target-tbl"
	_, err = c.CreateIntegrationTableProperties(ctx, &glue.CreateIntegrationTablePropertiesInput{
		ResourceArn: aws.String(tableResArn),
		TableName:   aws.String("orders"),
		SourceTableConfig: &gluetypes.SourceTableConfig{
			PrimaryKey: []string{"id"},
		},
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = c.DeleteIntegrationTableProperties(ctx, &glue.DeleteIntegrationTablePropertiesInput{
			ResourceArn: aws.String(tableResArn),
			TableName:   aws.String("orders"),
		})
	})

	gtp, err := c.GetIntegrationTableProperties(ctx, &glue.GetIntegrationTablePropertiesInput{
		ResourceArn: aws.String(tableResArn),
		TableName:   aws.String("orders"),
	})
	require.NoError(t, err)
	assert.Equal(t, "orders", aws.ToString(gtp.TableName))
	require.NotNil(t, gtp.SourceTableConfig)
	assert.Equal(t, []string{"id"}, gtp.SourceTableConfig.PrimaryKey)

	_, err = c.UpdateIntegrationTableProperties(ctx, &glue.UpdateIntegrationTablePropertiesInput{
		ResourceArn: aws.String(tableResArn),
		TableName:   aws.String("orders"),
		TargetTableConfig: &gluetypes.TargetTableConfig{
			TargetTableName: aws.String("orders_replica"),
		},
	})
	require.NoError(t, err)

	_, err = c.DeleteIntegration(ctx, &glue.DeleteIntegrationInput{
		IntegrationIdentifier: aws.String(integArn),
	})
	require.NoError(t, err)
}
