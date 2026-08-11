package gcp_sdk_test

import (
	"testing"

	artifactregistry "google.golang.org/api/artifactregistry/v1"
	"google.golang.org/api/option"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestArtifactRegistry_ListRepositoriesPagination covers the pageSize/pageToken
// support ListRepositories previously dropped (it returned every repository in
// one page with no nextPageToken).
func TestArtifactRegistry_ListRepositoriesPagination(t *testing.T) {
	svc, err := artifactregistry.NewService(ctx, option.WithEndpoint(baseURL), option.WithTokenSource(simTokenSource()))
	require.NoError(t, err)

	parent := "projects/ar-page-project/locations/us-central1"
	for _, id := range []string{"repo-a", "repo-b", "repo-c"} {
		_, err = svc.Projects.Locations.Repositories.Create(parent, &artifactregistry.Repository{
			Format: "DOCKER",
		}).RepositoryId(id).Do()
		require.NoError(t, err)
	}

	page1, err := svc.Projects.Locations.Repositories.List(parent).PageSize(2).Do()
	require.NoError(t, err)
	require.Len(t, page1.Repositories, 2)
	require.NotEmpty(t, page1.NextPageToken, "pageSize=2 over 3 repositories → nextPageToken")

	page2, err := svc.Projects.Locations.Repositories.List(parent).PageSize(2).PageToken(page1.NextPageToken).Do()
	require.NoError(t, err)
	assert.Len(t, page2.Repositories, 1)
}
