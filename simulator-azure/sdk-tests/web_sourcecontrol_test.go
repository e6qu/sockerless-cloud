package azure_sdk_test

import (
	"net/http"
	"testing"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore/to"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/appservice/armappservice/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// SDK coverage for deployment from source control: configuring a repository
// and branch deploys the branch's head into the app, and a sync redeploys its
// current head, each recorded as a deployment under the commit's ID.
//
//	PUT /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Web/sites/{siteName}/sourcecontrols/web
//	POST /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Web/sites/{siteName}/sync
//	GET /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Web/sites/{siteName}/deployments
//	DELETE /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Web/sites/{siteName}/sourcecontrols/web

func TestSDK_WebApps_SourceControlDeploysTheRepository(t *testing.T) {
	rg, name := "sdk-scm-rg", "sdk-scm-app"
	repo := startGitHTTPRepo(t, "site")
	first := repo.commit("main", "First page", map[string]string{
		"index.html":     "first",
		"css/site.css":   "body{}",
		"docs/notes.txt": "kept until removed",
	})
	repo.commit("other", "Another branch", map[string]string{"index.html": "other branch"})

	client := createFilesWebApp(t, rg, name, nil)
	cfg, err := client.GetConfiguration(ctx, rg, name, nil)
	require.NoError(t, err)
	assert.Equal(t, armappservice.ScmTypeNone, *cfg.Properties.ScmType, "a new app has no source control")

	poller, err := client.BeginCreateOrUpdateSourceControl(ctx, rg, name, armappservice.SiteSourceControl{
		Properties: &armappservice.SiteSourceControlProperties{
			RepoURL:             to.Ptr(repo.URL),
			Branch:              to.Ptr("main"),
			IsManualIntegration: to.Ptr(true),
		},
	}, nil)
	require.NoError(t, err)
	_, err = poller.PollUntilDone(ctx, nil)
	require.NoError(t, err)

	assert.Equal(t, "first", siteFile(t, name, "index.html"), "the configured branch's head is deployed")
	assert.Equal(t, "body{}", siteFile(t, name, "css/site.css"))
	status, _ := siteGet(t, name, "/files/.git/HEAD")
	assert.Equal(t, http.StatusNotFound, status, "the repository's own metadata is not deployed")
	cfg, err = client.GetConfiguration(ctx, rg, name, nil)
	require.NoError(t, err)
	assert.Equal(t, armappservice.ScmTypeExternalGit, *cfg.Properties.ScmType)

	deployments := siteDeployments(t, client, rg, name)
	require.Contains(t, deployments, first, "the deployment is recorded under the commit's ID")
	d := deployments[first]
	assert.Equal(t, int32(4), *d.Properties.Status, "the deployment succeeded")
	assert.True(t, *d.Properties.Active)
	assert.Equal(t, "Ada Lovelace", *d.Properties.Author)
	assert.Equal(t, "ada@example.com", *d.Properties.AuthorEmail)
	assert.Equal(t, "First page", *d.Properties.Message)

	// A sync deploys the branch's current head with Kudu's sync semantics: a
	// file the new commit removed leaves the app.
	repo.remove("docs/notes.txt")
	second := repo.commit("main", "Second page", map[string]string{"index.html": "second"})
	_, err = client.SyncRepository(ctx, rg, name, nil)
	require.NoError(t, err)
	assert.Equal(t, "second", siteFile(t, name, "index.html"))
	status, _ = siteGet(t, name, "/files/docs/notes.txt")
	assert.Equal(t, http.StatusNotFound, status, "a file the head no longer has is removed")
	deployments = siteDeployments(t, client, rg, name)
	require.Contains(t, deployments, second)
	assert.True(t, *deployments[second].Properties.Active)
	assert.False(t, *deployments[first].Properties.Active, "the previous deployment is no longer active")

	// A branch that does not exist fails the deployment and leaves the app
	// on what it ran.
	poller, err = client.BeginCreateOrUpdateSourceControl(ctx, rg, name, armappservice.SiteSourceControl{
		Properties: &armappservice.SiteSourceControlProperties{
			RepoURL:             to.Ptr(repo.URL),
			Branch:              to.Ptr("missing"),
			IsManualIntegration: to.Ptr(true),
		},
	}, nil)
	require.NoError(t, err)
	_, err = poller.PollUntilDone(ctx, nil)
	require.NoError(t, err)
	failed := 0
	for id, dep := range siteDeployments(t, client, rg, name) {
		if id != first && id != second {
			assert.Equal(t, int32(3), *dep.Properties.Status, "the fetch of a missing branch fails")
			failed++
		}
	}
	assert.Equal(t, 1, failed)
	assert.Equal(t, "second", siteFile(t, name, "index.html"))

	_, err = client.DeleteSourceControl(ctx, rg, name, nil)
	require.NoError(t, err)
	cfg, err = client.GetConfiguration(ctx, rg, name, nil)
	require.NoError(t, err)
	assert.Equal(t, armappservice.ScmTypeNone, *cfg.Properties.ScmType)
	_, err = client.SyncRepository(ctx, rg, name, nil)
	require.Error(t, err, "an app without source control has nothing to sync")
}

func siteFile(t *testing.T, host, name string) string {
	t.Helper()
	status, body := siteGet(t, host, "/files/"+name)
	require.Equal(t, http.StatusOK, status, "%s has no %s: %s", host, name, body)
	return body
}

// siteDeployments reads an app's deployments by ID.
func siteDeployments(t *testing.T, client *armappservice.WebAppsClient, rg, name string) map[string]*armappservice.Deployment {
	t.Helper()
	out := map[string]*armappservice.Deployment{}
	pager := client.NewListDeploymentsPager(rg, name, nil)
	for pager.More() {
		page, err := pager.NextPage(ctx)
		require.NoError(t, err)
		for _, d := range page.Value {
			out[*d.Name] = d
		}
	}
	return out
}
