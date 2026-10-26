package aws_cli_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestGlueUnfilteredMetadataCLI covers the AWS Lake Formation
// credential-vending reads.
func TestGlueUnfilteredMetadataCLI(t *testing.T) {
	t.Parallel()
	db := "glue-unf-cli-db"
	tbl := "glue-unf-cli-tbl"

	runCLI(t, awsCLI("glue", "create-database", "--database-input", `{"Name":"`+db+`"}`))
	t.Cleanup(func() {
		runCLI(t, awsCLI("glue", "delete-table", "--database-name", db, "--name", tbl))
		runCLI(t, awsCLI("glue", "delete-database", "--name", db))
	})

	runCLI(t, awsCLI("glue", "create-table",
		"--database-name", db,
		"--table-input", `{"Name":"`+tbl+`","StorageDescriptor":{"Location":"s3://bucket/unf/","Columns":[{"Name":"id","Type":"int"},{"Name":"name","Type":"string"}]},"PartitionKeys":[{"Name":"dt","Type":"string"}]}`,
	))

	runCLI(t, awsCLI("glue", "create-partition",
		"--database-name", db,
		"--table-name", tbl,
		"--partition-input", `{"Values":["2024-01-01"],"StorageDescriptor":{"Location":"s3://bucket/unf/dt=2024-01-01/"}}`,
	))

	out := runCLI(t, awsCLI("glue", "get-unfiltered-table-metadata",
		"--catalog-id", "123456789012",
		"--database-name", db,
		"--name", tbl,
		"--supported-permission-types", "COLUMN_PERMISSION"))
	var tm struct {
		Table struct {
			Name string `json:"Name"`
		} `json:"Table"`
		AuthorizedColumns             []string `json:"AuthorizedColumns"`
		IsRegisteredWithLakeFormation bool     `json:"IsRegisteredWithLakeFormation"`
	}
	parseJSON(t, out, &tm)
	assert.Equal(t, tbl, tm.Table.Name)
	assert.ElementsMatch(t, []string{"id", "name"}, tm.AuthorizedColumns)
	assert.False(t, tm.IsRegisteredWithLakeFormation)

	out = runCLI(t, awsCLI("glue", "get-unfiltered-partition-metadata",
		"--catalog-id", "123456789012",
		"--database-name", db,
		"--table-name", tbl,
		"--partition-values", "2024-01-01",
		"--supported-permission-types", "COLUMN_PERMISSION"))
	var pm struct {
		Partition struct {
			Values []string `json:"Values"`
		} `json:"Partition"`
		AuthorizedColumns []string `json:"AuthorizedColumns"`
	}
	parseJSON(t, out, &pm)
	assert.Equal(t, []string{"2024-01-01"}, pm.Partition.Values)
	assert.ElementsMatch(t, []string{"id", "name"}, pm.AuthorizedColumns)

	out = runCLI(t, awsCLI("glue", "get-unfiltered-partitions-metadata",
		"--catalog-id", "123456789012",
		"--database-name", db,
		"--table-name", tbl,
		"--supported-permission-types", "COLUMN_PERMISSION"))
	var pms struct {
		UnfilteredPartitions []struct {
			Partition struct {
				Values []string `json:"Values"`
			} `json:"Partition"`
			AuthorizedColumns []string `json:"AuthorizedColumns"`
		} `json:"UnfilteredPartitions"`
	}
	parseJSON(t, out, &pms)
	require.Len(t, pms.UnfilteredPartitions, 1)
	assert.Equal(t, []string{"2024-01-01"}, pms.UnfilteredPartitions[0].Partition.Values)
	assert.ElementsMatch(t, []string{"id", "name"}, pms.UnfilteredPartitions[0].AuthorizedColumns)
}

func TestGlueDQAnnotationsCLI(t *testing.T) {
	t.Parallel()
	statisticID := "glue-dq-stat-cli"

	// An annotation names a profile, so run an evaluation and read the profile it
	// wrote statistics to off the result: a profile nothing issued names nothing.
	const rulesetName = "glue-dq-cli-annotations-ruleset"
	runCLI(t, awsCLI("glue", "start-data-quality-ruleset-evaluation-run",
		"--role", cliServiceRole(t, "glue.amazonaws.com"),
		"--ruleset-names", rulesetName,
		"--data-source", "{}"))
	out := runCLI(t, awsCLI("glue", "list-data-quality-results"))
	var results struct {
		Results []struct {
			ResultId string `json:"ResultId"`
		} `json:"Results"`
	}
	parseJSON(t, out, &results)
	require.NotEmpty(t, results.Results)
	var profileID string
	for _, candidate := range results.Results {
		got := runCLI(t, awsCLI("glue", "get-data-quality-result",
			"--result-id", candidate.ResultId))
		var result struct {
			RulesetName string `json:"RulesetName"`
			ProfileId   string `json:"ProfileId"`
		}
		parseJSON(t, got, &result)
		if result.RulesetName == rulesetName {
			profileID = result.ProfileId
			break
		}
	}
	require.NotEmpty(t, profileID)

	// One entry lacks StatisticId and lands in FailedInclusionAnnotations.
	out = runCLI(t, awsCLI("glue", "batch-put-data-quality-statistic-annotation",
		"--inclusion-annotations",
		`[{"ProfileId":"`+profileID+`","StatisticId":"`+statisticID+`","InclusionAnnotation":"INCLUDE"},{"ProfileId":"`+profileID+`","InclusionAnnotation":"EXCLUDE"}]`))
	var batch struct {
		FailedInclusionAnnotations []struct {
			ProfileId     string `json:"ProfileId"`
			StatisticId   string `json:"StatisticId"`
			FailureReason string `json:"FailureReason"`
		} `json:"FailedInclusionAnnotations"`
	}
	parseJSON(t, out, &batch)
	require.Len(t, batch.FailedInclusionAnnotations, 1)
	assert.Equal(t, profileID, batch.FailedInclusionAnnotations[0].ProfileId)
	assert.NotEmpty(t, batch.FailedInclusionAnnotations[0].FailureReason)

	runCLI(t, awsCLI("glue", "put-data-quality-profile-annotation",
		"--profile-id", profileID,
		"--inclusion-annotation", "INCLUDE"))

	out = runCLI(t, awsCLI("glue", "list-data-quality-statistic-annotations",
		"--profile-id", profileID))
	var list struct {
		Annotations []struct {
			ProfileId           string `json:"ProfileId"`
			StatisticId         string `json:"StatisticId"`
			InclusionAnnotation struct {
				Value string `json:"Value"`
			} `json:"InclusionAnnotation"`
		} `json:"Annotations"`
	}
	parseJSON(t, out, &list)
	require.Len(t, list.Annotations, 1)
	assert.Equal(t, profileID, list.Annotations[0].ProfileId)
	assert.Equal(t, statisticID, list.Annotations[0].StatisticId)
	assert.Equal(t, "INCLUDE", list.Annotations[0].InclusionAnnotation.Value)
}
