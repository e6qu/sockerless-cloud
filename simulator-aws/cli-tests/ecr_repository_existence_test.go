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

// CLI coverage for Amazon ECR's rule that a repository is an explicit resource:
// "With Amazon ECR, new repositories must be explicitly created before they can
// be used" (Amazon ECR User Guide, "Troubleshooting Amazon ECR error messages"
// § HTTP 404: "Repository Does Not Exist" error). `aws ecr create-repository`
// is what the documented push flow has a user run first, and
// `aws ecr create-repository-creation-template --applied-for CREATE_ON_PUSH` is
// what lets a push create one instead.

// ecrCLIRegistryCredential returns the Basic credential a Docker client holds
// after `aws ecr get-login-password | docker login --username AWS
// --password-stdin <registry>`.
func ecrCLIRegistryCredential(t *testing.T) string {
	t.Helper()
	password := strings.TrimSpace(runCLI(t, awsCLI("ecr", "get-login-password")))
	require.NotEmpty(t, password)
	return "Basic " + base64.StdEncoding.EncodeToString([]byte("AWS:"+password))
}

// ecrCLIRegistryId reads the registry the CLI is talking to off the control
// plane, which is the identifier the registry names in its refusal.
func ecrCLIRegistryId(t *testing.T) string {
	t.Helper()
	var described struct {
		RegistryId string `json:"registryId"`
	}
	require.NoError(t, json.Unmarshal([]byte(runCLI(t, awsCLI("ecr", "describe-registry"))), &described))
	require.NotEmpty(t, described.RegistryId)
	return described.RegistryId
}

// ecrCLIRefusal reads the Docker Registry HTTP API v2 error envelope out of a
// registry refusal body.
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

	// The CLI agrees the repository is absent.
	assert.Contains(t, runCLIExpectError(t, awsCLI("ecr", "describe-repositories",
		"--repository-names", repo)), "RepositoryNotFoundException")

	// So the registry refuses the push a client would make.
	resp, body := ecrCLIRegistryRequest(t, http.MethodPost, "/v2/"+repo+"/blobs/uploads/", nil, "", credential)
	require.Equal(t, http.StatusNotFound, resp.StatusCode)
	code, message := ecrCLIRefusal(t, body)
	assert.Equal(t, "NAME_UNKNOWN", code)
	assert.Equal(t, wantMessage, message)
	assert.Equal(t, "registry/2.0", resp.Header.Get("Docker-Distribution-Api-Version"))

	// And the pull, which is the same refusal.
	resp, body = ecrCLIRegistryRequest(t, http.MethodGet, "/v2/"+repo+"/manifests/v1", nil, "", credential)
	require.Equal(t, http.StatusNotFound, resp.StatusCode)
	code, message = ecrCLIRefusal(t, body)
	assert.Equal(t, "NAME_UNKNOWN", code)
	assert.Equal(t, wantMessage, message)

	// `aws ecr create-repository` is what makes the registry serve the name.
	runCLI(t, awsCLI("ecr", "create-repository", "--repository-name", repo))
	resp, _ = ecrCLIRegistryRequest(t, http.MethodPost, "/v2/"+repo+"/blobs/uploads/", nil, "", credential)
	require.Equal(t, http.StatusAccepted, resp.StatusCode)

	// A repository creation template applied for CREATE_ON_PUSH is the other
	// way a repository comes into existence: "When there isn't a repository
	// creation template that matches the target repository for an image push,
	// Amazon ECR will not create a repository with default settings."
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
