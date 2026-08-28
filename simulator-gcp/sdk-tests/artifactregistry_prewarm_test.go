package gcp_sdk_test

import (
	"testing"

	artifactregistry "google.golang.org/api/artifactregistry/v1"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The prewarmed-artifact family and the custom methods that ride the
// repository segment alongside the IAM triple:
//
//	POST /v1/projects/{project}/locations/{location}/repositories/{repo}:prewarmArtifact
//	POST /v1/projects/{project}/locations/{location}/repositories/{repo}:checkPrewarmedArtifact
//	POST /v1/projects/{project}/locations/{location}/repositories/{repo}:removePrewarmedArtifact
//	GET /v1/projects/{project}/locations/{location}/repositories/{repo}/prewarmedArtifacts

func TestArtifactRegistry_PrewarmedArtifactLifecycle(t *testing.T) {
	svc := arAdminService(t)
	parent := "projects/ar-prewarm/locations/us-central1"
	repoName := parent + "/repositories/prewarm-repo"

	_, err := svc.Projects.Locations.Repositories.Create(parent, &artifactregistry.Repository{Format: "DOCKER"}).
		RepositoryId("prewarm-repo").Do()
	require.NoError(t, err)

	pkgName := repoName + "/packages/app"
	tagName := pkgName + "/tags/latest"
	_, err = svc.Projects.Locations.Repositories.Packages.Tags.Create(
		pkgName, &artifactregistry.Tag{Version: pkgName + "/versions/sha256:abc"}).TagId("latest").Do()
	require.NoError(t, err)

	// Nothing is cached before a prewarm.
	list, err := svc.Projects.Locations.Repositories.PrewarmedArtifacts.List(repoName).Do()
	require.NoError(t, err)
	assert.Empty(t, list.PrewarmedArtifacts)

	op, err := svc.Projects.Locations.Repositories.PrewarmArtifact(repoName,
		&artifactregistry.PrewarmArtifactRequest{Tag: tagName, StreamLocation: "us-west4"}).Do()
	require.NoError(t, err)
	assert.True(t, op.Done)

	// The uri is the registry address, not the resource name.
	list, err = svc.Projects.Locations.Repositories.PrewarmedArtifacts.List(repoName).Do()
	require.NoError(t, err)
	require.Len(t, list.PrewarmedArtifacts, 1)
	assert.Equal(t, "us-central1-docker.pkg.dev/ar-prewarm/prewarm-repo/app:latest",
		list.PrewarmedArtifacts[0].Uri)
	assert.Equal(t, "us-west4", list.PrewarmedArtifacts[0].Location)
	assert.NotEmpty(t, list.PrewarmedArtifacts[0].ExpirationTime)

	check, err := svc.Projects.Locations.Repositories.CheckPrewarmedArtifact(repoName,
		&artifactregistry.CheckPrewarmedArtifactRequest{Tag: tagName, StreamLocation: "us-west4"}).Do()
	require.NoError(t, err)
	require.NotNil(t, check.PrewarmedArtifact)
	assert.Equal(t, "us-west4", check.PrewarmedArtifact.Location)

	// Prewarming the same artifact twice is a conflict unless forced.
	_, err = svc.Projects.Locations.Repositories.PrewarmArtifact(repoName,
		&artifactregistry.PrewarmArtifactRequest{Tag: tagName, StreamLocation: "us-west4"}).Do()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "already prewarmed")
	_, err = svc.Projects.Locations.Repositories.PrewarmArtifact(repoName,
		&artifactregistry.PrewarmArtifactRequest{Tag: tagName, StreamLocation: "us-west4", Force: true}).Do()
	require.NoError(t, err)

	removed, err := svc.Projects.Locations.Repositories.RemovePrewarmedArtifact(repoName,
		&artifactregistry.RemovePrewarmedArtifactRequest{Tag: tagName, StreamLocation: "us-west4"}).Do()
	require.NoError(t, err)
	require.NotNil(t, removed.PrewarmedArtifact)

	list, err = svc.Projects.Locations.Repositories.PrewarmedArtifacts.List(repoName).Do()
	require.NoError(t, err)
	assert.Empty(t, list.PrewarmedArtifacts)

	// A check after removal reports the absence rather than an empty record.
	_, err = svc.Projects.Locations.Repositories.CheckPrewarmedArtifact(repoName,
		&artifactregistry.CheckPrewarmedArtifactRequest{Tag: tagName, StreamLocation: "us-west4"}).Do()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "is not prewarmed in")
}

// streamLocation is optional: an unset one caches the artifact where it lives.
func TestArtifactRegistry_PrewarmDefaultsToTheRepositoryLocation(t *testing.T) {
	svc := arAdminService(t)
	parent := "projects/ar-prewarm-default/locations/europe-west1"
	repoName := parent + "/repositories/dflt-repo"

	_, err := svc.Projects.Locations.Repositories.Create(parent, &artifactregistry.Repository{Format: "DOCKER"}).
		RepositoryId("dflt-repo").Do()
	require.NoError(t, err)
	pkgName := repoName + "/packages/svc"
	_, err = svc.Projects.Locations.Repositories.Packages.Tags.Create(
		pkgName, &artifactregistry.Tag{Version: pkgName + "/versions/sha256:def"}).TagId("v1").Do()
	require.NoError(t, err)

	_, err = svc.Projects.Locations.Repositories.PrewarmArtifact(repoName,
		&artifactregistry.PrewarmArtifactRequest{Tag: pkgName + "/tags/v1"}).Do()
	require.NoError(t, err)

	list, err := svc.Projects.Locations.Repositories.PrewarmedArtifacts.List(repoName).Do()
	require.NoError(t, err)
	require.Len(t, list.PrewarmedArtifacts, 1)
	assert.Equal(t, "europe-west1", list.PrewarmedArtifacts[0].Location)
}

func TestArtifactRegistry_PrewarmRejectsWhatItCannotSelect(t *testing.T) {
	svc := arAdminService(t)
	parent := "projects/ar-prewarm-bad/locations/us-central1"
	repoName := parent + "/repositories/bad-repo"

	_, err := svc.Projects.Locations.Repositories.Create(parent, &artifactregistry.Repository{Format: "DOCKER"}).
		RepositoryId("bad-repo").Do()
	require.NoError(t, err)

	// Neither member set.
	_, err = svc.Projects.Locations.Repositories.PrewarmArtifact(repoName,
		&artifactregistry.PrewarmArtifactRequest{StreamLocation: "us-west4"}).Do()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "one of version or tag is required")

	// Both members set — the request carries a oneof.
	_, err = svc.Projects.Locations.Repositories.PrewarmArtifact(repoName,
		&artifactregistry.PrewarmArtifactRequest{
			Tag:     repoName + "/packages/app/tags/latest",
			Version: repoName + "/packages/app/versions/sha256:abc",
		}).Do()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "mutually exclusive")

	// A name belonging to another repository.
	_, err = svc.Projects.Locations.Repositories.PrewarmArtifact(repoName,
		&artifactregistry.PrewarmArtifactRequest{
			Tag: parent + "/repositories/other/packages/app/tags/latest",
		}).Do()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "does not name an artifact in")

	// A well-formed name for an artifact that does not exist.
	_, err = svc.Projects.Locations.Repositories.PrewarmArtifact(repoName,
		&artifactregistry.PrewarmArtifactRequest{Tag: repoName + "/packages/app/tags/absent"}).Do()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

// The repository colon-verb fan-in serves the IAM triple as well as these
// custom methods, so the two must not shadow each other.
func TestArtifactRegistry_RepositoryIAMStillWorksBesideTheCustomVerbs(t *testing.T) {
	svc := arAdminService(t)
	parent := "projects/ar-verb-share/locations/us-central1"
	repoName := parent + "/repositories/share-repo"

	_, err := svc.Projects.Locations.Repositories.Create(parent, &artifactregistry.Repository{Format: "DOCKER"}).
		RepositoryId("share-repo").Do()
	require.NoError(t, err)

	_, err = svc.Projects.Locations.Repositories.SetIamPolicy(repoName,
		&artifactregistry.SetIamPolicyRequest{Policy: &artifactregistry.Policy{
			Bindings: []*artifactregistry.Binding{{
				Role:    "roles/artifactregistry.reader",
				Members: []string{"user:reader@example.com"},
			}},
		}}).Do()
	require.NoError(t, err)

	policy, err := svc.Projects.Locations.Repositories.GetIamPolicy(repoName).Do()
	require.NoError(t, err)
	require.Len(t, policy.Bindings, 1)
	assert.Equal(t, "roles/artifactregistry.reader", policy.Bindings[0].Role)
}
