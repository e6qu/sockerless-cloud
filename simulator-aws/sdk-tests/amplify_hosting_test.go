package aws_sdk_test

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/amplify"
	amplifytypes "github.com/aws/aws-sdk-go-v2/service/amplify/types"
	"github.com/aws/aws-sdk-go-v2/service/route53"
	r53types "github.com/aws/aws-sdk-go-v2/service/route53/types"
	"github.com/aws/aws-sdk-go-v2/service/sts"
	"github.com/e6qu/sockerless-cloud/testutil/githttp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// *.amplifyapp.com resolves nowhere locally, so hosting requests reach the
// simulator's loopback address with a Host header.

func amplifyHostGet(t *testing.T, host, path string, mutate func(*http.Request)) (*http.Response, []byte) {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, baseURL+path, nil)
	require.NoError(t, err)
	req.Host = host
	if mutate != nil {
		mutate(req)
	}
	client := &http.Client{
		// Redirect targets name the virtual host; assert the 3xx instead of following
		// it.
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		Timeout:       2 * time.Minute,
	}
	resp, err := client.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	return resp, body
}

func amplifyZipBytes(t *testing.T, files map[string][]byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, data := range files {
		f, err := zw.Create(name)
		require.NoError(t, err)
		_, err = f.Write(data)
		require.NoError(t, err)
	}
	require.NoError(t, zw.Close())
	return buf.Bytes()
}

// amplifyDeployZip runs the manual deploy: CreateDeployment, a presigned PUT
// of the zip, StartDeployment, SUCCEED.
func amplifyDeployZip(t *testing.T, ctx context.Context, c *amplify.Client, appID, branch string, zipBytes []byte) string {
	t.Helper()
	createDep, err := c.CreateDeployment(ctx, &amplify.CreateDeploymentInput{
		AppId:      aws.String(appID),
		BranchName: aws.String(branch),
	})
	require.NoError(t, err)
	putReq, err := http.NewRequestWithContext(ctx, http.MethodPut, aws.ToString(createDep.ZipUploadUrl), bytes.NewReader(zipBytes))
	require.NoError(t, err)
	putReq.Header.Set("Content-Type", "application/zip")
	putResp, err := http.DefaultClient.Do(putReq)
	require.NoError(t, err)
	putResp.Body.Close()
	require.Equal(t, http.StatusOK, putResp.StatusCode)
	jobID := aws.ToString(createDep.JobId)
	_, err = c.StartDeployment(ctx, &amplify.StartDeploymentInput{
		AppId:      aws.String(appID),
		BranchName: aws.String(branch),
		JobId:      aws.String(jobID),
	})
	require.NoError(t, err)
	amplifyWaitJobStatus(t, ctx, c, appID, branch, jobID, amplifytypes.JobStatusSucceed)
	return jobID
}

func TestAmplifyHostingStaticE2E(t *testing.T) {
	t.Parallel()
	c := amplifyClient()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	app, err := c.CreateApp(ctx, &amplify.CreateAppInput{
		Name: aws.String("host-app-" + time.Now().Format("150405.000000")),
		CustomRules: []amplifytypes.CustomRule{
			{Source: aws.String("/old"), Target: aws.String("/about.html"), Status: aws.String("301")},
			{Source: aws.String("/<*>"), Target: aws.String("/index.html"), Status: aws.String("404-200")},
		},
	})
	require.NoError(t, err)
	appID := aws.ToString(app.App.AppId)
	defer func() { _, _ = c.DeleteApp(ctx, &amplify.DeleteAppInput{AppId: aws.String(appID)}) }()
	_, err = c.CreateBranch(ctx, &amplify.CreateBranchInput{
		AppId: aws.String(appID), BranchName: aws.String("main"),
	})
	require.NoError(t, err)
	host := "main." + appID + ".amplifyapp.com"

	// Before any deployment the branch serves Amplify's no-content page.
	resp, _ := amplifyHostGet(t, host, "/", nil)
	require.Equal(t, http.StatusNotFound, resp.StatusCode)

	amplifyDeployZip(t, ctx, c, appID, "main", amplifyZipBytes(t, map[string][]byte{
		"index.html": []byte("<html>hosted home</html>"),
		"about.html": []byte("<html>about page</html>"),
	}))

	resp, body := amplifyHostGet(t, host, "/", nil)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, "<html>hosted home</html>", string(body))
	assert.Contains(t, resp.Header.Get("Content-Type"), "text/html")

	resp, body = amplifyHostGet(t, host, "/about.html", nil)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, "<html>about page</html>", string(body))

	resp, _ = amplifyHostGet(t, host, "/old", nil)
	require.Equal(t, http.StatusMovedPermanently, resp.StatusCode)
	assert.Equal(t, "/about.html", resp.Header.Get("Location"))

	resp, body = amplifyHostGet(t, host, "/client/side/route", nil)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, "<html>hosted home</html>", string(body))

	// Basic auth sends base64 "user:password".
	creds := base64.StdEncoding.EncodeToString([]byte("user:hunter2"))
	_, err = c.UpdateBranch(ctx, &amplify.UpdateBranchInput{
		AppId:                aws.String(appID),
		BranchName:           aws.String("main"),
		EnableBasicAuth:      aws.Bool(true),
		BasicAuthCredentials: aws.String(creds),
	})
	require.NoError(t, err)
	resp, _ = amplifyHostGet(t, host, "/", nil)
	require.Equal(t, http.StatusUnauthorized, resp.StatusCode)
	assert.Contains(t, resp.Header.Get("WWW-Authenticate"), "Basic")
	resp, body = amplifyHostGet(t, host, "/", func(r *http.Request) {
		r.SetBasicAuth("user", "hunter2")
	})
	require.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, "<html>hosted home</html>", string(body))
}

func TestAmplifyHostingSSRComputeE2E(t *testing.T) {
	t.Parallel()
	c := amplifyClient()
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()

	computeRole := serviceRole(t, "amplify.amazonaws.com")
	app, err := c.CreateApp(ctx, &amplify.CreateAppInput{
		Name:           aws.String("ssr-app-" + time.Now().Format("150405.000000")),
		Platform:       amplifytypes.PlatformWebCompute,
		ComputeRoleArn: aws.String(computeRole),
	})
	require.NoError(t, err)
	appID := aws.ToString(app.App.AppId)
	defer func() { _, _ = c.DeleteApp(ctx, &amplify.DeleteAppInput{AppId: aws.String(appID)}) }()
	_, err = c.CreateBranch(ctx, &amplify.CreateBranchInput{
		AppId: aws.String(appID), BranchName: aws.String("main"),
	})
	require.NoError(t, err)

	manifest, err := json.Marshal(map[string]any{
		"version":   1,
		"framework": map[string]string{"name": "custom", "version": "1.0.0"},
		"routes": []map[string]any{
			{"path": "/*.*", "target": map[string]string{"kind": "Static"}},
			{"path": "/*", "target": map[string]string{"kind": "Compute", "src": "default"}},
		},
		"computeResources": []map[string]string{
			{"name": "default", "runtime": "nodejs20.x", "entrypoint": "index.js"},
		},
	})
	require.NoError(t, err)
	// A small Node.js server answering with dynamic content on PORT (3000), where
	// the deployment spec's compute entrypoints listen. /creds reports the
	// container credentials endpoint the SDKs' default chain reads, and the session
	// it serves.
	entrypoint := `const http = require('http');
http.createServer(async (req, res) => {
  if (req.url === '/creds') {
    const uri = process.env.AWS_CONTAINER_CREDENTIALS_FULL_URI || '';
    const session = uri ? await (await fetch(uri)).json() : null;
    res.setHeader('content-type', 'application/json');
    res.end(JSON.stringify({uri, staticKey: 'AWS_ACCESS_KEY_ID' in process.env, session}));
    return;
  }
  res.setHeader('content-type', 'text/plain');
  res.end('ssr-dynamic:' + req.url);
}).listen(process.env.PORT);
`
	amplifyDeployZip(t, ctx, c, appID, "main", amplifyZipBytes(t, map[string][]byte{
		"deploy-manifest.json":     manifest,
		"compute/default/index.js": []byte(entrypoint),
		"static/assets/hello.txt":  []byte("static asset from compute bundle"),
	}))
	host := "main." + appID + ".amplifyapp.com"

	// The first compute request cold-starts the bundle's server.
	resp, body := amplifyHostGet(t, host, "/render/me", nil)
	require.Equal(t, http.StatusOK, resp.StatusCode, "compute proxy: %s", body)
	assert.Equal(t, "ssr-dynamic:/render/me", string(body))

	// The deployment spec puts static assets under static/.
	resp, body = amplifyHostGet(t, host, "/assets/hello.txt", nil)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, "static asset from compute bundle", string(body))

	resp, body = amplifyHostGet(t, host, "/search?q=x", nil)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, "ssr-dynamic:/search?q=x", string(body))

	// The compute holds its role through the container credentials endpoint on
	// its own loopback, which its SDKs refresh from, not static keys in its
	// environment.
	resp, body = amplifyHostGet(t, host, "/creds", nil)
	require.Equal(t, http.StatusOK, resp.StatusCode, "compute credentials: %s", body)
	var creds struct {
		URI       string `json:"uri"`
		StaticKey bool   `json:"staticKey"`
		Session   struct{ AccessKeyId, SecretAccessKey, Token, Expiration string }
	}
	require.NoError(t, json.Unmarshal(body, &creds), string(body))
	assert.True(t, strings.HasPrefix(creds.URI, "http://127.0.0.1:"), "AWS_CONTAINER_CREDENTIALS_FULL_URI %q names the compute's loopback", creds.URI)
	assert.False(t, creds.StaticKey, "the compute gets no static AWS_ACCESS_KEY_ID")
	require.NotEmpty(t, creds.Session.AccessKeyId)
	require.NotEmpty(t, creds.Session.Expiration)
	identity, err := sts.NewFromConfig(sdkConfig(), func(o *sts.Options) {
		o.BaseEndpoint = aws.String(baseURL)
		o.Credentials = credentials.NewStaticCredentialsProvider(creds.Session.AccessKeyId, creds.Session.SecretAccessKey, creds.Session.Token)
	}).GetCallerIdentity(ctx, &sts.GetCallerIdentityInput{})
	require.NoError(t, err)
	roleName := computeRole[strings.LastIndex(computeRole, "/")+1:]
	assert.Equal(t, "arn:aws:sts::"+strings.Split(computeRole, ":")[4]+":assumed-role/"+roleName+"/"+appID, aws.ToString(identity.Arn))
}

func amplifyServeGitRepo(t *testing.T, branch string, files map[string]string) string {
	t.Helper()
	return githttp.Serve(t, branch, files)
}

func amplifyServePrivateGitRepo(t *testing.T, branch string, files map[string]string, token string) string {
	t.Helper()
	return githttp.ServeBasicAuth(t, branch, files, "oauth2", token)
}

func TestAmplifyRealBuildE2E(t *testing.T) {
	t.Parallel()
	c := amplifyClient()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	marker := fmt.Sprintf("build-%d", time.Now().UnixNano())
	repoURL := amplifyServeGitRepo(t, "main", map[string]string{
		"index.html": "<html>built site " + marker + "</html>",
		"data.txt":   "source file",
	})
	buildSpec := `version: 1
frontend:
  phases:
    preBuild:
      commands:
        - echo "prebuild ` + marker + `"
    build:
      commands:
        - mkdir -p dist
        - cp index.html dist/index.html
        - echo "built by $AWS_BRANCH on app $AWS_APP_ID" > dist/build-info.txt
  artifacts:
    baseDirectory: dist
    files:
      - '**/*'
`
	app, err := c.CreateApp(ctx, &amplify.CreateAppInput{
		Name:       aws.String("build-app-" + time.Now().Format("150405.000000")),
		Repository: aws.String(repoURL),
		BuildSpec:  aws.String(buildSpec),
	})
	require.NoError(t, err)
	appID := aws.ToString(app.App.AppId)
	defer func() { _, _ = c.DeleteApp(ctx, &amplify.DeleteAppInput{AppId: aws.String(appID)}) }()
	_, err = c.CreateBranch(ctx, &amplify.CreateBranchInput{
		AppId: aws.String(appID), BranchName: aws.String("main"),
	})
	require.NoError(t, err)

	startOut, err := c.StartJob(ctx, &amplify.StartJobInput{
		AppId:      aws.String(appID),
		BranchName: aws.String("main"),
		JobType:    amplifytypes.JobTypeRelease,
		CommitId:   aws.String("HEAD"),
	})
	require.NoError(t, err)
	jobID := aws.ToString(startOut.JobSummary.JobId)

	// A real build takes time, so the job passes through RUNNING before SUCCEED.
	sawRunning := false
	var job *amplifytypes.Job
	require.Eventually(t, func() bool {
		out, err := c.GetJob(ctx, &amplify.GetJobInput{
			AppId: aws.String(appID), BranchName: aws.String("main"), JobId: aws.String(jobID),
		})
		if err != nil || out.Job.Summary == nil {
			return false
		}
		job = out.Job
		switch out.Job.Summary.Status {
		case amplifytypes.JobStatusRunning:
			sawRunning = true
			return false
		case amplifytypes.JobStatusSucceed:
			return true
		case amplifytypes.JobStatusFailed, amplifytypes.JobStatusCancelled:
			stepDump, _ := json.Marshal(out.Job.Steps)
			t.Fatalf("build job landed %s: %s", out.Job.Summary.Status, stepDump)
		}
		return false
	}, 4*time.Minute, 100*time.Millisecond, "build never succeeded")
	require.True(t, sawRunning, "job must be observable in RUNNING while the build executes")

	require.Len(t, job.Steps, 3)
	stepLogs := map[string]string{}
	for _, step := range job.Steps {
		assert.Equal(t, amplifytypes.JobStatusSucceed, step.Status, "step %s", aws.ToString(step.StepName))
		logURL := aws.ToString(step.LogUrl)
		require.NotEmpty(t, logURL, "step %s must carry a logUrl", aws.ToString(step.StepName))
		logResp, err := http.Get(logURL)
		require.NoError(t, err)
		logBody, err := io.ReadAll(logResp.Body)
		logResp.Body.Close()
		require.NoError(t, err)
		require.Equal(t, http.StatusOK, logResp.StatusCode)
		stepLogs[aws.ToString(step.StepName)] = string(logBody)
	}
	assert.Contains(t, stepLogs["PROVISION"], "Cloning repository")
	assert.Contains(t, stepLogs["BUILD"], "prebuild "+marker, "BUILD log must carry the command output")
	assert.Contains(t, stepLogs["DEPLOY"], "index.html")

	// GetJob's BUILD step returns the hosted build bundle; ListArtifacts holds only
	// end-to-end test outputs.
	buildStep := amplifyRequireJobStep(t, job, "BUILD")
	buildArtifactURL := aws.ToString(buildStep.ArtifactsUrl)
	require.NotEmpty(t, buildArtifactURL)
	buildArtifactResponse, err := http.Get(buildArtifactURL)
	require.NoError(t, err)
	buildArtifactBody, err := io.ReadAll(buildArtifactResponse.Body)
	buildArtifactResponse.Body.Close()
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, buildArtifactResponse.StatusCode)
	require.True(t, bytes.HasPrefix(buildArtifactBody, []byte("PK")))
	artifacts, err := c.ListArtifacts(ctx, &amplify.ListArtifactsInput{
		AppId: aws.String(appID), BranchName: aws.String("main"), JobId: aws.String(jobID),
	})
	require.NoError(t, err)
	require.Empty(t, artifacts.Artifacts)

	host := "main." + appID + ".amplifyapp.com"
	resp, body := amplifyHostGet(t, host, "/", nil)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, "<html>built site "+marker+"</html>", string(body))
	resp, body = amplifyHostGet(t, host, "/build-info.txt", nil)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Contains(t, string(body), "built by main on app "+appID)
	// Files outside the artifact baseDirectory are not deployed.
	resp, _ = amplifyHostGet(t, host, "/data.txt", nil)
	require.Equal(t, http.StatusNotFound, resp.StatusCode)
}

func TestAmplifyPrivateRepositoryPythonBuildE2E(t *testing.T) {
	t.Parallel()
	c := amplifyClient()
	testContext, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	const token = "private-repository-token"
	repoURL := amplifyServePrivateGitRepo(t, "main", map[string]string{
		"apps/site/render.py": `from pathlib import Path
Path("apps/site/dist").mkdir(exist_ok=True)
Path("apps/site/dist/index.html").write_text("<html>private python monorepo build</html>")
`,
	}, token)
	buildSpec := `version: 1
applications:
  - appRoot: apps/site
    env:
      variables:
        FROM_SPEC: build-spec
    backend:
      phases:
        build:
          commands:
            - mkdir -p apps/site/dist
            - echo "$FROM_SPEC/$APP_OVERRIDE" > apps/site/dist/backend.txt
    frontend:
      buildPath: /
      phases:
        preBuild:
          commands:
            - python3 apps/site/render.py
        build:
          commands:
            - if [ -f apps/site/.cache/seen ]; then echo cache-restored > apps/site/dist/cache.txt; else echo first-build > apps/site/dist/cache.txt; fi
            - mkdir -p apps/site/.cache
            - touch apps/site/.cache/seen
        postBuild:
          commands:
            - node --version > apps/site/dist/node-version.txt
      artifacts:
        baseDirectory: apps/site/dist
        files:
          - '**/*'
      cache:
        paths:
          - apps/site/.cache/**/*
    test:
      phases:
        test:
          commands:
            - test -s apps/site/dist/index.html
`
	app, err := c.CreateApp(testContext, &amplify.CreateAppInput{
		Name:        aws.String("private-python-" + time.Now().Format("150405.000000")),
		Repository:  aws.String(repoURL),
		AccessToken: aws.String(token),
		BuildSpec:   aws.String(buildSpec),
		EnvironmentVariables: map[string]string{
			"AMPLIFY_MONOREPO_APP_ROOT": "apps/site",
			"APP_OVERRIDE":              "app-environment",
		},
	})
	require.NoError(t, err)
	appID := aws.ToString(app.App.AppId)
	t.Cleanup(func() {
		_, _ = c.DeleteApp(context.Background(), &amplify.DeleteAppInput{AppId: aws.String(appID)})
	})
	// The credential is write-only: GetApp returns neither the token nor a
	// credential-bearing repository URL.
	readBack, err := c.GetApp(testContext, &amplify.GetAppInput{AppId: aws.String(appID)})
	require.NoError(t, err)
	assert.Equal(t, repoURL, aws.ToString(readBack.App.Repository))
	assert.NotContains(t, aws.ToString(readBack.App.Repository), token)

	_, err = c.CreateBranch(testContext, &amplify.CreateBranchInput{
		AppId: aws.String(appID), BranchName: aws.String("main"),
	})
	require.NoError(t, err)
	started, err := c.StartJob(testContext, &amplify.StartJobInput{
		AppId: aws.String(appID), BranchName: aws.String("main"), JobType: amplifytypes.JobTypeRelease,
	})
	require.NoError(t, err)
	jobID := aws.ToString(started.JobSummary.JobId)
	amplifyWaitJobStatus(t, testContext, c, appID, "main", jobID, amplifytypes.JobStatusSucceed)

	host := "main." + appID + ".amplifyapp.com"
	response, body := amplifyHostGet(t, host, "/", nil)
	require.Equal(t, http.StatusOK, response.StatusCode)
	assert.Equal(t, "<html>private python monorepo build</html>", string(body))
	response, body = amplifyHostGet(t, host, "/node-version.txt", nil)
	require.Equal(t, http.StatusOK, response.StatusCode)
	assert.Regexp(t, `^v22\.`, string(body))
	response, body = amplifyHostGet(t, host, "/backend.txt", nil)
	require.Equal(t, http.StatusOK, response.StatusCode)
	assert.Equal(t, "build-spec/app-environment\n", string(body))
	response, body = amplifyHostGet(t, host, "/cache.txt", nil)
	require.Equal(t, http.StatusOK, response.StatusCode)
	assert.Equal(t, "first-build\n", string(body))

	second, err := c.StartJob(testContext, &amplify.StartJobInput{
		AppId: aws.String(appID), BranchName: aws.String("main"), JobType: amplifytypes.JobTypeRelease,
	})
	require.NoError(t, err)
	amplifyWaitJobStatus(
		t, testContext, c, appID, "main", aws.ToString(second.JobSummary.JobId), amplifytypes.JobStatusSucceed,
	)
	response, body = amplifyHostGet(t, host, "/cache.txt", nil)
	require.Equal(t, http.StatusOK, response.StatusCode)
	assert.Equal(t, "cache-restored\n", string(body))
}

func TestAmplifyDomainVerificationRoute53Flow(t *testing.T) {
	t.Parallel()
	c := amplifyClient()
	r53 := r53Client()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	app, err := c.CreateApp(ctx, &amplify.CreateAppInput{
		Name: aws.String("verify-app-" + time.Now().Format("150405.000000")),
	})
	require.NoError(t, err)
	appID := aws.ToString(app.App.AppId)
	defer func() { _, _ = c.DeleteApp(ctx, &amplify.DeleteAppInput{AppId: aws.String(appID)}) }()
	_, err = c.CreateBranch(ctx, &amplify.CreateBranchInput{
		AppId: aws.String(appID), BranchName: aws.String("main"),
	})
	require.NoError(t, err)

	domain := fmt.Sprintf("verify-%d.example.dev", time.Now().UnixNano())
	dom, err := c.CreateDomainAssociation(ctx, &amplify.CreateDomainAssociationInput{
		AppId:      aws.String(appID),
		DomainName: aws.String(domain),
		SubDomainSettings: []amplifytypes.SubDomainSetting{
			{Prefix: aws.String("www"), BranchName: aws.String("main")},
		},
	})
	require.NoError(t, err)
	// An AMPLIFY_MANAGED certificate starts PENDING_VERIFICATION with the ACM
	// verification CNAME advertised.
	require.Equal(t, amplifytypes.DomainStatusPendingVerification, dom.DomainAssociation.DomainStatus)
	record := aws.ToString(dom.DomainAssociation.CertificateVerificationDNSRecord)
	parts := strings.Fields(record)
	require.Len(t, parts, 3, "verification record %q must be 'name CNAME value'", record)
	require.Equal(t, "CNAME", parts[1])
	require.True(t, strings.HasSuffix(parts[2], ".acm-validations.aws."), "value %q", parts[2])
	require.False(t, aws.ToBool(dom.DomainAssociation.SubDomains[0].Verified))
	assert.Contains(t, aws.ToString(dom.DomainAssociation.SubDomains[0].DnsRecord), ".cloudfront.net")

	getOut, err := c.GetDomainAssociation(ctx, &amplify.GetDomainAssociationInput{
		AppId: aws.String(appID), DomainName: aws.String(domain),
	})
	require.NoError(t, err)
	require.Equal(t, amplifytypes.DomainStatusPendingVerification, getOut.DomainAssociation.DomainStatus)

	// The hosted zone and verification CNAME a terraform configuration would
	// create.
	zoneOut, err := r53.CreateHostedZone(ctx, &route53.CreateHostedZoneInput{
		Name:            aws.String(domain),
		CallerReference: aws.String("amplify-verify-" + time.Now().Format("150405.000000")),
	})
	require.NoError(t, err)
	zoneID := strings.TrimPrefix(aws.ToString(zoneOut.HostedZone.Id), "/hostedzone/")
	_, err = r53.ChangeResourceRecordSets(ctx, &route53.ChangeResourceRecordSetsInput{
		HostedZoneId: aws.String(zoneID),
		ChangeBatch: &r53types.ChangeBatch{
			Changes: []r53types.Change{{
				Action: r53types.ChangeActionCreate,
				ResourceRecordSet: &r53types.ResourceRecordSet{
					Name: aws.String(parts[0]),
					Type: r53types.RRTypeCname,
					TTL:  aws.Int64(300),
					ResourceRecords: []r53types.ResourceRecord{
						{Value: aws.String(parts[2])},
					},
				},
			}},
		},
	})
	require.NoError(t, err)

	// Reading the association, as terraform's wait_for_verification polls it,
	// evaluates the record and reports AVAILABLE.
	getOut, err = c.GetDomainAssociation(ctx, &amplify.GetDomainAssociationInput{
		AppId: aws.String(appID), DomainName: aws.String(domain),
	})
	require.NoError(t, err)
	require.Equal(t, amplifytypes.DomainStatusAvailable, getOut.DomainAssociation.DomainStatus)
	require.True(t, aws.ToBool(getOut.DomainAssociation.SubDomains[0].Verified))

	amplifyDeployZip(t, ctx, c, appID, "main", amplifyZipBytes(t, map[string][]byte{
		"index.html": []byte("<html>custom domain content</html>"),
	}))
	resp, body := amplifyHostGet(t, "www."+domain, "/", nil)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, "<html>custom domain content</html>", string(body))

	_, err = c.DeleteDomainAssociation(ctx, &amplify.DeleteDomainAssociationInput{
		AppId: aws.String(appID), DomainName: aws.String(domain),
	})
	require.NoError(t, err)
	// Route 53 deletes only a zone emptied of user records.
	_, err = r53.ChangeResourceRecordSets(ctx, &route53.ChangeResourceRecordSetsInput{
		HostedZoneId: aws.String(zoneID),
		ChangeBatch: &r53types.ChangeBatch{
			Changes: []r53types.Change{{
				Action: r53types.ChangeActionDelete,
				ResourceRecordSet: &r53types.ResourceRecordSet{
					Name: aws.String(parts[0]),
					Type: r53types.RRTypeCname,
					TTL:  aws.Int64(300),
					ResourceRecords: []r53types.ResourceRecord{
						{Value: aws.String(parts[2])},
					},
				},
			}},
		},
	})
	require.NoError(t, err)
	_, err = r53.DeleteHostedZone(ctx, &route53.DeleteHostedZoneInput{Id: aws.String(zoneID)})
	require.NoError(t, err)
}

// TestAmplifyDeploymentInvalidManifestFailsE2E expects an invalid
// deploy-manifest.json to fail the job, with the validation error in the
// DEPLOY step's log, and never serve the bundle.
func TestAmplifyDeploymentInvalidManifestFailsE2E(t *testing.T) {
	t.Parallel()
	c := amplifyClient()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	app, err := c.CreateApp(ctx, &amplify.CreateAppInput{
		Name:     aws.String("badmanifest-app-" + time.Now().Format("150405.000000")),
		Platform: amplifytypes.PlatformWebCompute,
	})
	require.NoError(t, err)
	appID := aws.ToString(app.App.AppId)
	defer func() { _, _ = c.DeleteApp(ctx, &amplify.DeleteAppInput{AppId: aws.String(appID)}) }()
	_, err = c.CreateBranch(ctx, &amplify.CreateBranchInput{
		AppId: aws.String(appID), BranchName: aws.String("main"),
	})
	require.NoError(t, err)

	zipBytes := amplifyZipBytes(t, map[string][]byte{
		"deploy-manifest.json": []byte(`{"version": 2, "routes": []}`),
		"static/index.html":    []byte("<html>ssr bundle</html>"),
	})
	createDep, err := c.CreateDeployment(ctx, &amplify.CreateDeploymentInput{
		AppId: aws.String(appID), BranchName: aws.String("main"),
	})
	require.NoError(t, err)
	putReq, err := http.NewRequestWithContext(ctx, http.MethodPut, aws.ToString(createDep.ZipUploadUrl), bytes.NewReader(zipBytes))
	require.NoError(t, err)
	putReq.Header.Set("Content-Type", "application/zip")
	putResp, err := http.DefaultClient.Do(putReq)
	require.NoError(t, err)
	putResp.Body.Close()
	require.Equal(t, http.StatusOK, putResp.StatusCode)
	jobID := aws.ToString(createDep.JobId)
	_, err = c.StartDeployment(ctx, &amplify.StartDeploymentInput{
		AppId: aws.String(appID), BranchName: aws.String("main"), JobId: aws.String(jobID),
	})
	require.NoError(t, err)

	job := amplifyWaitJobStatus(t, ctx, c, appID, "main", jobID, amplifytypes.JobStatusFailed)
	deployStep := amplifyRequireJobStep(t, job, "DEPLOY")
	require.Equal(t, amplifytypes.JobStatusFailed, deployStep.Status)
	logURL := aws.ToString(deployStep.LogUrl)
	require.NotEmpty(t, logURL, "failed DEPLOY step must carry a logUrl")
	logResp, err := http.Get(logURL)
	require.NoError(t, err)
	logBody, err := io.ReadAll(logResp.Body)
	logResp.Body.Close()
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, logResp.StatusCode)
	assert.Contains(t, string(logBody), "!!! CustomerError: We failed to validate the deploy-manifest.json file found in your build output directory")
	assert.Contains(t, string(logBody), "version 2 not supported")

	resp, _ := amplifyHostGet(t, "main."+appID+".amplifyapp.com", "/", nil)
	require.Equal(t, http.StatusNotFound, resp.StatusCode)
}

// TestAmplifyHostingRouteFallbacksE2E covers the deployment specification's
// route fallbacks: a target answering 404 to a GET without a body falls back
// to the route's declared fallback target.
func TestAmplifyHostingRouteFallbacksE2E(t *testing.T) {
	t.Parallel()
	c := amplifyClient()
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()

	app, err := c.CreateApp(ctx, &amplify.CreateAppInput{
		Name:     aws.String("fallback-app-" + time.Now().Format("150405.000000")),
		Platform: amplifytypes.PlatformWebCompute,
	})
	require.NoError(t, err)
	appID := aws.ToString(app.App.AppId)
	defer func() { _, _ = c.DeleteApp(ctx, &amplify.DeleteAppInput{AppId: aws.String(appID)}) }()
	_, err = c.CreateBranch(ctx, &amplify.CreateBranchInput{
		AppId: aws.String(appID), BranchName: aws.String("main"),
	})
	require.NoError(t, err)

	manifest, err := json.Marshal(map[string]any{
		"version":   1,
		"framework": map[string]string{"name": "custom", "version": "1.0.0"},
		"routes": []map[string]any{
			{
				"path":     "/_next/image",
				"target":   map[string]string{"kind": "ImageOptimization"},
				"fallback": map[string]string{"kind": "Compute", "src": "default"},
			},
			{
				"path":     "/app/*",
				"target":   map[string]string{"kind": "Compute", "src": "default"},
				"fallback": map[string]string{"kind": "Static"},
			},
			{"path": "/*", "target": map[string]string{"kind": "Compute", "src": "default"}},
		},
		"computeResources": []map[string]string{
			{"name": "default", "runtime": "nodejs20.x", "entrypoint": "index.js"},
		},
		"imageSettings": map[string]any{
			"sizes":               []int{64},
			"domains":             []string{},
			"remotePatterns":      []any{},
			"formats":             []string{"image/webp"},
			"minimumCacheTTL":     60,
			"dangerouslyAllowSVG": false,
		},
	})
	require.NoError(t, err)
	entrypoint := `const http = require('http');
http.createServer((req, res) => {
  if (req.url.startsWith('/app/missing')) { res.statusCode = 404; res.end('compute-404'); return; }
  if (req.url === '/app/cut') { res.writeHead(200, {'content-type': 'text/plain'}); res.write('partial', () => res.socket.destroy()); return; }
  res.setHeader('content-type', 'text/plain');
  res.end('compute:' + req.url);
}).listen(process.env.PORT);
`
	amplifyDeployZip(t, ctx, c, appID, "main", amplifyZipBytes(t, map[string][]byte{
		"deploy-manifest.json":     manifest,
		"compute/default/index.js": []byte(entrypoint),
		"static/app/missing-page":  []byte("static fallback for missing page"),
	}))
	host := "main." + appID + ".amplifyapp.com"

	resp, body := amplifyHostGet(t, host, "/app/hello", nil)
	require.Equal(t, http.StatusOK, resp.StatusCode, "compute proxy: %s", body)
	assert.Equal(t, "compute:/app/hello", string(body))

	// Compute that closes its connection mid-body has had its status and headers
	// relayed, so the response ends where compute stopped rather than with an
	// error appended.
	cutReq, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/app/cut", nil)
	require.NoError(t, err)
	cutReq.Host = host
	cutResp, err := http.DefaultClient.Do(cutReq)
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, cutResp.StatusCode, "compute's status reached the client")
	body, err = io.ReadAll(cutResp.Body)
	_ = cutResp.Body.Close()
	assert.ErrorIs(t, err, io.ErrUnexpectedEOF, "the response ends where compute stopped")
	assert.Equal(t, "partial", string(body), "nothing follows what compute sent")

	resp, body = amplifyHostGet(t, host, "/app/missing-page", nil)
	require.Equal(t, http.StatusOK, resp.StatusCode, "compute->static fallback: %s", body)
	assert.Equal(t, "static fallback for missing page", string(body))

	resp, _ = amplifyHostGet(t, host, "/app/missing-other", nil)
	require.Equal(t, http.StatusNotFound, resp.StatusCode)

	// An ImageOptimization 404 (a missing source artifact) falls back to the
	// route's Compute fallback.
	resp, body = amplifyHostGet(t, host, "/_next/image?url=%2Fnope.png&w=64&q=75", nil)
	require.Equal(t, http.StatusOK, resp.StatusCode, "imageopt->compute fallback: %s", body)
	assert.Equal(t, "compute:/_next/image?url=%2Fnope.png&w=64&q=75", string(body))

	// Fallbacks apply to GET only.
	postReq := func(r *http.Request) { r.Method = http.MethodPost }
	resp, body = amplifyHostGet(t, host, "/app/missing-page", postReq)
	require.Equal(t, http.StatusNotFound, resp.StatusCode)
	assert.Equal(t, "compute-404", string(body))
}
