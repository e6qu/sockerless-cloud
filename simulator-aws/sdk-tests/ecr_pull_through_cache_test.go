package aws_sdk_test

import (
	"encoding/json"
	"io"
	"net/http"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ecr"
	ecrtypes "github.com/aws/aws-sdk-go-v2/service/ecr/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A pull through a cache rule creates the repository and caches the upstream
// image in it.
//
// The registry accepted pull-through cache rules and served them back, and then
// refused every pull through one with `NAME_UNKNOWN` — the repository a rule
// covers does not exist until something has been pulled through it, which is
// the whole of what the feature does. This drives the documented flow with the
// official SDK for the rule and a real registry client for the pull, and
// asserts the image that comes back is the upstream image's own content.
func TestECR_PullThroughCacheHydratesFromTheUpstreamRegistry(t *testing.T) {
	client := ecrClient()
	const prefix = "ecr-public-cache"
	const repo = prefix + "/docker/library/alpine"
	const tag = "3.21"

	_, err := client.CreatePullThroughCacheRule(ctx, &ecr.CreatePullThroughCacheRuleInput{
		EcrRepositoryPrefix: aws.String(prefix),
		UpstreamRegistryUrl: aws.String("public.ecr.aws"),
	})
	if err != nil {
		var exists *ecrtypes.PullThroughCacheRuleAlreadyExistsException
		require.ErrorAs(t, err, &exists)
	}

	// Nothing has been pulled through the rule yet, so the repository it covers
	// does not exist.
	_, err = client.DescribeRepositories(ctx, &ecr.DescribeRepositoriesInput{
		RepositoryNames: []string{repo},
	})
	var missing *ecrtypes.RepositoryNotFoundException
	require.ErrorAs(t, err, &missing, "the rule must not create its repository before a pull")

	// The pull. The manifest comes back rather than the NAME_UNKNOWN the
	// registry used to answer.
	resp := ociDo(t, http.MethodGet, baseURL+"/v2/"+repo+"/manifests/"+tag, nil, "")
	manifestBody, readErr := io.ReadAll(resp.Body)
	resp.Body.Close()
	require.NoError(t, readErr)
	require.Equal(t, http.StatusOK, resp.StatusCode,
		"pull through the cache rule: %s", manifestBody)

	var manifest struct {
		MediaType string `json:"mediaType"`
		Config    struct {
			Digest string `json:"digest"`
			Size   int64  `json:"size"`
		} `json:"config"`
		Layers []struct {
			Digest string `json:"digest"`
			Size   int64  `json:"size"`
		} `json:"layers"`
	}
	require.NoError(t, json.Unmarshal(manifestBody, &manifest))
	require.NotEmpty(t, manifest.Layers, "a cached image must carry the upstream image's layers")

	// The layers are really there: the cache holds the upstream content, not a
	// manifest describing content it does not have.
	for _, layer := range manifest.Layers {
		blob := ociDo(t, http.MethodGet, baseURL+"/v2/"+repo+"/blobs/"+layer.Digest, nil, "")
		data, err := io.ReadAll(blob.Body)
		blob.Body.Close()
		require.NoError(t, err)
		require.Equal(t, http.StatusOK, blob.StatusCode, "cached layer %s", layer.Digest)
		require.Equal(t, layer.Size, int64(len(data)), "cached layer %s is truncated", layer.Digest)
	}

	// The config blob is the upstream image's own: it names the architecture
	// and operating system the upstream image was built for.
	config := ociDo(t, http.MethodGet, baseURL+"/v2/"+repo+"/blobs/"+manifest.Config.Digest, nil, "")
	configData, err := io.ReadAll(config.Body)
	config.Body.Close()
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, config.StatusCode)
	var imageConfig struct {
		Architecture string `json:"architecture"`
		OS           string `json:"os"`
		RootFS       struct {
			DiffIDs []string `json:"diff_ids"`
		} `json:"rootfs"`
	}
	require.NoError(t, json.Unmarshal(configData, &imageConfig))
	assert.NotEmpty(t, imageConfig.Architecture)
	assert.Equal(t, "linux", imageConfig.OS)
	assert.Len(t, imageConfig.RootFS.DiffIDs, len(manifest.Layers))

	// Amazon ECR created the repository as part of the pull, and the cached
	// image is one the control plane can see — the cache is registry state, not
	// a pass-through.
	described, err := client.DescribeRepositories(ctx, &ecr.DescribeRepositoriesInput{
		RepositoryNames: []string{repo},
	})
	require.NoError(t, err, "the pull must have created the repository the rule covers")
	require.Len(t, described.Repositories, 1)

	images, err := client.DescribeImages(ctx, &ecr.DescribeImagesInput{
		RepositoryName: aws.String(repo),
	})
	require.NoError(t, err)
	require.NotEmpty(t, images.ImageDetails)
	assert.Contains(t, images.ImageDetails[0].ImageTags, tag)
}
