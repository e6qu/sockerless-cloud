package gcp_sdk_test

import (
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/api/cloudbuild/v1"
)

// TestCloudBuild_WorkerPoolCRUD round-trips a Cloud Build private worker
// pool through the Google Cloud Go SDK: create returns an LRO carrying the
// pool; get/list/patch/delete operate on the regional resource. The pool
// has a state lifecycle (CREATING → RUNNING) the simulator must report.
func TestCloudBuild_WorkerPoolCRUD(t *testing.T) {
	svc := cloudbuildService(t)
	parent := "projects/cb-wp-project/locations/us-central1"
	poolID := fmt.Sprintf("pool-%d", time.Now().UnixNano())

	pool := &cloudbuild.WorkerPool{
		DisplayName: "sdk worker pool",
		PrivatePoolV1Config: &cloudbuild.PrivatePoolV1Config{
			WorkerConfig: &cloudbuild.WorkerConfig{
				DiskSizeGb:  100,
				MachineType: "e2-medium",
			},
		},
	}
	op, err := svc.Projects.Locations.WorkerPools.Create(parent, pool).WorkerPoolId(poolID).Do()
	require.NoError(t, err)
	require.True(t, op.Done, "create LRO should resolve synchronously")
	require.Nil(t, op.Error)

	name := parent + "/workerPools/" + poolID

	got, err := svc.Projects.Locations.WorkerPools.Get(name).Do()
	require.NoError(t, err)
	assert.Equal(t, name, got.Name)
	assert.Equal(t, "sdk worker pool", got.DisplayName)
	assert.Equal(t, "RUNNING", got.State)
	require.NotEmpty(t, got.Uid)
	require.NotNil(t, got.PrivatePoolV1Config)
	require.NotNil(t, got.PrivatePoolV1Config.WorkerConfig)
	assert.Equal(t, "e2-medium", got.PrivatePoolV1Config.WorkerConfig.MachineType)

	list, err := svc.Projects.Locations.WorkerPools.List(parent).Do()
	require.NoError(t, err)
	found := false
	for _, p := range list.WorkerPools {
		if p.Name == name {
			found = true
		}
	}
	assert.True(t, found, "created pool must appear in list")

	patchOp, err := svc.Projects.Locations.WorkerPools.Patch(name,
		&cloudbuild.WorkerPool{DisplayName: "renamed pool"}).
		UpdateMask("displayName").Do()
	require.NoError(t, err)
	require.True(t, patchOp.Done)

	got2, err := svc.Projects.Locations.WorkerPools.Get(name).Do()
	require.NoError(t, err)
	assert.Equal(t, "renamed pool", got2.DisplayName)

	delOp, err := svc.Projects.Locations.WorkerPools.Delete(name).Do()
	require.NoError(t, err)
	require.True(t, delOp.Done)

	_, err = svc.Projects.Locations.WorkerPools.Get(name).Do()
	require.Error(t, err, "get after delete must fail")
}

// TestCloudBuild_GitHubEnterpriseConfigCRUD round-trips a GitHub Enterprise
// source-host config through the global githubEnterpriseConfigs collection.
func TestCloudBuild_GitHubEnterpriseConfigCRUD(t *testing.T) {
	svc := cloudbuildService(t)
	parent := "projects/cb-ghe-project"
	cfgID := fmt.Sprintf("ghe-%d", time.Now().UnixNano())

	cfg := &cloudbuild.GitHubEnterpriseConfig{
		HostUrl:     "https://ghe.example.com",
		AppId:       42,
		DisplayName: "ghe sdk config",
	}
	op, err := svc.Projects.GithubEnterpriseConfigs.Create(parent, cfg).GheConfigId(cfgID).Do()
	require.NoError(t, err)
	require.True(t, op.Done)
	require.Nil(t, op.Error)

	name := parent + "/locations/global/githubEnterpriseConfigs/" + cfgID
	got, err := svc.Projects.GithubEnterpriseConfigs.Get(name).Do()
	require.NoError(t, err)
	assert.Equal(t, "https://ghe.example.com", got.HostUrl)
	assert.Equal(t, "ghe sdk config", got.DisplayName)
	assert.Equal(t, int64(42), got.AppId)

	list, err := svc.Projects.GithubEnterpriseConfigs.List(parent).Do()
	require.NoError(t, err)
	found := false
	for _, c := range list.Configs {
		if c.Name == name {
			found = true
		}
	}
	assert.True(t, found, "created GHE config must appear in list")

	patchOp, err := svc.Projects.GithubEnterpriseConfigs.Patch(name,
		&cloudbuild.GitHubEnterpriseConfig{DisplayName: "renamed ghe"}).
		UpdateMask("displayName").Do()
	require.NoError(t, err)
	require.True(t, patchOp.Done)
	got2, err := svc.Projects.GithubEnterpriseConfigs.Get(name).Do()
	require.NoError(t, err)
	assert.Equal(t, "renamed ghe", got2.DisplayName)

	_, err = svc.Projects.GithubEnterpriseConfigs.Delete(name).Do()
	require.NoError(t, err)
	_, err = svc.Projects.GithubEnterpriseConfigs.Get(name).Do()
	require.Error(t, err)
}

// TestCloudBuild_GitHubEnterpriseConfigRegional covers the regional
// githubEnterpriseConfigs collection (locations/{loc}/...).
func TestCloudBuild_GitHubEnterpriseConfigRegional(t *testing.T) {
	svc := cloudbuildService(t)
	parent := "projects/cb-ghe-rgn/locations/us-east1"
	cfgID := fmt.Sprintf("ghe-rgn-%d", time.Now().UnixNano())

	op, err := svc.Projects.Locations.GithubEnterpriseConfigs.Create(parent,
		&cloudbuild.GitHubEnterpriseConfig{HostUrl: "https://ghe-rgn.example.com"}).
		GheConfigId(cfgID).Do()
	require.NoError(t, err)
	require.True(t, op.Done)

	name := parent + "/githubEnterpriseConfigs/" + cfgID
	got, err := svc.Projects.Locations.GithubEnterpriseConfigs.Get(name).Do()
	require.NoError(t, err)
	assert.Equal(t, "https://ghe-rgn.example.com", got.HostUrl)

	list, err := svc.Projects.Locations.GithubEnterpriseConfigs.List(parent).Do()
	require.NoError(t, err)
	assert.NotEmpty(t, list.Configs)

	_, err = svc.Projects.Locations.GithubEnterpriseConfigs.Delete(name).Do()
	require.NoError(t, err)
}

// TestCloudBuild_GitLabConfigCRUD round-trips a GitLab source-host config
// plus its repos-list endpoint.
func TestCloudBuild_GitLabConfigCRUD(t *testing.T) {
	svc := cloudbuildService(t)
	parent := "projects/cb-gitlab-project/locations/us-central1"
	cfgID := fmt.Sprintf("gl-%d", time.Now().UnixNano())

	cfg := &cloudbuild.GitLabConfig{Username: "gitlab-bot"}
	op, err := svc.Projects.Locations.GitLabConfigs.Create(parent, cfg).GitlabConfigId(cfgID).Do()
	require.NoError(t, err)
	require.True(t, op.Done)
	require.Nil(t, op.Error)

	name := parent + "/gitLabConfigs/" + cfgID
	got, err := svc.Projects.Locations.GitLabConfigs.Get(name).Do()
	require.NoError(t, err)
	assert.Equal(t, "gitlab-bot", got.Username)

	list, err := svc.Projects.Locations.GitLabConfigs.List(parent).Do()
	require.NoError(t, err)
	found := false
	for _, c := range list.GitlabConfigs {
		if c.Name == name {
			found = true
		}
	}
	assert.True(t, found, "created GitLab config must appear in list")

	patchOp, err := svc.Projects.Locations.GitLabConfigs.Patch(name,
		&cloudbuild.GitLabConfig{Username: "renamed-bot"}).UpdateMask("username").Do()
	require.NoError(t, err)
	require.True(t, patchOp.Done)
	got2, err := svc.Projects.Locations.GitLabConfigs.Get(name).Do()
	require.NoError(t, err)
	assert.Equal(t, "renamed-bot", got2.Username)

	// repos list — empty for a config with no completed connection.
	repos, err := svc.Projects.Locations.GitLabConfigs.Repos.List(name).Do()
	require.NoError(t, err)
	assert.Empty(t, repos.GitlabRepositories)

	_, err = svc.Projects.Locations.GitLabConfigs.Delete(name).Do()
	require.NoError(t, err)
	_, err = svc.Projects.Locations.GitLabConfigs.Get(name).Do()
	require.Error(t, err)
}

// TestCloudBuild_BitbucketServerConfigCRUD round-trips a Bitbucket Server
// source-host config plus its repos-list endpoint.
func TestCloudBuild_BitbucketServerConfigCRUD(t *testing.T) {
	svc := cloudbuildService(t)
	parent := "projects/cb-bb-project/locations/us-central1"
	cfgID := fmt.Sprintf("bb-%d", time.Now().UnixNano())

	cfg := &cloudbuild.BitbucketServerConfig{
		HostUri:  "https://bitbucket.example.com",
		Username: "bb-bot",
		ApiKey:   "k",
	}
	op, err := svc.Projects.Locations.BitbucketServerConfigs.Create(parent, cfg).
		BitbucketServerConfigId(cfgID).Do()
	require.NoError(t, err)
	require.True(t, op.Done)
	require.Nil(t, op.Error)

	name := parent + "/bitbucketServerConfigs/" + cfgID
	got, err := svc.Projects.Locations.BitbucketServerConfigs.Get(name).Do()
	require.NoError(t, err)
	assert.Equal(t, "https://bitbucket.example.com", got.HostUri)
	assert.Equal(t, "bb-bot", got.Username)

	list, err := svc.Projects.Locations.BitbucketServerConfigs.List(parent).Do()
	require.NoError(t, err)
	found := false
	for _, c := range list.BitbucketServerConfigs {
		if c.Name == name {
			found = true
		}
	}
	assert.True(t, found, "created Bitbucket config must appear in list")

	patchOp, err := svc.Projects.Locations.BitbucketServerConfigs.Patch(name,
		&cloudbuild.BitbucketServerConfig{Username: "renamed-bb"}).UpdateMask("username").Do()
	require.NoError(t, err)
	require.True(t, patchOp.Done)
	got2, err := svc.Projects.Locations.BitbucketServerConfigs.Get(name).Do()
	require.NoError(t, err)
	assert.Equal(t, "renamed-bb", got2.Username)

	repos, err := svc.Projects.Locations.BitbucketServerConfigs.Repos.List(name).Do()
	require.NoError(t, err)
	assert.Empty(t, repos.BitbucketServerRepositories)

	_, err = svc.Projects.Locations.BitbucketServerConfigs.Delete(name).Do()
	require.NoError(t, err)
	_, err = svc.Projects.Locations.BitbucketServerConfigs.Get(name).Do()
	require.Error(t, err)
}

// TestCloudBuild_RegionalBuildsReadback creates a build via the global
// endpoint, then reads it back through the regional builds list + get
// endpoints (which mirror the global ones).
func TestCloudBuild_RegionalBuildsReadback(t *testing.T) {
	project := "cb-regional-builds"
	bucket := "cb-regional-bucket"
	createBucket(t, project, bucket)
	objectName := fmt.Sprintf("cb-regional-%d.tar.gz", time.Now().UnixNano())
	tarball := makeTarGz(t, map[string]string{"Dockerfile": "FROM scratch\n"})
	uploadGCSObject(t, bucket, objectName, tarball)

	// Submit via the global endpoint; we only need the build record to exist.
	buildURL := fmt.Sprintf("%s/v1/projects/%s/builds", baseURL, project)
	body := fmt.Sprintf(`{"source":{"storageSource":{"bucket":%q,"object":%q}}}`, bucket, objectName)
	resp := httpPOST(t, buildURL, body)
	require.Contains(t, resp, `"build"`, "create LRO metadata should carry the build")

	svc := cloudbuildService(t)
	// Regional list must surface the build created for this project.
	list, err := svc.Projects.Locations.Builds.List(
		fmt.Sprintf("projects/%s/locations/us-central1", project)).Do()
	require.NoError(t, err)
	require.NotEmpty(t, list.Builds, "regional list must return the project's builds")
	id := list.Builds[0].Id
	require.NotEmpty(t, id)

	// Regional get by name.
	got, err := svc.Projects.Locations.Builds.Get(
		fmt.Sprintf("projects/%s/locations/us-central1/builds/%s", project, id)).Do()
	require.NoError(t, err)
	assert.Equal(t, id, got.Id)
	assert.Equal(t, project, got.ProjectId)
}

// TestCloudBuild_DefaultServiceAccount reads the per-location default
// service account used for builds.
func TestCloudBuild_DefaultServiceAccount(t *testing.T) {
	svc := cloudbuildService(t)
	name := "projects/cb-dsa-project/locations/us-central1/defaultServiceAccount"
	got, err := svc.Projects.Locations.GetDefaultServiceAccount(name).Do()
	require.NoError(t, err)
	assert.Equal(t, name, got.Name)
	assert.NotEmpty(t, got.ServiceAccountEmail)
}
