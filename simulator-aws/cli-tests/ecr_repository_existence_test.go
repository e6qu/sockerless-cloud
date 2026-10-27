package aws_cli_test

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Amazon ECR repositories are explicit resources: "With Amazon ECR, new
// repositories must be explicitly created before they can be used" (Amazon
// ECR User Guide, "Troubleshooting Amazon ECR error messages", HTTP 404:
// "Repository Does Not Exist" error). `aws ecr create-repository` creates one,
// and a creation template applied for CREATE_ON_PUSH lets a push create one.

func ecrCLIRegistryCredential(t *testing.T) string {
	t.Helper()
	password := strings.TrimSpace(runCLI(t, awsCLI("ecr", "get-login-password")))
	require.NotEmpty(t, password)
	return "Basic " + base64.StdEncoding.EncodeToString([]byte("AWS:"+password))
}

// ecrCLIRegistryId reads the registry id the registry names in its refusal.
func ecrCLIRegistryId(t *testing.T) string {
	t.Helper()
	var described struct {
		RegistryId string `json:"registryId"`
	}
	require.NoError(t, json.Unmarshal([]byte(runCLI(t, awsCLI("ecr", "describe-registry"))), &described))
	require.NotEmpty(t, described.RegistryId)
	return described.RegistryId
}

func ecrCLIRefusal(t *testing.T, body string) (code, message string) {
	t.Helper()
	var envelope struct {
		Errors []struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"errors"`
	}
	require.NoError(t, json.Unmarshal([]byte(body), &envelope), "registry error body: %s", body)
	require.Len(t, envelope.Errors, 1, "registry error body: %s", body)
	return envelope.Errors[0].Code, envelope.Errors[0].Message
}

func TestECRCLI_RegistryRefusesARepositoryThatDoesNotExist(t *testing.T) {
	t.Parallel()
	const repo = "ecr-cli-absent/app"
	const templatePrefix = "ecr-cli-create-on-push"
	const templated = templatePrefix + "/app"
	credential := ecrCLIRegistryCredential(t)
	registryId := ecrCLIRegistryId(t)
	wantMessage := "The repository with name '" + repo + "' does not exist in the registry with id '" + registryId + "'"

	t.Cleanup(func() {
		runCLI(t, awsCLI("ecr", "delete-repository", "--repository-name", repo, "--force"))
		runCLI(t, awsCLI("ecr", "delete-repository", "--repository-name", templated, "--force"))
		runCLI(t, awsCLI("ecr", "delete-repository-creation-template", "--prefix", templatePrefix))
	})

	assert.Contains(t, runCLIExpectError(t, awsCLI("ecr", "describe-repositories",
		"--repository-names", repo)), "RepositoryNotFoundException")

	resp, body := ecrCLIRegistryRequest(t, http.MethodPost, "/v2/"+repo+"/blobs/uploads/", nil, "", credential)
	require.Equal(t, http.StatusNotFound, resp.StatusCode)
	code, message := ecrCLIRefusal(t, body)
	assert.Equal(t, "NAME_UNKNOWN", code)
	assert.Equal(t, wantMessage, message)
	assert.Equal(t, "registry/2.0", resp.Header.Get("Docker-Distribution-Api-Version"))

	resp, body = ecrCLIRegistryRequest(t, http.MethodGet, "/v2/"+repo+"/manifests/v1", nil, "", credential)
	require.Equal(t, http.StatusNotFound, resp.StatusCode)
	code, message = ecrCLIRefusal(t, body)
	assert.Equal(t, "NAME_UNKNOWN", code)
	assert.Equal(t, wantMessage, message)

	runCLI(t, awsCLI("ecr", "create-repository", "--repository-name", repo))
	resp, _ = ecrCLIRegistryRequest(t, http.MethodPost, "/v2/"+repo+"/blobs/uploads/", nil, "", credential)
	require.Equal(t, http.StatusAccepted, resp.StatusCode)

	// "When there isn't a repository creation template that matches the target
	// repository for an image push, Amazon ECR will not create a repository with
	// default settings."
	runCLI(t, awsCLI("ecr", "create-repository-creation-template",
		"--prefix", templatePrefix,
		"--applied-for", "CREATE_ON_PUSH",
		"--image-tag-mutability", "IMMUTABLE"))

	resp, _ = ecrCLIRegistryRequest(t, http.MethodPost, "/v2/"+templated+"/blobs/uploads/", nil, "", credential)
	require.Equal(t, http.StatusAccepted, resp.StatusCode,
		"a push covered by a CREATE_ON_PUSH template must be accepted")

	var described struct {
		Repositories []struct {
			RepositoryName     string `json:"repositoryName"`
			ImageTagMutability string `json:"imageTagMutability"`
		} `json:"repositories"`
	}
	require.NoError(t, json.Unmarshal([]byte(runCLI(t, awsCLI("ecr", "describe-repositories",
		"--repository-names", templated))), &described))
	require.Len(t, described.Repositories, 1)
	assert.Equal(t, templated, described.Repositories[0].RepositoryName)
	assert.Equal(t, "IMMUTABLE", described.Repositories[0].ImageTagMutability,
		"the pushed-into repository must carry the template's settings")
}
