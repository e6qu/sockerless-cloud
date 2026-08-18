package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	sim "github.com/e6qu/sockerless-cloud/simulator-aws/shared"
)

// Handler-level tests for the Amplify sim: explicit-maxResults pagination,
// the DeleteApp cascade, presence-based updates, job/stop state rules, and
// the wire/store splits (webhook appId hydration, deployment rows).

func amplifyResetStores() {
	// Work started by whatever ran before this must finish before the
	// stores it is reading are replaced.
	AwaitSimulatorBackground()
	amplifyApps = sim.MakeStore[amplifyStoredApp](nil, "amplify_apps")
	amplifyWebhooks = sim.MakeStore[amplifyStoredWebhook](nil, "amplify_webhooks")
	amplifyJobs = sim.MakeStore[amplifyStoredJob](nil, "amplify_jobs")
	amplifyArtifacts = sim.MakeStore[amplifyStoredArtifact](nil, "amplify_artifacts")
	amplifyDeployments = sim.MakeStore[amplifyStoredDeployment](nil, "amplify_deployments")
	amplifyRepositoryConnections = sim.MakeStore[amplifyRepositoryConnection](nil, "amplify_repository_connections")
	amplifyOptimizedImages = sim.MakeStore[amplifyStoredOptimizedImage](nil, "amplify_optimized_images")
	amplifyDomains = sim.MakeStore[amplifyStoredDomain](nil, "amplify_domains")
	amplifyBackends = sim.MakeStore[amplifyStoredBackend](nil, "amplify_backends")
	s3Buckets_ = sim.MakeStore[S3Bucket](nil, "s3_buckets")
	s3Objects = sim.MakeStore[S3Object](nil, "s3_objects")
}

func amplifySeedApp(id string, branches ...string) {
	brs := map[string]AmplifyBranch{}
	for _, b := range branches {
		brs[b] = AmplifyBranch{
			BranchArn:  amplifyBranchARN(id, b),
			BranchName: b,
			Stage:      AmplifyStageNone,
		}
	}
	amplifyApps.Put(id, amplifyStoredApp{
		App:      AmplifyApp{AppArn: amplifyAppARN(id), AppId: id, Name: id, Platform: "WEB"},
		Branches: brs,
	})
}

func amplifyDoJSON(t *testing.T, handler http.HandlerFunc, method, target, body string, pathVals map[string]string) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()
	var rdr *strings.Reader
	if body == "" {
		rdr = strings.NewReader("{}")
	} else {
		rdr = strings.NewReader(body)
	}
	r := httptest.NewRequest(method, target, rdr)
	for k, v := range pathVals {
		r.SetPathValue(k, v)
	}
	rec := httptest.NewRecorder()
	handler(rec, r)
	var decoded map[string]any
	if rec.Body.Len() > 0 {
		if err := json.Unmarshal(rec.Body.Bytes(), &decoded); err != nil {
			t.Fatalf("decode response %q: %v", rec.Body.String(), err)
		}
	}
	return rec, decoded
}

// amplifyWalkPages walks a list handler with maxResults=1 until the
// nextToken disappears and returns the per-page item counts.
func amplifyWalkPages(t *testing.T, handler http.HandlerFunc, path, key string, pathVals map[string]string) []int {
	t.Helper()
	var pages []int
	token := ""
	for {
		target := path + "?maxResults=1"
		if token != "" {
			target += "&nextToken=" + token
		}
		rec, body := amplifyDoJSON(t, handler, http.MethodGet, target, "", pathVals)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s page %d: status %d body %s", path, len(pages), rec.Code, rec.Body.String())
		}
		items, ok := body[key].([]any)
		if !ok {
			t.Fatalf("%s: missing %q array in %s", path, key, rec.Body.String())
		}
		pages = append(pages, len(items))
		next, _ := body["nextToken"].(string)
		if next == "" {
			return pages
		}
		token = next
		if len(pages) > 20 {
			t.Fatalf("%s: runaway pagination", path)
		}
	}
}

func TestAmplifyListPaginationExplicitOnly(t *testing.T) {
	amplifyResetStores()
	amplifySeedApp("dapp1", "b1", "b2", "b3")
	amplifySeedApp("dapp2")
	amplifySeedApp("dapp3")
	for i := 1; i <= 3; i++ {
		id := fmt.Sprintf("dwh%d", i)
		amplifyWebhooks.Put(id, amplifyStoredWebhook{Webhook: AmplifyWebhook{WebhookId: id, BranchName: "b1"}, AppId: "dapp1"})
		jid := fmt.Sprintf("j%d", i)
		amplifyJobs.Put(jid, amplifyStoredJob{
			Job:        AmplifyJob{Summary: AmplifyJobSummary{JobId: jid, StartTime: float64(i), Status: AmplifyJobStatusSucceed}},
			AppId:      "dapp1",
			BranchName: "b1",
		})
		dom := fmt.Sprintf("d%d.example.com", i)
		amplifyDomains.Put(amplifyDomainKey("dapp1", dom), amplifyStoredDomain{
			Domain: AmplifyDomainAssociation{DomainName: dom, DomainStatus: AmplifyDomainStatusAvailable},
			AppId:  "dapp1",
		})
		env := fmt.Sprintf("env%d", i)
		amplifyBackends.Put(amplifyDomainKey("dapp1", env), amplifyStoredBackend{
			Env:   AmplifyBackendEnvironment{EnvironmentName: env},
			AppId: "dapp1",
		})
	}

	appVals := map[string]string{"appId": "dapp1"}
	branchVals := map[string]string{"appId": "dapp1", "name": "b1"}
	cases := []struct {
		name    string
		handler http.HandlerFunc
		path    string
		key     string
		vals    map[string]string
	}{
		{"ListApps", handleAmplifyListApps, "/apps", "apps", nil},
		{"ListBranches", handleAmplifyListBranches, "/apps/dapp1/branches", "branches", appVals},
		{"ListJobs", handleAmplifyListJobs, "/apps/dapp1/branches/b1/jobs", "jobSummaries", branchVals},
		{"ListWebhooks", handleAmplifyListWebhooks, "/apps/dapp1/webhooks", "webhooks", appVals},
		{"ListDomainAssociations", handleAmplifyListDomains, "/apps/dapp1/domains", "domainAssociations", appVals},
		{"ListBackendEnvironments", handleAmplifyListBackends, "/apps/dapp1/backendenvironments", "backendEnvironments", appVals},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pages := amplifyWalkPages(t, tc.handler, tc.path, tc.key, tc.vals)
			if len(pages) != 3 {
				t.Fatalf("expected 3 pages of 1, got %v", pages)
			}
			for _, n := range pages {
				if n != 1 {
					t.Fatalf("expected page size 1, got %v", pages)
				}
			}
			// Unset maxResults: full list, no token.
			rec, body := amplifyDoJSON(t, tc.handler, http.MethodGet, tc.path, "", tc.vals)
			if rec.Code != http.StatusOK {
				t.Fatalf("full list: status %d body %s", rec.Code, rec.Body.String())
			}
			if n := len(body[tc.key].([]any)); n != 3 {
				t.Fatalf("full list: expected 3 items, got %d", n)
			}
			if _, present := body["nextToken"]; present {
				t.Fatalf("full list must not carry nextToken: %s", rec.Body.String())
			}
			// Malformed pagination params are rejected.
			rec, _ = amplifyDoJSON(t, tc.handler, http.MethodGet, tc.path+"?maxResults=bogus", "", tc.vals)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("bogus maxResults: expected 400, got %d", rec.Code)
			}
			rec, _ = amplifyDoJSON(t, tc.handler, http.MethodGet, tc.path+"?nextToken=bogus", "", tc.vals)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("bogus nextToken: expected 400, got %d", rec.Code)
			}
		})
	}
}

func TestAmplifyDeleteAppCascade(t *testing.T) {
	amplifyResetStores()
	amplifySeedApp("doomed", "main")
	amplifySeedApp("keeper", "main")
	bucket := amplifyArtifactBucketName()
	for _, appID := range []string{"doomed", "keeper"} {
		amplifyWebhooks.Put("wh-"+appID, amplifyStoredWebhook{Webhook: AmplifyWebhook{WebhookId: "wh-" + appID}, AppId: appID})
		artifactKey := "artifacts/" + appID + "/main/job-" + appID + "/out.zip"
		amplifyPutS3Object(artifactKey, "application/zip", []byte("artifact"))
		amplifyArtifacts.Put("art-"+appID, amplifyStoredArtifact{
			Artifact: AmplifyArtifact{ArtifactId: "art-" + appID, ArtifactFileName: "out.zip"},
			AppId:    appID, BranchName: "main", JobId: "job-" + appID, Key: artifactKey,
		})
		amplifyJobs.Put("job-"+appID, amplifyStoredJob{
			Job:   AmplifyJob{Summary: AmplifyJobSummary{JobId: "job-" + appID, Status: AmplifyJobStatusSucceed}},
			AppId: appID, BranchName: "main",
		})
		depKey := "deployments/" + appID + "/main/dep-" + appID + "/archive.zip"
		amplifyPutS3Object(depKey, "application/zip", []byte("zip"))
		amplifyDeployments.Put("dep-"+appID, amplifyStoredDeployment{
			JobId: "dep-" + appID, AppId: appID, BranchName: "main", ZipKey: depKey, FileKeys: map[string]string{},
		})
		amplifyDomains.Put(amplifyDomainKey(appID, "x.example.com"), amplifyStoredDomain{
			Domain: AmplifyDomainAssociation{DomainName: "x.example.com"}, AppId: appID,
		})
		amplifyBackends.Put(amplifyDomainKey(appID, "staging"), amplifyStoredBackend{
			Env: AmplifyBackendEnvironment{EnvironmentName: "staging"}, AppId: appID,
		})
	}

	rec, _ := amplifyDoJSON(t, handleAmplifyDeleteApp, http.MethodDelete, "/apps/doomed", "", map[string]string{"appId": "doomed"})
	if rec.Code != http.StatusOK {
		t.Fatalf("delete app: status %d body %s", rec.Code, rec.Body.String())
	}

	counts := map[string]int{}
	for _, wh := range amplifyWebhooks.List() {
		counts[wh.AppId]++
	}
	for _, jb := range amplifyJobs.List() {
		counts[jb.AppId]++
	}
	for _, art := range amplifyArtifacts.List() {
		counts[art.AppId]++
	}
	for _, dep := range amplifyDeployments.List() {
		counts[dep.AppId]++
	}
	for _, dom := range amplifyDomains.List() {
		counts[dom.AppId]++
	}
	for _, be := range amplifyBackends.List() {
		counts[be.AppId]++
	}
	if counts["doomed"] != 0 {
		t.Fatalf("cascade left %d orphan rows for deleted app", counts["doomed"])
	}
	if counts["keeper"] != 6 {
		t.Fatalf("cascade deleted sibling app rows: keeper has %d of 6", counts["keeper"])
	}
	for _, key := range []string{
		"artifacts/doomed/main/job-doomed/out.zip",
		"deployments/doomed/main/dep-doomed/archive.zip",
	} {
		if _, ok := s3Objects.Get(s3ObjectKey(bucket, key)); ok {
			t.Fatalf("cascade left S3 object %s", key)
		}
	}
}

func TestAmplifyUpdateAppPresenceSemantics(t *testing.T) {
	amplifyResetStores()
	_, created := amplifyDoJSON(t, handleAmplifyCreateApp, http.MethodPost, "/apps",
		`{"name":"presence","description":"keep me","platform":"WEB","enableAutoBranchCreation":true,
		  "autoBranchCreationPatterns":["feature/*"],"autoBranchCreationConfig":{"stage":"BETA"},
		  "customRules":[{"source":"/<*>","target":"/index.html","status":"404-200"}],
		  "repository":"https://github.com/acme/site"}`, nil)
	appID := created["app"].(map[string]any)["appId"].(string)

	// Absent members stay untouched.
	_, updated := amplifyDoJSON(t, handleAmplifyUpdateApp, http.MethodPost, "/apps/"+appID,
		`{"platform":"WEB_COMPUTE"}`, map[string]string{"appId": appID})
	app := updated["app"].(map[string]any)
	if app["description"] != "keep me" || app["platform"] != "WEB_COMPUTE" {
		t.Fatalf("absent members must be preserved: %v", app)
	}
	if app["repositoryCloneMethod"] != "TOKEN" {
		t.Fatalf("github repository must derive TOKEN clone method, got %v", app["repositoryCloneMethod"])
	}

	// Explicit empty values clear.
	_, updated = amplifyDoJSON(t, handleAmplifyUpdateApp, http.MethodPost, "/apps/"+appID,
		`{"description":"","enableAutoBranchCreation":false,"autoBranchCreationPatterns":[],"customRules":[]}`,
		map[string]string{"appId": appID})
	app = updated["app"].(map[string]any)
	if _, present := app["description"]; present {
		t.Fatalf("explicit empty description must clear: %v", app)
	}
	if app["enableAutoBranchCreation"] != false {
		t.Fatalf("enableAutoBranchCreation must clear: %v", app)
	}
	if _, present := app["autoBranchCreationPatterns"]; present {
		t.Fatalf("autoBranchCreationPatterns must clear: %v", app)
	}
	if _, present := app["customRules"]; present {
		t.Fatalf("customRules must clear: %v", app)
	}
}

func TestAmplifyUpdateBranchPresenceSemantics(t *testing.T) {
	amplifyResetStores()
	amplifySeedApp("brapp")
	_, created := amplifyDoJSON(t, handleAmplifyCreateBranch, http.MethodPost, "/apps/brapp/branches",
		`{"branchName":"main","description":"first","displayName":"Main","enableSkewProtection":true,
		  "backend":{"stackArn":"arn:aws:cloudformation:us-east-1:123456789012:stack/amplify/x"}}`,
		map[string]string{"appId": "brapp"})
	br := created["branch"].(map[string]any)
	if br["enableSkewProtection"] != true {
		t.Fatalf("enableSkewProtection must round-trip on create: %v", br)
	}
	if br["backend"].(map[string]any)["stackArn"] == "" {
		t.Fatalf("backend must round-trip on create: %v", br)
	}

	_, updated := amplifyDoJSON(t, handleAmplifyUpdateBranch, http.MethodPost, "/apps/brapp/branches/main",
		`{"description":"","displayName":"","enableSkewProtection":false}`,
		map[string]string{"appId": "brapp", "name": "main"})
	br = updated["branch"].(map[string]any)
	if _, present := br["description"]; present {
		t.Fatalf("explicit empty description must clear: %v", br)
	}
	if _, present := br["displayName"]; present {
		t.Fatalf("explicit empty displayName must clear: %v", br)
	}
	if br["enableSkewProtection"] != false {
		t.Fatalf("enableSkewProtection must clear: %v", br)
	}
	if br["backend"].(map[string]any)["stackArn"] == "" {
		t.Fatalf("absent backend must be preserved: %v", br)
	}
}

func TestAmplifyStopJobRejectsFinishedJob(t *testing.T) {
	amplifyResetStores()
	amplifySeedApp("stopapp", "main")
	amplifyJobs.Put("done1", amplifyStoredJob{
		Job:   AmplifyJob{Summary: AmplifyJobSummary{JobId: "done1", Status: AmplifyJobStatusSucceed}},
		AppId: "stopapp", BranchName: "main",
	})
	vals := map[string]string{"appId": "stopapp", "name": "main", "jobId": "done1"}
	rec, body := amplifyDoJSON(t, handleAmplifyStopJob, http.MethodDelete, "/apps/stopapp/branches/main/jobs/done1/stop", "", vals)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("stopping a finished job must 400, got %d: %s", rec.Code, rec.Body.String())
	}
	if body["__type"] != "BadRequestException" {
		t.Fatalf("expected BadRequestException, got %v", body)
	}

	// An in-progress job still stops.
	amplifyJobs.Put("run1", amplifyStoredJob{
		Job:   AmplifyJob{Summary: AmplifyJobSummary{JobId: "run1", Status: AmplifyJobStatusRunning}, Steps: amplifyJobSteps(1, AmplifyJobStatusRunning)},
		AppId: "stopapp", BranchName: "main",
	})
	vals["jobId"] = "run1"
	rec, body = amplifyDoJSON(t, handleAmplifyStopJob, http.MethodDelete, "/apps/stopapp/branches/main/jobs/run1/stop", "", vals)
	if rec.Code != http.StatusOK {
		t.Fatalf("stopping a running job: status %d body %s", rec.Code, rec.Body.String())
	}
	if got := body["jobSummary"].(map[string]any)["status"]; got != string(AmplifyJobStatusCancelled) {
		t.Fatalf("expected CANCELLED, got %v", got)
	}
	// No asynchronous worker may resurrect a cancelled job.
	if amplifyAdvanceJob("run1", AmplifyJobStatusRunning, AmplifyJobStatusSucceed) {
		t.Fatal("advance must refuse a job that left the expected state")
	}
}

func TestAmplifyStartDeploymentValidation(t *testing.T) {
	amplifyResetStores()
	amplifySeedApp("depapp", "main")
	vals := map[string]string{"appId": "depapp", "name": "main"}

	rec, body := amplifyDoJSON(t, handleAmplifyStartDeployment, http.MethodPost, "/apps/depapp/branches/main/deployments/start", `{}`, vals)
	if rec.Code != http.StatusBadRequest || body["__type"] != "BadRequestException" {
		t.Fatalf("missing jobId+sourceUrl must 400 BadRequestException, got %d %v", rec.Code, body)
	}

	rec, body = amplifyDoJSON(t, handleAmplifyStartDeployment, http.MethodPost, "/apps/depapp/branches/main/deployments/start", `{"jobId":"nosuch"}`, vals)
	if rec.Code != http.StatusNotFound || body["__type"] != "NotFoundException" {
		t.Fatalf("unknown jobId must 404 NotFoundException, got %d %v", rec.Code, body)
	}

	rec, _ = amplifyDoJSON(t, handleAmplifyStartDeployment, http.MethodPost, "/apps/depapp/branches/main/deployments/start", `{"sourceUrl":"ftp://nope"}`, vals)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("non s3/http sourceUrl must 400, got %d", rec.Code)
	}

	s3Buckets_.Put("bucket", S3Bucket{Name: "bucket"})
	s3Objects.Put(s3ObjectKey("bucket", "prefix/index.html"), S3Object{
		Key:  s3ObjectKey("bucket", "prefix/index.html"),
		Data: []byte("<html>source prefix</html>"),
	})
	rec, body = amplifyDoJSON(t, handleAmplifyStartDeployment, http.MethodPost, "/apps/depapp/branches/main/deployments/start",
		`{"sourceUrl":"s3://bucket/prefix/","sourceUrlType":"BUCKET_PREFIX"}`, vals)
	if rec.Code != http.StatusOK {
		t.Fatalf("sourceUrl deployment: status %d body %s", rec.Code, rec.Body.String())
	}
	summary := body["jobSummary"].(map[string]any)
	if summary["sourceUrl"] != "s3://bucket/prefix/" || summary["sourceUrlType"] != string(AmplifySourceUrlBucketPrefix) {
		t.Fatalf("sourceUrl/sourceUrlType must be recorded on the summary: %v", summary)
	}
}

func TestAmplifyCreateDeploymentPendingRow(t *testing.T) {
	amplifyResetStores()
	amplifySeedApp("zipapp", "main")
	vals := map[string]string{"appId": "zipapp", "name": "main"}

	rec, body := amplifyDoJSON(t, handleAmplifyCreateDeployment, http.MethodPost, "/apps/zipapp/branches/main/deployments",
		`{"fileMap":{"index.html":"d41d8cd98f00b204e9800998ecf8427e"}}`, vals)
	if rec.Code != http.StatusOK {
		t.Fatalf("create deployment: status %d body %s", rec.Code, rec.Body.String())
	}
	jobID := body["jobId"].(string)
	zipURL := body["zipUploadUrl"].(string)
	if !strings.Contains(zipURL, amplifyArtifactBucketName()) {
		t.Fatalf("zipUploadUrl must target the sim's own S3 data plane, got %s", zipURL)
	}
	fileURLs := body["fileUploadUrls"].(map[string]any)
	if len(fileURLs) != 1 || fileURLs["index.html"] == "" {
		t.Fatalf("fileMap entries must each get an upload URL: %v", fileURLs)
	}
	dep, ok := amplifyDeployments.Get(jobID)
	if !ok || dep.AppId != "zipapp" || dep.BranchName != "main" || dep.ZipKey == "" {
		t.Fatalf("pending deployment row missing or incomplete: %+v", dep)
	}

	// Unknown app/branch surface as BadRequestException (the model carries
	// no NotFoundException for CreateDeployment).
	rec, body = amplifyDoJSON(t, handleAmplifyCreateDeployment, http.MethodPost, "/apps/nosuch/branches/main/deployments", "",
		map[string]string{"appId": "nosuch", "name": "main"})
	if rec.Code != http.StatusBadRequest || body["__type"] != "BadRequestException" {
		t.Fatalf("unknown app must 400 BadRequestException, got %d %v", rec.Code, body)
	}
}

func TestAmplifyWebhookWireStoreSplit(t *testing.T) {
	// A row persisted before appId joined the wire shape carries the
	// linkage only on the store struct; the wire view must hydrate it.
	row := `{"Webhook":{"webhookArn":"arn:aws:amplify:us-east-1:123456789012:webhooks/w1","webhookId":"w1","webhookUrl":"https://webhooks.amplify.us-east-1.amazonaws.com/prod/webhooks?id=w1","branchName":"main","createTime":1,"updateTime":1},"AppId":"oldapp"}`
	var stored amplifyStoredWebhook
	if err := json.Unmarshal([]byte(row), &stored); err != nil {
		t.Fatalf("unmarshal legacy row: %v", err)
	}
	wire := amplifyWebhookWire(stored)
	if wire.AppId != "oldapp" {
		t.Fatalf("wire view must hydrate appId from the store row, got %q", wire.AppId)
	}
	data, err := json.Marshal(wire)
	if err != nil {
		t.Fatalf("marshal wire: %v", err)
	}
	if !strings.Contains(string(data), `"appId":"oldapp"`) {
		t.Fatalf("wire shape must emit appId: %s", data)
	}

	// The job wire shape carries no store-only linkage members.
	jobRow, err := json.Marshal(AmplifyJob{Summary: AmplifyJobSummary{JobId: "j1", Status: AmplifyJobStatusSucceed}})
	if err != nil {
		t.Fatalf("marshal job: %v", err)
	}
	for _, key := range []string{`"appId"`, `"branchName"`} {
		if strings.Contains(string(jobRow), key) {
			t.Fatalf("job wire shape leaks %s: %s", key, jobRow)
		}
	}
}

func TestAmplifyProductionBranchWireShape(t *testing.T) {
	app := AmplifyApp{
		AppId: "papp",
		ProductionBranch: &AmplifyProductionBranch{
			BranchName:     "main",
			LastDeployTime: 1700000000,
			Status:         AmplifyJobStatusSucceed,
		},
	}
	data, err := json.Marshal(app)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	for _, want := range []string{`"productionBranch"`, `"branchName":"main"`, `"status":"SUCCEED"`, `"lastDeployTime":1700000000`} {
		if !strings.Contains(string(data), want) {
			t.Fatalf("productionBranch wire shape missing %s: %s", want, data)
		}
	}
	if strings.Contains(string(data), "thumbnailUrl") {
		t.Fatalf("thumbnailUrl is external and must stay absent: %s", data)
	}
}
