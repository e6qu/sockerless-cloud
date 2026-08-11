package aws_cli_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestEFS_CreateAndDescribeFileSystem(t *testing.T) {
	out := runCLI(t, awsCLI("efs", "create-file-system",
		"--creation-token", "cli-test-fs",
		"--tags", "Key=Name,Value=cli-test-fs",
		"--output", "json",
	))

	var createResult struct {
		FileSystemId   string `json:"FileSystemId"`
		LifeCycleState string `json:"LifeCycleState"`
		Name           string `json:"Name"`
	}
	parseJSON(t, out, &createResult)
	require.NotEmpty(t, createResult.FileSystemId)
	assert.Equal(t, "available", createResult.LifeCycleState)

	// Describe
	out = runCLI(t, awsCLI("efs", "describe-file-systems",
		"--file-system-id", createResult.FileSystemId,
		"--output", "json",
	))

	var descResult struct {
		FileSystems []struct {
			FileSystemId string `json:"FileSystemId"`
			Name         string `json:"Name"`
		} `json:"FileSystems"`
	}
	parseJSON(t, out, &descResult)
	require.Len(t, descResult.FileSystems, 1)
	assert.Equal(t, createResult.FileSystemId, descResult.FileSystems[0].FileSystemId)

	// Cleanup
	runCLI(t, awsCLI("efs", "delete-file-system",
		"--file-system-id", createResult.FileSystemId,
	))
}

func TestEFS_CreateMountTarget(t *testing.T) {
	out := runCLI(t, awsCLI("efs", "create-file-system",
		"--creation-token", "cli-mount-test",
		"--output", "json",
	))

	var fs struct {
		FileSystemId string `json:"FileSystemId"`
	}
	parseJSON(t, out, &fs)

	out = runCLI(t, awsCLI("efs", "create-mount-target",
		"--file-system-id", fs.FileSystemId,
		"--subnet-id", "subnet-0123456789abcdef0",
		"--output", "json",
	))

	var mt struct {
		MountTargetId  string `json:"MountTargetId"`
		FileSystemId   string `json:"FileSystemId"`
		LifeCycleState string `json:"LifeCycleState"`
	}
	parseJSON(t, out, &mt)
	require.NotEmpty(t, mt.MountTargetId)
	assert.Equal(t, fs.FileSystemId, mt.FileSystemId)
	assert.Equal(t, "available", mt.LifeCycleState)

	// Describe mount targets
	out = runCLI(t, awsCLI("efs", "describe-mount-targets",
		"--file-system-id", fs.FileSystemId,
		"--output", "json",
	))

	var descResult struct {
		MountTargets []struct {
			MountTargetId string `json:"MountTargetId"`
		} `json:"MountTargets"`
	}
	parseJSON(t, out, &descResult)
	require.Len(t, descResult.MountTargets, 1)

	// Cleanup
	runCLI(t, awsCLI("efs", "delete-file-system",
		"--file-system-id", fs.FileSystemId,
	))
}

func TestEFS_CreateAccessPoint(t *testing.T) {
	out := runCLI(t, awsCLI("efs", "create-file-system",
		"--creation-token", "cli-ap-test",
		"--output", "json",
	))

	var fs struct {
		FileSystemId string `json:"FileSystemId"`
	}
	parseJSON(t, out, &fs)

	out = runCLI(t, awsCLI("efs", "create-access-point",
		"--file-system-id", fs.FileSystemId,
		"--posix-user", "Uid=1000,Gid=1000",
		"--root-directory", `Path=/data,CreationInfo={OwnerUid=1000,OwnerGid=1000,Permissions=755}`,
		"--tags", "Key=Name,Value=cli-ap",
		"--output", "json",
	))

	var ap struct {
		AccessPointId  string `json:"AccessPointId"`
		FileSystemId   string `json:"FileSystemId"`
		LifeCycleState string `json:"LifeCycleState"`
	}
	parseJSON(t, out, &ap)
	require.NotEmpty(t, ap.AccessPointId)
	assert.Equal(t, fs.FileSystemId, ap.FileSystemId)

	// Describe access points
	out = runCLI(t, awsCLI("efs", "describe-access-points",
		"--file-system-id", fs.FileSystemId,
		"--output", "json",
	))

	var descResult struct {
		AccessPoints []struct {
			AccessPointId string `json:"AccessPointId"`
		} `json:"AccessPoints"`
	}
	parseJSON(t, out, &descResult)
	require.Len(t, descResult.AccessPoints, 1)

	// Cleanup
	runCLI(t, awsCLI("efs", "delete-access-point",
		"--access-point-id", ap.AccessPointId,
	))
	runCLI(t, awsCLI("efs", "delete-file-system",
		"--file-system-id", fs.FileSystemId,
	))
}

func TestEFSCLI_PolicyBackupAndTags(t *testing.T) {
	out := runCLI(t, awsCLI("efs", "create-file-system",
		"--creation-token", "cli-policy-fs",
		"--output", "json",
	))
	var fs struct {
		FileSystemId string `json:"FileSystemId"`
	}
	parseJSON(t, out, &fs)
	t.Cleanup(func() {
		runCLI(t, awsCLI("efs", "delete-file-system", "--file-system-id", fs.FileSystemId))
	})

	// File system policy put/describe/delete.
	policyDoc := `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"AWS":"*"},"Action":"elasticfilesystem:ClientMount","Resource":"*"}]}`
	out = runCLI(t, awsCLI("efs", "put-file-system-policy",
		"--file-system-id", fs.FileSystemId,
		"--policy", policyDoc,
		"--output", "json",
	))
	var putPol struct {
		FileSystemId string `json:"FileSystemId"`
		Policy       string `json:"Policy"`
	}
	parseJSON(t, out, &putPol)
	assert.Equal(t, fs.FileSystemId, putPol.FileSystemId)

	out = runCLI(t, awsCLI("efs", "describe-file-system-policy",
		"--file-system-id", fs.FileSystemId,
		"--output", "json",
	))
	var descPol struct {
		Policy string `json:"Policy"`
	}
	parseJSON(t, out, &descPol)
	assert.NotEmpty(t, descPol.Policy)

	runCLI(t, awsCLI("efs", "delete-file-system-policy", "--file-system-id", fs.FileSystemId))

	// Backup policy put/describe.
	out = runCLI(t, awsCLI("efs", "put-backup-policy",
		"--file-system-id", fs.FileSystemId,
		"--backup-policy", "Status=ENABLED",
		"--output", "json",
	))
	var putBak struct {
		BackupPolicy struct {
			Status string `json:"Status"`
		} `json:"BackupPolicy"`
	}
	parseJSON(t, out, &putBak)
	assert.Equal(t, "ENABLED", putBak.BackupPolicy.Status)

	out = runCLI(t, awsCLI("efs", "describe-backup-policy",
		"--file-system-id", fs.FileSystemId,
		"--output", "json",
	))
	var descBak struct {
		BackupPolicy struct {
			Status string `json:"Status"`
		} `json:"BackupPolicy"`
	}
	parseJSON(t, out, &descBak)
	assert.Equal(t, "ENABLED", descBak.BackupPolicy.Status)

	// Resource-ARN tagging API.
	runCLI(t, awsCLI("efs", "tag-resource",
		"--resource-id", fs.FileSystemId,
		"--tags", "Key=env,Value=prod", "Key=team,Value=infra",
	))
	out = runCLI(t, awsCLI("efs", "list-tags-for-resource",
		"--resource-id", fs.FileSystemId,
		"--output", "json",
	))
	var listTags struct {
		Tags []struct {
			Key   string `json:"Key"`
			Value string `json:"Value"`
		} `json:"Tags"`
	}
	parseJSON(t, out, &listTags)
	require.GreaterOrEqual(t, len(listTags.Tags), 2)

	runCLI(t, awsCLI("efs", "untag-resource",
		"--resource-id", fs.FileSystemId,
		"--tag-keys", "team",
	))

	// Legacy file-system tagging API: create-tags / describe-tags / delete-tags.
	runCLI(t, awsCLI("efs", "create-tags",
		"--file-system-id", fs.FileSystemId,
		"--tags", "Key=legacy,Value=yes",
	))
	out = runCLI(t, awsCLI("efs", "describe-tags",
		"--file-system-id", fs.FileSystemId,
		"--output", "json",
	))
	var descTags struct {
		Tags []struct {
			Key   string `json:"Key"`
			Value string `json:"Value"`
		} `json:"Tags"`
	}
	parseJSON(t, out, &descTags)
	var foundLegacy bool
	for _, tg := range descTags.Tags {
		if tg.Key == "legacy" {
			foundLegacy = true
		}
	}
	assert.True(t, foundLegacy)

	runCLI(t, awsCLI("efs", "delete-tags",
		"--file-system-id", fs.FileSystemId,
		"--tag-keys", "legacy",
	))
}

func TestEFSCLI_Replication(t *testing.T) {
	out := runCLI(t, awsCLI("efs", "create-file-system",
		"--creation-token", "cli-repl-src",
		"--output", "json",
	))
	var src struct {
		FileSystemId string `json:"FileSystemId"`
	}
	parseJSON(t, out, &src)

	out = runCLI(t, awsCLI("efs", "create-replication-configuration",
		"--source-file-system-id", src.FileSystemId,
		"--destinations", "Region=us-west-2",
		"--output", "json",
	))
	var repl struct {
		SourceFileSystemId string `json:"SourceFileSystemId"`
		Destinations       []struct {
			FileSystemId string `json:"FileSystemId"`
		} `json:"Destinations"`
	}
	parseJSON(t, out, &repl)
	require.Equal(t, src.FileSystemId, repl.SourceFileSystemId)
	require.Len(t, repl.Destinations, 1)
	destFsID := repl.Destinations[0].FileSystemId

	t.Cleanup(func() {
		runCLI(t, awsCLI("efs", "delete-replication-configuration", "--source-file-system-id", src.FileSystemId))
		runCLI(t, awsCLI("efs", "delete-file-system", "--file-system-id", src.FileSystemId))
		if destFsID != "" {
			runCLI(t, awsCLI("efs", "delete-file-system", "--file-system-id", destFsID))
		}
	})

	out = runCLI(t, awsCLI("efs", "describe-replication-configurations",
		"--file-system-id", src.FileSystemId,
		"--output", "json",
	))
	var descRepl struct {
		Replications []struct {
			SourceFileSystemId string `json:"SourceFileSystemId"`
		} `json:"Replications"`
	}
	parseJSON(t, out, &descRepl)
	require.Len(t, descRepl.Replications, 1)
	assert.Equal(t, src.FileSystemId, descRepl.Replications[0].SourceFileSystemId)
}

func TestEFSCLI_AccountPreferences(t *testing.T) {
	out := runCLI(t, awsCLI("efs", "put-account-preferences",
		"--resource-id-type", "LONG_ID",
		"--output", "json",
	))
	var putPref struct {
		ResourceIdPreference struct {
			ResourceIdType string `json:"ResourceIdType"`
		} `json:"ResourceIdPreference"`
	}
	parseJSON(t, out, &putPref)
	assert.Equal(t, "LONG_ID", putPref.ResourceIdPreference.ResourceIdType)

	out = runCLI(t, awsCLI("efs", "describe-account-preferences", "--output", "json"))
	var descPref struct {
		ResourceIdPreference struct {
			ResourceIdType string `json:"ResourceIdType"`
		} `json:"ResourceIdPreference"`
	}
	parseJSON(t, out, &descPref)
	assert.Equal(t, "LONG_ID", descPref.ResourceIdPreference.ResourceIdType)
}

func TestEFS_DeleteFileSystem(t *testing.T) {
	out := runCLI(t, awsCLI("efs", "create-file-system",
		"--creation-token", "cli-delete-test",
		"--output", "json",
	))

	var fs struct {
		FileSystemId string `json:"FileSystemId"`
	}
	parseJSON(t, out, &fs)

	runCLI(t, awsCLI("efs", "delete-file-system",
		"--file-system-id", fs.FileSystemId,
	))

	// Verify it's gone
	out = runCLI(t, awsCLI("efs", "describe-file-systems", "--output", "json"))
	var result struct {
		FileSystems []struct {
			FileSystemId string `json:"FileSystemId"`
		} `json:"FileSystems"`
	}
	parseJSON(t, out, &result)
	for _, f := range result.FileSystems {
		assert.NotEqual(t, fs.FileSystemId, f.FileSystemId)
	}
}
