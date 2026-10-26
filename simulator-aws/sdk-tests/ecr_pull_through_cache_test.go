package aws_sdk_test

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ecr"
	ecrtypes "github.com/aws/aws-sdk-go-v2/service/ecr/types"
	"github.com/e6qu/sockerless-cloud/testutil/uniquename"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A pull through a cache rule creates the repository and caches the upstream
// image in it: the repository a rule covers exists only once something is
// pulled through it. The SDK sets the rule, a registry client pulls, and the
// image that comes back is the upstream image's own content.
func TestECR_PullThroughCacheHydratesFromTheUpstreamRegistry(t *testing.T) {
	t.Parallel()
	client := ecrClient()
	const upstreamImage = "public.ecr.aws/docker/library/alpine:3.21"
	upstreamPath, tag, _ := strings.Cut(strings.TrimPrefix(upstreamImage, "public.ecr.aws/"), ":")
	prefix := uniquename.New("ecr-pub-cache")
	repo := prefix + "/" + upstreamPath

	_, err := client.CreatePullThroughCacheRule(ctx, &ecr.CreatePullThroughCacheRuleInput{
		EcrRepositoryPrefix: aws.String(prefix),
		UpstreamRegistryUrl: aws.String("public.ecr.aws"),
	})
	if err != nil {
		var exists *ecrtypes.PullThroughCacheRuleAlreadyExistsException
		require.ErrorAs(t, err, &exists)
	}

	_, err = client.DescribeRepositories(ctx, &ecr.DescribeRepositoriesInput{
		RepositoryNames: []string{repo},
	})
	var missing *ecrtypes.RepositoryNotFoundException
	require.ErrorAs(t, err, &missing, "the rule must not create its repository before a pull")

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

	// The cache holds the upstream layers, not only a manifest describing them.
	for _, layer := range manifest.Layers {
		blob := ociDo(t, http.MethodGet, baseURL+"/v2/"+repo+"/blobs/"+layer.Digest, nil, "")
		data, err := io.ReadAll(blob.Body)
		blob.Body.Close()
		require.NoError(t, err)
		require.Equal(t, http.StatusOK, blob.StatusCode, "cached layer %s", layer.Digest)
		require.Equal(t, layer.Size, int64(len(data)), "cached layer %s is truncated", layer.Digest)
	}

	// The config blob names the architecture and operating system the upstream
	// image was built for.
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

	// Amazon ECR created the repository during the pull, and the control plane
	// sees the cached image.
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
