package aws_sdk_test

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ecr"
	ecrtypes "github.com/aws/aws-sdk-go-v2/service/ecr/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func ecrClient() *ecr.Client {
	return ecr.NewFromConfig(sdkConfig(), func(o *ecr.Options) {
		o.BaseEndpoint = aws.String(baseURL)
	})
}

func TestECR_CreateRepository(t *testing.T) {
	repo := uniqueName("test-repo")
	client := ecrClient()
	out, err := client.CreateRepository(ctx, &ecr.CreateRepositoryInput{
		RepositoryName: aws.String(repo),
	})
	require.NoError(t, err)
	assert.Equal(t, repo, *out.Repository.RepositoryName)
	assert.Contains(t, *out.Repository.RepositoryUri, repo)
}

func TestECR_DescribeRepositories(t *testing.T) {
	repo := uniqueName("describe-repo")
	client := ecrClient()

	_, err := client.CreateRepository(ctx, &ecr.CreateRepositoryInput{
		RepositoryName: aws.String(repo),
	})
	require.NoError(t, err)

	out, err := client.DescribeRepositories(ctx, &ecr.DescribeRepositoriesInput{
		RepositoryNames: []string{repo},
	})
	require.NoError(t, err)
	require.Len(t, out.Repositories, 1)
	assert.Equal(t, repo, *out.Repositories[0].RepositoryName)
}

func TestECR_GetAuthorizationToken(t *testing.T) {
	client := ecrClient()
	out, err := client.GetAuthorizationToken(ctx, &ecr.GetAuthorizationTokenInput{})
	require.NoError(t, err)
	require.Len(t, out.AuthorizationData, 1)
	data := out.AuthorizationData[0]

	// The token is the credential `docker login` replays as Basic auth, so it
	// has to decode to `AWS:<password>` — the wire form the Amazon ECR API
	// Reference documents for AuthorizationData.authorizationToken, and the one
	// the registry's own authenticator parses.
	decoded, err := base64.StdEncoding.DecodeString(aws.ToString(data.AuthorizationToken))
	require.NoError(t, err, "the authorization token must be base64")
	user, password, ok := strings.Cut(string(decoded), ":")
	require.True(t, ok, "the decoded token must be user:password, got %q", decoded)
	assert.Equal(t, "AWS", user)
	assert.NotEmpty(t, password)

	// The proxy endpoint names this account's login server, and the token
	// expires twelve hours out, which is the service's documented lifetime.
	assert.Regexp(t, `^https://\d{12}\.dkr\.ecr\.us-east-1\.amazonaws\.com$`, aws.ToString(data.ProxyEndpoint))
	require.NotNil(t, data.ExpiresAt)
	assert.WithinDuration(t, time.Now().Add(12*time.Hour), *data.ExpiresAt, 5*time.Minute)

	// A second call issues a different password, so a client that cached the
	// first cannot be what a later request is accepted on.
	again, err := client.GetAuthorizationToken(ctx, &ecr.GetAuthorizationTokenInput{})
	require.NoError(t, err)
	require.Len(t, again.AuthorizationData, 1)
	assert.NotEqual(t, aws.ToString(data.AuthorizationToken), aws.ToString(again.AuthorizationData[0].AuthorizationToken),
		"each call must issue a fresh authorization token")
}

func TestECR_PutImageDigestIsContentAddressed(t *testing.T) {
	repo := uniqueName("content-digest-repo")
	client := ecrClient()
	_, err := client.CreateRepository(ctx, &ecr.CreateRepositoryInput{
		RepositoryName: aws.String(repo),
	})
	require.NoError(t, err)

	manifest := `{"schemaVersion":2,"mediaType":"application/vnd.docker.distribution.manifest.v2+json","config":{"mediaType":"application/vnd.docker.container.image.v1+json","size":2,"digest":"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},"layers":[]}`
	sum := sha256.Sum256([]byte(manifest))
	wantDigest := "sha256:" + hex.EncodeToString(sum[:])

	out1, err := client.PutImage(ctx, &ecr.PutImageInput{
		RepositoryName: aws.String(repo),
		ImageManifest:  aws.String(manifest),
		ImageTag:       aws.String("v1"),
	})
	require.NoError(t, err)
	out2, err := client.PutImage(ctx, &ecr.PutImageInput{
		RepositoryName: aws.String(repo),
		ImageManifest:  aws.String(manifest),
		ImageTag:       aws.String("v2"),
	})
	require.NoError(t, err)

	assert.Equal(t, wantDigest, aws.ToString(out1.Image.ImageId.ImageDigest))
	assert.Equal(t, wantDigest, aws.ToString(out2.Image.ImageId.ImageDigest))
}

// TestECR_PullThroughCacheCreate verifies the simulator accepts a
// pull-through-cache rule via the official ECR SDK, which is the path
// sockerless's image resolver and terraform's
// aws_ecr_pull_through_cache_rule both take.
func TestECR_PullThroughCacheCreate(t *testing.T) {
	prefix := uniqueName("docker-hub")
	client := ecrClient()
	out, err := client.CreatePullThroughCacheRule(ctx, &ecr.CreatePullThroughCacheRuleInput{
		EcrRepositoryPrefix: aws.String(prefix),
		UpstreamRegistryUrl: aws.String("registry-1.docker.io"),
		UpstreamRegistry:    ecrtypes.UpstreamRegistryDockerHub,
	})
	require.NoError(t, err)
	assert.Equal(t, prefix, aws.ToString(out.EcrRepositoryPrefix))
	assert.Equal(t, "registry-1.docker.io", aws.ToString(out.UpstreamRegistryUrl))
}

// TestECR_PullThroughCacheCreateAlreadyExists verifies the simulator
// returns the same error shape AWS does when a prefix is reused.
func TestECR_PullThroughCacheCreateAlreadyExists(t *testing.T) {
	prefix := uniqueName("already-exists")
	client := ecrClient()
	_, err := client.CreatePullThroughCacheRule(ctx, &ecr.CreatePullThroughCacheRuleInput{
		EcrRepositoryPrefix: aws.String(prefix),
		UpstreamRegistryUrl: aws.String("registry-1.docker.io"),
		UpstreamRegistry:    ecrtypes.UpstreamRegistryDockerHub,
	})
	require.NoError(t, err)

	_, err = client.CreatePullThroughCacheRule(ctx, &ecr.CreatePullThroughCacheRuleInput{
		EcrRepositoryPrefix: aws.String(prefix),
		UpstreamRegistryUrl: aws.String("registry-1.docker.io"),
		UpstreamRegistry:    ecrtypes.UpstreamRegistryDockerHub,
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "PullThroughCacheRuleAlreadyExists")
}

// TestECR_PullThroughCacheDescribe verifies list-all and filtered list
// paths both round-trip.
func TestECR_PullThroughCacheDescribe(t *testing.T) {
	prefix := uniqueName("describe-ptc")
	client := ecrClient()
	_, err := client.CreatePullThroughCacheRule(ctx, &ecr.CreatePullThroughCacheRuleInput{
		EcrRepositoryPrefix: aws.String(prefix),
		UpstreamRegistryUrl: aws.String("registry-1.docker.io"),
		UpstreamRegistry:    ecrtypes.UpstreamRegistryDockerHub,
	})
	require.NoError(t, err)

	// Filtered
	filtered, err := client.DescribePullThroughCacheRules(ctx, &ecr.DescribePullThroughCacheRulesInput{
		EcrRepositoryPrefixes: []string{prefix},
	})
	require.NoError(t, err)
	require.Len(t, filtered.PullThroughCacheRules, 1)
	assert.Equal(t, prefix, aws.ToString(filtered.PullThroughCacheRules[0].EcrRepositoryPrefix))

	// All (at least our entry must appear)
	all, err := client.DescribePullThroughCacheRules(ctx, &ecr.DescribePullThroughCacheRulesInput{})
	require.NoError(t, err)
	var found bool
	for _, r := range all.PullThroughCacheRules {
		if aws.ToString(r.EcrRepositoryPrefix) == prefix {
			found = true
			break
		}
	}
	assert.True(t, found, "listed rules should include the created rule")
}

// TestECR_PullThroughCacheDelete verifies delete removes the rule and
// that re-deleting produces the not-found error shape.
func TestECR_PullThroughCacheDelete(t *testing.T) {
	client := ecrClient()
	_, err := client.CreatePullThroughCacheRule(ctx, &ecr.CreatePullThroughCacheRuleInput{
		EcrRepositoryPrefix: aws.String("delete-prefix"),
		UpstreamRegistryUrl: aws.String("registry-1.docker.io"),
		UpstreamRegistry:    ecrtypes.UpstreamRegistryDockerHub,
	})
	require.NoError(t, err)

	out, err := client.DeletePullThroughCacheRule(ctx, &ecr.DeletePullThroughCacheRuleInput{
		EcrRepositoryPrefix: aws.String("delete-prefix"),
	})
	require.NoError(t, err)
	assert.Equal(t, "delete-prefix", aws.ToString(out.EcrRepositoryPrefix))
	assert.Equal(t, "registry-1.docker.io", aws.ToString(out.UpstreamRegistryUrl))
	assert.NotNil(t, out.CreatedAt)

	// Second delete should fail with not-found.
	_, err = client.DeletePullThroughCacheRule(ctx, &ecr.DeletePullThroughCacheRuleInput{
		EcrRepositoryPrefix: aws.String("delete-prefix"),
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "PullThroughCacheRuleNotFound")
}

func TestECR_LifecyclePolicy(t *testing.T) {
	repo := uniqueName("lifecycle-repo")
	client := ecrClient()

	_, err := client.CreateRepository(ctx, &ecr.CreateRepositoryInput{
		RepositoryName: aws.String(repo),
	})
	require.NoError(t, err)

	policy := `{"rules":[{"rulePriority":1,"selection":{"tagStatus":"untagged","countType":"imageCountMoreThan","countNumber":5},"action":{"type":"expire"}}]}`
	_, err = client.PutLifecyclePolicy(ctx, &ecr.PutLifecyclePolicyInput{
		RepositoryName:      aws.String(repo),
		LifecyclePolicyText: aws.String(policy),
	})
	require.NoError(t, err)

	getOut, err := client.GetLifecyclePolicy(ctx, &ecr.GetLifecyclePolicyInput{
		RepositoryName: aws.String(repo),
	})
	require.NoError(t, err)
	assert.JSONEq(t, policy, aws.ToString(getOut.LifecyclePolicyText),
		"the policy read back must be the one that was put")
	assert.Equal(t, repo, aws.ToString(getOut.RepositoryName))
}

func TestECR_DescribeRepositories_Pagination(t *testing.T) {
	client := ecrClient()
	names := []string{"pag-ecr-repo-a", "pag-ecr-repo-b", "pag-ecr-repo-c"}
	for _, n := range names {
		_, err := client.CreateRepository(ctx, &ecr.CreateRepositoryInput{
			RepositoryName: aws.String(n),
		})
		require.NoError(t, err)
		t.Cleanup(func() {
			client.DeleteRepository(ctx, &ecr.DeleteRepositoryInput{RepositoryName: aws.String(n), Force: true})
		})
	}

	seen := map[string]bool{}
	pager := ecr.NewDescribeRepositoriesPaginator(client, &ecr.DescribeRepositoriesInput{MaxResults: aws.Int32(1)})
	for pager.HasMorePages() {
		page, err := pager.NextPage(ctx)
		require.NoError(t, err)
		for _, r := range page.Repositories {
			seen[aws.ToString(r.RepositoryName)] = true
		}
	}
	for _, n := range names {
		assert.True(t, seen[n], "repo %s should appear via pagination", n)
	}
}

// TestECR_DeleteRepositoryRefusesANonEmptyRepositoryWithoutForce deletes a
// repository holding an image: Amazon ECR refuses the delete unless the
// request sets force, keeps the repository and its image, and deletes both once
// force is set.
func TestECR_DeleteRepositoryRefusesANonEmptyRepositoryWithoutForce(t *testing.T) {
	repo := uniqueName("not-empty-repo")
	client := ecrClient()
	_, err := client.CreateRepository(ctx, &ecr.CreateRepositoryInput{RepositoryName: aws.String(repo)})
	require.NoError(t, err)
	manifest := `{"schemaVersion":2,"mediaType":"application/vnd.docker.distribution.manifest.v2+json","config":{"mediaType":"application/vnd.docker.container.image.v1+json","size":2,"digest":"sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"},"layers":[]}`
	_, err = client.PutImage(ctx, &ecr.PutImageInput{
		RepositoryName: aws.String(repo),
		ImageManifest:  aws.String(manifest),
		ImageTag:       aws.String("v1"),
	})
	require.NoError(t, err)

	_, err = client.DeleteRepository(ctx, &ecr.DeleteRepositoryInput{RepositoryName: aws.String(repo)})
	var notEmpty *ecrtypes.RepositoryNotEmptyException
	require.ErrorAs(t, err, &notEmpty)
	assert.Equal(t, "The repository with name '"+repo+"' in registry with id '"+ecrRegistryId(t)+
		"' cannot be deleted because it still contains images", notEmpty.ErrorMessage())

	images, err := client.ListImages(ctx, &ecr.ListImagesInput{RepositoryName: aws.String(repo)})
	require.NoError(t, err, "a refused delete keeps the repository")
	require.Len(t, images.ImageIds, 1, "a refused delete keeps the repository's images")

	_, err = client.DeleteRepository(ctx, &ecr.DeleteRepositoryInput{RepositoryName: aws.String(repo), Force: true})
	require.NoError(t, err)
	_, err = client.DescribeRepositories(ctx, &ecr.DescribeRepositoriesInput{RepositoryNames: []string{repo}})
	var missing *ecrtypes.RepositoryNotFoundException
	require.ErrorAs(t, err, &missing)
}

// TestECR_DeleteRepositoryDeletesAnEmptyRepositoryWithoutForce deletes a
// repository whose images were all removed: an empty repository needs no force.
func TestECR_DeleteRepositoryDeletesAnEmptyRepositoryWithoutForce(t *testing.T) {
	repo := uniqueName("emptied-repo")
	client := ecrClient()
	_, err := client.CreateRepository(ctx, &ecr.CreateRepositoryInput{RepositoryName: aws.String(repo)})
	require.NoError(t, err)
	manifest := `{"schemaVersion":2,"mediaType":"application/vnd.docker.distribution.manifest.v2+json","config":{"mediaType":"application/vnd.docker.container.image.v1+json","size":2,"digest":"sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"},"layers":[]}`
	_, err = client.PutImage(ctx, &ecr.PutImageInput{
		RepositoryName: aws.String(repo),
		ImageManifest:  aws.String(manifest),
		ImageTag:       aws.String("v1"),
	})
	require.NoError(t, err)
	deleted, err := client.BatchDeleteImage(ctx, &ecr.BatchDeleteImageInput{
		RepositoryName: aws.String(repo),
		ImageIds:       []ecrtypes.ImageIdentifier{{ImageTag: aws.String("v1")}},
	})
	require.NoError(t, err)
	require.Empty(t, deleted.Failures)

	_, err = client.DeleteRepository(ctx, &ecr.DeleteRepositoryInput{RepositoryName: aws.String(repo)})
	require.NoError(t, err)
}
