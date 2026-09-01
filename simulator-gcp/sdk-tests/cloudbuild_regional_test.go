package gcp_sdk_test

import (
	"encoding/json"
	"testing"

	cloudbuild "google.golang.org/api/cloudbuild/v1"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// buildFromOperation reads the Build out of a done build operation's metadata.
func buildFromOperation(t *testing.T, op *cloudbuild.Operation) cloudbuild.Build {
	t.Helper()
	var metadata struct {
		Build cloudbuild.Build `json:"build"`
	}
	require.NoError(t, json.Unmarshal(op.Metadata, &metadata))
	return metadata.Build
}

// The regional build surface, the build and trigger colon-verbs, and the
// Bitbucket Server connected-repository pair:
//
//	POST /v1/projects/{project}/locations/{location}/builds
//	POST /v1/projects/{project}/locations/{location}/bitbucketServerConfigs/{config}/connectedRepositories:batchCreate

func TestCloudBuild_RegionalBuildCreateAndRetry(t *testing.T) {
	svc := cloudbuildService(t)
	parent := "projects/cb-regional/locations/us-central1"

	op, err := svc.Projects.Locations.Builds.Create(parent, &cloudbuild.Build{
		Steps: []*cloudbuild.BuildStep{{Name: "alpine", Args: []string{"true"}}},
	}).Do()
	require.NoError(t, err)
	require.True(t, op.Done)

	// The build is addressable under the location it was created in.
	created := buildFromOperation(t, op)
	require.NotEmpty(t, created.Id)
	assert.Contains(t, created.Name, "/locations/us-central1/builds/")

	// Retrying creates a new build rather than re-running the record.
	retried, err := svc.Projects.Locations.Builds.Retry(
		parent+"/builds/"+created.Id, &cloudbuild.RetryBuildRequest{}).Do()
	require.NoError(t, err)
	require.True(t, retried.Done)
	second := buildFromOperation(t, retried)
	assert.NotEqual(t, created.Id, second.Id, "retry starts a new build")

	// Both records survive.
	got, err := svc.Projects.Locations.Builds.Get(parent + "/builds/" + created.Id).Do()
	require.NoError(t, err)
	assert.Equal(t, created.Id, got.Id)
}

// A build that was never pending approval has no decision to record.
func TestCloudBuild_ApproveRejectsABuildThatIsNotPending(t *testing.T) {
	svc := cloudbuildService(t)
	parent := "projects/cb-approve/locations/us-central1"

	op, err := svc.Projects.Locations.Builds.Create(parent, &cloudbuild.Build{
		Steps: []*cloudbuild.BuildStep{{Name: "alpine", Args: []string{"true"}}},
	}).Do()
	require.NoError(t, err)
	created := buildFromOperation(t, op)

	_, err = svc.Projects.Locations.Builds.Approve(parent+"/builds/"+created.Id,
		&cloudbuild.ApproveBuildRequest{
			ApprovalResult: &cloudbuild.ApprovalResult{Decision: "APPROVED"},
		}).Do()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not pending approval")

	// A decision the enum does not carry is refused before the state is read.
	_, err = svc.Projects.Locations.Builds.Approve(parent+"/builds/"+created.Id,
		&cloudbuild.ApproveBuildRequest{
			ApprovalResult: &cloudbuild.ApprovalResult{Decision: "MAYBE"},
		}).Do()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "APPROVED or REJECTED")
}

func TestCloudBuild_RunTriggerStartsItsInlineBuild(t *testing.T) {
	svc := cloudbuildService(t)
	parent := "projects/cb-run/locations/us-central1"

	trigger, err := svc.Projects.Locations.Triggers.Create(parent, &cloudbuild.BuildTrigger{
		Name: "run-me",
		Build: &cloudbuild.Build{
			Steps: []*cloudbuild.BuildStep{{Name: "alpine", Args: []string{"true"}}},
		},
	}).Do()
	require.NoError(t, err)
	require.NotEmpty(t, trigger.Id)

	op, err := svc.Projects.Locations.Triggers.Run(parent+"/triggers/"+trigger.Id,
		&cloudbuild.RunBuildTriggerRequest{}).Do()
	require.NoError(t, err)
	require.True(t, op.Done)
	started := buildFromOperation(t, op)
	assert.Equal(t, trigger.Id, started.BuildTriggerId,
		"the started build names the trigger that ran it")

	// A trigger that declares no webhookConfig has no webhook to call, so the
	// call says so rather than answering as though it had started something.
	_, err = svc.Projects.Locations.Triggers.Webhook(parent+"/triggers/"+trigger.Id,
		&cloudbuild.HttpBody{}).Do()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "declares no webhookConfig")

	// And reports the absence of one that does not.
	_, err = svc.Projects.Locations.Triggers.Run(parent+"/triggers/absent",
		&cloudbuild.RunBuildTriggerRequest{}).Do()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

func TestCloudBuild_BitbucketConnectedRepositories(t *testing.T) {
	svc := cloudbuildService(t)
	parent := "projects/cb-bitbucket/locations/us-central1"

	_, err := svc.Projects.Locations.BitbucketServerConfigs.Create(parent,
		&cloudbuild.BitbucketServerConfig{
			HostUri:  "https://bitbucket.example.com",
			Username: "builder",
		}).BitbucketServerConfigId("bb").Do()
	require.NoError(t, err)
	configName := parent + "/bitbucketServerConfigs/bb"

	op, err := svc.Projects.Locations.BitbucketServerConfigs.ConnectedRepositories.BatchCreate(
		configName, &cloudbuild.BatchCreateBitbucketServerConnectedRepositoriesRequest{
			Requests: []*cloudbuild.CreateBitbucketServerConnectedRepositoryRequest{{
				Parent: configName,
				BitbucketServerConnectedRepository: &cloudbuild.BitbucketServerConnectedRepository{
					Parent: configName,
					Repo:   &cloudbuild.BitbucketServerRepositoryId{ProjectKey: "TEAM", RepoSlug: "app"},
				},
			}},
		}).Do()
	require.NoError(t, err)
	require.True(t, op.Done)

	// The connection is recorded on the config, not just acknowledged.
	config, err := svc.Projects.Locations.BitbucketServerConfigs.Get(configName).Do()
	require.NoError(t, err)
	require.Len(t, config.ConnectedRepositories, 1)
	assert.Equal(t, "TEAM", config.ConnectedRepositories[0].ProjectKey)

	_, err = svc.Projects.Locations.BitbucketServerConfigs.RemoveBitbucketServerConnectedRepository(
		configName, &cloudbuild.RemoveBitbucketServerConnectedRepositoryRequest{
			ConnectedRepository: &cloudbuild.BitbucketServerRepositoryId{ProjectKey: "TEAM", RepoSlug: "app"},
		}).Do()
	require.NoError(t, err)

	config, err = svc.Projects.Locations.BitbucketServerConfigs.Get(configName).Do()
	require.NoError(t, err)
	assert.Empty(t, config.ConnectedRepositories)

	// Removing what is not connected reports that, rather than succeeding.
	_, err = svc.Projects.Locations.BitbucketServerConfigs.RemoveBitbucketServerConnectedRepository(
		configName, &cloudbuild.RemoveBitbucketServerConnectedRepositoryRequest{
			ConnectedRepository: &cloudbuild.BitbucketServerRepositoryId{ProjectKey: "TEAM", RepoSlug: "app"},
		}).Do()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "is not connected to")
}
