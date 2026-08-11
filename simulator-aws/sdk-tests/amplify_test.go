package aws_sdk_test

import (
	"bytes"
	"context"
	"crypto/md5"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/amplify"
	amplifytypes "github.com/aws/aws-sdk-go-v2/service/amplify/types"
	"github.com/aws/aws-sdk-go-v2/service/wafv2"
	wafv2types "github.com/aws/aws-sdk-go-v2/service/wafv2/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func amplifyClient() *amplify.Client {
	cfg := sdkConfig()
	return amplify.NewFromConfig(cfg, func(o *amplify.Options) {
		o.BaseEndpoint = aws.String(baseURL)
	})
}

// amplifyWaitJobStatus polls GetJob until the job reaches the wanted status
// while real builds and direct deployments drive PENDING → RUNNING → terminal.
func amplifyWaitJobStatus(t *testing.T, ctx context.Context, c *amplify.Client, appID, branch, jobID string, want amplifytypes.JobStatus) *amplifytypes.Job {
	t.Helper()
	var job *amplifytypes.Job
	require.Eventually(t, func() bool {
		out, err := c.GetJob(ctx, &amplify.GetJobInput{
			AppId: aws.String(appID), BranchName: aws.String(branch), JobId: aws.String(jobID),
		})
		if err != nil {
			return false
		}
		job = out.Job
		return job.Summary != nil && job.Summary.Status == want
	}, 15*time.Second, 100*time.Millisecond, "job %s never reached %s", jobID, want)
	return job
}

func TestAmplifyWAFv2Association(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	amplifyAPI := amplifyClient()
	wafAPI := wafClient()

	app, err := amplifyAPI.CreateApp(ctx, &amplify.CreateAppInput{
		Name: aws.String("waf-app-" + time.Now().Format("150405.000000")),
	})
	require.NoError(t, err)
	appID := aws.ToString(app.App.AppId)
	appARN := aws.ToString(app.App.AppArn)
	t.Cleanup(func() {
		_, _ = amplifyAPI.DeleteApp(context.Background(), &amplify.DeleteAppInput{AppId: aws.String(appID)})
	})

	aclName := "amplify-waf-" + wafStamp()
	acl, err := wafAPI.CreateWebACL(ctx, &wafv2.CreateWebACLInput{
		Name:  aws.String(aclName),
		Scope: wafv2types.ScopeCloudfront,
		DefaultAction: &wafv2types.DefaultAction{
			Allow: &wafv2types.AllowAction{},
		},
		VisibilityConfig: &wafv2types.VisibilityConfig{
			SampledRequestsEnabled:   true,
			CloudWatchMetricsEnabled: true,
			MetricName:               aws.String(aclName),
		},
	})
	require.NoError(t, err)
	aclARN := aws.ToString(acl.Summary.ARN)
	lockToken := aws.ToString(acl.Summary.LockToken)
	t.Cleanup(func() {
		_, _ = wafAPI.DisassociateWebACL(context.Background(), &wafv2.DisassociateWebACLInput{
			ResourceArn: aws.String(appARN),
		})
		_, _ = wafAPI.DeleteWebACL(context.Background(), &wafv2.DeleteWebACLInput{
			Name: acl.Summary.Name, Scope: wafv2types.ScopeCloudfront,
			Id: acl.Summary.Id, LockToken: aws.String(lockToken),
		})
	})

	_, err = wafAPI.AssociateWebACL(ctx, &wafv2.AssociateWebACLInput{
		WebACLArn: acl.Summary.ARN, ResourceArn: aws.String(appARN),
	})
	require.NoError(t, err)

	gotApp, err := amplifyAPI.GetApp(ctx, &amplify.GetAppInput{AppId: aws.String(appID)})
	require.NoError(t, err)
	require.NotNil(t, gotApp.App.WafConfiguration)
	assert.Equal(t, aclARN, aws.ToString(gotApp.App.WafConfiguration.WebAclArn))
	assert.Equal(t, amplifytypes.WafStatusAssociationSuccess, gotApp.App.WafConfiguration.WafStatus)
	assert.Empty(t, aws.ToString(gotApp.App.WafConfiguration.StatusReason))

	gotACL, err := wafAPI.GetWebACLForResource(ctx, &wafv2.GetWebACLForResourceInput{
		ResourceArn: aws.String(appARN),
	})
	require.NoError(t, err)
	require.NotNil(t, gotACL.WebACL)
	assert.Equal(t, aclARN, aws.ToString(gotACL.WebACL.ARN))
	resources, err := wafAPI.ListResourcesForWebACL(ctx, &wafv2.ListResourcesForWebACLInput{
		WebACLArn: aws.String(aclARN),
	})
	require.NoError(t, err)
	assert.Contains(t, resources.ResourceArns, appARN)

	_, err = wafAPI.DeleteWebACL(ctx, &wafv2.DeleteWebACLInput{
		Name: acl.Summary.Name, Scope: wafv2types.ScopeCloudfront,
		Id: acl.Summary.Id, LockToken: aws.String(lockToken),
	})
	require.Error(t, err, "an Amplify-associated WebACL cannot be deleted")

	_, err = wafAPI.AssociateWebACL(ctx, &wafv2.AssociateWebACLInput{
		WebACLArn: acl.Summary.ARN,
		ResourceArn: aws.String(
			"arn:aws:amplify:us-east-1:123456789012:apps/dmissing",
		),
	})
	require.Error(t, err, "an association cannot target a nonexistent Amplify app")

	_, err = wafAPI.DisassociateWebACL(ctx, &wafv2.DisassociateWebACLInput{
		ResourceArn: aws.String(appARN),
	})
	require.NoError(t, err)
	gotApp, err = amplifyAPI.GetApp(ctx, &amplify.GetAppInput{AppId: aws.String(appID)})
	require.NoError(t, err)
	assert.Nil(t, gotApp.App.WafConfiguration)
}

func TestAmplifyWAFv2RequestEvaluation(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	amplifyAPI := amplifyClient()
	wafAPI := wafClient()

	app, err := amplifyAPI.CreateApp(ctx, &amplify.CreateAppInput{
		Name: aws.String("waf-runtime-" + time.Now().Format("150405.000000")),
	})
	require.NoError(t, err)
	appID := aws.ToString(app.App.AppId)
	appARN := aws.ToString(app.App.AppArn)
	_, err = amplifyAPI.CreateBranch(ctx, &amplify.CreateBranchInput{
		AppId: aws.String(appID), BranchName: aws.String("main"),
	})
	require.NoError(t, err)
	amplifyDeployZip(t, ctx, amplifyAPI, appID, "main", amplifyZipBytes(t, map[string][]byte{
		"index.html": []byte("allowed"),
	}))
	t.Cleanup(func() {
		_, _ = amplifyAPI.DeleteApp(context.Background(), &amplify.DeleteAppInput{AppId: aws.String(appID)})
	})

	visibility := func(metric string) *wafv2types.VisibilityConfig {
		return &wafv2types.VisibilityConfig{
			SampledRequestsEnabled: true, CloudWatchMetricsEnabled: true, MetricName: aws.String(metric),
		}
	}
	aclName := "waf-runtime-" + wafStamp()
	acl, err := wafAPI.CreateWebACL(ctx, &wafv2.CreateWebACLInput{
		Name:  aws.String(aclName),
		Scope: wafv2types.ScopeCloudfront,
		DefaultAction: &wafv2types.DefaultAction{
			Allow: &wafv2types.AllowAction{},
		},
		Rules: []wafv2types.Rule{
			{
				Name: aws.String("blocked-path"), Priority: 1,
				Action: &wafv2types.RuleAction{Block: &wafv2types.BlockAction{}},
				Statement: &wafv2types.Statement{ByteMatchStatement: &wafv2types.ByteMatchStatement{
					FieldToMatch:         &wafv2types.FieldToMatch{UriPath: &wafv2types.UriPath{}},
					PositionalConstraint: wafv2types.PositionalConstraintStartsWith,
					SearchString:         []byte("/private"),
					TextTransformations: []wafv2types.TextTransformation{
						{Priority: 0, Type: wafv2types.TextTransformationTypeLowercase},
					},
				}},
				VisibilityConfig: visibility("blocked-path"),
			},
			{
				Name: aws.String("request-rate"), Priority: 2,
				Action: &wafv2types.RuleAction{Block: &wafv2types.BlockAction{}},
				Statement: &wafv2types.Statement{RateBasedStatement: &wafv2types.RateBasedStatement{
					AggregateKeyType:    wafv2types.RateBasedStatementAggregateKeyTypeIp,
					Limit:               aws.Int64(2),
					EvaluationWindowSec: 60,
				}},
				VisibilityConfig: visibility("request-rate"),
			},
		},
		VisibilityConfig: visibility(aclName),
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = wafAPI.DisassociateWebACL(context.Background(), &wafv2.DisassociateWebACLInput{
			ResourceArn: aws.String(appARN),
		})
		_, _ = wafAPI.DeleteWebACL(context.Background(), &wafv2.DeleteWebACLInput{
			Name: acl.Summary.Name, Scope: wafv2types.ScopeCloudfront,
			Id: acl.Summary.Id, LockToken: acl.Summary.LockToken,
		})
	})
	_, err = wafAPI.AssociateWebACL(ctx, &wafv2.AssociateWebACLInput{
		WebACLArn: acl.Summary.ARN, ResourceArn: aws.String(appARN),
	})
	require.NoError(t, err)

	host := "main." + appID + ".amplifyapp.com"
	response, body := amplifyHostGet(t, host, "/private/data", nil)
	require.Equal(t, http.StatusForbidden, response.StatusCode)
	require.Equal(t, []byte("Forbidden\n"), body)

	for request := 1; request <= 3; request++ {
		response, body = amplifyHostGet(t, host, "/index.html", nil)
		if request <= 2 {
			require.Equal(t, http.StatusOK, response.StatusCode)
			require.Equal(t, []byte("allowed"), body)
		} else {
			require.Equal(t, http.StatusForbidden, response.StatusCode)
		}
	}

	keys, err := wafAPI.GetRateBasedStatementManagedKeys(ctx, &wafv2.GetRateBasedStatementManagedKeysInput{
		Scope: wafv2types.ScopeCloudfront, WebACLName: aws.String(aclName),
		WebACLId: acl.Summary.Id, RuleName: aws.String("request-rate"),
	})
	require.NoError(t, err)
	require.Contains(t, keys.ManagedKeysIPV4.Addresses, "127.0.0.1")

	samples, err := wafAPI.GetSampledRequests(ctx, &wafv2.GetSampledRequestsInput{
		WebAclArn: acl.Summary.ARN, RuleMetricName: aws.String("request-rate"),
		Scope: wafv2types.ScopeCloudfront,
		TimeWindow: &wafv2types.TimeWindow{
			StartTime: aws.Time(time.Now().Add(-time.Minute)),
			EndTime:   aws.Time(time.Now().Add(time.Minute)),
		},
		MaxItems: aws.Int64(10),
	})
	require.NoError(t, err)
	require.Len(t, samples.SampledRequests, 1)
	require.Equal(t, "BLOCK", aws.ToString(samples.SampledRequests[0].Action))
}

func TestAmplifyAppLifecycle(t *testing.T) {
	c := amplifyClient()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	name := "sdk-app-" + time.Now().Format("150405.000000")
	createOut, err := c.CreateApp(ctx, &amplify.CreateAppInput{
		Name:                  aws.String(name),
		Description:           aws.String("sdk test"),
		Platform:              amplifytypes.PlatformWeb,
		Repository:            aws.String("https://github.com/acme/site"),
		EnableBranchAutoBuild: aws.Bool(true),
		Tags:                  map[string]string{"env": "test"},
	})
	require.NoError(t, err)
	require.NotNil(t, createOut.App)
	appID := aws.ToString(createOut.App.AppId)
	require.NotEmpty(t, appID)
	assert.Equal(t, name, aws.ToString(createOut.App.Name))
	assert.Equal(t, appID+".amplifyapp.com", aws.ToString(createOut.App.DefaultDomain))
	// Documented defaults applied at create.
	require.NotNil(t, createOut.App.CacheConfig)
	assert.Equal(t, amplifytypes.CacheConfigTypeAmplifyManaged, createOut.App.CacheConfig.Type)
	require.NotNil(t, createOut.App.JobConfig)
	assert.Equal(t, amplifytypes.BuildComputeTypeStandard8gb, createOut.App.JobConfig.BuildComputeType)
	// Clone method derived from the repository host.
	assert.Equal(t, amplifytypes.RepositoryCloneMethodToken, createOut.App.RepositoryCloneMethod)

	getOut, err := c.GetApp(ctx, &amplify.GetAppInput{AppId: aws.String(appID)})
	require.NoError(t, err)
	assert.Equal(t, name, aws.ToString(getOut.App.Name))

	updOut, err := c.UpdateApp(ctx, &amplify.UpdateAppInput{
		AppId:       aws.String(appID),
		Description: aws.String("updated"),
	})
	require.NoError(t, err)
	assert.Equal(t, "updated", aws.ToString(updOut.App.Description))
	// Members absent from the update request stay untouched.
	assert.Equal(t, name, aws.ToString(updOut.App.Name))
	assert.Equal(t, "https://github.com/acme/site", aws.ToString(updOut.App.Repository))

	// An explicit empty value clears the member (presence-based updates).
	updOut, err = c.UpdateApp(ctx, &amplify.UpdateAppInput{
		AppId:       aws.String(appID),
		Description: aws.String(""),
	})
	require.NoError(t, err)
	assert.Empty(t, aws.ToString(updOut.App.Description))

	// enableAutoBranchCreation + config are accepted by UpdateApp.
	updOut, err = c.UpdateApp(ctx, &amplify.UpdateAppInput{
		AppId:                      aws.String(appID),
		EnableAutoBranchCreation:   aws.Bool(true),
		AutoBranchCreationPatterns: []string{"feature/*"},
		AutoBranchCreationConfig: &amplifytypes.AutoBranchCreationConfig{
			Stage:           amplifytypes.StageBeta,
			EnableAutoBuild: aws.Bool(true),
		},
	})
	require.NoError(t, err)
	assert.True(t, aws.ToBool(updOut.App.EnableAutoBranchCreation))
	assert.Equal(t, []string{"feature/*"}, updOut.App.AutoBranchCreationPatterns)
	require.NotNil(t, updOut.App.AutoBranchCreationConfig)
	assert.Equal(t, amplifytypes.StageBeta, updOut.App.AutoBranchCreationConfig.Stage)

	updOut, err = c.UpdateApp(ctx, &amplify.UpdateAppInput{
		AppId:                      aws.String(appID),
		EnableAutoBranchCreation:   aws.Bool(false),
		AutoBranchCreationPatterns: []string{},
	})
	require.NoError(t, err)
	assert.False(t, aws.ToBool(updOut.App.EnableAutoBranchCreation))
	assert.Empty(t, updOut.App.AutoBranchCreationPatterns)

	listOut, err := c.ListApps(ctx, &amplify.ListAppsInput{})
	require.NoError(t, err)
	found := false
	for _, a := range listOut.Apps {
		if aws.ToString(a.AppId) == appID {
			found = true
		}
	}
	assert.True(t, found)

	_, err = c.DeleteApp(ctx, &amplify.DeleteAppInput{AppId: aws.String(appID)})
	require.NoError(t, err)

	_, err = c.GetApp(ctx, &amplify.GetAppInput{AppId: aws.String(appID)})
	require.Error(t, err)
}

func TestAmplifyBranchLifecycle(t *testing.T) {
	c := amplifyClient()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	app, err := c.CreateApp(ctx, &amplify.CreateAppInput{
		Name:     aws.String("br-app-" + time.Now().Format("150405.000000")),
		Platform: amplifytypes.PlatformWeb,
	})
	require.NoError(t, err)
	appID := aws.ToString(app.App.AppId)
	defer func() {
		_, _ = c.DeleteApp(ctx, &amplify.DeleteAppInput{AppId: aws.String(appID)})
	}()

	br, err := c.CreateBranch(ctx, &amplify.CreateBranchInput{
		AppId:                aws.String(appID),
		BranchName:           aws.String("main"),
		Stage:                amplifytypes.StagePullRequest, // any valid enum
		EnableAutoBuild:      aws.Bool(true),
		EnableSkewProtection: aws.Bool(true),
		Backend: &amplifytypes.Backend{
			StackArn: aws.String("arn:aws:cloudformation:us-east-1:123456789012:stack/amplify-be/x"),
		},
	})
	require.NoError(t, err)
	assert.Equal(t, "main", aws.ToString(br.Branch.BranchName))
	assert.True(t, aws.ToBool(br.Branch.EnableSkewProtection))
	require.NotNil(t, br.Branch.Backend)
	assert.Contains(t, aws.ToString(br.Branch.Backend.StackArn), "amplify-be")

	// Duplicate create
	_, err = c.CreateBranch(ctx, &amplify.CreateBranchInput{
		AppId: aws.String(appID), BranchName: aws.String("main"),
	})
	require.Error(t, err)

	getBr, err := c.GetBranch(ctx, &amplify.GetBranchInput{
		AppId: aws.String(appID), BranchName: aws.String("main"),
	})
	require.NoError(t, err)
	assert.Equal(t, "main", aws.ToString(getBr.Branch.BranchName))

	updBr, err := c.UpdateBranch(ctx, &amplify.UpdateBranchInput{
		AppId: aws.String(appID), BranchName: aws.String("main"),
		Description: aws.String("updated branch"),
	})
	require.NoError(t, err)
	assert.Equal(t, "updated branch", aws.ToString(updBr.Branch.Description))
	// Members absent from the update stay untouched.
	assert.True(t, aws.ToBool(updBr.Branch.EnableSkewProtection))
	require.NotNil(t, updBr.Branch.Backend)

	// Explicit empty values clear.
	updBr, err = c.UpdateBranch(ctx, &amplify.UpdateBranchInput{
		AppId: aws.String(appID), BranchName: aws.String("main"),
		Description:          aws.String(""),
		EnableSkewProtection: aws.Bool(false),
	})
	require.NoError(t, err)
	assert.Empty(t, aws.ToString(updBr.Branch.Description))
	assert.False(t, aws.ToBool(updBr.Branch.EnableSkewProtection))

	listBr, err := c.ListBranches(ctx, &amplify.ListBranchesInput{AppId: aws.String(appID)})
	require.NoError(t, err)
	require.Len(t, listBr.Branches, 1)

	_, err = c.DeleteBranch(ctx, &amplify.DeleteBranchInput{
		AppId: aws.String(appID), BranchName: aws.String("main"),
	})
	require.NoError(t, err)
}

func TestAmplifyWebhookLifecycle(t *testing.T) {
	c := amplifyClient()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	app, err := c.CreateApp(ctx, &amplify.CreateAppInput{
		Name: aws.String("wh-app-" + time.Now().Format("150405.000000")),
	})
	require.NoError(t, err)
	appID := aws.ToString(app.App.AppId)
	defer func() {
		_, _ = c.DeleteApp(ctx, &amplify.DeleteAppInput{AppId: aws.String(appID)})
	}()
	_, _ = c.CreateBranch(ctx, &amplify.CreateBranchInput{
		AppId: aws.String(appID), BranchName: aws.String("main"),
	})

	wh, err := c.CreateWebhook(ctx, &amplify.CreateWebhookInput{
		AppId:       aws.String(appID),
		BranchName:  aws.String("main"),
		Description: aws.String("sdk webhook"),
	})
	require.NoError(t, err)
	whID := aws.ToString(wh.Webhook.WebhookId)
	require.NotEmpty(t, whID)
	require.NotEmpty(t, aws.ToString(wh.Webhook.WebhookUrl))
	assert.Equal(t, appID, aws.ToString(wh.Webhook.AppId))

	// The app reflects that a webhook exists.
	appOut, err := c.GetApp(ctx, &amplify.GetAppInput{AppId: aws.String(appID)})
	require.NoError(t, err)
	require.NotNil(t, appOut.App.WebhookCreateTime)

	getWh, err := c.GetWebhook(ctx, &amplify.GetWebhookInput{WebhookId: aws.String(whID)})
	require.NoError(t, err)
	assert.Equal(t, "main", aws.ToString(getWh.Webhook.BranchName))
	assert.Equal(t, appID, aws.ToString(getWh.Webhook.AppId))

	updWh, err := c.UpdateWebhook(ctx, &amplify.UpdateWebhookInput{
		WebhookId:   aws.String(whID),
		Description: aws.String("updated"),
	})
	require.NoError(t, err)
	assert.Equal(t, "updated", aws.ToString(updWh.Webhook.Description))

	listWh, err := c.ListWebhooks(ctx, &amplify.ListWebhooksInput{AppId: aws.String(appID)})
	require.NoError(t, err)
	require.Len(t, listWh.Webhooks, 1)
	assert.Equal(t, appID, aws.ToString(listWh.Webhooks[0].AppId))

	_, err = c.DeleteWebhook(ctx, &amplify.DeleteWebhookInput{WebhookId: aws.String(whID)})
	require.NoError(t, err)
}

func TestAmplifyJobLifecycle(t *testing.T) {
	c := amplifyClient()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	repository := amplifyServeGitRepo(t, "main", map[string]string{
		"index.html": "<html>job lifecycle</html>",
	})
	buildSpec := `version: 1
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
        - printf screenshot-bytes > cypress/screenshots/home.png
        - printf '{"stats":{"tests":1,"passes":1}}' > cypress/report/mochawesome.json
  artifacts:
    baseDirectory: cypress
    configFilePath: '**/mochawesome.json'
    files:
      - '**/*.png'
`
	app, err := c.CreateApp(ctx, &amplify.CreateAppInput{
		Name:       aws.String("job-app-" + time.Now().Format("150405.000000")),
		Repository: aws.String(repository),
		BuildSpec:  aws.String(buildSpec),
	})
	require.NoError(t, err)
	appID := aws.ToString(app.App.AppId)
	defer func() {
		_, _ = c.DeleteApp(ctx, &amplify.DeleteAppInput{AppId: aws.String(appID)})
	}()
	_, _ = c.CreateBranch(ctx, &amplify.CreateBranchInput{
		AppId: aws.String(appID), BranchName: aws.String("main"),
	})

	startOut, err := c.StartJob(ctx, &amplify.StartJobInput{
		AppId:         aws.String(appID),
		BranchName:    aws.String("main"),
		JobType:       amplifytypes.JobTypeRelease,
		CommitId:      aws.String("HEAD"),
		CommitMessage: aws.String("sdk test commit"),
		CommitTime:    aws.Time(time.Now()),
	})
	require.NoError(t, err)
	jobID := aws.ToString(startOut.JobSummary.JobId)
	require.NotEmpty(t, jobID)
	_, err = strconv.ParseUint(jobID, 10, 64)
	require.NoError(t, err, "Amplify job IDs must be numeric")
	// The job starts PENDING and settles only after the real repository build.
	assert.Contains(t, []amplifytypes.JobStatus{
		amplifytypes.JobStatusPending, amplifytypes.JobStatusRunning, amplifytypes.JobStatusSucceed,
	}, startOut.JobSummary.Status)

	job := amplifyWaitJobStatus(t, ctx, c, appID, "main", jobID, amplifytypes.JobStatusSucceed)
	require.NotEmpty(t, job.Steps)
	for _, step := range job.Steps {
		assert.Equal(t, amplifytypes.JobStatusSucceed, step.Status)
	}
	buildStep := amplifyRequireJobStep(t, job, "BUILD")
	buildArtifactURL := aws.ToString(buildStep.ArtifactsUrl)
	testArtifactsURL := aws.ToString(buildStep.TestArtifactsUrl)
	testConfigURL := aws.ToString(buildStep.TestConfigUrl)
	require.NotEmpty(t, buildArtifactURL)
	require.NotEmpty(t, testArtifactsURL)
	require.NotEmpty(t, testConfigURL)

	listJobs, err := c.ListJobs(ctx, &amplify.ListJobsInput{
		AppId: aws.String(appID), BranchName: aws.String("main"),
	})
	require.NoError(t, err)
	require.Len(t, listJobs.JobSummaries, 1)

	// ListArtifacts is specifically the end-to-end test artifact surface.
	// The hosted build bundle is exposed separately on GetJob's BUILD step.
	var artifactID string
	require.Eventually(t, func() bool {
		artifacts, err := c.ListArtifacts(ctx, &amplify.ListArtifactsInput{
			AppId:      aws.String(appID),
			BranchName: aws.String("main"),
			JobId:      aws.String(jobID),
			MaxResults: 1,
		})
		if err != nil || len(artifacts.Artifacts) != 1 {
			return false
		}
		artifactID = aws.ToString(artifacts.Artifacts[0].ArtifactId)
		return artifactID != "" && aws.ToString(artifacts.Artifacts[0].ArtifactFileName) != ""
	}, 15*time.Second, 100*time.Millisecond)

	artifactURL, err := c.GetArtifactUrl(ctx, &amplify.GetArtifactUrlInput{
		ArtifactId: aws.String(artifactID),
	})
	require.NoError(t, err)
	assert.Equal(t, artifactID, aws.ToString(artifactURL.ArtifactId))
	testArtifactURL := aws.ToString(artifactURL.ArtifactUrl)
	require.NotEmpty(t, testArtifactURL)
	artifactResp, err := http.Get(testArtifactURL)
	require.NoError(t, err)
	defer artifactResp.Body.Close()
	assert.Equal(t, http.StatusOK, artifactResp.StatusCode)
	artifactBody, err := io.ReadAll(artifactResp.Body)
	require.NoError(t, err)
	assert.Equal(t, "screenshot-bytes", string(artifactBody))

	buildArtifactResp, err := http.Get(buildArtifactURL)
	require.NoError(t, err)
	defer buildArtifactResp.Body.Close()
	assert.Equal(t, http.StatusOK, buildArtifactResp.StatusCode)
	buildArtifactBody, err := io.ReadAll(buildArtifactResp.Body)
	require.NoError(t, err)
	assert.True(t, bytes.HasPrefix(buildArtifactBody, []byte("PK")), "build artifact must be a real zip")

	logs, err := c.GenerateAccessLogs(ctx, &amplify.GenerateAccessLogsInput{
		AppId:      aws.String(appID),
		DomainName: app.App.DefaultDomain,
		StartTime:  aws.Time(time.Now().Add(-time.Hour)),
		EndTime:    aws.Time(time.Now()),
	})
	require.NoError(t, err)
	logURLString := aws.ToString(logs.LogUrl)
	require.NotEmpty(t, logURLString)
	logResp, err := http.Get(logURLString)
	require.NoError(t, err)
	defer logResp.Body.Close()
	assert.Equal(t, http.StatusOK, logResp.StatusCode)
	logBody, err := io.ReadAll(logResp.Body)
	require.NoError(t, err)
	assert.Contains(t, string(logBody), "cs-method")

	_, err = c.StartJob(ctx, &amplify.StartJobInput{
		AppId:      aws.String(appID),
		BranchName: aws.String("main"),
		JobType:    amplifytypes.JobTypeRetry,
	})
	require.Error(t, err)

	retryOut, err := c.StartJob(ctx, &amplify.StartJobInput{
		AppId:      aws.String(appID),
		BranchName: aws.String("main"),
		JobType:    amplifytypes.JobTypeRetry,
		JobId:      aws.String(jobID),
	})
	require.NoError(t, err)
	retryJobID := aws.ToString(retryOut.JobSummary.JobId)
	require.NotEqual(t, jobID, retryJobID)
	_, err = strconv.ParseUint(retryJobID, 10, 64)
	require.NoError(t, err, "Amplify retry job IDs must be numeric")
	assert.Equal(t, amplifytypes.JobTypeRetry, retryOut.JobSummary.JobType)
	assert.Equal(t, aws.ToString(job.Summary.CommitId), aws.ToString(retryOut.JobSummary.CommitId))
	assert.Equal(t, aws.ToString(job.Summary.CommitMessage), aws.ToString(retryOut.JobSummary.CommitMessage))
	amplifyWaitJobStatus(t, ctx, c, appID, "main", retryJobID, amplifytypes.JobStatusSucceed)

	listJobs, err = c.ListJobs(ctx, &amplify.ListJobsInput{
		AppId: aws.String(appID), BranchName: aws.String("main"),
	})
	require.NoError(t, err)
	require.Len(t, listJobs.JobSummaries, 2)

	// Stopping the finished job is rejected (real Amplify only stops jobs
	// that are in progress).
	_, err = c.StopJob(ctx, &amplify.StopJobInput{
		AppId:      aws.String(appID),
		BranchName: aws.String("main"),
		JobId:      aws.String(jobID),
	})
	require.Error(t, err)
	var badReq *amplifytypes.BadRequestException
	require.ErrorAs(t, err, &badReq)

	// A job stopped inside its run window lands CANCELLED.
	secondJob, err := c.StartJob(ctx, &amplify.StartJobInput{
		AppId:      aws.String(appID),
		BranchName: aws.String("main"),
		JobType:    amplifytypes.JobTypeRelease,
		CommitId:   aws.String("HEAD"),
	})
	require.NoError(t, err)
	secondJobID := aws.ToString(secondJob.JobSummary.JobId)
	stopOut, err := c.StopJob(ctx, &amplify.StopJobInput{
		AppId:      aws.String(appID),
		BranchName: aws.String("main"),
		JobId:      aws.String(secondJobID),
	})
	require.NoError(t, err)
	assert.Equal(t, amplifytypes.JobStatusCancelled, stopOut.JobSummary.Status)
	// The cancelled clone/build must not resurrect the job.
	time.Sleep(1200 * time.Millisecond)
	cancelled, err := c.GetJob(ctx, &amplify.GetJobInput{
		AppId: aws.String(appID), BranchName: aws.String("main"), JobId: aws.String(secondJobID),
	})
	require.NoError(t, err)
	assert.Equal(t, amplifytypes.JobStatusCancelled, cancelled.Job.Summary.Status)

	for _, id := range []string{jobID, retryJobID, secondJobID} {
		deleteOut, err := c.DeleteJob(ctx, &amplify.DeleteJobInput{
			AppId:      aws.String(appID),
			BranchName: aws.String("main"),
			JobId:      aws.String(id),
		})
		require.NoError(t, err)
		assert.Equal(t, id, aws.ToString(deleteOut.JobSummary.JobId))
	}

	_, err = c.GetJob(ctx, &amplify.GetJobInput{
		AppId: aws.String(appID), BranchName: aws.String("main"), JobId: aws.String(jobID),
	})
	require.Error(t, err)
	_, err = c.GetArtifactUrl(ctx, &amplify.GetArtifactUrlInput{
		ArtifactId: aws.String(artifactID),
	})
	require.Error(t, err)
	deletedBuildArtifact, err := http.Get(buildArtifactURL)
	require.NoError(t, err)
	deletedBuildArtifact.Body.Close()
	assert.Equal(t, http.StatusNotFound, deletedBuildArtifact.StatusCode)
	for _, deletedURL := range []string{testArtifactsURL, testConfigURL} {
		deletedArtifact, getErr := http.Get(deletedURL)
		require.NoError(t, getErr)
		deletedArtifact.Body.Close()
		assert.Equal(t, http.StatusNotFound, deletedArtifact.StatusCode)
	}

	listJobs, err = c.ListJobs(ctx, &amplify.ListJobsInput{
		AppId: aws.String(appID), BranchName: aws.String("main"),
	})
	require.NoError(t, err)
	require.Empty(t, listJobs.JobSummaries)

	branch, err := c.GetBranch(ctx, &amplify.GetBranchInput{
		AppId: aws.String(appID), BranchName: aws.String("main"),
	})
	require.NoError(t, err)
	assert.Empty(t, aws.ToString(branch.Branch.ActiveJobId))
	assert.Equal(t, "0", aws.ToString(branch.Branch.TotalNumberOfJobs))
}

func amplifyRequireJobStep(t *testing.T, job *amplifytypes.Job, name string) amplifytypes.Step {
	t.Helper()
	for _, step := range job.Steps {
		if aws.ToString(step.StepName) == name {
			return step
		}
	}
	t.Fatalf("job has no %s step", name)
	return amplifytypes.Step{}
}

func TestAmplifyDeploymentFlow(t *testing.T) {
	c := amplifyClient()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	app, err := c.CreateApp(ctx, &amplify.CreateAppInput{
		Name: aws.String("dep-app-" + time.Now().Format("150405.000000")),
	})
	require.NoError(t, err)
	appID := aws.ToString(app.App.AppId)
	defer func() {
		_, _ = c.DeleteApp(ctx, &amplify.DeleteAppInput{AppId: aws.String(appID)})
	}()
	_, err = c.CreateBranch(ctx, &amplify.CreateBranchInput{
		AppId: aws.String(appID), BranchName: aws.String("main"),
		Stage: amplifytypes.StageProduction,
	})
	require.NoError(t, err)
	require.Nil(t, app.App.ProductionBranch)

	// StartDeployment requires a deployment reference.
	_, err = c.StartDeployment(ctx, &amplify.StartDeploymentInput{
		AppId: aws.String(appID), BranchName: aws.String("main"),
	})
	var badReq *amplifytypes.BadRequestException
	require.ErrorAs(t, err, &badReq)

	_, err = c.StartDeployment(ctx, &amplify.StartDeploymentInput{
		AppId: aws.String(appID), BranchName: aws.String("main"),
		JobId: aws.String("nosuchdeployment"),
	})
	var notFound *amplifytypes.NotFoundException
	require.ErrorAs(t, err, &notFound)

	// CreateDeployment → HTTP PUT of the zip → StartDeployment → SUCCEED.
	createDep, err := c.CreateDeployment(ctx, &amplify.CreateDeploymentInput{
		AppId:      aws.String(appID),
		BranchName: aws.String("main"),
	})
	require.NoError(t, err)
	depJobID := aws.ToString(createDep.JobId)
	require.NotEmpty(t, depJobID)
	zipURL := aws.ToString(createDep.ZipUploadUrl)
	require.NotEmpty(t, zipURL)
	require.NotNil(t, createDep.FileUploadUrls)

	zipBytes := []byte("PK\x03\x04 sdk deployment payload " + depJobID)
	putReq, err := http.NewRequestWithContext(ctx, http.MethodPut, zipURL, bytes.NewReader(zipBytes))
	require.NoError(t, err)
	putReq.Header.Set("Content-Type", "application/zip")
	putResp, err := http.DefaultClient.Do(putReq)
	require.NoError(t, err)
	defer putResp.Body.Close()
	require.Equal(t, http.StatusOK, putResp.StatusCode, "presigned zip upload URL must accept the PUT")

	startDep, err := c.StartDeployment(ctx, &amplify.StartDeploymentInput{
		AppId:      aws.String(appID),
		BranchName: aws.String("main"),
		JobId:      aws.String(depJobID),
	})
	require.NoError(t, err)
	assert.Equal(t, depJobID, aws.ToString(startDep.JobSummary.JobId))
	assert.Equal(t, amplifytypes.JobTypeManual, startDep.JobSummary.JobType)

	deploymentJob := amplifyWaitJobStatus(t, ctx, c, appID, "main", depJobID, amplifytypes.JobStatusSucceed)

	// The uploaded build bundle is exposed through GetJob's DEPLOY step.
	// ListArtifacts remains reserved for end-to-end test outputs.
	deployStep := amplifyRequireJobStep(t, deploymentJob, "DEPLOY")
	require.NotEmpty(t, aws.ToString(deployStep.ArtifactsUrl))
	artifacts, err := c.ListArtifacts(ctx, &amplify.ListArtifactsInput{
		AppId: aws.String(appID), BranchName: aws.String("main"), JobId: aws.String(depJobID),
	})
	require.NoError(t, err)
	require.Empty(t, artifacts.Artifacts)
	resp, err := http.Get(aws.ToString(deployStep.ArtifactsUrl))
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	gotBytes, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	assert.Equal(t, zipBytes, gotBytes, "artifact must round-trip the uploaded zip content")

	// A successful job on the PRODUCTION-stage branch populates the app's
	// productionBranch.
	require.Eventually(t, func() bool {
		out, err := c.GetApp(ctx, &amplify.GetAppInput{AppId: aws.String(appID)})
		if err != nil || out.App.ProductionBranch == nil {
			return false
		}
		pb := out.App.ProductionBranch
		return aws.ToString(pb.BranchName) == "main" &&
			aws.ToString(pb.Status) == string(amplifytypes.JobStatusSucceed) &&
			pb.LastDeployTime != nil
	}, 15*time.Second, 100*time.Millisecond, "productionBranch never populated")

	// The pending deployment is consumed by StartDeployment.
	_, err = c.StartDeployment(ctx, &amplify.StartDeploymentInput{
		AppId: aws.String(appID), BranchName: aws.String("main"),
		JobId: aws.String(depJobID),
	})
	require.ErrorAs(t, err, &notFound)

	// sourceUrl-style deployment fetches real bytes from the external source.
	sourceBytes := []byte("PK\x03\x04 external deployment archive")
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodGet, r.Method)
		w.Header().Set("Content-Type", "application/zip")
		_, _ = w.Write(sourceBytes)
	}))
	defer source.Close()
	srcDep, err := c.StartDeployment(ctx, &amplify.StartDeploymentInput{
		AppId:      aws.String(appID),
		BranchName: aws.String("main"),
		SourceUrl:  aws.String(source.URL + "/site-archive.zip"),
	})
	require.NoError(t, err)
	assert.Equal(t, source.URL+"/site-archive.zip", aws.ToString(srcDep.JobSummary.SourceUrl))
	assert.Equal(t, amplifytypes.SourceUrlTypeZip, srcDep.JobSummary.SourceUrlType)
	amplifyWaitJobStatus(t, ctx, c, appID, "main", aws.ToString(srcDep.JobSummary.JobId), amplifytypes.JobStatusSucceed)
}

func TestAmplifyDeploymentFileMapUploads(t *testing.T) {
	c := amplifyClient()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	app, err := c.CreateApp(ctx, &amplify.CreateAppInput{
		Name: aws.String("filemap-" + time.Now().Format("150405.000000")),
	})
	require.NoError(t, err)
	appID := aws.ToString(app.App.AppId)
	defer func() {
		_, _ = c.DeleteApp(ctx, &amplify.DeleteAppInput{AppId: aws.String(appID)})
	}()
	_, err = c.CreateBranch(ctx, &amplify.CreateBranchInput{
		AppId: aws.String(appID), BranchName: aws.String("main"),
	})
	require.NoError(t, err)

	files := map[string][]byte{
		"index.html": []byte("<html>sdk filemap deployment</html>"),
		"app.js":     []byte("console.log('filemap');"),
	}
	fileMap := map[string]string{}
	for name, contents := range files {
		fileMap[name] = fmt.Sprintf("%x", md5.Sum(contents))
	}
	createDep, err := c.CreateDeployment(ctx, &amplify.CreateDeploymentInput{
		AppId:      aws.String(appID),
		BranchName: aws.String("main"),
		FileMap:    fileMap,
	})
	require.NoError(t, err)
	require.Len(t, createDep.FileUploadUrls, len(files))
	for name, url := range createDep.FileUploadUrls {
		putReq, err := http.NewRequestWithContext(ctx, http.MethodPut, url, bytes.NewReader(files[name]))
		require.NoError(t, err)
		putResp, err := http.DefaultClient.Do(putReq)
		require.NoError(t, err)
		putResp.Body.Close()
		require.Equal(t, http.StatusOK, putResp.StatusCode, "file upload URL for %s must accept the PUT", name)
	}

	depJobID := aws.ToString(createDep.JobId)
	_, err = c.StartDeployment(ctx, &amplify.StartDeploymentInput{
		AppId:      aws.String(appID),
		BranchName: aws.String("main"),
		JobId:      aws.String(depJobID),
	})
	require.NoError(t, err)
	amplifyWaitJobStatus(t, ctx, c, appID, "main", depJobID, amplifytypes.JobStatusSucceed)

	artifacts, err := c.ListArtifacts(ctx, &amplify.ListArtifactsInput{
		AppId: aws.String(appID), BranchName: aws.String("main"), JobId: aws.String(depJobID),
	})
	require.NoError(t, err)
	require.Empty(t, artifacts.Artifacts, "manual deployment files are build content, not end-to-end test artifacts")
	host := "main." + appID + ".amplifyapp.com"
	for name, want := range files {
		response, body := amplifyHostGet(t, host, "/"+name, nil)
		require.Equal(t, http.StatusOK, response.StatusCode)
		require.Equal(t, want, body)
	}

	wafAPI := wafClient()
	aclName := "filemap-block-" + wafStamp()
	acl, err := wafAPI.CreateWebACL(ctx, &wafv2.CreateWebACLInput{
		Name:  aws.String(aclName),
		Scope: wafv2types.ScopeCloudfront,
		DefaultAction: &wafv2types.DefaultAction{
			Block: &wafv2types.BlockAction{},
		},
		VisibilityConfig: &wafv2types.VisibilityConfig{
			SampledRequestsEnabled:   true,
			CloudWatchMetricsEnabled: true,
			MetricName:               aws.String(aclName),
		},
	})
	require.NoError(t, err)
	appARN := aws.ToString(app.App.AppArn)
	_, err = wafAPI.AssociateWebACL(ctx, &wafv2.AssociateWebACLInput{
		WebACLArn: acl.Summary.ARN, ResourceArn: aws.String(appARN),
	})
	require.NoError(t, err)

	blockedResponse, _ := amplifyHostGet(t, host, "/index.html", nil)
	require.Equal(t, http.StatusForbidden, blockedResponse.StatusCode)
	samples, err := wafAPI.GetSampledRequests(ctx, &wafv2.GetSampledRequestsInput{
		WebAclArn:      acl.Summary.ARN,
		RuleMetricName: aws.String(aclName),
		Scope:          wafv2types.ScopeCloudfront,
		TimeWindow: &wafv2types.TimeWindow{
			StartTime: aws.Time(time.Now().Add(-time.Minute)),
			EndTime:   aws.Time(time.Now().Add(time.Minute)),
		},
		MaxItems: aws.Int64(10),
	})
	require.NoError(t, err)
	require.EqualValues(t, 1, samples.PopulationSize)
	require.Len(t, samples.SampledRequests, 1)
	assert.Equal(t, "BLOCK", aws.ToString(samples.SampledRequests[0].Action))
	assert.Equal(t, "/index.html", aws.ToString(samples.SampledRequests[0].Request.URI))
	assert.Equal(t, "GET", aws.ToString(samples.SampledRequests[0].Request.Method))
	assert.EqualValues(t, http.StatusForbidden, aws.ToInt32(samples.SampledRequests[0].ResponseCodeSent))

	_, err = wafAPI.DisassociateWebACL(ctx, &wafv2.DisassociateWebACLInput{
		ResourceArn: aws.String(appARN),
	})
	require.NoError(t, err)
	_, err = wafAPI.DeleteWebACL(ctx, &wafv2.DeleteWebACLInput{
		Name: acl.Summary.Name, Scope: wafv2types.ScopeCloudfront,
		Id: acl.Summary.Id, LockToken: acl.Summary.LockToken,
	})
	require.NoError(t, err)
	unblockedResponse, unblockedBody := amplifyHostGet(t, host, "/index.html", nil)
	require.Equal(t, http.StatusOK, unblockedResponse.StatusCode)
	require.Equal(t, files["index.html"], unblockedBody)
}

func TestAmplifyListPagination(t *testing.T) {
	c := amplifyClient()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	app, err := c.CreateApp(ctx, &amplify.CreateAppInput{
		Name: aws.String("page-app-" + time.Now().Format("150405.000000")),
	})
	require.NoError(t, err)
	appID := aws.ToString(app.App.AppId)
	defer func() {
		_, _ = c.DeleteApp(ctx, &amplify.DeleteAppInput{AppId: aws.String(appID)})
	}()
	for _, name := range []string{"alpha", "beta", "gamma"} {
		_, err := c.CreateBranch(ctx, &amplify.CreateBranchInput{
			AppId: aws.String(appID), BranchName: aws.String(name),
		})
		require.NoError(t, err)
		_, err = c.CreateWebhook(ctx, &amplify.CreateWebhookInput{
			AppId: aws.String(appID), BranchName: aws.String(name),
		})
		require.NoError(t, err)
	}

	// Page size 1 + token walk over branches.
	seenBranches := map[string]bool{}
	token := (*string)(nil)
	pages := 0
	for {
		out, err := c.ListBranches(ctx, &amplify.ListBranchesInput{
			AppId:      aws.String(appID),
			MaxResults: 1,
			NextToken:  token,
		})
		require.NoError(t, err)
		require.Len(t, out.Branches, 1)
		seenBranches[aws.ToString(out.Branches[0].BranchName)] = true
		pages++
		require.LessOrEqual(t, pages, 10, "runaway branch pagination")
		if aws.ToString(out.NextToken) == "" {
			break
		}
		token = out.NextToken
	}
	assert.Equal(t, 3, pages)
	assert.Len(t, seenBranches, 3)

	// Unset maxResults: full list, no token.
	all, err := c.ListBranches(ctx, &amplify.ListBranchesInput{AppId: aws.String(appID)})
	require.NoError(t, err)
	assert.Len(t, all.Branches, 3)
	assert.Empty(t, aws.ToString(all.NextToken))

	// Webhook walk via the same contract.
	whOut, err := c.ListWebhooks(ctx, &amplify.ListWebhooksInput{
		AppId:      aws.String(appID),
		MaxResults: 2,
	})
	require.NoError(t, err)
	require.Len(t, whOut.Webhooks, 2)
	require.NotEmpty(t, aws.ToString(whOut.NextToken))
	whRest, err := c.ListWebhooks(ctx, &amplify.ListWebhooksInput{
		AppId:      aws.String(appID),
		NextToken:  whOut.NextToken,
		MaxResults: 2,
	})
	require.NoError(t, err)
	require.Len(t, whRest.Webhooks, 1)
	assert.Empty(t, aws.ToString(whRest.NextToken))
}
