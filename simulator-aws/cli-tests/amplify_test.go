package aws_cli_test

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/e6qu/sockerless-cloud/testutil/githttp"
	"github.com/stretchr/testify/require"
)

func TestAmplify_WAFv2_Association_CLI(t *testing.T) {
	appOut := runCLI(t, awsCLI("amplify", "create-app",
		"--name", "cli-amplify-waf-"+wafStamp(),
		"--output", "json",
	))
	var appResult struct {
		App struct {
			AppID  string `json:"appId"`
			AppARN string `json:"appArn"`
		} `json:"app"`
	}
	require.NoError(t, json.Unmarshal([]byte(appOut), &appResult))
	require.NotEmpty(t, appResult.App.AppID)
	t.Cleanup(func() {
		_ = awsCLI("amplify", "delete-app", "--app-id", appResult.App.AppID).Run()
	})

	aclName := "cli-amplify-waf-" + wafStamp()
	aclOut := runCLI(t, awsCLI("wafv2", "create-web-acl",
		"--name", aclName,
		"--scope", "CLOUDFRONT",
		"--default-action", `{"Allow":{}}`,
		"--visibility-config", fmt.Sprintf(
			`{"SampledRequestsEnabled":true,"CloudWatchMetricsEnabled":true,"MetricName":%q}`,
			aclName,
		),
		"--output", "json",
	))
	var aclResult struct {
		Summary struct {
			ID        string `json:"Id"`
			ARN       string `json:"ARN"`
			LockToken string `json:"LockToken"`
		} `json:"Summary"`
	}
	require.NoError(t, json.Unmarshal([]byte(aclOut), &aclResult))
	t.Cleanup(func() {
		_ = awsCLI("wafv2", "disassociate-web-acl", "--resource-arn", appResult.App.AppARN).Run()
		_ = awsCLI("wafv2", "delete-web-acl",
			"--name", aclName, "--scope", "CLOUDFRONT",
			"--id", aclResult.Summary.ID, "--lock-token", aclResult.Summary.LockToken).Run()
	})

	runCLI(t, awsCLI("wafv2", "associate-web-acl",
		"--web-acl-arn", aclResult.Summary.ARN,
		"--resource-arn", appResult.App.AppARN,
	))
	getOut := runCLI(t, awsCLI("amplify", "get-app",
		"--app-id", appResult.App.AppID,
		"--output", "json",
	))
	var associated struct {
		App struct {
			WAFConfiguration struct {
				WebACLARN string `json:"webAclArn"`
				WAFStatus string `json:"wafStatus"`
			} `json:"wafConfiguration"`
		} `json:"app"`
	}
	require.NoError(t, json.Unmarshal([]byte(getOut), &associated))
	require.Equal(t, aclResult.Summary.ARN, associated.App.WAFConfiguration.WebACLARN)
	require.Equal(t, "ASSOCIATION_SUCCESS", associated.App.WAFConfiguration.WAFStatus)

	runCLI(t, awsCLI("wafv2", "disassociate-web-acl",
		"--resource-arn", appResult.App.AppARN,
	))
	getOut = runCLI(t, awsCLI("amplify", "get-app",
		"--app-id", appResult.App.AppID,
		"--output", "json",
	))
	var disassociated map[string]any
	require.NoError(t, json.Unmarshal([]byte(getOut), &disassociated))
	appBody := disassociated["app"].(map[string]any)
	_, hasWAFConfiguration := appBody["wafConfiguration"]
	require.False(t, hasWAFConfiguration)
}

// amplifyWaitJobStatus polls get-job while the real build or deployment runs.
func amplifyWaitJobStatus(t *testing.T, appID, branch, jobID, want string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Minute)
	for {
		out := runCLI(t, awsCLI("amplify", "get-job",
			"--app-id", appID,
			"--branch-name", branch,
			"--job-id", jobID,
			"--output", "json"))
		var result struct {
			Job struct {
				Summary struct {
					Status string `json:"status"`
				} `json:"summary"`
			} `json:"job"`
		}
		require.NoError(t, json.Unmarshal([]byte(out), &result))
		if result.Job.Summary.Status == want {
			return
		}
		if result.Job.Summary.Status == "FAILED" || result.Job.Summary.Status == "CANCELLED" {
			t.Fatalf("job %s reached terminal status %s while waiting for %s: %s", jobID, result.Job.Summary.Status, want, out)
		}
		if time.Now().After(deadline) {
			t.Fatalf("job %s never reached %s (last status %s)", jobID, want, result.Job.Summary.Status)
		}
		time.Sleep(150 * time.Millisecond)
	}
}

func amplifyCLIServeGitRepo(t *testing.T, files map[string]string) string {
	t.Helper()
	return githttp.Serve(t, "main", files)
}

func TestAmplify_App_Lifecycle(t *testing.T) {
	name := "cli-app-" + time.Now().Format("150405.000000")
	repository := amplifyCLIServeGitRepo(t, map[string]string{
		"index.html": "<html>CLI real build</html>",
		"amplify.yml": `version: 1
frontend:
  phases:
    build:
      commands:
        - sleep 2
        - mkdir -p dist
        - cp index.html dist/index.html
  artifacts:
    baseDirectory: dist
    files:
      - '**/*'
test:
  phases:
    test:
      commands:
        - mkdir -p cypress/screenshots cypress/report
        - printf cli-screenshot > cypress/screenshots/home.png
        - printf '{"stats":{"tests":1,"passes":1}}' > cypress/report/mochawesome.json
  artifacts:
    baseDirectory: cypress
    configFilePath: '**/mochawesome.json'
    files:
      - '**/*.png'
`,
	})
	out := runCLI(t, awsCLI("amplify", "create-app",
		"--name", name,
		"--description", "cli test",
		"--platform", "WEB",
		"--repository", repository,
		"--output", "json",
	))
	var createResult struct {
		App struct {
			AppId         string `json:"appId"`
			Name          string `json:"name"`
			DefaultDomain string `json:"defaultDomain"`
		} `json:"app"`
	}
	require.NoError(t, json.Unmarshal([]byte(out), &createResult))
	require.NotEmpty(t, createResult.App.AppId)
	require.Equal(t, name, createResult.App.Name)
	appID := createResult.App.AppId

	runCLI(t, awsCLI("amplify", "get-app", "--app-id", appID, "--output", "json"))
	runCLI(t, awsCLI("amplify", "list-apps", "--output", "json"))

	// Branch
	brOut := runCLI(t, awsCLI("amplify", "create-branch",
		"--app-id", appID, "--branch-name", "main", "--output", "json"))
	var brResult struct {
		Branch struct {
			BranchName string `json:"branchName"`
		} `json:"branch"`
	}
	require.NoError(t, json.Unmarshal([]byte(brOut), &brResult))
	require.Equal(t, "main", brResult.Branch.BranchName)

	// Webhook
	whOut := runCLI(t, awsCLI("amplify", "create-webhook",
		"--app-id", appID, "--branch-name", "main",
		"--description", "cli webhook", "--output", "json"))
	var whResult struct {
		Webhook struct {
			WebhookId  string `json:"webhookId"`
			WebhookUrl string `json:"webhookUrl"`
			AppId      string `json:"appId"`
		} `json:"webhook"`
	}
	require.NoError(t, json.Unmarshal([]byte(whOut), &whResult))
	require.NotEmpty(t, whResult.Webhook.WebhookId)
	require.NotEmpty(t, whResult.Webhook.WebhookUrl)
	require.Equal(t, appID, whResult.Webhook.AppId)

	// Job — clones the repository, reads its checked-in amplify.yml, executes
	// the build, and publishes the resulting zip.
	jobOut := runCLI(t, awsCLI("amplify", "start-job",
		"--app-id", appID, "--branch-name", "main",
		"--job-type", "RELEASE",
		"--output", "json"))
	var jobResult struct {
		JobSummary struct {
			JobId  string `json:"jobId"`
			Status string `json:"status"`
		} `json:"jobSummary"`
	}
	require.NoError(t, json.Unmarshal([]byte(jobOut), &jobResult))
	require.NotEmpty(t, jobResult.JobSummary.JobId)
	jobID := jobResult.JobSummary.JobId
	amplifyWaitJobStatus(t, appID, "main", jobID, "SUCCEED")

	getJobOut := runCLI(t, awsCLI("amplify", "get-job",
		"--app-id", appID,
		"--branch-name", "main",
		"--job-id", jobID,
		"--output", "json"))
	var getJobResult struct {
		Job struct {
			Steps []struct {
				StepName         string `json:"stepName"`
				ArtifactsURL     string `json:"artifactsUrl"`
				TestArtifactsURL string `json:"testArtifactsUrl"`
				TestConfigURL    string `json:"testConfigUrl"`
			} `json:"steps"`
		} `json:"job"`
	}
	require.NoError(t, json.Unmarshal([]byte(getJobOut), &getJobResult))
	var buildArtifactURL, testArtifactsURL, testConfigURL string
	for _, step := range getJobResult.Job.Steps {
		if step.StepName == "BUILD" {
			buildArtifactURL = step.ArtifactsURL
			testArtifactsURL = step.TestArtifactsURL
			testConfigURL = step.TestConfigURL
		}
	}
	require.NotEmpty(t, buildArtifactURL)
	require.NotEmpty(t, testArtifactsURL)
	require.NotEmpty(t, testConfigURL)
	buildArtifactResponse, err := http.Get(buildArtifactURL)
	require.NoError(t, err)
	buildArtifactResponse.Body.Close()
	require.Equal(t, http.StatusOK, buildArtifactResponse.StatusCode)

	// ListArtifacts returns the concrete end-to-end test outputs and
	// GetArtifactUrl resolves the selected output through its presigned URL.
	artifactsOut := runCLI(t, awsCLI("amplify", "list-artifacts",
		"--app-id", appID,
		"--branch-name", "main",
		"--job-id", jobID,
		"--max-results", "1",
		"--output", "json"))
	var artifactsResult struct {
		Artifacts []struct {
			ArtifactID       string `json:"artifactId"`
			ArtifactFileName string `json:"artifactFileName"`
		} `json:"artifacts"`
	}
	require.NoError(t, json.Unmarshal([]byte(artifactsOut), &artifactsResult))
	require.Len(t, artifactsResult.Artifacts, 1)
	require.Equal(t, "screenshots/home.png", artifactsResult.Artifacts[0].ArtifactFileName)

	artifactURLOut := runCLI(t, awsCLI("amplify", "get-artifact-url",
		"--artifact-id", artifactsResult.Artifacts[0].ArtifactID,
		"--output", "json"))
	var artifactURLResult struct {
		ArtifactURL string `json:"artifactUrl"`
	}
	require.NoError(t, json.Unmarshal([]byte(artifactURLOut), &artifactURLResult))
	artifactResponse, err := http.Get(artifactURLResult.ArtifactURL)
	require.NoError(t, err)
	defer artifactResponse.Body.Close()
	require.Equal(t, http.StatusOK, artifactResponse.StatusCode)
	artifactBody, err := io.ReadAll(artifactResponse.Body)
	require.NoError(t, err)
	require.Equal(t, "cli-screenshot", string(artifactBody))

	logsOut := runCLI(t, awsCLI("amplify", "generate-access-logs",
		"--app-id", appID,
		"--domain-name", createResult.App.DefaultDomain,
		"--output", "json"))
	var logsResult struct {
		LogUrl string `json:"logUrl"`
	}
	require.NoError(t, json.Unmarshal([]byte(logsOut), &logsResult))
	require.NotEmpty(t, logsResult.LogUrl)

	// Stopping the finished job is rejected.
	_, err = awsCLI("amplify", "stop-job",
		"--app-id", appID,
		"--branch-name", "main",
		"--job-id", jobID,
		"--output", "json").CombinedOutput()
	require.Error(t, err)

	// A job stopped inside its run window lands CANCELLED.
	secondJobOut := runCLI(t, awsCLI("amplify", "start-job",
		"--app-id", appID, "--branch-name", "main",
		"--job-type", "RELEASE",
		"--output", "json"))
	var secondJobResult struct {
		JobSummary struct {
			JobId string `json:"jobId"`
		} `json:"jobSummary"`
	}
	require.NoError(t, json.Unmarshal([]byte(secondJobOut), &secondJobResult))
	secondJobID := secondJobResult.JobSummary.JobId
	stopOut := runCLI(t, awsCLI("amplify", "stop-job",
		"--app-id", appID,
		"--branch-name", "main",
		"--job-id", secondJobID,
		"--output", "json"))
	var stopResult struct {
		JobSummary struct {
			JobId  string `json:"jobId"`
			Status string `json:"status"`
		} `json:"jobSummary"`
	}
	require.NoError(t, json.Unmarshal([]byte(stopOut), &stopResult))
	require.Equal(t, secondJobID, stopResult.JobSummary.JobId)
	require.Equal(t, "CANCELLED", stopResult.JobSummary.Status)

	for _, id := range []string{jobID, secondJobID} {
		runCLI(t, awsCLI("amplify", "delete-job",
			"--app-id", appID,
			"--branch-name", "main",
			"--job-id", id,
			"--output", "json"))
	}

	_, err = awsCLI("amplify", "get-job",
		"--app-id", appID,
		"--branch-name", "main",
		"--job-id", jobID,
		"--output", "json").CombinedOutput()
	require.Error(t, err)

	// Cleanup
	runCLI(t, awsCLI("amplify", "delete-webhook", "--webhook-id", whResult.Webhook.WebhookId))
	runCLI(t, awsCLI("amplify", "delete-app", "--app-id", appID))
}

func TestAmplify_Deployment_Flow(t *testing.T) {
	name := "cli-dep-" + time.Now().Format("150405.000000")
	out := runCLI(t, awsCLI("amplify", "create-app", "--name", name, "--output", "json"))
	var createResult struct {
		App struct {
			AppId string `json:"appId"`
		} `json:"app"`
	}
	require.NoError(t, json.Unmarshal([]byte(out), &createResult))
	appID := createResult.App.AppId
	defer runCLI(t, awsCLI("amplify", "delete-app", "--app-id", appID))
	runCLI(t, awsCLI("amplify", "create-branch",
		"--app-id", appID, "--branch-name", "main", "--output", "json"))

	depOut := runCLI(t, awsCLI("amplify", "create-deployment",
		"--app-id", appID, "--branch-name", "main", "--output", "json"))
	var depResult struct {
		JobId          string            `json:"jobId"`
		ZipUploadUrl   string            `json:"zipUploadUrl"`
		FileUploadUrls map[string]string `json:"fileUploadUrls"`
	}
	require.NoError(t, json.Unmarshal([]byte(depOut), &depResult))
	require.NotEmpty(t, depResult.JobId)
	require.NotEmpty(t, depResult.ZipUploadUrl)

	// Upload a real site zip to the presigned URL the way the console /
	// amplify tooling does.
	var zipBuf bytes.Buffer
	zw := zip.NewWriter(&zipBuf)
	indexFile, err := zw.Create("index.html")
	require.NoError(t, err)
	_, err = indexFile.Write([]byte("<html>cli deployed " + depResult.JobId + "</html>"))
	require.NoError(t, err)
	require.NoError(t, zw.Close())
	zipBytes := zipBuf.Bytes()
	putReq, err := http.NewRequest(http.MethodPut, depResult.ZipUploadUrl, bytes.NewReader(zipBytes))
	require.NoError(t, err)
	putReq.Header.Set("Content-Type", "application/zip")
	putResp, err := http.DefaultClient.Do(putReq)
	require.NoError(t, err)
	defer putResp.Body.Close()
	require.Equal(t, http.StatusOK, putResp.StatusCode)

	startOut := runCLI(t, awsCLI("amplify", "start-deployment",
		"--app-id", appID, "--branch-name", "main",
		"--job-id", depResult.JobId,
		"--output", "json"))
	var startResult struct {
		JobSummary struct {
			JobId   string `json:"jobId"`
			JobType string `json:"jobType"`
		} `json:"jobSummary"`
	}
	require.NoError(t, json.Unmarshal([]byte(startOut), &startResult))
	require.Equal(t, depResult.JobId, startResult.JobSummary.JobId)
	require.Equal(t, "MANUAL", startResult.JobSummary.JobType)

	amplifyWaitJobStatus(t, appID, "main", depResult.JobId, "SUCCEED")

	// The uploaded build bundle is returned by GetJob's DEPLOY step, byte for
	// byte. ListArtifacts remains the end-to-end test artifact surface.
	getJobOut := runCLI(t, awsCLI("amplify", "get-job",
		"--app-id", appID, "--branch-name", "main",
		"--job-id", depResult.JobId, "--output", "json"))
	var getJobResult struct {
		Job struct {
			Steps []struct {
				StepName     string `json:"stepName"`
				ArtifactsURL string `json:"artifactsUrl"`
			} `json:"steps"`
		} `json:"job"`
	}
	require.NoError(t, json.Unmarshal([]byte(getJobOut), &getJobResult))
	var deploymentArtifactURL string
	for _, step := range getJobResult.Job.Steps {
		if step.StepName == "DEPLOY" {
			deploymentArtifactURL = step.ArtifactsURL
		}
	}
	require.NotEmpty(t, deploymentArtifactURL)

	artifactsOut := runCLI(t, awsCLI("amplify", "list-artifacts",
		"--app-id", appID, "--branch-name", "main",
		"--job-id", depResult.JobId, "--output", "json"))
	var artifactsResult struct {
		Artifacts []json.RawMessage `json:"artifacts"`
	}
	require.NoError(t, json.Unmarshal([]byte(artifactsOut), &artifactsResult))
	require.Empty(t, artifactsResult.Artifacts)
	resp, err := http.Get(deploymentArtifactURL)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	gotBytes, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Equal(t, zipBytes, gotBytes)

	// Hosting smoke: the deployed branch serves on its defaultDomain child
	// host (curl-style loopback request with a Host: header — the
	// cross-platform pattern, *.amplifyapp.com does not resolve locally).
	hostReq, err := http.NewRequest(http.MethodGet, baseURL+"/", nil)
	require.NoError(t, err)
	hostReq.Host = "main." + appID + ".amplifyapp.com"
	hostResp, err := http.DefaultClient.Do(hostReq)
	require.NoError(t, err)
	defer hostResp.Body.Close()
	require.Equal(t, http.StatusOK, hostResp.StatusCode)
	hostBody, err := io.ReadAll(hostResp.Body)
	require.NoError(t, err)
	require.Equal(t, "<html>cli deployed "+depResult.JobId+"</html>", string(hostBody))
	require.Contains(t, hostResp.Header.Get("Content-Type"), "text/html")

	// start-deployment without jobId or sourceUrl is rejected.
	_, err = awsCLI("amplify", "start-deployment",
		"--app-id", appID, "--branch-name", "main",
		"--output", "json").CombinedOutput()
	require.Error(t, err)
}
