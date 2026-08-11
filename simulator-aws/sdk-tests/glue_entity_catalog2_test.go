package aws_sdk_test

import (
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/glue"
	gluetypes "github.com/aws/aws-sdk-go-v2/service/glue/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestGlue_CustomEntityType_SDK round-trips a custom entity type through
// Create/Get/List/Delete.
func TestGlue_CustomEntityType_SDK(t *testing.T) {
	c := glueClient()
	name := "glue-sdk-cet"

	_, err := c.CreateCustomEntityType(ctx, &glue.CreateCustomEntityTypeInput{
		Name:         aws.String(name),
		RegexString:  aws.String(`\d{3}-\d{2}-\d{4}`),
		ContextWords: []string{"ssn", "social"},
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = c.DeleteCustomEntityType(ctx, &glue.DeleteCustomEntityTypeInput{Name: aws.String(name)})
	})

	get, err := c.GetCustomEntityType(ctx, &glue.GetCustomEntityTypeInput{Name: aws.String(name)})
	require.NoError(t, err)
	assert.Equal(t, name, aws.ToString(get.Name))
	assert.Equal(t, `\d{3}-\d{2}-\d{4}`, aws.ToString(get.RegexString))
	assert.ElementsMatch(t, []string{"ssn", "social"}, get.ContextWords)

	list, err := c.ListCustomEntityTypes(ctx, &glue.ListCustomEntityTypesInput{})
	require.NoError(t, err)
	found := false
	for _, cet := range list.CustomEntityTypes {
		if aws.ToString(cet.Name) == name {
			found = true
		}
	}
	assert.True(t, found)

	del, err := c.DeleteCustomEntityType(ctx, &glue.DeleteCustomEntityTypeInput{Name: aws.String(name)})
	require.NoError(t, err)
	assert.Equal(t, name, aws.ToString(del.Name))

	_, err = c.GetCustomEntityType(ctx, &glue.GetCustomEntityTypeInput{Name: aws.String(name)})
	assert.Error(t, err)
}

// TestGlue_UsageProfile_SDK round-trips a usage profile through
// Create/Get/List/Update/Delete.
func TestGlue_UsageProfile_SDK(t *testing.T) {
	c := glueClient()
	name := "glue-sdk-profile"

	_, err := c.CreateUsageProfile(ctx, &glue.CreateUsageProfileInput{
		Name:        aws.String(name),
		Description: aws.String("initial"),
		Configuration: &gluetypes.ProfileConfiguration{
			JobConfiguration: map[string]gluetypes.ConfigurationObject{
				"--enable-metrics": {DefaultValue: aws.String("true")},
			},
		},
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = c.DeleteUsageProfile(ctx, &glue.DeleteUsageProfileInput{Name: aws.String(name)})
	})

	get, err := c.GetUsageProfile(ctx, &glue.GetUsageProfileInput{Name: aws.String(name)})
	require.NoError(t, err)
	assert.Equal(t, name, aws.ToString(get.Name))
	assert.Equal(t, "initial", aws.ToString(get.Description))
	require.NotNil(t, get.Configuration)
	require.Contains(t, get.Configuration.JobConfiguration, "--enable-metrics")
	assert.Equal(t, "true", aws.ToString(get.Configuration.JobConfiguration["--enable-metrics"].DefaultValue))

	upd, err := c.UpdateUsageProfile(ctx, &glue.UpdateUsageProfileInput{
		Name:        aws.String(name),
		Description: aws.String("updated"),
		Configuration: &gluetypes.ProfileConfiguration{
			SessionConfiguration: map[string]gluetypes.ConfigurationObject{
				"--worker": {DefaultValue: aws.String("G.1X")},
			},
		},
	})
	require.NoError(t, err)
	assert.Equal(t, name, aws.ToString(upd.Name))

	get2, err := c.GetUsageProfile(ctx, &glue.GetUsageProfileInput{Name: aws.String(name)})
	require.NoError(t, err)
	assert.Equal(t, "updated", aws.ToString(get2.Description))
	require.NotNil(t, get2.Configuration)
	assert.Contains(t, get2.Configuration.SessionConfiguration, "--worker")

	list, err := c.ListUsageProfiles(ctx, &glue.ListUsageProfilesInput{})
	require.NoError(t, err)
	found := false
	for _, p := range list.Profiles {
		if aws.ToString(p.Name) == name {
			found = true
		}
	}
	assert.True(t, found)

	_, err = c.DeleteUsageProfile(ctx, &glue.DeleteUsageProfileInput{Name: aws.String(name)})
	require.NoError(t, err)
}

// TestGlue_SchemaMetadata_SDK exercises ListSchemas, UpdateSchema,
// UpdateRegistry, CheckSchemaVersionValidity, schema-version metadata
// (Put/Query/Remove), and GetSchemaVersionsDiff against the schema/registry
// stores.
func TestGlue_SchemaMetadata_SDK(t *testing.T) {
	c := glueClient()
	reg := "glue-sdk-meta-reg"
	sch := "glue-sdk-meta-schema"

	_, err := c.CreateRegistry(ctx, &glue.CreateRegistryInput{RegistryName: aws.String(reg)})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = c.DeleteSchema(ctx, &glue.DeleteSchemaInput{
			SchemaId: &gluetypes.SchemaId{RegistryName: aws.String(reg), SchemaName: aws.String(sch)},
		})
		_, _ = c.DeleteRegistry(ctx, &glue.DeleteRegistryInput{
			RegistryId: &gluetypes.RegistryId{RegistryName: aws.String(reg)},
		})
	})

	createSchema, err := c.CreateSchema(ctx, &glue.CreateSchemaInput{
		RegistryId:       &gluetypes.RegistryId{RegistryName: aws.String(reg)},
		SchemaName:       aws.String(sch),
		DataFormat:       gluetypes.DataFormatAvro,
		SchemaDefinition: aws.String(`{"type":"record","name":"r","fields":[]}`),
	})
	require.NoError(t, err)
	versionID := aws.ToString(createSchema.SchemaVersionId)
	require.NotEmpty(t, versionID)

	// RegisterSchemaVersion adds version 2 so the diff has two versions.
	reg2, err := c.RegisterSchemaVersion(ctx, &glue.RegisterSchemaVersionInput{
		SchemaId:         &gluetypes.SchemaId{RegistryName: aws.String(reg), SchemaName: aws.String(sch)},
		SchemaDefinition: aws.String(`{"type":"record","name":"r","fields":[{"name":"x","type":"string"}]}`),
	})
	require.NoError(t, err)
	assert.EqualValues(t, 2, aws.ToInt64(reg2.VersionNumber))

	// ListSchemas finds the schema in the registry.
	listSchemas, err := c.ListSchemas(ctx, &glue.ListSchemasInput{
		RegistryId: &gluetypes.RegistryId{RegistryName: aws.String(reg)},
	})
	require.NoError(t, err)
	foundSchema := false
	for _, s := range listSchemas.Schemas {
		if aws.ToString(s.SchemaName) == sch {
			foundSchema = true
		}
	}
	assert.True(t, foundSchema)

	// UpdateSchema changes the compatibility + description.
	updSchema, err := c.UpdateSchema(ctx, &glue.UpdateSchemaInput{
		SchemaId:      &gluetypes.SchemaId{RegistryName: aws.String(reg), SchemaName: aws.String(sch)},
		Compatibility: gluetypes.CompatibilityFull,
		Description:   aws.String("desc"),
	})
	require.NoError(t, err)
	assert.Equal(t, sch, aws.ToString(updSchema.SchemaName))

	getSchema, err := c.GetSchema(ctx, &glue.GetSchemaInput{
		SchemaId: &gluetypes.SchemaId{RegistryName: aws.String(reg), SchemaName: aws.String(sch)},
	})
	require.NoError(t, err)
	assert.Equal(t, gluetypes.CompatibilityFull, getSchema.Compatibility)

	// UpdateRegistry sets the registry description.
	updReg, err := c.UpdateRegistry(ctx, &glue.UpdateRegistryInput{
		RegistryId:  &gluetypes.RegistryId{RegistryName: aws.String(reg)},
		Description: aws.String("registry desc"),
	})
	require.NoError(t, err)
	assert.Equal(t, reg, aws.ToString(updReg.RegistryName))

	// CheckSchemaVersionValidity for a valid + invalid definition.
	valid, err := c.CheckSchemaVersionValidity(ctx, &glue.CheckSchemaVersionValidityInput{
		DataFormat:       gluetypes.DataFormatAvro,
		SchemaDefinition: aws.String(`{"type":"record"}`),
	})
	require.NoError(t, err)
	assert.True(t, valid.Valid)

	invalid, err := c.CheckSchemaVersionValidity(ctx, &glue.CheckSchemaVersionValidityInput{
		DataFormat:       gluetypes.DataFormatAvro,
		SchemaDefinition: aws.String(`{not json`),
	})
	require.NoError(t, err)
	assert.False(t, invalid.Valid)
	assert.NotEmpty(t, aws.ToString(invalid.Error))

	// PutSchemaVersionMetadata + QuerySchemaVersionMetadata + Remove.
	put, err := c.PutSchemaVersionMetadata(ctx, &glue.PutSchemaVersionMetadataInput{
		SchemaVersionId: aws.String(versionID),
		MetadataKeyValue: &gluetypes.MetadataKeyValuePair{
			MetadataKey:   aws.String("owner"),
			MetadataValue: aws.String("data-team"),
		},
	})
	require.NoError(t, err)
	assert.Equal(t, "owner", aws.ToString(put.MetadataKey))
	assert.Equal(t, versionID, aws.ToString(put.SchemaVersionId))

	query, err := c.QuerySchemaVersionMetadata(ctx, &glue.QuerySchemaVersionMetadataInput{
		SchemaVersionId: aws.String(versionID),
	})
	require.NoError(t, err)
	require.Contains(t, query.MetadataInfoMap, "owner")
	assert.Equal(t, "data-team", aws.ToString(query.MetadataInfoMap["owner"].MetadataValue))

	rem, err := c.RemoveSchemaVersionMetadata(ctx, &glue.RemoveSchemaVersionMetadataInput{
		SchemaVersionId: aws.String(versionID),
		MetadataKeyValue: &gluetypes.MetadataKeyValuePair{
			MetadataKey:   aws.String("owner"),
			MetadataValue: aws.String("data-team"),
		},
	})
	require.NoError(t, err)
	assert.Equal(t, "owner", aws.ToString(rem.MetadataKey))

	// GetSchemaVersionsDiff between version 1 and 2.
	diff, err := c.GetSchemaVersionsDiff(ctx, &glue.GetSchemaVersionsDiffInput{
		SchemaId:                  &gluetypes.SchemaId{RegistryName: aws.String(reg), SchemaName: aws.String(sch)},
		FirstSchemaVersionNumber:  &gluetypes.SchemaVersionNumber{VersionNumber: aws.Int64(1)},
		SecondSchemaVersionNumber: &gluetypes.SchemaVersionNumber{VersionNumber: aws.Int64(2)},
		SchemaDiffType:            gluetypes.SchemaDiffTypeSyntaxDiff,
	})
	require.NoError(t, err)
	assert.NotEmpty(t, aws.ToString(diff.Diff))
}

// TestGlue_SearchTables_SDK creates tables and searches the catalog store.
func TestGlue_SearchTables_SDK(t *testing.T) {
	c := glueClient()
	db := "glue-sdk-search-db"

	_, err := c.CreateDatabase(ctx, &glue.CreateDatabaseInput{
		DatabaseInput: &gluetypes.DatabaseInput{Name: aws.String(db)},
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = c.DeleteTable(ctx, &glue.DeleteTableInput{DatabaseName: aws.String(db), Name: aws.String("customer-link")})
		_, _ = c.DeleteTable(ctx, &glue.DeleteTableInput{DatabaseName: aws.String(db), Name: aws.String("orders")})
		_, _ = c.DeleteDatabase(ctx, &glue.DeleteDatabaseInput{Name: aws.String(db)})
	})

	for _, tn := range []string{"customer-link", "orders"} {
		_, err = c.CreateTable(ctx, &glue.CreateTableInput{
			DatabaseName: aws.String(db),
			TableInput:   &gluetypes.TableInput{Name: aws.String(tn)},
		})
		require.NoError(t, err)
	}

	search, err := c.SearchTables(ctx, &glue.SearchTablesInput{
		SearchText: aws.String("link"),
	})
	require.NoError(t, err)
	foundLink := false
	for _, tbl := range search.TableList {
		if aws.ToString(tbl.Name) == "customer-link" {
			foundLink = true
		}
	}
	assert.True(t, foundLink)

	// Filter by DatabaseName narrows to the two tables we created.
	byDB, err := c.SearchTables(ctx, &glue.SearchTablesInput{
		Filters: []gluetypes.PropertyPredicate{
			{Key: aws.String("databaseName"), Value: aws.String(db)},
		},
	})
	require.NoError(t, err)
	assert.GreaterOrEqual(t, len(byDB.TableList), 2)
}

// TestGlue_CodeGen_SDK exercises the code-generation helpers (GetMapping,
// GetPlan, GetDataflowGraph, CreateScript, GetDashboardUrl), source-control
// sync (UpdateJobFromSourceControl / UpdateSourceControlFromJob),
// UpdateUserDefinedFunction, and GetResourcePolicies.
func TestGlue_CodeGen_SDK(t *testing.T) {
	c := glueClient()
	db := "glue-sdk-codegen-db"
	srcTable := "src"

	_, err := c.CreateDatabase(ctx, &glue.CreateDatabaseInput{
		DatabaseInput: &gluetypes.DatabaseInput{Name: aws.String(db)},
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = c.DeleteUserDefinedFunction(ctx, &glue.DeleteUserDefinedFunctionInput{DatabaseName: aws.String(db), FunctionName: aws.String("udf1")})
		_, _ = c.DeleteTable(ctx, &glue.DeleteTableInput{DatabaseName: aws.String(db), Name: aws.String(srcTable)})
		_, _ = c.DeleteJob(ctx, &glue.DeleteJobInput{JobName: aws.String("glue-sdk-codegen-job")})
		_, _ = c.DeleteDatabase(ctx, &glue.DeleteDatabaseInput{Name: aws.String(db)})
	})

	_, err = c.CreateTable(ctx, &glue.CreateTableInput{
		DatabaseName: aws.String(db),
		TableInput: &gluetypes.TableInput{
			Name: aws.String(srcTable),
			StorageDescriptor: &gluetypes.StorageDescriptor{
				Columns: []gluetypes.Column{
					{Name: aws.String("id"), Type: aws.String("int")},
					{Name: aws.String("name"), Type: aws.String("string")},
				},
			},
		},
	})
	require.NoError(t, err)

	// GetMapping derives a 1:1 column mapping from the source table.
	mapping, err := c.GetMapping(ctx, &glue.GetMappingInput{
		Source: &gluetypes.CatalogEntry{DatabaseName: aws.String(db), TableName: aws.String(srcTable)},
	})
	require.NoError(t, err)
	require.Len(t, mapping.Mapping, 2)
	assert.Equal(t, "id", aws.ToString(mapping.Mapping[0].SourcePath))

	// GetPlan produces a Python script from the mapping.
	plan, err := c.GetPlan(ctx, &glue.GetPlanInput{
		Mapping:  mapping.Mapping,
		Source:   &gluetypes.CatalogEntry{DatabaseName: aws.String(db), TableName: aws.String(srcTable)},
		Language: gluetypes.LanguagePython,
	})
	require.NoError(t, err)
	assert.Contains(t, aws.ToString(plan.PythonScript), "glueContext")

	// GetDataflowGraph parses the script back to a DAG.
	graph, err := c.GetDataflowGraph(ctx, &glue.GetDataflowGraphInput{
		PythonScript: plan.PythonScript,
	})
	require.NoError(t, err)
	assert.NotEmpty(t, graph.DagNodes)

	// CreateScript generates a script from a DAG.
	script, err := c.CreateScript(ctx, &glue.CreateScriptInput{
		DagNodes: []gluetypes.CodeGenNode{
			{Id: aws.String("source0"), NodeType: aws.String("DataSource"), Args: []gluetypes.CodeGenNodeArg{
				{Name: aws.String("database"), Value: aws.String(db)},
			}},
		},
		Language: gluetypes.LanguagePython,
	})
	require.NoError(t, err)
	assert.Contains(t, aws.ToString(script.PythonScript), "source0")

	// GetDashboardUrl returns a shaped URL.
	url, err := c.GetDashboardUrl(ctx, &glue.GetDashboardUrlInput{
		ResourceId:   aws.String("session-1"),
		ResourceType: gluetypes.GlueResourceTypeSession,
	})
	require.NoError(t, err)
	assert.Contains(t, aws.ToString(url.Url), "session-1")

	// A job for source-control sync.
	_, err = c.CreateJob(ctx, &glue.CreateJobInput{
		Name: aws.String("glue-sdk-codegen-job"),
		Role: aws.String("arn:aws:iam::123456789012:role/GlueRole"),
		Command: &gluetypes.JobCommand{
			Name:           aws.String("glueetl"),
			ScriptLocation: aws.String("s3://bucket/script.py"),
		},
	})
	require.NoError(t, err)

	updFromSC, err := c.UpdateJobFromSourceControl(ctx, &glue.UpdateJobFromSourceControlInput{
		JobName:         aws.String("glue-sdk-codegen-job"),
		Provider:        gluetypes.SourceControlProviderGithub,
		RepositoryName:  aws.String("repo"),
		RepositoryOwner: aws.String("owner"),
		BranchName:      aws.String("main"),
	})
	require.NoError(t, err)
	assert.Equal(t, "glue-sdk-codegen-job", aws.ToString(updFromSC.JobName))

	updToSC, err := c.UpdateSourceControlFromJob(ctx, &glue.UpdateSourceControlFromJobInput{
		JobName:         aws.String("glue-sdk-codegen-job"),
		Provider:        gluetypes.SourceControlProviderGithub,
		RepositoryName:  aws.String("repo"),
		RepositoryOwner: aws.String("owner"),
		BranchName:      aws.String("main"),
	})
	require.NoError(t, err)
	assert.Equal(t, "glue-sdk-codegen-job", aws.ToString(updToSC.JobName))

	// UpdateUserDefinedFunction on an existing UDF.
	_, err = c.CreateUserDefinedFunction(ctx, &glue.CreateUserDefinedFunctionInput{
		DatabaseName: aws.String(db),
		FunctionInput: &gluetypes.UserDefinedFunctionInput{
			FunctionName: aws.String("udf1"),
			ClassName:    aws.String("com.example.Old"),
			OwnerName:    aws.String("owner"),
			OwnerType:    gluetypes.PrincipalTypeUser,
		},
	})
	require.NoError(t, err)

	_, err = c.UpdateUserDefinedFunction(ctx, &glue.UpdateUserDefinedFunctionInput{
		DatabaseName: aws.String(db),
		FunctionName: aws.String("udf1"),
		FunctionInput: &gluetypes.UserDefinedFunctionInput{
			FunctionName: aws.String("udf1"),
			ClassName:    aws.String("com.example.New"),
			OwnerName:    aws.String("owner"),
			OwnerType:    gluetypes.PrincipalTypeUser,
		},
	})
	require.NoError(t, err)

	getUDF, err := c.GetUserDefinedFunction(ctx, &glue.GetUserDefinedFunctionInput{
		DatabaseName: aws.String(db),
		FunctionName: aws.String("udf1"),
	})
	require.NoError(t, err)
	assert.Equal(t, "com.example.New", aws.ToString(getUDF.UserDefinedFunction.ClassName))

	// GetResourcePolicies returns the stored catalog resource policy.
	_, err = c.PutResourcePolicy(ctx, &glue.PutResourcePolicyInput{
		PolicyInJson: aws.String(`{"Version":"2012-10-17","Statement":[]}`),
	})
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = c.DeleteResourcePolicy(ctx, &glue.DeleteResourcePolicyInput{}) })

	pols, err := c.GetResourcePolicies(ctx, &glue.GetResourcePoliciesInput{})
	require.NoError(t, err)
	assert.GreaterOrEqual(t, len(pols.GetResourcePoliciesResponseList), 1)
}

// TestGlue_IdentityCenter_SDK round-trips the singleton Glue Identity Center
// configuration through Create/Get/Update/Delete.
func TestGlue_IdentityCenter_SDK(t *testing.T) {
	c := glueClient()

	_, err := c.CreateGlueIdentityCenterConfiguration(ctx, &glue.CreateGlueIdentityCenterConfigurationInput{
		InstanceArn: aws.String("arn:aws:sso:::instance/ssoins-1234567890abcdef"),
		Scopes:      []string{"glue:read"},
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = c.DeleteGlueIdentityCenterConfiguration(ctx, &glue.DeleteGlueIdentityCenterConfigurationInput{})
	})

	get, err := c.GetGlueIdentityCenterConfiguration(ctx, &glue.GetGlueIdentityCenterConfigurationInput{})
	require.NoError(t, err)
	assert.NotEmpty(t, aws.ToString(get.ApplicationArn))
	assert.Equal(t, "arn:aws:sso:::instance/ssoins-1234567890abcdef", aws.ToString(get.InstanceArn))
	assert.Contains(t, get.Scopes, "glue:read")

	_, err = c.UpdateGlueIdentityCenterConfiguration(ctx, &glue.UpdateGlueIdentityCenterConfigurationInput{
		Scopes: []string{"glue:write"},
	})
	require.NoError(t, err)

	get2, err := c.GetGlueIdentityCenterConfiguration(ctx, &glue.GetGlueIdentityCenterConfigurationInput{})
	require.NoError(t, err)
	assert.Contains(t, get2.Scopes, "glue:write")

	_, err = c.DeleteGlueIdentityCenterConfiguration(ctx, &glue.DeleteGlueIdentityCenterConfigurationInput{})
	require.NoError(t, err)
}
