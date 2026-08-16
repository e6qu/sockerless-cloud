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

// Amazon ECR repositories are explicit resources, and the registry data plane
// serves only the ones the registry holds:
//
//	"If you specify a Docker Hub repository that does not currently exist,
//	 Docker Hub creates it automatically. With Amazon ECR, new repositories must
//	 be explicitly created before they can be used. This prevents new
//	 repositories from being created accidentally (for example, due to typos),
//	 and it also ensures that an appropriate security access policy is
//	 explicitly assigned to any new repositories."
//
// (Amazon ECR User Guide, "Troubleshooting Amazon ECR error messages", the
// section titled `HTTP 404: "Repository Does Not Exist" error`.)
//
// The refusal is a Docker Registry HTTP API v2 error envelope carrying
// NAME_UNKNOWN, which `docker push` renders as `name unknown: The repository
// with name 'dev/app1' does not exist in the registry with id '…'`
// (aws/containers-roadmap#1299) and go-containerregistry, printing the code
// verbatim, as `NAME_UNKNOWN: The repository with name '…' does not exist in
// the registry with id '…'` — on a tags list
// (concourse/registry-image-resource#268), on a manifest GET
// (GoogleContainerTools/kaniko#3443), and on the blob-upload probe a push
// permission check makes (GoogleContainerTools/kaniko#1120).

// ociErrorEnvelope reads the Docker Registry HTTP API v2 error envelope out of
// a registry response and returns its first error's code and message.
func ociErrorEnvelope(t *testing.T, resp *http.Response) (code, message string) {
	t.Helper()
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	var envelope struct {
		Errors []struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"errors"`
	}
	require.NoError(t, json.Unmarshal(body, &envelope), "registry error body: %s", body)
	require.NotEmpty(t, envelope.Errors, "registry error body carries no errors: %s", body)
	return envelope.Errors[0].Code, envelope.Errors[0].Message
}

// ecrRegistryId returns the registry the client is talking to, which a real
// client reads from the control plane rather than assuming — the same
// identifier the registry names in its refusal.
func ecrRegistryId(t *testing.T) string {
	t.Helper()
	registry, err := ecrClient().DescribeRegistry(ctx, &ecr.DescribeRegistryInput{})
	require.NoError(t, err)
	return aws.ToString(registry.RegistryId)
}

// ecrDeleteRepository removes a repository and everything in it, so a test that
// asserts on a repository's absence starts from it.
func ecrDeleteRepository(t *testing.T, repo string) {
	t.Helper()
	_, err := ecrClient().DeleteRepository(ctx, &ecr.DeleteRepositoryInput{
		RepositoryName: aws.String(repo),
		Force:          true,
	})
	if err != nil {
		var missing *ecrtypes.RepositoryNotFoundException
		require.ErrorAs(t, err, &missing)
	}
}

// TestECR_RegistryRefusesARepositoryThatDoesNotExist drives every
// repository-scoped registry route against a repository the registry does not
// hold, then creates the repository and shows the same routes start working,
// then deletes it and shows they stop again. A push that lands in a repository
// nobody created is the defect this locks out.
func TestECR_RegistryRefusesARepositoryThatDoesNotExist(t *testing.T) {
	const repo = "ecr-absent/app"
	ecrDeleteRepository(t, repo)
	t.Cleanup(func() { ecrDeleteRepository(t, repo) })

	layer := []byte("a-layer-for-a-repository-that-does-not-exist")
	digest := ociDigest(layer)
	manifest := []byte(`{"schemaVersion":2,"mediaType":"application/vnd.docker.distribution.manifest.v2+json","config":{"mediaType":"application/vnd.docker.container.image.v1+json","size":2,"digest":"` + digest + `"},"layers":[]}`)
	wantMessage := "The repository with name '" + repo + "' does not exist in the registry with id '" + ecrRegistryId(t) + "'"

	for _, route := range []struct {
		name        string
		method      string
		target      string
		body        []byte
		contentType string
	}{
		{"manifest pull", http.MethodGet, "/v2/" + repo + "/manifests/v1", nil, ""},
		{"blob pull", http.MethodGet, "/v2/" + repo + "/blobs/" + digest, nil, ""},
		{"tags list", http.MethodGet, "/v2/" + repo + "/tags/list", nil, ""},
		{"blob upload", http.MethodPost, "/v2/" + repo + "/blobs/uploads/", nil, ""},
		{"manifest push", http.MethodPut, "/v2/" + repo + "/manifests/v1", manifest,
			"application/vnd.docker.distribution.manifest.v2+json"},
	} {
		resp := ociDo(t, route.method, baseURL+route.target, route.body, route.contentType)
		code, message := ociErrorEnvelope(t, resp)
		resp.Body.Close()
		require.Equal(t, http.StatusNotFound, resp.StatusCode,
			"%s to a repository that does not exist must be refused", route.name)
		assert.Equal(t, "NAME_UNKNOWN", code, "%s must be refused with NAME_UNKNOWN", route.name)
		assert.Equal(t, wantMessage, message)
		assert.Equal(t, "registry/2.0", resp.Header.Get("Docker-Distribution-Api-Version"))
		assert.Empty(t, resp.Header.Get("Docker-Upload-UUID"),
			"a refused %s must not allocate an upload", route.name)
	}

	// A HEAD carries no body, so the refusal a client sees there is the status.
	resp := ociDo(t, http.MethodHead, baseURL+"/v2/"+repo+"/manifests/v1", nil, "")
	resp.Body.Close()
	require.Equal(t, http.StatusNotFound, resp.StatusCode)

	// Nothing the refused push carried reached the control plane.
	_, err := ecrClient().DescribeImages(ctx, &ecr.DescribeImagesInput{
		RepositoryName: aws.String(repo),
	})
	var missing *ecrtypes.RepositoryNotFoundException
	require.ErrorAs(t, err, &missing,
		"a refused push must not have brought the repository into existence")

	// Creating the repository — what the documented push flow tells a user to do
	// first — is what makes the registry serve it.
	ecrCreateRepository(t, repo)

	resp = ociDo(t, http.MethodPost, baseURL+"/v2/"+repo+"/blobs/uploads/", nil, "")
	location := resp.Header.Get("Location")
	resp.Body.Close()
	require.Equal(t, http.StatusAccepted, resp.StatusCode,
		"a push to a repository that exists must be accepted")
	require.NotEmpty(t, location)

	// A repository that exists but holds nothing answers a pull for the absent
	// tag, not for the absent repository.
	resp = ociDo(t, http.MethodGet, baseURL+"/v2/"+repo+"/manifests/v1", nil, "")
	code, _ := ociErrorEnvelope(t, resp)
	resp.Body.Close()
	require.Equal(t, http.StatusNotFound, resp.StatusCode)
	assert.Equal(t, "MANIFEST_UNKNOWN", code,
		"an empty repository that exists must report the manifest missing, not the name unknown")

	// Finish the push, then delete the repository: the content goes with it and
	// the registry is back to refusing the name.
	resp = ociDo(t, http.MethodPatch, baseURL+location, layer, "application/octet-stream")
	resp.Body.Close()
	resp = ociDo(t, http.MethodPut, baseURL+location+"?digest="+digest, nil, "")
	resp.Body.Close()
	require.Equal(t, http.StatusCreated, resp.StatusCode)
	resp = ociDo(t, http.MethodPut, baseURL+"/v2/"+repo+"/manifests/v1", manifest,
		"application/vnd.docker.distribution.manifest.v2+json")
	resp.Body.Close()
	require.Equal(t, http.StatusCreated, resp.StatusCode)

	ecrDeleteRepository(t, repo)

	resp = ociDo(t, http.MethodGet, baseURL+"/v2/"+repo+"/manifests/v1", nil, "")
	code, message := ociErrorEnvelope(t, resp)
	resp.Body.Close()
	require.Equal(t, http.StatusNotFound, resp.StatusCode)
	assert.Equal(t, "NAME_UNKNOWN", code,
		"a deleted repository is a repository that does not exist")
	assert.Equal(t, wantMessage, message)
}

// TestECR_RegistryCreatesOnPushFromARepositoryCreationTemplate covers the one
// push Amazon ECR does not refuse:
//
//	"When there isn't a repository creation template that matches the target
//	 repository for an image push, Amazon ECR will not create a repository with
//	 default settings."
//
// (Amazon ECR User Guide, "Templates to control repositories created during a
// pull through cache, create on push, or replication action".) So a push
// covered by a template applied for CREATE_ON_PUSH creates the repository with
// that template's settings, and nothing else does.
func TestECR_RegistryCreatesOnPushFromARepositoryCreationTemplate(t *testing.T) {
	client := ecrClient()
	const prefix = "ecr-create-on-push"
	const pushed = prefix + "/app"
	const pulled = prefix + "/never-pushed"
	const uncovered = "ecr-no-template/app"
	const lifecyclePolicy = `{"rules":[{"rulePriority":1,"description":"expire untagged","selection":{"tagStatus":"untagged","countType":"sinceImagePushed","countUnit":"days","countNumber":14},"action":{"type":"expire"}}]}`

	for _, repo := range []string{pushed, pulled, uncovered} {
		ecrDeleteRepository(t, repo)
		t.Cleanup(func() { ecrDeleteRepository(t, repo) })
	}
	// The prefix is this test's own, and a previous run against a persisted
	// registry may have left the template behind, so it is removed before it is
	// created rather than asserted absent.
	_, _ = client.DeleteRepositoryCreationTemplate(ctx, &ecr.DeleteRepositoryCreationTemplateInput{
		Prefix: aws.String(prefix),
	})
	_, err := client.CreateRepositoryCreationTemplate(ctx, &ecr.CreateRepositoryCreationTemplateInput{
		Prefix:             aws.String(prefix),
		AppliedFor:         []ecrtypes.RCTAppliedFor{ecrtypes.RCTAppliedForCreateOnPush},
		Description:        aws.String("repositories created by an image push"),
		ImageTagMutability: ecrtypes.ImageTagMutabilityImmutable,
		LifecyclePolicy:    aws.String(lifecyclePolicy),
		CustomRoleArn:      aws.String("arn:aws:iam::" + ecrRegistryId(t) + ":role/ecr-repository-creation"),
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = client.DeleteRepositoryCreationTemplate(ctx, &ecr.DeleteRepositoryCreationTemplateInput{
			Prefix: aws.String(prefix),
		})
	})

	// A pull is not a push: the template covers the name but nothing is being
	// created, so the registry still refuses it.
	resp := ociDo(t, http.MethodGet, baseURL+"/v2/"+pulled+"/manifests/v1", nil, "")
	code, _ := ociErrorEnvelope(t, resp)
	resp.Body.Close()
	require.Equal(t, http.StatusNotFound, resp.StatusCode)
	assert.Equal(t, "NAME_UNKNOWN", code, "a pull must not create a repository")
	_, err = client.DescribeRepositories(ctx, &ecr.DescribeRepositoriesInput{
		RepositoryNames: []string{pulled},
	})
	var missing *ecrtypes.RepositoryNotFoundException
	require.ErrorAs(t, err, &missing)

	// A push covered by the template creates the repository from it.
	resp = ociDo(t, http.MethodPost, baseURL+"/v2/"+pushed+"/blobs/uploads/", nil, "")
	resp.Body.Close()
	require.Equal(t, http.StatusAccepted, resp.StatusCode,
		"a push covered by a CREATE_ON_PUSH template must be accepted")

	described, err := client.DescribeRepositories(ctx, &ecr.DescribeRepositoriesInput{
		RepositoryNames: []string{pushed},
	})
	require.NoError(t, err)
	require.Len(t, described.Repositories, 1)
	assert.Equal(t, ecrtypes.ImageTagMutabilityImmutable, described.Repositories[0].ImageTagMutability,
		"the created repository must carry the template's tag mutability")

	policy, err := client.GetLifecyclePolicy(ctx, &ecr.GetLifecyclePolicyInput{
		RepositoryName: aws.String(pushed),
	})
	require.NoError(t, err)
	assert.JSONEq(t, lifecyclePolicy, aws.ToString(policy.LifecyclePolicyText),
		"the created repository must carry the template's lifecycle policy")

	// A push outside every template's prefix is still refused.
	resp = ociDo(t, http.MethodPost, baseURL+"/v2/"+uncovered+"/blobs/uploads/", nil, "")
	code, _ = ociErrorEnvelope(t, resp)
	resp.Body.Close()
	require.Equal(t, http.StatusNotFound, resp.StatusCode)
	assert.Equal(t, "NAME_UNKNOWN", code)

	// And so is one covered only by a template applied for another scenario: a
	// pull-through cache template is not in the path of an image push.
	const cachePrefix = "ecr-cache-only"
	_, _ = client.DeleteRepositoryCreationTemplate(ctx, &ecr.DeleteRepositoryCreationTemplateInput{
		Prefix: aws.String(cachePrefix),
	})
	_, err = client.CreateRepositoryCreationTemplate(ctx, &ecr.CreateRepositoryCreationTemplateInput{
		Prefix:     aws.String(cachePrefix),
		AppliedFor: []ecrtypes.RCTAppliedFor{ecrtypes.RCTAppliedForPullThroughCache},
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = client.DeleteRepositoryCreationTemplate(ctx, &ecr.DeleteRepositoryCreationTemplateInput{
			Prefix: aws.String(cachePrefix),
		})
	})

	resp = ociDo(t, http.MethodPost, baseURL+"/v2/"+cachePrefix+"/app/blobs/uploads/", nil, "")
	code, _ = ociErrorEnvelope(t, resp)
	resp.Body.Close()
	require.Equal(t, http.StatusNotFound, resp.StatusCode)
	assert.Equal(t, "NAME_UNKNOWN", code,
		"a template applied only for PULL_THROUGH_CACHE must not create a repository on push")
	t.Cleanup(func() { ecrDeleteRepository(t, cachePrefix+"/app") })
}
