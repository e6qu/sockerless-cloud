package main

import (
	"archive/zip"
	"bytes"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/e6qu/sockerless-cloud/sim"
)

// Docker-free hosting data-plane tests: host matcher, custom-rule matcher,
// static content resolution, and basic auth — all served from in-memory
// stores.

func amplifyResetHostingState() {
	amplifyResetStores()
	// Background work from an earlier test must finish before the stores
	// it is reading are replaced.
	AwaitSimulatorBackground()
	r53Zones = sim.MakeStore[r53StoredZone](nil, "route53_zones")
	wafAssociations = sim.MakeStore[wafAssociation](nil, "wafv2_associations")
	amplifyHostingMu.Lock()
	amplifyHostingCache = map[string]*amplifyHostedContent{}
	amplifyHostingMu.Unlock()
}

func amplifyZipOf(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, content := range files {
		f, err := zw.Create(name)
		if err != nil {
			t.Fatalf("zip create %s: %v", name, err)
		}
		if _, err := f.Write([]byte(content)); err != nil {
			t.Fatalf("zip write %s: %v", name, err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("zip close: %v", err)
	}
	return buf.Bytes()
}

// amplifySeedDeployment stores a SUCCEED job whose artifact is a zip of the
// given files, making it the branch's active deployment.
func amplifySeedDeployment(t *testing.T, appID, branch, jobID string, files map[string]string) {
	t.Helper()
	key := "artifacts/" + appID + "/" + branch + "/" + jobID + "/artifacts.zip"
	amplifyPutS3Object(key, "application/zip", amplifyZipOf(t, files))
	amplifyArtifacts.Put(jobID+"-art", amplifyStoredArtifact{
		Artifact:      AmplifyArtifact{ArtifactId: jobID + "-art", ArtifactFileName: "artifacts.zip"},
		AppId:         appID,
		BranchName:    branch,
		JobId:         jobID,
		Key:           key,
		HostedContent: true,
	})
	amplifyJobs.Put(jobID, amplifyStoredJob{
		Job:        AmplifyJob{Summary: AmplifyJobSummary{JobId: jobID, Status: AmplifyJobStatusSucceed, StartTime: amplifyEpoch()}},
		AppId:      appID,
		BranchName: branch,
	})
}

func amplifyHostingGet(t *testing.T, host, path string, header http.Header) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(http.MethodGet, "http://placeholder"+path, nil)
	r.Host = host
	for k, vs := range header {
		for _, v := range vs {
			r.Header.Add(k, v)
		}
	}
	rec := httptest.NewRecorder()
	appID, branch, ok := amplifyHostingTarget(r.Host)
	if !ok {
		t.Fatalf("host %s did not match any hosting target", host)
	}
	handleAmplifyHosting(rec, r, appID, branch)
	return rec
}

func TestAmplifyHostingTargetTable(t *testing.T) {
	amplifyResetHostingState()
	amplifySeedApp("dhost1", "main", "dev")
	amplifySeedApp("dhost2", "main")
	// dhost2 has a production branch → its cloudfront host routes there.
	st, _ := amplifyApps.Get("dhost2")
	st.App.ProductionBranch = &AmplifyProductionBranch{BranchName: "main"}
	amplifyApps.Put("dhost2", st)
	// Custom domain: AVAILABLE association with one verified + one
	// unverified subdomain, plus a PENDING association that must not serve.
	amplifyDomains.Put(amplifyDomainKey("dhost1", "example.com"), amplifyStoredDomain{
		AppId: "dhost1",
		Domain: AmplifyDomainAssociation{
			DomainName:   "example.com",
			DomainStatus: AmplifyDomainStatusAvailable,
			SubDomains: []AmplifySubDomain{
				{SubDomainSetting: AmplifySubDomainSetting{Prefix: "www", BranchName: "main"}, Verified: true},
				{SubDomainSetting: AmplifySubDomainSetting{Prefix: "", BranchName: "main"}, Verified: true},
				{SubDomainSetting: AmplifySubDomainSetting{Prefix: "beta", BranchName: "dev"}, Verified: false},
			},
		},
	})
	amplifyDomains.Put(amplifyDomainKey("dhost1", "pending.example.org"), amplifyStoredDomain{
		AppId: "dhost1",
		Domain: AmplifyDomainAssociation{
			DomainName:   "pending.example.org",
			DomainStatus: AmplifyDomainStatusPendingVerification,
			Certificate:  &AmplifyCertificate{Type: "AMPLIFY_MANAGED"},
			SubDomains: []AmplifySubDomain{
				{SubDomainSetting: AmplifySubDomainSetting{Prefix: "www", BranchName: "main"}},
			},
		},
	})

	cases := []struct {
		host       string
		wantApp    string
		wantBranch string
		wantOK     bool
	}{
		{"main.dhost1.amplifyapp.com", "dhost1", "main", true},
		{"dev.dhost1.amplifyapp.com:443", "dhost1", "dev", true},
		{"MAIN.DHOST1.AMPLIFYAPP.COM", "dhost1", "main", true},
		{"missing.dhost1.amplifyapp.com", "", "", false},
		{"main.nosuchapp.amplifyapp.com", "", "", false},
		{"dhost1.amplifyapp.com", "", "", false}, // apex carries no branch
		{amplifyCloudFrontDomain("dhost2"), "dhost2", "main", true},
		{amplifyCloudFrontDomain("dhost1"), "", "", false}, // two branches, no production branch
		{"d0000000000000.cloudfront.net", "", "", false},
		{"www.example.com", "dhost1", "main", true},
		{"example.com", "dhost1", "main", true}, // apex subdomain (empty prefix)
		{"beta.example.com", "", "", false},     // unverified subdomain
		{"www.pending.example.org", "", "", false},
		{"unrelated.localhost", "", "", false},
	}
	for _, tc := range cases {
		appID, branch, ok := amplifyHostingTarget(tc.host)
		if ok != tc.wantOK || appID != tc.wantApp || branch != tc.wantBranch {
			t.Fatalf("host %q: got (%q,%q,%v), want (%q,%q,%v)", tc.host, appID, branch, ok, tc.wantApp, tc.wantBranch, tc.wantOK)
		}
	}
}

func TestAmplifyMatchCustomRuleTable(t *testing.T) {
	cases := []struct {
		rule       AmplifyCustomRule
		path       string
		wantTarget string
		wantOK     bool
	}{
		{AmplifyCustomRule{Source: "/old", Target: "/new"}, "/old", "/new", true},
		{AmplifyCustomRule{Source: "/old", Target: "/new"}, "/older", "", false},
		{AmplifyCustomRule{Source: "/<*>", Target: "/index.html"}, "/anything/at/all", "/index.html", true},
		{AmplifyCustomRule{Source: "<*>", Target: "/index.html"}, "/x", "/index.html", true},
		{AmplifyCustomRule{Source: "/docs/<*>", Target: "/documents/<*>"}, "/docs/a/b.html", "/documents/a/b.html", true},
		{AmplifyCustomRule{Source: "/docs/<*>", Target: "/documents/<*>"}, "/blog/a", "", false},
		// Geo-conditioned rules never match: the sim has no request geo.
		{AmplifyCustomRule{Source: "/<*>", Target: "/us.html", Condition: "<US>"}, "/x", "", false},
	}
	for _, tc := range cases {
		target, ok := amplifyMatchCustomRule(tc.rule, tc.path)
		if ok != tc.wantOK || target != tc.wantTarget {
			t.Fatalf("rule %+v path %q: got (%q,%v), want (%q,%v)", tc.rule, tc.path, target, ok, tc.wantTarget, tc.wantOK)
		}
	}
}

func TestAmplifyHostingStaticServing(t *testing.T) {
	amplifyResetHostingState()
	amplifySeedApp("dserve", "main")
	st, _ := amplifyApps.Get("dserve")
	st.App.CustomRules = []AmplifyCustomRule{
		{Source: "/old", Target: "/about/", Status: "301"},
		{Source: "/temp", Target: "/index.html", Status: "302"},
		{Source: "/<*>", Target: "/index.html", Status: "404-200"},
	}
	amplifyApps.Put("dserve", st)
	amplifySeedDeployment(t, "dserve", "main", "djob1", map[string]string{
		"index.html":       "<html>home</html>",
		"about/index.html": "<html>about</html>",
		"app.css":          "body{}",
	})
	host := "main.dserve.amplifyapp.com"

	// Exact file.
	rec := amplifyHostingGet(t, host, "/app.css", nil)
	if rec.Code != http.StatusOK || rec.Body.String() != "body{}" {
		t.Fatalf("exact file: %d %q", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); ct != "text/css; charset=utf-8" {
		t.Fatalf("css content type: %q", ct)
	}
	// Root → index.html.
	rec = amplifyHostingGet(t, host, "/", nil)
	if rec.Code != http.StatusOK || rec.Body.String() != "<html>home</html>" {
		t.Fatalf("root index: %d %q", rec.Code, rec.Body.String())
	}
	// Directory → its index.html.
	rec = amplifyHostingGet(t, host, "/about", nil)
	if rec.Code != http.StatusOK || rec.Body.String() != "<html>about</html>" {
		t.Fatalf("directory index: %d %q", rec.Code, rec.Body.String())
	}
	// Redirects.
	rec = amplifyHostingGet(t, host, "/old", nil)
	if rec.Code != http.StatusMovedPermanently || rec.Header().Get("Location") != "/about/" {
		t.Fatalf("301 rule: %d %q", rec.Code, rec.Header().Get("Location"))
	}
	rec = amplifyHostingGet(t, host, "/temp", nil)
	if rec.Code != http.StatusFound {
		t.Fatalf("302 rule: %d", rec.Code)
	}
	// SPA fallback for unknown extensionless route.
	rec = amplifyHostingGet(t, host, "/client/route", nil)
	if rec.Code != http.StatusOK || rec.Body.String() != "<html>home</html>" {
		t.Fatalf("SPA fallback: %d %q", rec.Code, rec.Body.String())
	}
}

func TestAmplifyHostingSPAlessDefault404(t *testing.T) {
	amplifyResetHostingState()
	amplifySeedApp("dnospa", "main")
	amplifySeedDeployment(t, "dnospa", "main", "djob2", map[string]string{"index.html": "x"})
	rec := amplifyHostingGet(t, "main.dnospa.amplifyapp.com", "/missing", nil)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("no rules + missing file must 404, got %d", rec.Code)
	}
}

func TestAmplifyHostingNoDeployment404(t *testing.T) {
	amplifyResetHostingState()
	amplifySeedApp("dempty", "main")
	rec := amplifyHostingGet(t, "main.dempty.amplifyapp.com", "/", nil)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("branch without deployment must 404, got %d", rec.Code)
	}
}

func TestAmplifyHostingInvalidArtifactIsNotContent(t *testing.T) {
	amplifyResetHostingState()
	amplifySeedApp("dsynth", "main")
	// An invalid deployment artifact is not a valid zip, so the
	// hosting plane must treat the branch as having no servable content.
	key := "artifacts/dsynth/main/djob3/e2e-test-artifacts.zip"
	amplifyPutS3Object(key, "application/zip", []byte("amplify artifact placeholder\n"))
	amplifyArtifacts.Put("djob3-art", amplifyStoredArtifact{
		Artifact:      AmplifyArtifact{ArtifactId: "djob3-art", ArtifactFileName: "e2e-test-artifacts.zip"},
		AppId:         "dsynth",
		BranchName:    "main",
		JobId:         "djob3",
		Key:           key,
		HostedContent: true,
	})
	amplifyJobs.Put("djob3", amplifyStoredJob{
		Job:   AmplifyJob{Summary: AmplifyJobSummary{JobId: "djob3", Status: AmplifyJobStatusSucceed, StartTime: 1}},
		AppId: "dsynth", BranchName: "main",
	})
	rec := amplifyHostingGet(t, "main.dsynth.amplifyapp.com", "/", nil)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("invalid zip artifact must serve no content, got %d", rec.Code)
	}
}

func TestAmplifyHostingFileMapDeployment(t *testing.T) {
	amplifyResetHostingState()
	amplifySeedApp("dmap", "main")
	// fileMap deployments register one artifact per uploaded file.
	for name, content := range map[string]string{"index.html": "<html>map</html>", "app.js": "js"} {
		key := "deployments/dmap/main/djob4/files/" + name
		amplifyPutS3Object(key, "", []byte(content))
		amplifyArtifacts.Put("djob4-"+name, amplifyStoredArtifact{
			Artifact:      AmplifyArtifact{ArtifactId: "djob4-" + name, ArtifactFileName: name},
			AppId:         "dmap",
			BranchName:    "main",
			JobId:         "djob4",
			Key:           key,
			HostedContent: true,
		})
	}
	amplifyJobs.Put("djob4", amplifyStoredJob{
		Job:   AmplifyJob{Summary: AmplifyJobSummary{JobId: "djob4", Status: AmplifyJobStatusSucceed, StartTime: 1}},
		AppId: "dmap", BranchName: "main",
	})
	rec := amplifyHostingGet(t, "main.dmap.amplifyapp.com", "/app.js", nil)
	if rec.Code != http.StatusOK || rec.Body.String() != "js" {
		t.Fatalf("fileMap deployment file: %d %q", rec.Code, rec.Body.String())
	}
}

func TestAmplifyHostingCacheInvalidatesOnNewJob(t *testing.T) {
	amplifyResetHostingState()
	amplifySeedApp("dcache", "main")
	amplifySeedDeployment(t, "dcache", "main", "djob5", map[string]string{"index.html": "v1"})
	host := "main.dcache.amplifyapp.com"
	if rec := amplifyHostingGet(t, host, "/", nil); rec.Body.String() != "v1" {
		t.Fatalf("first deploy: %q", rec.Body.String())
	}
	// A newer SUCCEED job replaces the served content.
	amplifySeedDeployment(t, "dcache", "main", "djob6", map[string]string{"index.html": "v2"})
	amplifyJobs.Update("djob6", func(j *amplifyStoredJob) { j.Job.Summary.StartTime += 10 })
	if rec := amplifyHostingGet(t, host, "/", nil); rec.Body.String() != "v2" {
		t.Fatalf("new deploy must invalidate cache: %q", rec.Body.String())
	}
}

func TestAmplifyHostingBasicAuth(t *testing.T) {
	amplifyResetHostingState()
	amplifySeedApp("dauth", "main", "open")
	st, _ := amplifyApps.Get("dauth")
	creds := base64.StdEncoding.EncodeToString([]byte("user:secret"))
	br := st.Branches["main"]
	br.EnableBasicAuth = true
	br.BasicAuthCredentials = creds
	st.Branches["main"] = br
	amplifyApps.Put("dauth", st)
	amplifySeedDeployment(t, "dauth", "main", "djob7", map[string]string{"index.html": "private"})
	amplifySeedDeployment(t, "dauth", "open", "djob8", map[string]string{"index.html": "public"})
	host := "main.dauth.amplifyapp.com"

	rec := amplifyHostingGet(t, host, "/", nil)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("missing credentials must 401, got %d", rec.Code)
	}
	if got := rec.Header().Get("WWW-Authenticate"); !contains(got, "Basic") {
		t.Fatalf("401 must advertise Basic, got %q", got)
	}
	wrong := base64.StdEncoding.EncodeToString([]byte("user:wrong"))
	rec = amplifyHostingGet(t, host, "/", http.Header{"Authorization": {"Basic " + wrong}})
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("wrong credentials must 401, got %d", rec.Code)
	}
	rec = amplifyHostingGet(t, host, "/", http.Header{"Authorization": {"Basic " + creds}})
	if rec.Code != http.StatusOK || rec.Body.String() != "private" {
		t.Fatalf("valid credentials: %d %q", rec.Code, rec.Body.String())
	}
	// The sibling branch has no basic auth.
	rec = amplifyHostingGet(t, "open.dauth.amplifyapp.com", "/", nil)
	if rec.Code != http.StatusOK || rec.Body.String() != "public" {
		t.Fatalf("branch without basic auth: %d %q", rec.Code, rec.Body.String())
	}
}

func contains(s, sub string) bool { return bytes.Contains([]byte(s), []byte(sub)) }

func TestAmplifyDomainVerificationEvaluation(t *testing.T) {
	amplifyResetHostingState()
	amplifySeedApp("dverify", "main")

	name, value := amplifyCertVerificationParts("dverify", "shop.example.net")
	pending := amplifyStoredDomain{
		AppId: "dverify",
		Domain: AmplifyDomainAssociation{
			DomainName:                       "shop.example.net",
			DomainStatus:                     AmplifyDomainStatusPendingVerification,
			UpdateStatus:                     AmplifyDomainUpdatePendingVerification,
			Certificate:                      &AmplifyCertificate{Type: "AMPLIFY_MANAGED", CertificateVerificationDNSRecord: name + " CNAME " + value},
			CertificateVerificationDNSRecord: name + " CNAME " + value,
			SubDomains: []AmplifySubDomain{
				{SubDomainSetting: AmplifySubDomainSetting{Prefix: "www", BranchName: "main"}},
			},
		},
	}
	amplifyDomains.Put(amplifyDomainKey("dverify", "shop.example.net"), pending)

	// No hosted zone → honestly stays PENDING_VERIFICATION.
	got := amplifyEvaluateDomainVerification(pending)
	if got.Domain.DomainStatus != AmplifyDomainStatusPendingVerification {
		t.Fatalf("no zone: status %s", got.Domain.DomainStatus)
	}

	// Zone exists but no verification record → still pending.
	r53Zones.Put("Z1", r53StoredZone{Zone: R53HostedZone{Id: "/hostedzone/Z1", Name: "example.net."}})
	got = amplifyEvaluateDomainVerification(pending)
	if got.Domain.DomainStatus != AmplifyDomainStatusPendingVerification {
		t.Fatalf("zone without record: status %s", got.Domain.DomainStatus)
	}

	// Verification CNAME lands in the covering zone → AVAILABLE, subdomains
	// verified, settled state persisted.
	zone, _ := r53Zones.Get("Z1")
	zone.Records = append(zone.Records, R53ResourceRecordSet{
		Name: name,
		Type: "CNAME",
		ResourceRecords: &R53ResourceRecords{
			Items: []R53ResourceRecord{{Value: value}},
		},
	})
	r53Zones.Put("Z1", zone)
	got = amplifyEvaluateDomainVerification(pending)
	if got.Domain.DomainStatus != AmplifyDomainStatusAvailable {
		t.Fatalf("record present: status %s", got.Domain.DomainStatus)
	}
	if got.Domain.UpdateStatus != AmplifyDomainUpdateComplete {
		t.Fatalf("updateStatus %s", got.Domain.UpdateStatus)
	}
	if !got.Domain.SubDomains[0].Verified {
		t.Fatal("subdomains must verify with the association")
	}
	persisted, _ := amplifyDomains.Get(amplifyDomainKey("dverify", "shop.example.net"))
	if persisted.Domain.DomainStatus != AmplifyDomainStatusAvailable {
		t.Fatal("settled state must persist")
	}

	// CUSTOM certificates have no DNS challenge: they settle immediately.
	custom := amplifyStoredDomain{
		AppId: "dverify",
		Domain: AmplifyDomainAssociation{
			DomainName:   "custom.example.org",
			DomainStatus: AmplifyDomainStatusPendingVerification,
			Certificate:  &AmplifyCertificate{Type: "CUSTOM", CustomCertificateArn: "arn:aws:acm:us-east-1:123456789012:certificate/x"},
		},
	}
	amplifyDomains.Put(amplifyDomainKey("dverify", "custom.example.org"), custom)
	if got := amplifyEvaluateDomainVerification(custom); got.Domain.DomainStatus != AmplifyDomainStatusAvailable {
		t.Fatalf("custom certificate: status %s", got.Domain.DomainStatus)
	}
}

func TestAmplifyHostingCorruptStoredManifestServesNothing(t *testing.T) {
	amplifyResetHostingState()
	files := map[string]string{
		"deploy-manifest.json": `{"version": 2, "routes": []}`,
		"index.html":           "<html>root content</html>",
	}

	// A manifest-consuming platform whose stored manifest no longer parses
	// has no servable content — never the SSR bundle served statically.
	// (New deployments with an invalid manifest fail at deploy time; this
	// covers corrupt stored state.)
	amplifySeedApp("badmanifest1", "main")
	amplifyApps.Update("badmanifest1", func(a *amplifyStoredApp) { a.App.Platform = "WEB_COMPUTE" })
	amplifySeedDeployment(t, "badmanifest1", "main", "job-badman-1", files)
	rec := amplifyHostingGet(t, "main.badmanifest1.amplifyapp.com", "/", nil)
	if rec.Code != http.StatusNotFound {
		t.Errorf("WEB_COMPUTE corrupt manifest: status %d body %q", rec.Code, rec.Body.String())
	}

	// On a static platform the same file is ordinary site content.
	amplifySeedApp("okmanifest1", "main")
	amplifySeedDeployment(t, "okmanifest1", "main", "job-okman-1", files)
	rec = amplifyHostingGet(t, "main.okmanifest1.amplifyapp.com", "/", nil)
	if rec.Code != http.StatusOK || rec.Body.String() != "<html>root content</html>" {
		t.Errorf("WEB static platform: status %d body %q", rec.Code, rec.Body.String())
	}
}
