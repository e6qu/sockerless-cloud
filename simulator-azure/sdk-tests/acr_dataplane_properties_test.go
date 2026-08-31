package azure_sdk_test

import (
	"strings"
	"testing"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore/to"
	"github.com/Azure/azure-sdk-for-go/sdk/containers/azcontainerregistry"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/containerregistry/armcontainerregistry"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// SDK coverage for the Azure Container Registry data plane's properties APIs:
//
//	GET /acr/v1/{name}
//	PATCH /acr/v1/{name}
//	DELETE /acr/v1/{name}
//	GET /acr/v1/{name}/_manifests
//	GET /acr/v1/{name}/_manifests/{digest}
//	PATCH /acr/v1/{name}/_manifests/{digest}
//	GET /acr/v1/{name}/_tags/{reference}
//	PATCH /acr/v1/{name}/_tags/{reference}
//	DELETE /acr/v1/{name}/_tags/{reference}
//
// Everything these report is read from the registry: what it holds decides the
// counts, the digests, the sizes and the tags, and pushing or deleting through
// the Docker protocol moves them. The one thing they add is the attributes a
// client sets, which the registry then honours.
func TestACR_DataPlanePropertiesReadTheRegistry(t *testing.T) {
	fixture := newACRRegistryFixture(t, "acr-props-rg", "acrpropsregistry",
		&armcontainerregistry.RegistryProperties{AdminUserEnabled: to.Ptr(true)})

	client, err := azcontainerregistry.NewClient(fixture.endpoint(), &fakeCredential{}, acrClientOptions())
	require.NoError(t, err)

	// A repository nothing was pushed to is not in the registry, which is the
	// negative control for every count that follows.
	_, err = client.GetRepositoryProperties(ctx, "sdk/absent", nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not in this registry")

	const repo = "sdk/props"
	first, err := client.UploadManifest(ctx, repo, "v1",
		azcontainerregistry.ContentTypeApplicationVndDockerDistributionManifestV2JSON,
		nopSeekCloser(strings.NewReader(acrAuthTestManifest)), nil)
	require.NoError(t, err)
	digest := *first.DockerContentDigest

	// The repository describes what the registry holds: one manifest under one
	// tag, and the same manifest counted once however many names point at it.
	props, err := client.GetRepositoryProperties(ctx, repo, nil)
	require.NoError(t, err)
	require.NotNil(t, props.ManifestCount)
	assert.EqualValues(t, 1, *props.ManifestCount)
	assert.EqualValues(t, 1, *props.TagCount)
	require.NotNil(t, props.CreatedOn)
	assert.False(t, props.CreatedOn.IsZero(), "the registry knows when it received the manifest")

	// A second tag on the same content adds a tag, not a manifest.
	_, err = client.UploadManifest(ctx, repo, "latest",
		azcontainerregistry.ContentTypeApplicationVndDockerDistributionManifestV2JSON,
		nopSeekCloser(strings.NewReader(acrAuthTestManifest)), nil)
	require.NoError(t, err)

	props, err = client.GetRepositoryProperties(ctx, repo, nil)
	require.NoError(t, err)
	assert.EqualValues(t, 1, *props.ManifestCount, "two names for one manifest is still one manifest")
	assert.EqualValues(t, 2, *props.TagCount)

	// The manifest reports the size of the document the registry stored and the
	// tags currently pointing at it.
	manifest, err := client.GetManifestProperties(ctx, repo, digest, nil)
	require.NoError(t, err)
	require.NotNil(t, manifest.Manifest)
	assert.Equal(t, digest, *manifest.Manifest.Digest)
	assert.EqualValues(t, len(acrAuthTestManifest), *manifest.Manifest.Size)
	tags := []string{}
	for _, tag := range manifest.Manifest.Tags {
		tags = append(tags, *tag)
	}
	assert.ElementsMatch(t, []string{"v1", "latest"}, tags)

	// The manifest list is the same rows, keyed by digest.
	listed, err := client.NewListManifestsPager(repo, nil).NextPage(ctx)
	require.NoError(t, err)
	require.Len(t, listed.Attributes, 1)
	assert.Equal(t, digest, *listed.Attributes[0].Digest)

	// A tag names the manifest it points at.
	tag, err := client.GetTagProperties(ctx, repo, "v1", nil)
	require.NoError(t, err)
	require.NotNil(t, tag.Tag)
	assert.Equal(t, digest, *tag.Tag.Digest)
	assert.Equal(t, "v1", *tag.Tag.Name)

	// The attributes are the state a client sets, and they come back on the
	// next read rather than only in the response that set them.
	_, err = client.UpdateTagProperties(ctx, repo, "v1",
		&azcontainerregistry.ClientUpdateTagPropertiesOptions{
			Value: &azcontainerregistry.TagWriteableProperties{CanDelete: to.Ptr(false)},
		})
	require.NoError(t, err)
	tag, err = client.GetTagProperties(ctx, repo, "v1", nil)
	require.NoError(t, err)
	assert.False(t, *tag.Tag.ChangeableAttributes.CanDelete)
	assert.True(t, *tag.Tag.ChangeableAttributes.CanRead, "a member the update left out keeps the value it had")

	// And the registry honours them: a tag that cannot be deleted is not.
	_, err = client.DeleteTag(ctx, repo, "v1", nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "deletion disabled")

	_, err = client.UpdateTagProperties(ctx, repo, "v1",
		&azcontainerregistry.ClientUpdateTagPropertiesOptions{
			Value: &azcontainerregistry.TagWriteableProperties{CanDelete: to.Ptr(true)},
		})
	require.NoError(t, err)

	// Deleting a tag removes the name and leaves the manifest addressable by
	// digest — untagged, not gone.
	_, err = client.DeleteTag(ctx, repo, "v1", nil)
	require.NoError(t, err)
	_, err = client.GetTagProperties(ctx, repo, "v1", nil)
	require.Error(t, err)
	manifest, err = client.GetManifestProperties(ctx, repo, digest, nil)
	require.NoError(t, err, "the manifest survives the tag that pointed at it")
	require.Len(t, manifest.Manifest.Tags, 1)
	assert.Equal(t, "latest", *manifest.Manifest.Tags[0])

	props, err = client.GetRepositoryProperties(ctx, repo, nil)
	require.NoError(t, err)
	assert.EqualValues(t, 1, *props.TagCount, "the deleted tag is gone from the count")

	// The manifest's own attributes work the same way.
	_, err = client.UpdateManifestProperties(ctx, repo, digest,
		&azcontainerregistry.ClientUpdateManifestPropertiesOptions{
			Value: &azcontainerregistry.ManifestWriteableProperties{CanWrite: to.Ptr(false)},
		})
	require.NoError(t, err)
	manifest, err = client.GetManifestProperties(ctx, repo, digest, nil)
	require.NoError(t, err)
	assert.False(t, *manifest.Manifest.ChangeableAttributes.CanWrite)

	// So do the repository's, and a repository that cannot be deleted is not.
	_, err = client.UpdateRepositoryProperties(ctx, repo,
		&azcontainerregistry.ClientUpdateRepositoryPropertiesOptions{
			Value: &azcontainerregistry.RepositoryWriteableProperties{CanDelete: to.Ptr(false)},
		})
	require.NoError(t, err)
	_, err = client.DeleteRepository(ctx, repo, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "deletion disabled")

	_, err = client.UpdateRepositoryProperties(ctx, repo,
		&azcontainerregistry.ClientUpdateRepositoryPropertiesOptions{
			Value: &azcontainerregistry.RepositoryWriteableProperties{CanDelete: to.Ptr(true)},
		})
	require.NoError(t, err)
	_, err = client.DeleteRepository(ctx, repo, nil)
	require.NoError(t, err)

	// Deleting the repository takes everything under it, so the registry no
	// longer holds the manifest either.
	_, err = client.GetRepositoryProperties(ctx, repo, nil)
	require.Error(t, err)
	_, err = client.GetManifestProperties(ctx, repo, digest, nil)
	require.Error(t, err)
}
