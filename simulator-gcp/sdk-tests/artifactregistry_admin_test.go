package gcp_sdk_test

import (
	"testing"

	artifactregistry "google.golang.org/api/artifactregistry/v1"
	"google.golang.org/api/option"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func arAdminService(t *testing.T) *artifactregistry.Service {
	t.Helper()
	svc, err := artifactregistry.NewService(ctx, option.WithEndpoint(baseURL), option.WithTokenSource(simTokenSource()))
	require.NoError(t, err)
	return svc
}

// TestArtifactRegistry_PackagesVersionsTagsFiles exercises the package / version
// / tag / file admin surface. Packages and versions are created by artifact
// upload, not by a JSON create call, so the SDK-reachable round-trips are the
// list/get reads (empty repository) plus the tag CRUD lifecycle the SDK can
// drive directly.
func TestArtifactRegistry_PackagesVersionsTagsFiles(t *testing.T) {
	svc := arAdminService(t)
	parent := "projects/ar-admin/locations/us-central1"
	repoID := "subres-repo"
	repoName := parent + "/repositories/" + repoID

	_, err := svc.Projects.Locations.Repositories.Create(parent, &artifactregistry.Repository{Format: "DOCKER"}).RepositoryId(repoID).Do()
	require.NoError(t, err)

	// Empty listings round-trip with the documented response shapes.
	pkgList, err := svc.Projects.Locations.Repositories.Packages.List(repoName).Do()
	require.NoError(t, err)
	assert.Empty(t, pkgList.Packages)

	fileList, err := svc.Projects.Locations.Repositories.Files.List(repoName).Do()
	require.NoError(t, err)
	assert.Empty(t, fileList.Files)

	// Unknown package/version/file reads are NOT_FOUND, not a 200 with an
	// empty body — the SDK surfaces this as an API error.
	_, err = svc.Projects.Locations.Repositories.Packages.Get(repoName + "/packages/nope").Do()
	require.Error(t, err)

	// Tag lifecycle: create → get → list → patch → delete, all SDK-driven.
	pkgName := repoName + "/packages/mypkg"
	verName := pkgName + "/versions/sha256:abc"
	created, err := svc.Projects.Locations.Repositories.Packages.Tags.Create(
		pkgName, &artifactregistry.Tag{Version: verName}).TagId("latest").Do()
	require.NoError(t, err)
	assert.Equal(t, pkgName+"/tags/latest", created.Name)
	assert.Equal(t, verName, created.Version)

	got, err := svc.Projects.Locations.Repositories.Packages.Tags.Get(pkgName + "/tags/latest").Do()
	require.NoError(t, err)
	assert.Equal(t, verName, got.Version)

	tagList, err := svc.Projects.Locations.Repositories.Packages.Tags.List(pkgName).Do()
	require.NoError(t, err)
	require.Len(t, tagList.Tags, 1)
	assert.Equal(t, pkgName+"/tags/latest", tagList.Tags[0].Name)

	verName2 := pkgName + "/versions/sha256:def"
	patched, err := svc.Projects.Locations.Repositories.Packages.Tags.Patch(
		pkgName+"/tags/latest", &artifactregistry.Tag{Version: verName2}).Do()
	require.NoError(t, err)
	assert.Equal(t, verName2, patched.Version)

	_, err = svc.Projects.Locations.Repositories.Packages.Tags.Delete(pkgName + "/tags/latest").Do()
	require.NoError(t, err)
	_, err = svc.Projects.Locations.Repositories.Packages.Tags.Get(pkgName + "/tags/latest").Do()
	require.Error(t, err)
}

// TestArtifactRegistry_Rules drives the repository cleanup/access rules CRUD.
func TestArtifactRegistry_Rules(t *testing.T) {
	svc := arAdminService(t)
	parent := "projects/ar-admin/locations/us-central1"
	repoID := "rules-repo"
	repoName := parent + "/repositories/" + repoID

	_, err := svc.Projects.Locations.Repositories.Create(parent, &artifactregistry.Repository{Format: "DOCKER"}).RepositoryId(repoID).Do()
	require.NoError(t, err)

	created, err := svc.Projects.Locations.Repositories.Rules.Create(repoName,
		&artifactregistry.GoogleDevtoolsArtifactregistryV1Rule{Action: "DENY", Operation: "DOWNLOAD"}).RuleId("deny-dl").Do()
	require.NoError(t, err)
	assert.Equal(t, repoName+"/rules/deny-dl", created.Name)
	assert.Equal(t, "DENY", created.Action)

	got, err := svc.Projects.Locations.Repositories.Rules.Get(repoName + "/rules/deny-dl").Do()
	require.NoError(t, err)
	assert.Equal(t, "DOWNLOAD", got.Operation)

	list, err := svc.Projects.Locations.Repositories.Rules.List(repoName).Do()
	require.NoError(t, err)
	require.Len(t, list.Rules, 1)

	patched, err := svc.Projects.Locations.Repositories.Rules.Patch(repoName+"/rules/deny-dl",
		&artifactregistry.GoogleDevtoolsArtifactregistryV1Rule{Action: "ALLOW"}).Do()
	require.NoError(t, err)
	assert.Equal(t, "ALLOW", patched.Action)

	_, err = svc.Projects.Locations.Repositories.Rules.Delete(repoName + "/rules/deny-dl").Do()
	require.NoError(t, err)
}

// TestArtifactRegistry_RepositoryIAM covers set/get/test IAM on a repository.
func TestArtifactRegistry_RepositoryIAM(t *testing.T) {
	svc := arAdminService(t)
	parent := "projects/ar-admin/locations/us-central1"
	repoID := "iam-repo"
	repoName := parent + "/repositories/" + repoID

	_, err := svc.Projects.Locations.Repositories.Create(parent, &artifactregistry.Repository{Format: "DOCKER"}).RepositoryId(repoID).Do()
	require.NoError(t, err)

	setReq := &artifactregistry.SetIamPolicyRequest{Policy: &artifactregistry.Policy{
		Bindings: []*artifactregistry.Binding{{
			Role:    "roles/artifactregistry.reader",
			Members: []string{"user:reader@example.com"},
		}},
	}}
	set, err := svc.Projects.Locations.Repositories.SetIamPolicy(repoName, setReq).Do()
	require.NoError(t, err)
	require.Len(t, set.Bindings, 1)
	assert.Equal(t, "roles/artifactregistry.reader", set.Bindings[0].Role)

	got, err := svc.Projects.Locations.Repositories.GetIamPolicy(repoName).Do()
	require.NoError(t, err)
	require.Len(t, got.Bindings, 1)
	assert.Contains(t, got.Bindings[0].Members, "user:reader@example.com")

	testResp, err := svc.Projects.Locations.Repositories.TestIamPermissions(repoName,
		&artifactregistry.TestIamPermissionsRequest{Permissions: []string{"artifactregistry.repositories.get"}}).Do()
	require.NoError(t, err)
	assert.NotNil(t, testResp)
}

// TestArtifactRegistry_ProjectSettingsAndVPCSC covers the project-scoped
// projectSettings, vpcscConfig and projectConfig singletons.
func TestArtifactRegistry_ProjectSettingsAndVPCSC(t *testing.T) {
	svc := arAdminService(t)
	project := "ar-admin-settings"
	location := "us-central1"

	psName := "projects/" + project + "/projectSettings"
	ps, err := svc.Projects.GetProjectSettings(psName).Do()
	require.NoError(t, err)
	assert.Equal(t, psName, ps.Name)

	psUpd, err := svc.Projects.UpdateProjectSettings(psName,
		&artifactregistry.ProjectSettings{LegacyRedirectionState: "REDIRECTION_FROM_GCR_IO_ENABLED"}).Do()
	require.NoError(t, err)
	assert.Equal(t, "REDIRECTION_FROM_GCR_IO_ENABLED", psUpd.LegacyRedirectionState)

	vpcscName := "projects/" + project + "/locations/" + location + "/vpcscConfig"
	vc, err := svc.Projects.Locations.GetVpcscConfig(vpcscName).Do()
	require.NoError(t, err)
	assert.Equal(t, vpcscName, vc.Name)

	vcUpd, err := svc.Projects.Locations.UpdateVpcscConfig(vpcscName,
		&artifactregistry.VPCSCConfig{VpcscPolicy: "DENY"}).Do()
	require.NoError(t, err)
	assert.Equal(t, "DENY", vcUpd.VpcscPolicy)

	pcName := "projects/" + project + "/locations/" + location + "/projectConfig"
	pc, err := svc.Projects.Locations.GetProjectConfig(pcName).Do()
	require.NoError(t, err)
	assert.Equal(t, pcName, pc.Name)
}
