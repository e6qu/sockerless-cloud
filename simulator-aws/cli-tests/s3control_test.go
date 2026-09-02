package aws_cli_test

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The S3 control plane is host-addressed: every operation carries the account
// id as a modeled endpoint host prefix, so these run through
// awsCLIHostPrefixed the way Cloud Map's data plane and Step Functions' sync
// executions do.

const s3ControlCLIAccount = "123456789012"

// s3ControlCLI runs one s3control operation for the test account. The account
// id is an operation parameter, so it follows the subcommand.
func s3ControlCLI(t *testing.T, args ...string) string {
	t.Helper()
	return runCLI(t, s3ControlCLICommand(args...))
}

func s3ControlCLICommand(args ...string) *exec.Cmd {
	full := append([]string{"s3control", args[0], "--account-id", s3ControlCLIAccount}, args[1:]...)
	return awsCLIHostPrefixed(full...)
}

// TestS3ControlCLI_AccessPointAndObjectLambda covers the access point family
// the CLI manages, and the Object Lambda access point layered over it.
func TestS3ControlCLI_AccessPointAndObjectLambda(t *testing.T) {
	bucket, apName, olapName := "cli-olap-bucket", "cli-olap-support", "cli-olap"
	fnName := "cli-olap-transform"
	runCLI(t, awsCLI("s3api", "create-bucket", "--bucket", bucket))
	runCLI(t, awsCLI("lambda", "create-function",
		"--function-name", fnName,
		"--package-type", "Image",
		"--code", "ImageUri="+lambdaHandlerImageName,
		"--role", "arn:aws:iam::"+s3ControlCLIAccount+":role/cli-olap"))
	t.Cleanup(func() {
		_ = awsCLI("lambda", "delete-function", "--function-name", fnName).Run()
	})

	created := s3ControlCLI(t, "create-access-point", "--name", apName, "--bucket", bucket)
	var createdAP struct {
		AccessPointArn string `json:"AccessPointArn"`
		Alias          string `json:"Alias"`
	}
	require.NoError(t, json.Unmarshal([]byte(created), &createdAP))
	assert.Contains(t, createdAP.AccessPointArn, ":accesspoint/"+apName)
	t.Cleanup(func() {
		_ = awsCLIHostPrefixed("s3control", "--account-id", s3ControlCLIAccount,
			"delete-access-point", "--name", apName).Run()
	})

	got := s3ControlCLI(t, "get-access-point", "--name", apName)
	assert.Contains(t, got, bucket)
	assert.Contains(t, got, "Internet")

	listed := s3ControlCLI(t, "list-access-points", "--bucket", bucket)
	assert.Contains(t, listed, apName)

	// The Object Lambda access point is created from a JSON configuration
	// file, which is how the CLI takes a nested structure.
	configuration := fmt.Sprintf(`{
	  "SupportingAccessPoint": %q,
	  "TransformationConfigurations": [{
	    "Actions": ["GetObject"],
	    "ContentTransformation": {
	      "AwsLambda": {"FunctionArn": "arn:aws:lambda:us-east-1:%s:function:%s"}
	    }
	  }]
	}`, createdAP.AccessPointArn, s3ControlCLIAccount, fnName)
	configPath := filepath.Join(tmpDir, "olap-configuration.json")
	require.NoError(t, os.WriteFile(configPath, []byte(configuration), 0o644))

	s3ControlCLI(t, "create-access-point-for-object-lambda",
		"--name", olapName, "--configuration", "file://"+configPath)
	t.Cleanup(func() {
		_ = awsCLIHostPrefixed("s3control", "--account-id", s3ControlCLIAccount,
			"delete-access-point-for-object-lambda", "--name", olapName).Run()
	})

	readConfig := s3ControlCLI(t, "get-access-point-configuration-for-object-lambda", "--name", olapName)
	assert.Contains(t, readConfig, createdAP.AccessPointArn)
	assert.Contains(t, readConfig, fnName)

	listedOLAP := s3ControlCLI(t, "list-access-points-for-object-lambda")
	assert.Contains(t, listedOLAP, olapName)

	// The access point's scope, which narrows what it admits.
	scopePath := filepath.Join(tmpDir, "ap-scope.json")
	require.NoError(t, os.WriteFile(scopePath,
		[]byte(`{"Prefixes":["reports/"],"Permissions":["GetObject"]}`), 0o644))
	s3ControlCLI(t, "put-access-point-scope", "--name", apName, "--scope", "file://"+scopePath)
	scope := s3ControlCLI(t, "get-access-point-scope", "--name", apName)
	assert.Contains(t, scope, "reports/")
	assert.Contains(t, scope, "GetObject")
	s3ControlCLI(t, "delete-access-point-scope", "--name", apName)
}

// TestS3ControlCLI_StorageLens covers a Storage Lens dashboard configuration
// and a Storage Lens group.
func TestS3ControlCLI_StorageLens(t *testing.T) {
	configID, groupName := "cli-storage-lens", "cli-lens-group"

	configPath := filepath.Join(tmpDir, "storage-lens.json")
	require.NoError(t, os.WriteFile(configPath, []byte(fmt.Sprintf(`{
	  "Id": %q,
	  "IsEnabled": true,
	  "AccountLevel": {"BucketLevel": {}}
	}`, configID)), 0o644))
	s3ControlCLI(t, "put-storage-lens-configuration",
		"--config-id", configID, "--storage-lens-configuration", "file://"+configPath)
	t.Cleanup(func() {
		_ = awsCLIHostPrefixed("s3control", "--account-id", s3ControlCLIAccount,
			"delete-storage-lens-configuration", "--config-id", configID).Run()
	})

	got := s3ControlCLI(t, "get-storage-lens-configuration", "--config-id", configID)
	assert.Contains(t, got, configID)
	assert.Contains(t, got, "storage-lens/"+configID)

	listed := s3ControlCLI(t, "list-storage-lens-configurations")
	assert.Contains(t, listed, configID)

	groupPath := filepath.Join(tmpDir, "lens-group.json")
	require.NoError(t, os.WriteFile(groupPath, []byte(fmt.Sprintf(`{
	  "Name": %q,
	  "Filter": {"MatchAnyPrefix": ["logs/"]}
	}`, groupName)), 0o644))
	s3ControlCLI(t, "create-storage-lens-group", "--storage-lens-group", "file://"+groupPath)
	t.Cleanup(func() {
		_ = awsCLIHostPrefixed("s3control", "--account-id", s3ControlCLIAccount,
			"delete-storage-lens-group", "--name", groupName).Run()
	})

	group := s3ControlCLI(t, "get-storage-lens-group", "--name", groupName)
	assert.Contains(t, group, "logs/")
	assert.Contains(t, group, "storage-lens-group/"+groupName)

	groups := s3ControlCLI(t, "list-storage-lens-groups")
	assert.Contains(t, groups, groupName)
}

// TestS3ControlCLI_AccessGrants covers the Access Grants instance, a location,
// and a grant inside it.
func TestS3ControlCLI_AccessGrants(t *testing.T) {
	bucket, roleName := "cli-grants-bucket", "cli-grants-role"
	runCLI(t, awsCLI("s3api", "create-bucket", "--bucket", bucket))
	runCLI(t, awsCLI("iam", "create-role", "--role-name", roleName,
		"--assume-role-policy-document",
		`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"Service":"s3.amazonaws.com"},"Action":"sts:AssumeRole"}]}`))
	t.Cleanup(func() { _ = awsCLI("iam", "delete-role", "--role-name", roleName).Run() })
	roleArn := "arn:aws:iam::" + s3ControlCLIAccount + ":role/" + roleName

	s3ControlCLI(t, "create-access-grants-instance")
	t.Cleanup(func() {
		_ = awsCLIHostPrefixed("s3control", "--account-id", s3ControlCLIAccount,
			"delete-access-grants-instance").Run()
	})

	instance := s3ControlCLI(t, "get-access-grants-instance")
	assert.Contains(t, instance, "access-grants/default")

	location := s3ControlCLI(t, "create-access-grants-location",
		"--location-scope", "s3://"+bucket+"/data/*", "--iam-role-arn", roleArn)
	var createdLocation struct {
		AccessGrantsLocationId string `json:"AccessGrantsLocationId"`
	}
	require.NoError(t, json.Unmarshal([]byte(location), &createdLocation))
	require.NotEmpty(t, createdLocation.AccessGrantsLocationId)

	grant := s3ControlCLI(t, "create-access-grant",
		"--access-grants-location-id", createdLocation.AccessGrantsLocationId,
		"--grantee", "GranteeType=IAM,GranteeIdentifier=arn:aws:iam::"+s3ControlCLIAccount+":user/cli-analyst",
		"--permission", "READ")
	var createdGrant struct {
		AccessGrantId string `json:"AccessGrantId"`
		GrantScope    string `json:"GrantScope"`
	}
	require.NoError(t, json.Unmarshal([]byte(grant), &createdGrant))
	require.NotEmpty(t, createdGrant.AccessGrantId)
	assert.Equal(t, "s3://"+bucket+"/data/*", createdGrant.GrantScope)

	grants := s3ControlCLI(t, "list-access-grants")
	assert.Contains(t, grants, createdGrant.AccessGrantId)

	s3ControlCLI(t, "delete-access-grant", "--access-grant-id", createdGrant.AccessGrantId)
	s3ControlCLI(t, "delete-access-grants-location",
		"--access-grants-location-id", createdLocation.AccessGrantsLocationId)
}

// TestS3ControlCLI_MultiRegionAccessPoint covers the asynchronous endpoint:
// the create hands back a token, and describing that token is where the CLI
// learns the outcome.
func TestS3ControlCLI_MultiRegionAccessPoint(t *testing.T) {
	bucket, name := "cli-mrap-bucket", "cli-mrap"
	runCLI(t, awsCLI("s3api", "create-bucket", "--bucket", bucket))

	detailsPath := filepath.Join(tmpDir, "mrap-details.json")
	require.NoError(t, os.WriteFile(detailsPath, []byte(fmt.Sprintf(`{
	  "Name": %q,
	  "Regions": [{"Bucket": %q}]
	}`, name, bucket)), 0o644))
	created := s3ControlCLI(t, "create-multi-region-access-point",
		"--client-token", "cli-token-1", "--details", "file://"+detailsPath)
	var createdMRAP struct {
		RequestTokenARN string `json:"RequestTokenARN"`
	}
	require.NoError(t, json.Unmarshal([]byte(created), &createdMRAP))
	require.NotEmpty(t, createdMRAP.RequestTokenARN)
	t.Cleanup(func() {
		deletePath := filepath.Join(tmpDir, "mrap-delete.json")
		_ = os.WriteFile(deletePath, []byte(fmt.Sprintf(`{"Name": %q}`, name)), 0o644)
		_ = s3ControlCLICommand("delete-multi-region-access-point",
			"--client-token", "cli-token-cleanup", "--details", "file://"+deletePath).Run()
	})

	operation := s3ControlCLI(t, "describe-multi-region-access-point-operation",
		"--request-token-arn", createdMRAP.RequestTokenARN)
	assert.Contains(t, operation, "SUCCEEDED")
	assert.Contains(t, operation, "CreateMultiRegionAccessPoint")

	got := s3ControlCLI(t, "get-multi-region-access-point", "--name", name)
	assert.Contains(t, got, bucket)

	routes := s3ControlCLI(t, "get-multi-region-access-point-routes", "--mrap", name)
	assert.Contains(t, routes, bucket)

	s3ControlCLI(t, "submit-multi-region-access-point-routes", "--mrap", name,
		"--route-updates", "Bucket="+bucket+",TrafficDialPercentage=0")
	var updated struct {
		Routes []struct {
			Bucket                string `json:"Bucket"`
			TrafficDialPercentage int    `json:"TrafficDialPercentage"`
		} `json:"Routes"`
	}
	require.NoError(t, json.Unmarshal(
		[]byte(s3ControlCLI(t, "get-multi-region-access-point-routes", "--mrap", name)), &updated))
	require.Len(t, updated.Routes, 1)
	assert.Equal(t, 0, updated.Routes[0].TrafficDialPercentage)

	listed := s3ControlCLI(t, "list-multi-region-access-points")
	assert.Contains(t, listed, name)
}

// TestS3ControlCLI_BatchJob runs a batch job through the CLI: the manifest
// lists two objects, the job tags them, and the tags are readable afterwards.
func TestS3ControlCLI_BatchJob(t *testing.T) {
	bucket, roleName := "cli-batch-bucket", "cli-batch-role"
	runCLI(t, awsCLI("s3api", "create-bucket", "--bucket", bucket))
	runCLI(t, awsCLI("iam", "create-role", "--role-name", roleName,
		"--assume-role-policy-document",
		`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"Service":"batchoperations.s3.amazonaws.com"},"Action":"sts:AssumeRole"}]}`))
	t.Cleanup(func() { _ = awsCLI("iam", "delete-role", "--role-name", roleName).Run() })

	for _, key := range []string{"alpha.txt", "beta.txt"} {
		local := filepath.Join(tmpDir, key)
		require.NoError(t, os.WriteFile(local, []byte(key), 0o644))
		runCLI(t, awsCLI("s3api", "put-object", "--bucket", bucket, "--key", key, "--body", local))
	}
	manifestLocal := filepath.Join(tmpDir, "batch-manifest.csv")
	require.NoError(t, os.WriteFile(manifestLocal,
		[]byte(bucket+",alpha.txt\n"+bucket+",beta.txt\n"), 0o644))
	putManifest := runCLI(t, awsCLI("s3api", "put-object", "--bucket", bucket,
		"--key", "manifest.csv", "--body", manifestLocal))
	var manifestPut struct {
		ETag string `json:"ETag"`
	}
	require.NoError(t, json.Unmarshal([]byte(putManifest), &manifestPut))

	created := s3ControlCLI(t, "create-job",
		"--client-request-token", "cli-batch-token-1",
		"--priority", "10",
		"--role-arn", "arn:aws:iam::"+s3ControlCLIAccount+":role/"+roleName,
		"--operation", `{"S3PutObjectTagging":{"TagSet":[{"Key":"reviewed","Value":"yes"}]}}`,
		"--report", `{"Enabled":false}`,
		"--manifest", fmt.Sprintf(
			`{"Spec":{"Format":"S3BatchOperations_CSV_20180820","Fields":["Bucket","Key"]},`+
				`"Location":{"ObjectArn":"arn:aws:s3:::%s/manifest.csv","ETag":%q}}`,
			bucket, manifestPut.ETag))
	var createdJob struct {
		JobId string `json:"JobId"`
	}
	require.NoError(t, json.Unmarshal([]byte(created), &createdJob))
	require.NotEmpty(t, createdJob.JobId)

	described := s3ControlCLI(t, "describe-job", "--job-id", createdJob.JobId)
	assert.Contains(t, described, "Complete")
	var job struct {
		Job struct {
			ProgressSummary struct {
				NumberOfTasksSucceeded int `json:"NumberOfTasksSucceeded"`
			} `json:"ProgressSummary"`
		} `json:"Job"`
	}
	require.NoError(t, json.Unmarshal([]byte(described), &job))
	assert.Equal(t, 2, job.Job.ProgressSummary.NumberOfTasksSucceeded)

	// The job really tagged the objects, which is what the progress report is
	// a report of.
	tags := runCLI(t, awsCLI("s3api", "get-object-tagging", "--bucket", bucket, "--key", "alpha.txt"))
	assert.Contains(t, tags, "reviewed")

	s3ControlCLI(t, "put-job-tagging", "--job-id", createdJob.JobId,
		"--tags", "Key=owner,Value=data-team")
	jobTags := s3ControlCLI(t, "get-job-tagging", "--job-id", createdJob.JobId)
	assert.Contains(t, jobTags, "data-team")
	s3ControlCLI(t, "delete-job-tagging", "--job-id", createdJob.JobId)

	s3ControlCLI(t, "update-job-priority", "--job-id", createdJob.JobId, "--priority", "42")
	jobs := s3ControlCLI(t, "list-jobs", "--job-statuses", "Complete")
	assert.Contains(t, jobs, createdJob.JobId)
}

// TestS3ControlCLI_ListRegionalBuckets lists the account's regional buckets.
func TestS3ControlCLI_ListRegionalBuckets(t *testing.T) {
	bucket := "cli-regional-bucket"
	runCLI(t, awsCLI("s3api", "create-bucket", "--bucket", bucket))
	listed := s3ControlCLI(t, "list-regional-buckets")
	assert.Contains(t, listed, bucket)
}
