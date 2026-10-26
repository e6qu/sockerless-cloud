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
	"github.com/e6qu/sockerless-cloud/testutil/uniquename"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func ecrClient() *ecr.Client {
	return ecr.NewFromConfig(sdkConfig(), func(o *ecr.Options) {
		o.BaseEndpoint = aws.String(baseURL)
	})
}

func TestECR_CreateRepository(t *testing.T) {
	t.Parallel()
	repo := uniquename.New("test-repo")
	client := ecrClient()
	out, err := client.CreateRepository(ctx, &ecr.CreateRepositoryInput{
		RepositoryName: aws.String(repo),
	})
	require.NoError(t, err)
	assert.Equal(t, repo, *out.Repository.RepositoryName)
	assert.Contains(t, *out.Repository.RepositoryUri, repo)
}

func TestECR_DescribeRepositories(t *testing.T) {
	t.Parallel()
	repo := uniquename.New("describe-repo")
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
	t.Parallel()
	client := ecrClient()
	out, err := client.GetAuthorizationToken(ctx, &ecr.GetAuthorizationTokenInput{})
	require.NoError(t, err)
	require.Len(t, out.AuthorizationData, 1)
	data := out.AuthorizationData[0]

	// `docker login` replays the token as Basic auth, so it decodes to
	// `AWS:<password>`, the form the Amazon ECR API Reference documents for
	// AuthorizationData.authorizationToken.
	decoded, err := base64.StdEncoding.DecodeString(aws.ToString(data.AuthorizationToken))
	require.NoError(t, err, "the authorization token must be base64")
	user, password, ok := strings.Cut(string(decoded), ":")
	require.True(t, ok, "the decoded token must be user:password, got %q", decoded)
	assert.Equal(t, "AWS", user)
	assert.NotEmpty(t, password)

	// The proxy endpoint names this account's login server, and the token expires
	// in twelve hours, the documented lifetime.
	assert.Regexp(t, `^https://\d{12}\.dkr\.ecr\.us-east-1\.amazonaws\.com$`, aws.ToString(data.ProxyEndpoint))
	require.NotNil(t, data.ExpiresAt)
	assert.WithinDuration(t, time.Now().Add(12*time.Hour), *data.ExpiresAt, 5*time.Minute)

	// A second call issues a different password.
	again, err := client.GetAuthorizationToken(ctx, &ecr.GetAuthorizationTokenInput{})
	require.NoError(t, err)
	require.Len(t, again.AuthorizationData, 1)
	assert.NotEqual(t, aws.ToString(data.AuthorizationToken), aws.ToString(again.AuthorizationData[0].AuthorizationToken),
		"each call must issue a fresh authorization token")
}

func TestECR_PutImageDigestIsContentAddressed(t *testing.T) {
	t.Parallel()
	repo := uniquename.New("content-digest-repo")
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

// TestECR_PullThroughCacheCreate covers the path sockerless's image resolver
// and terraform's aws_ecr_pull_through_cache_rule take.
func TestECR_PullThroughCacheCreate(t *testing.T) {
	t.Parallel()
	prefix := uniquename.New("docker-hub")
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

// TestECR_PullThroughCacheCreateAlreadyExists expects AWS's error shape for a
// reused prefix.
func TestECR_PullThroughCacheCreateAlreadyExists(t *testing.T) {
	t.Parallel()
	prefix := uniquename.New("already-exists")
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

func TestECR_PullThroughCacheDescribe(t *testing.T) {
	t.Parallel()
	prefix := uniquename.New("describe-ptc")
	client := ecrClient()
	_, err := client.CreatePullThroughCacheRule(ctx, &ecr.CreatePullThroughCacheRuleInput{
		EcrRepositoryPrefix: aws.String(prefix),
		UpstreamRegistryUrl: aws.String("registry-1.docker.io"),
		UpstreamRegistry:    ecrtypes.UpstreamRegistryDockerHub,
	})
	require.NoError(t, err)

	filtered, err := client.DescribePullThroughCacheRules(ctx, &ecr.DescribePullThroughCacheRulesInput{
		EcrRepositoryPrefixes: []string{prefix},
	})
	require.NoError(t, err)
	require.Len(t, filtered.PullThroughCacheRules, 1)
	assert.Equal(t, prefix, aws.ToString(filtered.PullThroughCacheRules[0].EcrRepositoryPrefix))

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

func TestECR_PullThroughCacheDelete(t *testing.T) {
	t.Parallel()
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

	_, err = client.DeletePullThroughCacheRule(ctx, &ecr.DeletePullThroughCacheRuleInput{
		EcrRepositoryPrefix: aws.String("delete-prefix"),
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "PullThroughCacheRuleNotFound")
}

func TestECR_LifecyclePolicy(t *testing.T) {
	t.Parallel()
	repo := uniquename.New("lifecycle-repo")
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
	t.Parallel()
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

// TestECR_DeleteRepositoryRefusesANonEmptyRepositoryWithoutForce expects Amazon
// ECR to refuse deleting a repository holding an image unless force is set,
// keeping both, and to delete both with force.
func TestECR_DeleteRepositoryRefusesANonEmptyRepositoryWithoutForce(t *testing.T) {
	t.Parallel()
	repo := uniquename.New("not-empty-repo")
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

// TestECR_DeleteRepositoryDeletesAnEmptyRepositoryWithoutForce expects an
// emptied repository to delete without force.
func TestECR_DeleteRepositoryDeletesAnEmptyRepositoryWithoutForce(t *testing.T) {
	t.Parallel()
	repo := uniquename.New("emptied-repo")
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
