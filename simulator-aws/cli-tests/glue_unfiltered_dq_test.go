package aws_cli_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestGlueUnfilteredMetadataCLI exercises the Lake Formation credential-vending
// reads (get-unfiltered-table-metadata / get-unfiltered-partition-metadata /
// get-unfiltered-partitions-metadata) through the aws CLI.
func TestGlueUnfilteredMetadataCLI(t *testing.T) {
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

	// get-unfiltered-table-metadata.
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

	// get-unfiltered-partition-metadata.
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

	// get-unfiltered-partitions-metadata.
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

// TestGlueDQAnnotationsCLI round-trips the Data Quality annotations through the
// aws CLI: batch-put-data-quality-statistic-annotation +
// put-data-quality-profile-annotation store,
// list-data-quality-statistic-annotations reads back.
func TestGlueDQAnnotationsCLI(t *testing.T) {
	profileID := "glue-dq-profile-cli"
	statisticID := "glue-dq-stat-cli"

	// batch-put: one good entry, one missing StatisticId -> FailedInclusionAnnotations.
	out := runCLI(t, awsCLI("glue", "batch-put-data-quality-statistic-annotation",
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

	// put-data-quality-profile-annotation.
	runCLI(t, awsCLI("glue", "put-data-quality-profile-annotation",
		"--profile-id", profileID,
		"--inclusion-annotation", "INCLUDE"))

	// list-data-quality-statistic-annotations reads back the stored annotation.
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
