package aws_cli_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGlue_DatabaseCRUD_CLI(t *testing.T) {
	runCLI(t, awsCLI("glue", "create-database",
		"--database-input", `{"Name":"glue-cli-db","Description":"cli test"}`,
	))
	t.Cleanup(func() {
		runCLI(t, awsCLI("glue", "delete-database", "--name", "glue-cli-db"))
	})

	out := runCLI(t, awsCLI("glue", "get-database", "--name", "glue-cli-db"))
	var get struct {
		Database struct {
			Name string `json:"Name"`
		} `json:"Database"`
	}
	parseJSON(t, out, &get)
	assert.Equal(t, "glue-cli-db", get.Database.Name)

	out = runCLI(t, awsCLI("glue", "get-databases"))
	var list struct {
		DatabaseList []struct {
			Name string `json:"Name"`
		} `json:"DatabaseList"`
	}
	parseJSON(t, out, &list)
	found := false
	for _, db := range list.DatabaseList {
		if db.Name == "glue-cli-db" {
			found = true
		}
	}
	assert.True(t, found)
}

func TestGlue_TableCRUD_CLI(t *testing.T) {
	runCLI(t, awsCLI("glue", "create-database",
		"--database-input", `{"Name":"glue-cli-tbl-db"}`,
	))
	t.Cleanup(func() {
		runCLI(t, awsCLI("glue", "delete-table",
			"--database-name", "glue-cli-tbl-db",
			"--name", "glue-cli-table"))
		runCLI(t, awsCLI("glue", "delete-database", "--name", "glue-cli-tbl-db"))
	})

	runCLI(t, awsCLI("glue", "create-table",
		"--database-name", "glue-cli-tbl-db",
		"--table-input", `{"Name":"glue-cli-table","StorageDescriptor":{"Location":"s3://bucket/","InputFormat":"org.apache.hadoop.mapred.TextInputFormat","OutputFormat":"org.apache.hadoop.hive.ql.io.HiveIgnoreKeyTextOutputFormat"}}`,
	))

	out := runCLI(t, awsCLI("glue", "get-table",
		"--database-name", "glue-cli-tbl-db",
		"--name", "glue-cli-table"))
	var get struct {
		Table struct {
			Name         string `json:"Name"`
			DatabaseName string `json:"DatabaseName"`
		} `json:"Table"`
	}
	parseJSON(t, out, &get)
	assert.Equal(t, "glue-cli-table", get.Table.Name)
	assert.Equal(t, "glue-cli-tbl-db", get.Table.DatabaseName)

	out = runCLI(t, awsCLI("glue", "get-tables", "--database-name", "glue-cli-tbl-db"))
	var list struct {
		TableList []struct {
			Name string `json:"Name"`
		} `json:"TableList"`
	}
	parseJSON(t, out, &list)
	require.Len(t, list.TableList, 1)
	assert.Equal(t, "glue-cli-table", list.TableList[0].Name)
}

func TestGlue_DatabaseUpdate_CLI(t *testing.T) {
	runCLI(t, awsCLI("glue", "create-database",
		"--database-input", `{"Name":"glue-cli-updb","Description":"before"}`,
	))
	t.Cleanup(func() {
		runCLI(t, awsCLI("glue", "delete-database", "--name", "glue-cli-updb"))
	})

	runCLI(t, awsCLI("glue", "update-database",
		"--name", "glue-cli-updb",
		"--database-input", `{"Name":"glue-cli-updb","Description":"after","Parameters":{"owner":"data-eng"}}`,
	))

	out := runCLI(t, awsCLI("glue", "get-database", "--name", "glue-cli-updb"))
	var get struct {
		Database struct {
			Description string            `json:"Description"`
			Parameters  map[string]string `json:"Parameters"`
		} `json:"Database"`
	}
	parseJSON(t, out, &get)
	assert.Equal(t, "after", get.Database.Description)
	assert.Equal(t, "data-eng", get.Database.Parameters["owner"])
}

func TestGlue_TableUpdateAndBatchDelete_CLI(t *testing.T) {
	db := "glue-cli-tblupd-db"
	runCLI(t, awsCLI("glue", "create-database", "--database-input", `{"Name":"`+db+`"}`))
	t.Cleanup(func() {
		runCLI(t, awsCLI("glue", "batch-delete-table", "--database-name", db, "--tables-to-delete", "t1", "t2"))
		runCLI(t, awsCLI("glue", "delete-database", "--name", db))
	})

	for _, name := range []string{"t1", "t2"} {
		runCLI(t, awsCLI("glue", "create-table",
			"--database-name", db,
			"--table-input", `{"Name":"`+name+`","StorageDescriptor":{"Location":"s3://bucket/`+name+`/"}}`,
		))
	}

	runCLI(t, awsCLI("glue", "update-table",
		"--database-name", db,
		"--table-input", `{"Name":"t1","TableType":"EXTERNAL_TABLE","StorageDescriptor":{"Location":"s3://bucket/t1-new/"}}`,
	))
	out := runCLI(t, awsCLI("glue", "get-table", "--database-name", db, "--name", "t1"))
	var get struct {
		Table struct {
			TableType         string `json:"TableType"`
			StorageDescriptor struct {
				Location string `json:"Location"`
			} `json:"StorageDescriptor"`
		} `json:"Table"`
	}
	parseJSON(t, out, &get)
	assert.Equal(t, "EXTERNAL_TABLE", get.Table.TableType)
	assert.Equal(t, "s3://bucket/t1-new/", get.Table.StorageDescriptor.Location)

	out = runCLI(t, awsCLI("glue", "batch-delete-table",
		"--database-name", db,
		"--tables-to-delete", "t1", "t2", "missing"))
	var bd struct {
		Errors []struct {
			TableName string `json:"TableName"`
		} `json:"Errors"`
	}
	parseJSON(t, out, &bd)
	require.Len(t, bd.Errors, 1)
	assert.Equal(t, "missing", bd.Errors[0].TableName)
}

func TestGlue_PartitionLifecycle_CLI(t *testing.T) {
	db := "glue-cli-part-db"
	tbl := "glue-cli-part-tbl"
	runCLI(t, awsCLI("glue", "create-database", "--database-input", `{"Name":"`+db+`"}`))
	t.Cleanup(func() {
		runCLI(t, awsCLI("glue", "delete-table", "--database-name", db, "--name", tbl))
		runCLI(t, awsCLI("glue", "delete-database", "--name", db))
	})
	runCLI(t, awsCLI("glue", "create-table",
		"--database-name", db,
		"--table-input", `{"Name":"`+tbl+`","StorageDescriptor":{"Location":"s3://bucket/part/"},"PartitionKeys":[{"Name":"dt","Type":"string"}]}`,
	))

	runCLI(t, awsCLI("glue", "create-partition",
		"--database-name", db,
		"--table-name", tbl,
		"--partition-input", `{"Values":["2024-01-01"],"StorageDescriptor":{"Location":"s3://bucket/part/dt=2024-01-01/"}}`,
	))

	out := runCLI(t, awsCLI("glue", "batch-create-partition",
		"--database-name", db,
		"--table-name", tbl,
		"--partition-input-list", `[{"Values":["2024-01-02"],"StorageDescriptor":{"Location":"s3://bucket/part/dt=2024-01-02/"}},{"Values":["2024-01-01"]}]`,
	))
	var bcp struct {
		Errors []struct {
			PartitionValues []string `json:"PartitionValues"`
		} `json:"Errors"`
	}
	parseJSON(t, out, &bcp)
	require.Len(t, bcp.Errors, 1)
	assert.Equal(t, []string{"2024-01-01"}, bcp.Errors[0].PartitionValues)

	out = runCLI(t, awsCLI("glue", "get-partition",
		"--database-name", db, "--table-name", tbl, "--partition-values", "2024-01-01"))
	var gp struct {
		Partition struct {
			Values       []string `json:"Values"`
			DatabaseName string   `json:"DatabaseName"`
			TableName    string   `json:"TableName"`
		} `json:"Partition"`
	}
	parseJSON(t, out, &gp)
	assert.Equal(t, []string{"2024-01-01"}, gp.Partition.Values)
	assert.Equal(t, db, gp.Partition.DatabaseName)
	assert.Equal(t, tbl, gp.Partition.TableName)

	out = runCLI(t, awsCLI("glue", "get-partitions", "--database-name", db, "--table-name", tbl))
	var gps struct {
		Partitions []struct {
			Values []string `json:"Values"`
		} `json:"Partitions"`
	}
	parseJSON(t, out, &gps)
	assert.Len(t, gps.Partitions, 2)

	out = runCLI(t, awsCLI("glue", "batch-get-partition",
		"--database-name", db, "--table-name", tbl,
		"--partitions-to-get", `[{"Values":["2024-01-02"]},{"Values":["2099-12-31"]}]`))
	var bgp struct {
		Partitions []struct {
			Values []string `json:"Values"`
		} `json:"Partitions"`
		UnprocessedKeys []struct {
			Values []string `json:"Values"`
		} `json:"UnprocessedKeys"`
	}
	parseJSON(t, out, &bgp)
	require.Len(t, bgp.Partitions, 1)
	assert.Equal(t, []string{"2024-01-02"}, bgp.Partitions[0].Values)
	require.Len(t, bgp.UnprocessedKeys, 1)
	assert.Equal(t, []string{"2099-12-31"}, bgp.UnprocessedKeys[0].Values)

	runCLI(t, awsCLI("glue", "update-partition",
		"--database-name", db, "--table-name", tbl,
		"--partition-value-list", "2024-01-01",
		"--partition-input", `{"Values":["2024-01-01"],"Parameters":{"rows":"100"}}`))
	out = runCLI(t, awsCLI("glue", "get-partition",
		"--database-name", db, "--table-name", tbl, "--partition-values", "2024-01-01"))
	var gp2 struct {
		Partition struct {
			Parameters map[string]string `json:"Parameters"`
		} `json:"Partition"`
	}
	parseJSON(t, out, &gp2)
	assert.Equal(t, "100", gp2.Partition.Parameters["rows"])

	runCLI(t, awsCLI("glue", "delete-partition",
		"--database-name", db, "--table-name", tbl, "--partition-values", "2024-01-01"))

	out = runCLI(t, awsCLI("glue", "batch-delete-partition",
		"--database-name", db, "--table-name", tbl,
		"--partitions-to-delete", `[{"Values":["2024-01-02"]},{"Values":["2099-12-31"]}]`))
	var bdp struct {
		Errors []struct {
			PartitionValues []string `json:"PartitionValues"`
		} `json:"Errors"`
	}
	parseJSON(t, out, &bdp)
	require.Len(t, bdp.Errors, 1)
	assert.Equal(t, []string{"2099-12-31"}, bdp.Errors[0].PartitionValues)

	out = runCLI(t, awsCLI("glue", "get-partitions", "--database-name", db, "--table-name", tbl))
	parseJSON(t, out, &gps)
	assert.Empty(t, gps.Partitions)
}

func TestGlue_JobCRUD_CLI(t *testing.T) {
	bucket := "glue-cli-scripts"
	scriptPath := filepath.Join(tmpDir, "glue-cli-script.py")
	require.NoError(t, os.WriteFile(scriptPath, []byte("import sys\nprint('glue-cli-ready')\nsys.exit(0)\n"), 0644))
	runCLI(t, awsCLI("s3api", "create-bucket", "--bucket", bucket))
	runCLI(t, awsCLI("s3api", "put-object", "--bucket", bucket, "--key", "script.py", "--body", scriptPath))
	t.Cleanup(func() {
		runCLI(t, awsCLI("s3api", "delete-object", "--bucket", bucket, "--key", "script.py"))
		runCLI(t, awsCLI("s3api", "delete-bucket", "--bucket", bucket))
	})

	runCLI(t, awsCLI("glue", "create-job",
		"--name", "glue-cli-job",
		"--role", "arn:aws:iam::123456789012:role/glue-role",
		"--command", `{"Name":"pythonshell","ScriptLocation":"s3://`+bucket+`/script.py"}`,
		"--glue-version", "4.0",
		"--worker-type", "G.1X",
		"--number-of-workers", "2",
	))
	t.Cleanup(func() {
		runCLI(t, awsCLI("glue", "delete-job", "--job-name", "glue-cli-job"))
	})

	out := runCLI(t, awsCLI("glue", "get-job", "--job-name", "glue-cli-job"))
	var get struct {
		Job struct {
			Name string `json:"Name"`
		} `json:"Job"`
	}
	parseJSON(t, out, &get)
	assert.Equal(t, "glue-cli-job", get.Job.Name)

	out = runCLI(t, awsCLI("glue", "get-jobs"))
	var list struct {
		Jobs []struct {
			Name string `json:"Name"`
		} `json:"Jobs"`
	}
	parseJSON(t, out, &list)
	found := false
	for _, j := range list.Jobs {
		if j.Name == "glue-cli-job" {
			found = true
		}
	}
	assert.True(t, found)

	out = runCLI(t, awsCLI("glue", "start-job-run", "--job-name", "glue-cli-job"))
	var run struct {
		JobRunID string `json:"JobRunId"`
	}
	parseJSON(t, out, &run)
	require.NotEmpty(t, run.JobRunID)

	var getRun struct {
		JobRun struct {
			ID          string `json:"Id"`
			JobRunState string `json:"JobRunState"`
		} `json:"JobRun"`
	}
	require.Eventually(t, func() bool {
		out = runCLI(t, awsCLI("glue", "get-job-run",
			"--job-name", "glue-cli-job",
			"--run-id", run.JobRunID))
		parseJSON(t, out, &getRun)
		return getRun.JobRun.JobRunState == "SUCCEEDED"
	}, 10*time.Second, 100*time.Millisecond)
	assert.Equal(t, run.JobRunID, getRun.JobRun.ID)
	assert.Equal(t, "SUCCEEDED", getRun.JobRun.JobRunState)

	// get-job-runs for a non-existent job must error (EntityNotFoundException),
	// not return an empty list.
	errOut := runCLIExpectError(t, awsCLI("glue", "get-job-runs", "--job-name", "glue-cli-no-such-job"))
	assert.Contains(t, errOut, "EntityNotFoundException")
}

func TestGlue_JobUpdateListBatchStop_CLI(t *testing.T) {
	runCLI(t, awsCLI("glue", "create-job",
		"--name", "glue-cli-upd-job",
		"--role", "arn:aws:iam::123456789012:role/glue-role",
		"--command", `{"Name":"glueetl","ScriptLocation":"s3://bucket/script.py"}`,
		"--description", "before",
	))
	t.Cleanup(func() {
		runCLI(t, awsCLI("glue", "delete-job", "--job-name", "glue-cli-upd-job"))
	})

	runCLI(t, awsCLI("glue", "update-job",
		"--job-name", "glue-cli-upd-job",
		"--job-update", `{"Role":"arn:aws:iam::123456789012:role/glue-role","Command":{"Name":"glueetl","ScriptLocation":"s3://bucket/updated.py"},"Description":"after"}`,
	))

	out := runCLI(t, awsCLI("glue", "get-job", "--job-name", "glue-cli-upd-job"))
	var get struct {
		Job struct {
			Description string `json:"Description"`
			Command     struct {
				ScriptLocation string `json:"ScriptLocation"`
			} `json:"Command"`
		} `json:"Job"`
	}
	parseJSON(t, out, &get)
	assert.Equal(t, "after", get.Job.Description)
	assert.Equal(t, "s3://bucket/updated.py", get.Job.Command.ScriptLocation)

	out = runCLI(t, awsCLI("glue", "list-jobs"))
	var list struct {
		JobNames []string `json:"JobNames"`
	}
	parseJSON(t, out, &list)
	assert.Contains(t, list.JobNames, "glue-cli-upd-job")

	out = runCLI(t, awsCLI("glue", "batch-stop-job-run",
		"--job-name", "glue-cli-upd-job",
		"--job-run-ids", "jr_nope"))
	var stop struct {
		Errors []struct {
			JobRunID string `json:"JobRunId"`
		} `json:"Errors"`
	}
	parseJSON(t, out, &stop)
	require.Len(t, stop.Errors, 1)
	assert.Equal(t, "jr_nope", stop.Errors[0].JobRunID)
}

func TestGlue_CrawlerCRUD_CLI(t *testing.T) {
	runCLI(t, awsCLI("glue", "create-crawler",
		"--name", "glue-cli-crawler",
		"--role", "arn:aws:iam::123456789012:role/glue-crawler-role",
		"--database-name", "glue-cli-crawler-db",
		"--targets", `{"S3Targets":[{"Path":"s3://bucket/data/"}]}`,
		"--description", "cli crawler",
	))
	t.Cleanup(func() {
		runCLI(t, awsCLI("glue", "delete-crawler", "--name", "glue-cli-crawler"))
	})

	out := runCLI(t, awsCLI("glue", "get-crawler", "--name", "glue-cli-crawler"))
	var get struct {
		Crawler struct {
			Name         string `json:"Name"`
			DatabaseName string `json:"DatabaseName"`
			Description  string `json:"Description"`
			State        string `json:"State"`
			Targets      struct {
				S3Targets []struct {
					Path string `json:"Path"`
				} `json:"S3Targets"`
			} `json:"Targets"`
		} `json:"Crawler"`
	}
	parseJSON(t, out, &get)
	assert.Equal(t, "glue-cli-crawler", get.Crawler.Name)
	assert.Equal(t, "glue-cli-crawler-db", get.Crawler.DatabaseName)
	require.Len(t, get.Crawler.Targets.S3Targets, 1)
	assert.Equal(t, "s3://bucket/data/", get.Crawler.Targets.S3Targets[0].Path)

	runCLI(t, awsCLI("glue", "update-crawler",
		"--name", "glue-cli-crawler",
		"--role", "arn:aws:iam::123456789012:role/glue-crawler-role",
		"--description", "updated crawler",
	))
	out = runCLI(t, awsCLI("glue", "get-crawler", "--name", "glue-cli-crawler"))
	parseJSON(t, out, &get)
	assert.Equal(t, "updated crawler", get.Crawler.Description)

	runCLI(t, awsCLI("glue", "start-crawler", "--name", "glue-cli-crawler"))
	out = runCLI(t, awsCLI("glue", "get-crawler", "--name", "glue-cli-crawler"))
	parseJSON(t, out, &get)
	assert.Equal(t, "RUNNING", get.Crawler.State)

	runCLI(t, awsCLI("glue", "stop-crawler", "--name", "glue-cli-crawler"))

	out = runCLI(t, awsCLI("glue", "get-crawlers"))
	var gcs struct {
		Crawlers []struct {
			Name string `json:"Name"`
		} `json:"Crawlers"`
	}
	parseJSON(t, out, &gcs)
	found := false
	for _, cr := range gcs.Crawlers {
		if cr.Name == "glue-cli-crawler" {
			found = true
		}
	}
	assert.True(t, found)

	out = runCLI(t, awsCLI("glue", "list-crawlers"))
	var lcs struct {
		CrawlerNames []string `json:"CrawlerNames"`
	}
	parseJSON(t, out, &lcs)
	assert.Contains(t, lcs.CrawlerNames, "glue-cli-crawler")
}

func TestGlue_TriggerCRUD_CLI(t *testing.T) {
	runCLI(t, awsCLI("glue", "create-job",
		"--name", "glue-cli-trigger-job",
		"--role", "arn:aws:iam::123456789012:role/glue-role",
		"--command", `{"Name":"glueetl","ScriptLocation":"s3://bucket/script.py"}`,
	))
	t.Cleanup(func() {
		runCLI(t, awsCLI("glue", "delete-job", "--job-name", "glue-cli-trigger-job"))
	})

	out := runCLI(t, awsCLI("glue", "create-trigger",
		"--name", "glue-cli-trigger",
		"--type", "SCHEDULED",
		"--schedule", "cron(15 12 * * ? *)",
		"--actions", `[{"JobName":"glue-cli-trigger-job"}]`,
		"--description", "cli trigger",
	))
	var created struct {
		Name string `json:"Name"`
	}
	parseJSON(t, out, &created)
	assert.Equal(t, "glue-cli-trigger", created.Name)
	t.Cleanup(func() {
		runCLI(t, awsCLI("glue", "delete-trigger", "--name", "glue-cli-trigger"))
	})

	out = runCLI(t, awsCLI("glue", "get-trigger", "--name", "glue-cli-trigger"))
	var get struct {
		Trigger struct {
			Name    string `json:"Name"`
			Type    string `json:"Type"`
			State   string `json:"State"`
			Actions []struct {
				JobName string `json:"JobName"`
			} `json:"Actions"`
		} `json:"Trigger"`
	}
	parseJSON(t, out, &get)
	assert.Equal(t, "SCHEDULED", get.Trigger.Type)
	require.Len(t, get.Trigger.Actions, 1)
	assert.Equal(t, "glue-cli-trigger-job", get.Trigger.Actions[0].JobName)

	runCLI(t, awsCLI("glue", "start-trigger", "--name", "glue-cli-trigger"))
	out = runCLI(t, awsCLI("glue", "get-trigger", "--name", "glue-cli-trigger"))
	parseJSON(t, out, &get)
	assert.Equal(t, "ACTIVATED", get.Trigger.State)

	runCLI(t, awsCLI("glue", "stop-trigger", "--name", "glue-cli-trigger"))

	out = runCLI(t, awsCLI("glue", "get-triggers"))
	var gts struct {
		Triggers []struct {
			Name string `json:"Name"`
		} `json:"Triggers"`
	}
	parseJSON(t, out, &gts)
	found := false
	for _, tr := range gts.Triggers {
		if tr.Name == "glue-cli-trigger" {
			found = true
		}
	}
	assert.True(t, found)
}

func TestGlue_ConnectionCRUD_CLI(t *testing.T) {
	runCLI(t, awsCLI("glue", "create-connection",
		"--connection-input", `{"Name":"glue-cli-conn","Description":"cli connection","ConnectionType":"JDBC","ConnectionProperties":{"JDBC_CONNECTION_URL":"jdbc:mysql://host:3306/db","USERNAME":"admin"}}`,
	))
	t.Cleanup(func() {
		runCLI(t, awsCLI("glue", "delete-connection", "--connection-name", "glue-cli-conn"))
	})

	out := runCLI(t, awsCLI("glue", "get-connection", "--name", "glue-cli-conn"))
	var get struct {
		Connection struct {
			Name                 string            `json:"Name"`
			ConnectionType       string            `json:"ConnectionType"`
			ConnectionProperties map[string]string `json:"ConnectionProperties"`
		} `json:"Connection"`
	}
	parseJSON(t, out, &get)
	assert.Equal(t, "glue-cli-conn", get.Connection.Name)
	assert.Equal(t, "JDBC", get.Connection.ConnectionType)
	assert.Equal(t, "admin", get.Connection.ConnectionProperties["USERNAME"])

	runCLI(t, awsCLI("glue", "update-connection",
		"--name", "glue-cli-conn",
		"--connection-input", `{"Name":"glue-cli-conn","Description":"updated connection","ConnectionType":"JDBC","ConnectionProperties":{"JDBC_CONNECTION_URL":"jdbc:mysql://host:3306/db2","USERNAME":"root"}}`,
	))
	out = runCLI(t, awsCLI("glue", "get-connection", "--name", "glue-cli-conn"))
	parseJSON(t, out, &get)
	assert.Equal(t, "root", get.Connection.ConnectionProperties["USERNAME"])

	out = runCLI(t, awsCLI("glue", "get-connections"))
	var gcs struct {
		ConnectionList []struct {
			Name string `json:"Name"`
		} `json:"ConnectionList"`
	}
	parseJSON(t, out, &gcs)
	found := false
	for _, cn := range gcs.ConnectionList {
		if cn.Name == "glue-cli-conn" {
			found = true
		}
	}
	assert.True(t, found)
}

func TestGlue_SecurityConfigurationCRUD_CLI(t *testing.T) {
	runCLI(t, awsCLI("glue", "create-security-configuration",
		"--name", "glue-cli-secconf",
		"--encryption-configuration", `{"S3Encryption":[{"S3EncryptionMode":"SSE-S3"}]}`,
	))
	t.Cleanup(func() {
		runCLIIgnore(awsCLI("glue", "delete-security-configuration", "--name", "glue-cli-secconf"))
	})

	out := runCLI(t, awsCLI("glue", "get-security-configuration", "--name", "glue-cli-secconf"))
	var get struct {
		SecurityConfiguration struct {
			Name                    string `json:"Name"`
			EncryptionConfiguration struct {
				S3Encryption []struct {
					S3EncryptionMode string `json:"S3EncryptionMode"`
				} `json:"S3Encryption"`
			} `json:"EncryptionConfiguration"`
		} `json:"SecurityConfiguration"`
	}
	parseJSON(t, out, &get)
	assert.Equal(t, "glue-cli-secconf", get.SecurityConfiguration.Name)
	require.Len(t, get.SecurityConfiguration.EncryptionConfiguration.S3Encryption, 1)
	assert.Equal(t, "SSE-S3", get.SecurityConfiguration.EncryptionConfiguration.S3Encryption[0].S3EncryptionMode)

	out = runCLI(t, awsCLI("glue", "get-security-configurations"))
	var list struct {
		SecurityConfigurations []struct {
			Name string `json:"Name"`
		} `json:"SecurityConfigurations"`
	}
	parseJSON(t, out, &list)
	found := false
	for _, sc := range list.SecurityConfigurations {
		if sc.Name == "glue-cli-secconf" {
			found = true
		}
	}
	assert.True(t, found)
}

func TestGlue_WorkflowLifecycle_CLI(t *testing.T) {
	runCLI(t, awsCLI("glue", "create-workflow",
		"--name", "glue-cli-wf",
		"--description", "cli workflow",
		"--default-run-properties", `{"env":"test"}`,
	))
	t.Cleanup(func() {
		runCLIIgnore(awsCLI("glue", "delete-workflow", "--name", "glue-cli-wf"))
	})

	out := runCLI(t, awsCLI("glue", "get-workflow", "--name", "glue-cli-wf"))
	var get struct {
		Workflow struct {
			Name                 string            `json:"Name"`
			Description          string            `json:"Description"`
			DefaultRunProperties map[string]string `json:"DefaultRunProperties"`
		} `json:"Workflow"`
	}
	parseJSON(t, out, &get)
	assert.Equal(t, "glue-cli-wf", get.Workflow.Name)
	assert.Equal(t, "cli workflow", get.Workflow.Description)
	assert.Equal(t, "test", get.Workflow.DefaultRunProperties["env"])

	out = runCLI(t, awsCLI("glue", "list-workflows"))
	var list struct {
		Workflows []string `json:"Workflows"`
	}
	parseJSON(t, out, &list)
	found := false
	for _, n := range list.Workflows {
		if n == "glue-cli-wf" {
			found = true
		}
	}
	assert.True(t, found)

	out = runCLI(t, awsCLI("glue", "start-workflow-run", "--name", "glue-cli-wf"))
	var start struct {
		RunId string `json:"RunId"`
	}
	parseJSON(t, out, &start)
	require.NotEmpty(t, start.RunId)

	out = runCLI(t, awsCLI("glue", "get-workflow-run", "--name", "glue-cli-wf", "--run-id", start.RunId))
	var run struct {
		Run struct {
			WorkflowRunId string `json:"WorkflowRunId"`
			Status        string `json:"Status"`
		} `json:"Run"`
	}
	parseJSON(t, out, &run)
	assert.Equal(t, start.RunId, run.Run.WorkflowRunId)
	assert.Equal(t, "COMPLETED", run.Run.Status)
}

func TestGlue_ClassifierCRUD_CLI(t *testing.T) {
	runCLI(t, awsCLI("glue", "create-classifier",
		"--csv-classifier", `{"Name":"glue-cli-classifier","Delimiter":",","ContainsHeader":"PRESENT","Header":["a","b"]}`,
	))
	t.Cleanup(func() {
		runCLIIgnore(awsCLI("glue", "delete-classifier", "--name", "glue-cli-classifier"))
	})

	out := runCLI(t, awsCLI("glue", "get-classifier", "--name", "glue-cli-classifier"))
	var get struct {
		Classifier struct {
			CsvClassifier struct {
				Name      string `json:"Name"`
				Delimiter string `json:"Delimiter"`
			} `json:"CsvClassifier"`
		} `json:"Classifier"`
	}
	parseJSON(t, out, &get)
	assert.Equal(t, "glue-cli-classifier", get.Classifier.CsvClassifier.Name)
	assert.Equal(t, ",", get.Classifier.CsvClassifier.Delimiter)

	runCLI(t, awsCLI("glue", "update-classifier",
		"--csv-classifier", `{"Name":"glue-cli-classifier","Delimiter":";"}`,
	))
	out = runCLI(t, awsCLI("glue", "get-classifier", "--name", "glue-cli-classifier"))
	parseJSON(t, out, &get)
	assert.Equal(t, ";", get.Classifier.CsvClassifier.Delimiter)

	out = runCLI(t, awsCLI("glue", "get-classifiers"))
	var list struct {
		Classifiers []struct {
			CsvClassifier struct {
				Name string `json:"Name"`
			} `json:"CsvClassifier"`
		} `json:"Classifiers"`
	}
	parseJSON(t, out, &list)
	found := false
	for _, cl := range list.Classifiers {
		if cl.CsvClassifier.Name == "glue-cli-classifier" {
			found = true
		}
	}
	assert.True(t, found)
}

func TestGlue_UserDefinedFunctionCRUD_CLI(t *testing.T) {
	runCLI(t, awsCLI("glue", "create-database", "--database-input", `{"Name":"glue-cli-udf-db"}`))
	t.Cleanup(func() {
		runCLIIgnore(awsCLI("glue", "delete-user-defined-function",
			"--database-name", "glue-cli-udf-db", "--function-name", "glue-cli-udf"))
		runCLIIgnore(awsCLI("glue", "delete-database", "--name", "glue-cli-udf-db"))
	})

	runCLI(t, awsCLI("glue", "create-user-defined-function",
		"--database-name", "glue-cli-udf-db",
		"--function-input", `{"FunctionName":"glue-cli-udf","ClassName":"com.example.MyUDF","OwnerName":"owner","OwnerType":"USER","ResourceUris":[{"ResourceType":"JAR","Uri":"s3://bucket/udf.jar"}]}`,
	))

	out := runCLI(t, awsCLI("glue", "get-user-defined-function",
		"--database-name", "glue-cli-udf-db", "--function-name", "glue-cli-udf"))
	var get struct {
		UserDefinedFunction struct {
			FunctionName string `json:"FunctionName"`
			ClassName    string `json:"ClassName"`
			ResourceUris []struct {
				Uri string `json:"Uri"`
			} `json:"ResourceUris"`
		} `json:"UserDefinedFunction"`
	}
	parseJSON(t, out, &get)
	assert.Equal(t, "glue-cli-udf", get.UserDefinedFunction.FunctionName)
	assert.Equal(t, "com.example.MyUDF", get.UserDefinedFunction.ClassName)
	require.Len(t, get.UserDefinedFunction.ResourceUris, 1)
	assert.Equal(t, "s3://bucket/udf.jar", get.UserDefinedFunction.ResourceUris[0].Uri)

	out = runCLI(t, awsCLI("glue", "get-user-defined-functions",
		"--database-name", "glue-cli-udf-db", "--pattern", "*"))
	var list struct {
		UserDefinedFunctions []struct {
			FunctionName string `json:"FunctionName"`
		} `json:"UserDefinedFunctions"`
	}
	parseJSON(t, out, &list)
	found := false
	for _, f := range list.UserDefinedFunctions {
		if f.FunctionName == "glue-cli-udf" {
			found = true
		}
	}
	assert.True(t, found)
}

func TestGlue_SchemaRegistry_CLI(t *testing.T) {
	runCLI(t, awsCLI("glue", "create-registry",
		"--registry-name", "glue-cli-registry", "--description", "cli registry"))
	t.Cleanup(func() {
		runCLIIgnore(awsCLI("glue", "delete-schema",
			"--schema-id", `{"RegistryName":"glue-cli-registry","SchemaName":"glue-cli-schema"}`))
		runCLIIgnore(awsCLI("glue", "delete-registry",
			"--registry-id", `{"RegistryName":"glue-cli-registry"}`))
	})

	out := runCLI(t, awsCLI("glue", "get-registry",
		"--registry-id", `{"RegistryName":"glue-cli-registry"}`))
	var getReg struct {
		RegistryName string `json:"RegistryName"`
		RegistryArn  string `json:"RegistryArn"`
	}
	parseJSON(t, out, &getReg)
	assert.Equal(t, "glue-cli-registry", getReg.RegistryName)
	assert.NotEmpty(t, getReg.RegistryArn)

	out = runCLI(t, awsCLI("glue", "list-registries"))
	var listReg struct {
		Registries []struct {
			RegistryName string `json:"RegistryName"`
		} `json:"Registries"`
	}
	parseJSON(t, out, &listReg)
	foundReg := false
	for _, r := range listReg.Registries {
		if r.RegistryName == "glue-cli-registry" {
			foundReg = true
		}
	}
	assert.True(t, foundReg)

	out = runCLI(t, awsCLI("glue", "create-schema",
		"--registry-id", `{"RegistryName":"glue-cli-registry"}`,
		"--schema-name", "glue-cli-schema",
		"--data-format", "AVRO",
		"--compatibility", "BACKWARD",
		"--schema-definition", `{"type":"record","name":"r","fields":[]}`,
	))
	var createSchema struct {
		SchemaName string `json:"SchemaName"`
		SchemaArn  string `json:"SchemaArn"`
	}
	parseJSON(t, out, &createSchema)
	assert.Equal(t, "glue-cli-schema", createSchema.SchemaName)
	assert.NotEmpty(t, createSchema.SchemaArn)

	out = runCLI(t, awsCLI("glue", "get-schema",
		"--schema-id", `{"RegistryName":"glue-cli-registry","SchemaName":"glue-cli-schema"}`))
	var getSchema struct {
		SchemaName    string `json:"SchemaName"`
		DataFormat    string `json:"DataFormat"`
		Compatibility string `json:"Compatibility"`
	}
	parseJSON(t, out, &getSchema)
	assert.Equal(t, "glue-cli-schema", getSchema.SchemaName)
	assert.Equal(t, "AVRO", getSchema.DataFormat)
	assert.Equal(t, "BACKWARD", getSchema.Compatibility)
}

func TestGlue_TableVersionsAndIndexes_CLI(t *testing.T) {
	db := "glue-cli-ver-db"
	tbl := "glue-cli-ver-tbl"
	runCLI(t, awsCLI("glue", "create-database", "--database-input", `{"Name":"`+db+`"}`))
	t.Cleanup(func() {
		runCLI(t, awsCLI("glue", "delete-table", "--database-name", db, "--name", tbl))
		runCLI(t, awsCLI("glue", "delete-database", "--name", db))
	})
	runCLI(t, awsCLI("glue", "create-table",
		"--database-name", db,
		"--table-input", `{"Name":"`+tbl+`","StorageDescriptor":{"Location":"s3://bucket/v1/"},"PartitionKeys":[{"Name":"dt","Type":"string"}]}`,
	))
	runCLI(t, awsCLI("glue", "update-table",
		"--database-name", db,
		"--table-input", `{"Name":"`+tbl+`","StorageDescriptor":{"Location":"s3://bucket/v2/"}}`,
	))

	out := runCLI(t, awsCLI("glue", "get-table-versions", "--database-name", db, "--table-name", tbl))
	var vers struct {
		TableVersions []struct {
			VersionId string `json:"VersionId"`
		} `json:"TableVersions"`
	}
	parseJSON(t, out, &vers)
	require.Len(t, vers.TableVersions, 2)
	assert.Equal(t, "1", vers.TableVersions[0].VersionId)
	assert.Equal(t, "2", vers.TableVersions[1].VersionId)

	out = runCLI(t, awsCLI("glue", "get-table-version",
		"--database-name", db, "--table-name", tbl, "--version-id", "1"))
	var gv struct {
		TableVersion struct {
			VersionId string `json:"VersionId"`
			Table     struct {
				StorageDescriptor struct {
					Location string `json:"Location"`
				} `json:"StorageDescriptor"`
			} `json:"Table"`
		} `json:"TableVersion"`
	}
	parseJSON(t, out, &gv)
	assert.Equal(t, "1", gv.TableVersion.VersionId)
	assert.Equal(t, "s3://bucket/v1/", gv.TableVersion.Table.StorageDescriptor.Location)

	runCLI(t, awsCLI("glue", "delete-table-version",
		"--database-name", db, "--table-name", tbl, "--version-id", "1"))

	out = runCLI(t, awsCLI("glue", "batch-delete-table-version",
		"--database-name", db, "--table-name", tbl, "--version-ids", "999"))
	var bd struct {
		Errors []struct {
			VersionId string `json:"VersionId"`
		} `json:"Errors"`
	}
	parseJSON(t, out, &bd)
	require.Len(t, bd.Errors, 1)
	assert.Equal(t, "999", bd.Errors[0].VersionId)

	// Partition index lifecycle.
	runCLI(t, awsCLI("glue", "create-partition-index",
		"--database-name", db, "--table-name", tbl,
		"--partition-index", `{"IndexName":"dt-index","Keys":["dt"]}`))

	out = runCLI(t, awsCLI("glue", "get-partition-indexes", "--database-name", db, "--table-name", tbl))
	var idx struct {
		PartitionIndexDescriptorList []struct {
			IndexName   string `json:"IndexName"`
			IndexStatus string `json:"IndexStatus"`
			Keys        []struct {
				Name string `json:"Name"`
				Type string `json:"Type"`
			} `json:"Keys"`
		} `json:"PartitionIndexDescriptorList"`
	}
	parseJSON(t, out, &idx)
	require.Len(t, idx.PartitionIndexDescriptorList, 1)
	assert.Equal(t, "dt-index", idx.PartitionIndexDescriptorList[0].IndexName)
	assert.Equal(t, "ACTIVE", idx.PartitionIndexDescriptorList[0].IndexStatus)
	require.Len(t, idx.PartitionIndexDescriptorList[0].Keys, 1)
	assert.Equal(t, "dt", idx.PartitionIndexDescriptorList[0].Keys[0].Name)

	runCLI(t, awsCLI("glue", "delete-partition-index",
		"--database-name", db, "--table-name", tbl, "--index-name", "dt-index"))
	out = runCLI(t, awsCLI("glue", "get-partition-indexes", "--database-name", db, "--table-name", tbl))
	parseJSON(t, out, &idx)
	assert.Empty(t, idx.PartitionIndexDescriptorList)
}

func TestGlue_ColumnStatistics_CLI(t *testing.T) {
	db := "glue-cli-stats-db"
	tbl := "glue-cli-stats-tbl"
	runCLI(t, awsCLI("glue", "create-database", "--database-input", `{"Name":"`+db+`"}`))
	t.Cleanup(func() {
		runCLI(t, awsCLI("glue", "delete-table", "--database-name", db, "--name", tbl))
		runCLI(t, awsCLI("glue", "delete-database", "--name", db))
	})
	runCLI(t, awsCLI("glue", "create-table",
		"--database-name", db,
		"--table-input", `{"Name":"`+tbl+`","StorageDescriptor":{"Location":"s3://bucket/stats/","Columns":[{"Name":"id","Type":"bigint"}]},"PartitionKeys":[{"Name":"dt","Type":"string"}]}`,
	))

	statsList := `[{"ColumnName":"id","ColumnType":"bigint","AnalyzedTime":1700000000,"StatisticsData":{"Type":"LONG","LongColumnStatisticsData":{"MinimumValue":1,"MaximumValue":100,"NumberOfNulls":0,"NumberOfDistinctValues":100}}}]`

	runCLI(t, awsCLI("glue", "update-column-statistics-for-table",
		"--database-name", db, "--table-name", tbl,
		"--column-statistics-list", statsList))

	out := runCLI(t, awsCLI("glue", "get-column-statistics-for-table",
		"--database-name", db, "--table-name", tbl, "--column-names", "id"))
	var gcs struct {
		ColumnStatisticsList []struct {
			ColumnName     string `json:"ColumnName"`
			StatisticsData struct {
				LongColumnStatisticsData struct {
					MaximumValue int64 `json:"MaximumValue"`
				} `json:"LongColumnStatisticsData"`
			} `json:"StatisticsData"`
		} `json:"ColumnStatisticsList"`
	}
	parseJSON(t, out, &gcs)
	require.Len(t, gcs.ColumnStatisticsList, 1)
	assert.Equal(t, "id", gcs.ColumnStatisticsList[0].ColumnName)
	assert.Equal(t, int64(100), gcs.ColumnStatisticsList[0].StatisticsData.LongColumnStatisticsData.MaximumValue)

	runCLI(t, awsCLI("glue", "delete-column-statistics-for-table",
		"--database-name", db, "--table-name", tbl, "--column-name", "id"))
	out = runCLI(t, awsCLI("glue", "get-column-statistics-for-table",
		"--database-name", db, "--table-name", tbl, "--column-names", "id"))
	parseJSON(t, out, &gcs)
	assert.Empty(t, gcs.ColumnStatisticsList)

	// Partition-scoped statistics.
	runCLI(t, awsCLI("glue", "create-partition",
		"--database-name", db, "--table-name", tbl,
		"--partition-input", `{"Values":["2024-01-01"],"StorageDescriptor":{"Location":"s3://bucket/stats/dt=2024-01-01/"}}`))

	runCLI(t, awsCLI("glue", "update-column-statistics-for-partition",
		"--database-name", db, "--table-name", tbl,
		"--partition-values", "2024-01-01",
		"--column-statistics-list", statsList))

	out = runCLI(t, awsCLI("glue", "get-column-statistics-for-partition",
		"--database-name", db, "--table-name", tbl,
		"--partition-values", "2024-01-01", "--column-names", "id"))
	parseJSON(t, out, &gcs)
	require.Len(t, gcs.ColumnStatisticsList, 1)
	assert.Equal(t, "id", gcs.ColumnStatisticsList[0].ColumnName)

	runCLI(t, awsCLI("glue", "delete-column-statistics-for-partition",
		"--database-name", db, "--table-name", tbl,
		"--partition-values", "2024-01-01", "--column-name", "id"))
	out = runCLI(t, awsCLI("glue", "get-column-statistics-for-partition",
		"--database-name", db, "--table-name", tbl,
		"--partition-values", "2024-01-01", "--column-names", "id"))
	parseJSON(t, out, &gcs)
	assert.Empty(t, gcs.ColumnStatisticsList)
}

func TestGlue_ResourcePolicyAndCatalog_CLI(t *testing.T) {
	policy := `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"AWS":"arn:aws:iam::123456789012:root"},"Action":"glue:GetTable","Resource":"*"}]}`
	out := runCLI(t, awsCLI("glue", "put-resource-policy", "--policy-in-json", policy))
	var put struct {
		PolicyHash string `json:"PolicyHash"`
	}
	parseJSON(t, out, &put)
	assert.NotEmpty(t, put.PolicyHash)
	t.Cleanup(func() {
		runCLIIgnore(awsCLI("glue", "delete-resource-policy"))
	})

	out = runCLI(t, awsCLI("glue", "get-resource-policy"))
	var get struct {
		PolicyInJson string `json:"PolicyInJson"`
		PolicyHash   string `json:"PolicyHash"`
	}
	parseJSON(t, out, &get)
	assert.Equal(t, policy, get.PolicyInJson)
	assert.NotEmpty(t, get.PolicyHash)

	runCLI(t, awsCLI("glue", "delete-resource-policy"))

	// Data catalog encryption + catalog import status.
	runCLI(t, awsCLI("glue", "put-data-catalog-encryption-settings",
		"--data-catalog-encryption-settings",
		`{"EncryptionAtRest":{"CatalogEncryptionMode":"SSE-KMS","SseAwsKmsKeyId":"arn:aws:kms:us-east-1:123456789012:key/abc"},"ConnectionPasswordEncryption":{"ReturnConnectionPasswordEncrypted":true}}`))

	out = runCLI(t, awsCLI("glue", "get-data-catalog-encryption-settings"))
	var enc struct {
		DataCatalogEncryptionSettings struct {
			EncryptionAtRest struct {
				CatalogEncryptionMode string `json:"CatalogEncryptionMode"`
			} `json:"EncryptionAtRest"`
		} `json:"DataCatalogEncryptionSettings"`
	}
	parseJSON(t, out, &enc)
	assert.Equal(t, "SSE-KMS", enc.DataCatalogEncryptionSettings.EncryptionAtRest.CatalogEncryptionMode)

	runCLI(t, awsCLI("glue", "import-catalog-to-glue"))
	out = runCLI(t, awsCLI("glue", "get-catalog-import-status"))
	var status struct {
		ImportStatus struct {
			ImportCompleted bool `json:"ImportCompleted"`
		} `json:"ImportStatus"`
	}
	parseJSON(t, out, &status)
	assert.True(t, status.ImportStatus.ImportCompleted)
}

func TestGlue_SchemaVersions_CLI(t *testing.T) {
	reg := "glue-cli-sv-registry"
	sch := "glue-cli-sv-schema"
	runCLI(t, awsCLI("glue", "create-registry", "--registry-name", reg))
	t.Cleanup(func() {
		runCLI(t, awsCLI("glue", "delete-schema",
			"--schema-id", `{"RegistryName":"`+reg+`","SchemaName":"`+sch+`"}`))
		runCLI(t, awsCLI("glue", "delete-registry",
			"--registry-id", `{"RegistryName":"`+reg+`"}`))
	})

	def1 := `{"type":"record","name":"r","fields":[{"name":"a","type":"int"}]}`
	def2 := `{"type":"record","name":"r","fields":[{"name":"a","type":"int"},{"name":"b","type":["null","string"],"default":null}]}`

	runCLI(t, awsCLI("glue", "create-schema",
		"--registry-id", `{"RegistryName":"`+reg+`"}`,
		"--schema-name", sch,
		"--data-format", "AVRO",
		"--compatibility", "BACKWARD",
		"--schema-definition", def1))

	out := runCLI(t, awsCLI("glue", "register-schema-version",
		"--schema-id", `{"RegistryName":"`+reg+`","SchemaName":"`+sch+`"}`,
		"--schema-definition", def2))
	var rv struct {
		SchemaVersionId string `json:"SchemaVersionId"`
		VersionNumber   int64  `json:"VersionNumber"`
	}
	parseJSON(t, out, &rv)
	assert.Equal(t, int64(2), rv.VersionNumber)
	assert.NotEmpty(t, rv.SchemaVersionId)

	out = runCLI(t, awsCLI("glue", "list-schema-versions",
		"--schema-id", `{"RegistryName":"`+reg+`","SchemaName":"`+sch+`"}`))
	var ls struct {
		Schemas []struct {
			VersionNumber int64 `json:"VersionNumber"`
		} `json:"Schemas"`
	}
	parseJSON(t, out, &ls)
	require.Len(t, ls.Schemas, 2)

	out = runCLI(t, awsCLI("glue", "get-schema-version",
		"--schema-version-id", rv.SchemaVersionId))
	var gsv struct {
		SchemaDefinition string `json:"SchemaDefinition"`
		VersionNumber    int64  `json:"VersionNumber"`
	}
	parseJSON(t, out, &gsv)
	assert.Equal(t, def2, gsv.SchemaDefinition)
	assert.Equal(t, int64(2), gsv.VersionNumber)

	out = runCLI(t, awsCLI("glue", "get-schema-by-definition",
		"--schema-id", `{"RegistryName":"`+reg+`","SchemaName":"`+sch+`"}`,
		"--schema-definition", def2))
	var gbd struct {
		SchemaVersionId string `json:"SchemaVersionId"`
	}
	parseJSON(t, out, &gbd)
	assert.Equal(t, rv.SchemaVersionId, gbd.SchemaVersionId)

	runCLI(t, awsCLI("glue", "delete-schema-versions",
		"--schema-id", `{"RegistryName":"`+reg+`","SchemaName":"`+sch+`"}`,
		"--versions", "2"))
	out = runCLI(t, awsCLI("glue", "list-schema-versions",
		"--schema-id", `{"RegistryName":"`+reg+`","SchemaName":"`+sch+`"}`))
	parseJSON(t, out, &ls)
	require.Len(t, ls.Schemas, 1)
	assert.Equal(t, int64(1), ls.Schemas[0].VersionNumber)
}
