package aws_cli_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestGlueCustomEntityTypeCLI round-trips a custom entity type through the
// create/get/list/delete CLI verbs.
func TestGlueCustomEntityTypeCLI(t *testing.T) {
	name := "glue-cli-cet"
	runCLI(t, awsCLI("glue", "create-custom-entity-type",
		"--name", name,
		"--regex-string", `\d{3}-\d{2}-\d{4}`,
		"--context-words", "ssn", "social",
	))
	t.Cleanup(func() {
		_ = awsCLI("glue", "delete-custom-entity-type", "--name", name).Run()
	})

	out := runCLI(t, awsCLI("glue", "get-custom-entity-type", "--name", name))
	var get struct {
		Name         string   `json:"Name"`
		RegexString  string   `json:"RegexString"`
		ContextWords []string `json:"ContextWords"`
	}
	parseJSON(t, out, &get)
	assert.Equal(t, name, get.Name)
	assert.Equal(t, `\d{3}-\d{2}-\d{4}`, get.RegexString)
	assert.ElementsMatch(t, []string{"ssn", "social"}, get.ContextWords)

	out = runCLI(t, awsCLI("glue", "list-custom-entity-types"))
	var list struct {
		CustomEntityTypes []struct {
			Name string `json:"Name"`
		} `json:"CustomEntityTypes"`
	}
	parseJSON(t, out, &list)
	found := false
	for _, cet := range list.CustomEntityTypes {
		if cet.Name == name {
			found = true
		}
	}
	assert.True(t, found)

	out = runCLI(t, awsCLI("glue", "delete-custom-entity-type", "--name", name))
	var del struct {
		Name string `json:"Name"`
	}
	parseJSON(t, out, &del)
	assert.Equal(t, name, del.Name)
}

// TestGlueUsageProfileCLI round-trips a usage profile through
// create/get/list/update/delete.
func TestGlueUsageProfileCLI(t *testing.T) {
	name := "glue-cli-profile"
	runCLI(t, awsCLI("glue", "create-usage-profile",
		"--name", name,
		"--description", "initial",
		"--configuration", `{"JobConfiguration":{"--enable-metrics":{"DefaultValue":"true"}}}`,
	))
	t.Cleanup(func() {
		_ = awsCLI("glue", "delete-usage-profile", "--name", name).Run()
	})

	out := runCLI(t, awsCLI("glue", "get-usage-profile", "--name", name))
	var get struct {
		Name          string `json:"Name"`
		Description   string `json:"Description"`
		Configuration struct {
			JobConfiguration map[string]struct {
				DefaultValue string `json:"DefaultValue"`
			} `json:"JobConfiguration"`
		} `json:"Configuration"`
	}
	parseJSON(t, out, &get)
	assert.Equal(t, name, get.Name)
	assert.Equal(t, "initial", get.Description)
	require.Contains(t, get.Configuration.JobConfiguration, "--enable-metrics")

	runCLI(t, awsCLI("glue", "update-usage-profile",
		"--name", name,
		"--description", "updated",
		"--configuration", `{"SessionConfiguration":{"--worker":{"DefaultValue":"G.1X"}}}`,
	))
	out = runCLI(t, awsCLI("glue", "get-usage-profile", "--name", name))
	parseJSON(t, out, &get)
	assert.Equal(t, "updated", get.Description)

	out = runCLI(t, awsCLI("glue", "list-usage-profiles"))
	var list struct {
		Profiles []struct {
			Name string `json:"Name"`
		} `json:"Profiles"`
	}
	parseJSON(t, out, &list)
	found := false
	for _, p := range list.Profiles {
		if p.Name == name {
			found = true
		}
	}
	assert.True(t, found)

	runCLI(t, awsCLI("glue", "delete-usage-profile", "--name", name))
}

// TestGlueSchemaMetadataCLI exercises list-schemas, update-schema,
// update-registry, check-schema-version-validity, schema-version metadata, and
// get-schema-versions-diff via the CLI.
func TestGlueSchemaMetadataCLI(t *testing.T) {
	reg := "glue-cli-meta-reg"
	sch := "glue-cli-meta-schema"
	runCLI(t, awsCLI("glue", "create-registry", "--registry-name", reg))
	t.Cleanup(func() {
		_ = awsCLI("glue", "delete-schema", "--schema-id", `{"RegistryName":"`+reg+`","SchemaName":"`+sch+`"}`).Run()
		_ = awsCLI("glue", "delete-registry", "--registry-id", `{"RegistryName":"`+reg+`"}`).Run()
	})

	out := runCLI(t, awsCLI("glue", "create-schema",
		"--registry-id", `{"RegistryName":"`+reg+`"}`,
		"--schema-name", sch,
		"--data-format", "AVRO",
		"--schema-definition", `{"type":"record","name":"r","fields":[]}`,
	))
	var createSchema struct {
		SchemaVersionId string `json:"SchemaVersionId"`
	}
	parseJSON(t, out, &createSchema)
	require.NotEmpty(t, createSchema.SchemaVersionId)

	runCLI(t, awsCLI("glue", "register-schema-version",
		"--schema-id", `{"RegistryName":"`+reg+`","SchemaName":"`+sch+`"}`,
		"--schema-definition", `{"type":"record","name":"r","fields":[{"name":"x","type":"string"}]}`,
	))

	out = runCLI(t, awsCLI("glue", "list-schemas", "--registry-id", `{"RegistryName":"`+reg+`"}`))
	var listSchemas struct {
		Schemas []struct {
			SchemaName string `json:"SchemaName"`
		} `json:"Schemas"`
	}
	parseJSON(t, out, &listSchemas)
	foundSchema := false
	for _, s := range listSchemas.Schemas {
		if s.SchemaName == sch {
			foundSchema = true
		}
	}
	assert.True(t, foundSchema)

	out = runCLI(t, awsCLI("glue", "update-schema",
		"--schema-id", `{"RegistryName":"`+reg+`","SchemaName":"`+sch+`"}`,
		"--compatibility", "FULL",
		"--description", "desc",
	))
	var updSchema struct {
		SchemaName string `json:"SchemaName"`
	}
	parseJSON(t, out, &updSchema)
	assert.Equal(t, sch, updSchema.SchemaName)

	out = runCLI(t, awsCLI("glue", "update-registry",
		"--registry-id", `{"RegistryName":"`+reg+`"}`,
		"--description", "registry desc",
	))
	var updReg struct {
		RegistryName string `json:"RegistryName"`
	}
	parseJSON(t, out, &updReg)
	assert.Equal(t, reg, updReg.RegistryName)

	out = runCLI(t, awsCLI("glue", "check-schema-version-validity",
		"--data-format", "AVRO",
		"--schema-definition", `{"type":"record"}`,
	))
	var valid struct {
		Valid bool `json:"Valid"`
	}
	parseJSON(t, out, &valid)
	assert.True(t, valid.Valid)

	out = runCLI(t, awsCLI("glue", "put-schema-version-metadata",
		"--schema-version-id", createSchema.SchemaVersionId,
		"--metadata-key-value", `{"MetadataKey":"owner","MetadataValue":"data-team"}`,
	))
	var put struct {
		MetadataKey string `json:"MetadataKey"`
	}
	parseJSON(t, out, &put)
	assert.Equal(t, "owner", put.MetadataKey)

	out = runCLI(t, awsCLI("glue", "query-schema-version-metadata",
		"--schema-version-id", createSchema.SchemaVersionId,
	))
	var query struct {
		MetadataInfoMap map[string]struct {
			MetadataValue string `json:"MetadataValue"`
		} `json:"MetadataInfoMap"`
	}
	parseJSON(t, out, &query)
	require.Contains(t, query.MetadataInfoMap, "owner")
	assert.Equal(t, "data-team", query.MetadataInfoMap["owner"].MetadataValue)

	runCLI(t, awsCLI("glue", "remove-schema-version-metadata",
		"--schema-version-id", createSchema.SchemaVersionId,
		"--metadata-key-value", `{"MetadataKey":"owner","MetadataValue":"data-team"}`,
	))

	out = runCLI(t, awsCLI("glue", "get-schema-versions-diff",
		"--schema-id", `{"RegistryName":"`+reg+`","SchemaName":"`+sch+`"}`,
		"--first-schema-version-number", `{"VersionNumber":1}`,
		"--second-schema-version-number", `{"VersionNumber":2}`,
		"--schema-diff-type", "SYNTAX_DIFF",
	))
	var diff struct {
		Diff string `json:"Diff"`
	}
	parseJSON(t, out, &diff)
	assert.NotEmpty(t, diff.Diff)
}

// TestGlueSearchTablesCLI creates tables then searches the catalog store, and
// also covers the code-generation / source-control / resource-policy verbs.
func TestGlueSearchTablesCLI(t *testing.T) {
	db := "glue-cli-search-db"
	runCLI(t, awsCLI("glue", "create-database", "--database-input", `{"Name":"`+db+`"}`))
	t.Cleanup(func() {
		_ = awsCLI("glue", "delete-table", "--database-name", db, "--name", "customer-link").Run()
		_ = awsCLI("glue", "delete-table", "--database-name", db, "--name", "src").Run()
		_ = awsCLI("glue", "delete-user-defined-function", "--database-name", db, "--function-name", "udf1").Run()
		_ = awsCLI("glue", "delete-job", "--job-name", "glue-cli-codegen-job").Run()
		_ = awsCLI("glue", "delete-database", "--name", db).Run()
		_ = awsCLI("glue", "delete-resource-policy").Run()
	})

	runCLI(t, awsCLI("glue", "create-table", "--database-name", db,
		"--table-input", `{"Name":"customer-link"}`))
	runCLI(t, awsCLI("glue", "create-table", "--database-name", db,
		"--table-input", `{"Name":"src","StorageDescriptor":{"Columns":[{"Name":"id","Type":"int"},{"Name":"name","Type":"string"}]}}`))

	out := runCLI(t, awsCLI("glue", "search-tables", "--search-text", "link"))
	var search struct {
		TableList []struct {
			Name string `json:"Name"`
		} `json:"TableList"`
	}
	parseJSON(t, out, &search)
	foundLink := false
	for _, tbl := range search.TableList {
		if tbl.Name == "customer-link" {
			foundLink = true
		}
	}
	assert.True(t, foundLink)

	// GetMapping derives a column mapping from the source table.
	out = runCLI(t, awsCLI("glue", "get-mapping",
		"--source", `{"DatabaseName":"`+db+`","TableName":"src"}`))
	var mapping struct {
		Mapping []struct {
			SourcePath string `json:"SourcePath"`
		} `json:"Mapping"`
	}
	parseJSON(t, out, &mapping)
	require.Len(t, mapping.Mapping, 2)

	// GetPlan produces a Python script.
	out = runCLI(t, awsCLI("glue", "get-plan",
		"--mapping", `[{"SourceTable":"src","SourcePath":"id","TargetTable":"src","TargetPath":"id"}]`,
		"--source", `{"DatabaseName":"`+db+`","TableName":"src"}`))
	var plan struct {
		PythonScript string `json:"PythonScript"`
	}
	parseJSON(t, out, &plan)
	assert.Contains(t, plan.PythonScript, "glueContext")

	// GetDataflowGraph parses the script to a DAG.
	out = runCLI(t, awsCLI("glue", "get-dataflow-graph", "--python-script", plan.PythonScript))
	var graph struct {
		DagNodes []struct {
			Id string `json:"Id"`
		} `json:"DagNodes"`
	}
	parseJSON(t, out, &graph)
	assert.NotEmpty(t, graph.DagNodes)

	// CreateScript generates a script from a DAG.
	out = runCLI(t, awsCLI("glue", "create-script",
		"--dag-nodes", `[{"Id":"source0","NodeType":"DataSource","Args":[{"Name":"database","Value":"`+db+`"}]}]`,
		"--language", "PYTHON"))
	var script struct {
		PythonScript string `json:"PythonScript"`
	}
	parseJSON(t, out, &script)
	assert.Contains(t, script.PythonScript, "source0")

	// GetDashboardUrl returns a shaped URL.
	out = runCLI(t, awsCLI("glue", "get-dashboard-url",
		"--resource-id", "session-1", "--resource-type", "SESSION"))
	var url struct {
		Url string `json:"Url"`
	}
	parseJSON(t, out, &url)
	assert.Contains(t, url.Url, "session-1")

	// Source-control sync on a job.
	runCLI(t, awsCLI("glue", "create-job",
		"--name", "glue-cli-codegen-job",
		"--role", "arn:aws:iam::123456789012:role/GlueRole",
		"--command", `{"Name":"glueetl","ScriptLocation":"s3://bucket/script.py"}`))
	out = runCLI(t, awsCLI("glue", "update-job-from-source-control",
		"--job-name", "glue-cli-codegen-job",
		"--provider", "GITHUB",
		"--repository-name", "repo",
		"--repository-owner", "owner",
		"--branch-name", "main"))
	var scResp struct {
		JobName string `json:"JobName"`
	}
	parseJSON(t, out, &scResp)
	assert.Equal(t, "glue-cli-codegen-job", scResp.JobName)

	out = runCLI(t, awsCLI("glue", "update-source-control-from-job",
		"--job-name", "glue-cli-codegen-job",
		"--provider", "GITHUB",
		"--repository-name", "repo",
		"--repository-owner", "owner",
		"--branch-name", "main"))
	parseJSON(t, out, &scResp)
	assert.Equal(t, "glue-cli-codegen-job", scResp.JobName)

	// UpdateUserDefinedFunction on an existing UDF.
	runCLI(t, awsCLI("glue", "create-user-defined-function",
		"--database-name", db,
		"--function-input", `{"FunctionName":"udf1","ClassName":"com.example.Old","OwnerName":"owner","OwnerType":"USER"}`))
	runCLI(t, awsCLI("glue", "update-user-defined-function",
		"--database-name", db,
		"--function-name", "udf1",
		"--function-input", `{"FunctionName":"udf1","ClassName":"com.example.New","OwnerName":"owner","OwnerType":"USER"}`))
	out = runCLI(t, awsCLI("glue", "get-user-defined-function",
		"--database-name", db, "--function-name", "udf1"))
	var getUDF struct {
		UserDefinedFunction struct {
			ClassName string `json:"ClassName"`
		} `json:"UserDefinedFunction"`
	}
	parseJSON(t, out, &getUDF)
	assert.Equal(t, "com.example.New", getUDF.UserDefinedFunction.ClassName)

	// GetResourcePolicies enumerates the catalog resource policy.
	runCLI(t, awsCLI("glue", "put-resource-policy",
		"--policy-in-json", `{"Version":"2012-10-17","Statement":[]}`))
	out = runCLI(t, awsCLI("glue", "get-resource-policies"))
	var pols struct {
		GetResourcePoliciesResponseList []struct {
			PolicyInJson string `json:"PolicyInJson"`
		} `json:"GetResourcePoliciesResponseList"`
	}
	parseJSON(t, out, &pols)
	assert.GreaterOrEqual(t, len(pols.GetResourcePoliciesResponseList), 1)
}

// TestGlueIdentityCenterCLI round-trips the singleton Glue Identity Center
// configuration through create/get/update/delete.
func TestGlueIdentityCenterCLI(t *testing.T) {
	runCLI(t, awsCLI("glue", "create-glue-identity-center-configuration",
		"--instance-arn", "arn:aws:sso:::instance/ssoins-1234567890abcdef",
		"--scopes", "glue:read"))
	t.Cleanup(func() {
		_ = awsCLI("glue", "delete-glue-identity-center-configuration").Run()
	})

	out := runCLI(t, awsCLI("glue", "get-glue-identity-center-configuration"))
	var get struct {
		ApplicationArn string   `json:"ApplicationArn"`
		InstanceArn    string   `json:"InstanceArn"`
		Scopes         []string `json:"Scopes"`
	}
	parseJSON(t, out, &get)
	assert.NotEmpty(t, get.ApplicationArn)
	assert.Equal(t, "arn:aws:sso:::instance/ssoins-1234567890abcdef", get.InstanceArn)
	assert.Contains(t, get.Scopes, "glue:read")

	runCLI(t, awsCLI("glue", "update-glue-identity-center-configuration", "--scopes", "glue:write"))
	out = runCLI(t, awsCLI("glue", "get-glue-identity-center-configuration"))
	parseJSON(t, out, &get)
	assert.Contains(t, get.Scopes, "glue:write")

	runCLI(t, awsCLI("glue", "delete-glue-identity-center-configuration"))
}
