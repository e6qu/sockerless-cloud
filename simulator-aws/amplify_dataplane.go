package main

import (
	"archive/zip"
	"bytes"
	"crypto/md5"
	"crypto/subtle"
	"encoding/hex"
	"io"
	"mime"
	"net"
	"net/http"
	"path"
	"sort"
	"strconv"
	"strings"
	"sync"

	sim "github.com/e6qu/sockerless-cloud/simulator-aws/shared"
)

// Amplify Hosting data plane. Serves each branch's ACTIVE deployment — the
// latest SUCCEED job's artifact content — on the hosts Amplify advertises:
//
//   - {branch}.{appId}.amplifyapp.com        (defaultDomain children)
//   - {hash}.cloudfront.net                  (the per-app distribution host
//     the subdomain dnsRecords CNAME-target; deterministic per app, no
//     CloudFront control-plane object — real Amplify's distribution is
//     internal and never customer-visible)
//   - verified custom domains                (subdomain prefix + domainName,
//     only when the association/subdomain is verified — amplify_domains.go)
//
// Registered as a WrapHandler AFTER the spec validator so hosting traffic
// is intercepted before mux routing and never misattributed to a
// Smithy-modeled control-plane operation (same arming order as the ELBv2
// data plane; see buildSimulator).
func registerAmplifyDataPlane(srv *sim.Server) {
	srv.WrapHandler(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if appID, branch, ok := amplifyHostingTarget(r.Host); ok {
				handleAmplifyHosting(w, r, appID, branch)
				return
			}
			next.ServeHTTP(w, r)
		})
	})
}

// amplifyCloudFrontDomain is the per-app cloudfront.net hostname the domain
// association subdomain dnsRecords advertise. Deterministic per app so the
// advertised CNAME target and the host matcher always agree.
func amplifyCloudFrontDomain(appID string) string {
	hash := md5.Sum([]byte("amplify-distribution/" + appID))
	return "d" + hex.EncodeToString(hash[:])[:13] + ".cloudfront.net"
}

// amplifyHostingTarget resolves a request Host to the app + branch whose
// active deployment it serves.
func amplifyHostingTarget(host string) (appID, branch string, ok bool) {
	hostname := host
	if h, _, err := net.SplitHostPort(host); err == nil {
		hostname = h
	}
	hostname = strings.TrimSuffix(strings.ToLower(hostname), ".")

	if rest, found := strings.CutSuffix(hostname, ".amplifyapp.com"); found {
		// {branch}.{appId}: the branch label is everything before the appId
		// label (branch names themselves never contain dots).
		idx := strings.LastIndex(rest, ".")
		if idx <= 0 {
			return "", "", false
		}
		branchLabel, appLabel := rest[:idx], rest[idx+1:]
		stored, found := amplifyApps.Get(appLabel)
		if !found {
			return "", "", false
		}
		if _, found := stored.Branches[branchLabel]; !found {
			return "", "", false
		}
		return appLabel, branchLabel, true
	}

	if strings.HasSuffix(hostname, ".cloudfront.net") {
		if stored, found := amplifyAppsByDistribution.Lookup(amplifyApps, hostname,
			func(a amplifyStoredApp) []string {
				return []string{amplifyCloudFrontDomain(a.App.AppId)}
			}); found {
			// The distribution host carries no subdomain prefix to pick a
			// branch from; serve the production branch, or the app's only
			// branch.
			if pb := stored.App.ProductionBranch; pb != nil && pb.BranchName != "" {
				return stored.App.AppId, pb.BranchName, true
			}
			if len(stored.Branches) == 1 {
				for name := range stored.Branches {
					return stored.App.AppId, name, true
				}
			}
			return "", "", false
		}
		return "", "", false
	}

	// Custom domains: only verified subdomains of an AVAILABLE association
	// serve traffic. Verification is evaluated read-time against the sim's
	// own Route 53 (amplify_domains.go), so the index keys on the hostnames a
	// stored association declares and verification is evaluated for the one
	// row a request matches. Reading every association here — and evaluating
	// each one's verification, which reads the Route 53 zone store in turn —
	// was the cost every request that is not for Amplify paid, this being the
	// fall-through for every other hostname the simulator serves.
	for _, stored := range amplifyDomainsBySubdomain.LookupAll(amplifyDomains, hostname,
		amplifyDomainSubdomainHosts) {
		stored = amplifyEvaluateDomainVerification(stored)
		if stored.Domain.DomainStatus != AmplifyDomainStatusAvailable {
			continue
		}
		for _, sub := range stored.Domain.SubDomains {
			if !sub.Verified {
				continue
			}
			subHost := stored.Domain.DomainName
			if sub.SubDomainSetting.Prefix != "" {
				subHost = sub.SubDomainSetting.Prefix + "." + stored.Domain.DomainName
			}
			if strings.EqualFold(subHost, hostname) {
				return stored.AppId, sub.SubDomainSetting.BranchName, true
			}
		}
	}
	return "", "", false
}

// The Amplify hosting wrapper decides whether to claim a request, so both of
// these ran for every request into the simulator before any handler did.
var (
	amplifyAppsByDistribution sim.GenerationIndex[amplifyStoredApp]
	amplifyDomainsBySubdomain sim.GenerationIndex[amplifyStoredDomain]

	// A served request resolves the branch's newest successful job and then
	// that job's hosted files, so both of these ran once per hosted request
	// over every job and artifact the simulator held, for every application.
	amplifySucceededJobsByBranch sim.GenerationIndex[amplifyStoredJob]
	amplifyHostedArtifactsByJob  sim.GenerationIndex[amplifyStoredArtifact]
)

// amplifyBranchKey names the branch a job or artifact belongs to.
func amplifyBranchKey(appID, branch string) string { return appID + "\x00" + branch }

// amplifyJobKey names the job an artifact belongs to.
func amplifyJobKey(appID, branch, jobID string) string {
	return appID + "\x00" + branch + "\x00" + jobID
}

// amplifyDomainSubdomainHosts returns every hostname an association declares,
// before verification is evaluated: the index selects candidates and the caller
// decides whether the one it matched actually serves.
func amplifyDomainSubdomainHosts(stored amplifyStoredDomain) []string {
	hosts := make([]string, 0, len(stored.Domain.SubDomains))
	for _, sub := range stored.Domain.SubDomains {
		host := stored.Domain.DomainName
		if sub.SubDomainSetting.Prefix != "" {
			host = sub.SubDomainSetting.Prefix + "." + stored.Domain.DomainName
		}
		hosts = append(hosts, strings.ToLower(host))
	}
	return hosts
}

// ---------- active deployment content ----------

// amplifyHostedContent is one branch's unpacked active deployment.
type amplifyHostedContent struct {
	JobID string
	// Files maps slash-separated paths (no leading slash) to bytes.
	Files map[string][]byte
	// Manifest is non-nil when the bundle root carries deploy-manifest.json
	// (the Amplify Hosting deployment specification; SSR bundles).
	Manifest *amplifyDeployManifest
}

var (
	amplifyHostingMu    sync.Mutex
	amplifyHostingCache = map[string]*amplifyHostedContent{} // appID/branch → unpacked content
)

// amplifyActiveContent resolves and caches a branch's active deployment:
// the latest SUCCEED job's artifacts, unzipped (zip deploys/builds) or as a
// direct file map (fileMap deploys). Returns nil when the branch has no
// servable content — the real "no content yet" experience is a 404.
func amplifyActiveContent(appID, branch string) *amplifyHostedContent {
	job, ok := amplifyLatestSucceededJob(appID, branch)
	if !ok {
		amplifyInvalidateHostingCache(appID, branch)
		return nil
	}
	cacheKey := appID + "/" + branch

	amplifyHostingMu.Lock()
	cached := amplifyHostingCache[cacheKey]
	amplifyHostingMu.Unlock()
	if cached != nil && cached.JobID == job.Job.Summary.JobId {
		return cached
	}

	content := amplifyUnpackJobArtifacts(appID, branch, job.Job.Summary.JobId)
	amplifyHostingMu.Lock()
	if content == nil {
		delete(amplifyHostingCache, cacheKey)
	} else {
		amplifyHostingCache[cacheKey] = content
	}
	amplifyHostingMu.Unlock()
	return content
}

func amplifyInvalidateHostingCache(appID, branch string) {
	amplifyHostingMu.Lock()
	delete(amplifyHostingCache, appID+"/"+branch)
	amplifyHostingMu.Unlock()
}

func amplifyLatestSucceededJob(appID, branch string) (amplifyStoredJob, bool) {
	// Only a succeeded job can serve, so only those are indexed; a job that
	// reaches or leaves that status writes its row, and the write moves the
	// store's generation, which is what discards the index.
	jobs := amplifySucceededJobsByBranch.LookupAll(amplifyJobs, amplifyBranchKey(appID, branch),
		func(j amplifyStoredJob) []string {
			if j.Job.Summary.Status != AmplifyJobStatusSucceed {
				return nil
			}
			return []string{amplifyBranchKey(j.AppId, j.BranchName)}
		})
	if len(jobs) == 0 {
		return amplifyStoredJob{}, false
	}
	jobs = append([]amplifyStoredJob(nil), jobs...)
	sort.Slice(jobs, func(i, k int) bool {
		si, sk := jobs[i].Job.Summary, jobs[k].Job.Summary
		if si.StartTime != sk.StartTime {
			return si.StartTime > sk.StartTime
		}
		return si.JobId > sk.JobId
	})
	return jobs[0], true
}

// amplifyJobArtifactFiles assembles a job's hosted-content file map: a
// single zip artifact (build output / zip deployment) is expanded, a
// multi-file artifact set (fileMap deployment) is used directly. End-to-end
// test outputs and their metadata are separate Amplify artifact roles and
// are never published as site content.
func amplifyJobArtifactFiles(appID, branch, jobID string) map[string][]byte {
	// Only hosted content is published, so only those rows are indexed.
	stored := amplifyHostedArtifactsByJob.LookupAll(amplifyArtifacts, amplifyJobKey(appID, branch, jobID),
		func(a amplifyStoredArtifact) []string {
			if !a.HostedContent {
				return nil
			}
			return []string{amplifyJobKey(a.AppId, a.BranchName, a.JobId)}
		})
	if len(stored) == 0 {
		return nil
	}
	bucket := amplifyArtifactBucketName()
	files := map[string][]byte{}
	if len(stored) == 1 && strings.HasSuffix(stored[0].Artifact.ArtifactFileName, ".zip") {
		obj, ok := s3Objects.Get(s3ObjectKey(bucket, stored[0].Key))
		if !ok {
			return nil
		}
		zr, err := zip.NewReader(bytes.NewReader(obj.Data), int64(len(obj.Data)))
		if err != nil {
			return nil
		}
		for _, f := range zr.File {
			if f.FileInfo().IsDir() {
				continue
			}
			rc, err := f.Open()
			if err != nil {
				return nil
			}
			data, err := io.ReadAll(rc)
			_ = rc.Close()
			if err != nil {
				return nil
			}
			files[path.Clean(strings.TrimPrefix(f.Name, "/"))] = data
		}
	} else {
		for _, a := range stored {
			obj, ok := s3Objects.Get(s3ObjectKey(bucket, a.Key))
			if !ok {
				continue
			}
			files[path.Clean(strings.TrimPrefix(a.Artifact.ArtifactFileName, "/"))] = obj.Data
		}
	}
	return files
}

// amplifyPlatformUsesManifest reports whether the app platform consumes the
// deployment specification's deploy-manifest.json.
func amplifyPlatformUsesManifest(platform string) bool {
	return platform == "WEB_COMPUTE" || platform == "WEB_DYNAMIC"
}

// amplifyUnpackJobArtifacts builds a branch's servable deployment content.
// Deployments to manifest-consuming platforms are validated when the job
// settles, so a stored deployment whose manifest no longer parses is corrupt
// state — it yields no servable content rather than silently serving the SSR
// bundle as a static site. On static platforms a deploy-manifest.json is
// ordinary site content.
func amplifyUnpackJobArtifacts(appID, branch, jobID string) *amplifyHostedContent {
	files := amplifyJobArtifactFiles(appID, branch, jobID)
	if len(files) == 0 {
		return nil
	}
	content := &amplifyHostedContent{JobID: jobID, Files: files}
	if manifestData, ok := files["deploy-manifest.json"]; ok {
		manifest, err := amplifyParseDeployManifest(manifestData)
		if err != nil {
			if stored, ok := amplifyApps.Get(appID); ok && amplifyPlatformUsesManifest(stored.App.Platform) {
				return nil
			}
		} else {
			content.Manifest = manifest
		}
	}
	return content
}

// ---------- request serving ----------

func handleAmplifyHosting(w http.ResponseWriter, r *http.Request, appID, branch string) {
	stored, ok := amplifyApps.Get(appID)
	if !ok {
		http.NotFound(w, r)
		return
	}
	br, ok := stored.Branches[branch]
	if !ok {
		http.NotFound(w, r)
		return
	}
	if !wafAssociatedRequestAllowed(stored.App.AppArn, r) {
		http.Error(w, http.StatusText(http.StatusForbidden), http.StatusForbidden)
		return
	}
	if !amplifyBasicAuthOK(stored.App, br, r) {
		w.Header().Set("WWW-Authenticate", `Basic realm="Restricted"`)
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}
	content := amplifyActiveContent(appID, branch)
	if content == nil {
		// No active deployment: the real no-content experience.
		http.NotFound(w, r)
		return
	}
	platform := stored.App.Platform
	if content.Manifest != nil && (platform == "WEB_COMPUTE" || platform == "WEB_DYNAMIC") {
		amplifyServeManifestRoutes(w, r, stored.App, br, content)
		return
	}
	amplifyServeStatic(w, r, stored.App.CustomRules, content)
}

// amplifyBasicAuthOK enforces enableBasicAuth (branch-level settings
// override the app default). basicAuthCredentials is the wire format real
// Amplify stores: base64("user:password") — the Authorization header's
// Basic payload is compared against it directly.
func amplifyBasicAuthOK(app AmplifyApp, br AmplifyBranch, r *http.Request) bool {
	enabled := app.EnableBasicAuth || br.EnableBasicAuth
	if !enabled {
		return true
	}
	want := br.BasicAuthCredentials
	if want == "" {
		want = app.BasicAuthCredentials
	}
	if want == "" {
		return false
	}
	auth := r.Header.Get("Authorization")
	got, found := strings.CutPrefix(auth, "Basic ")
	if !found {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(strings.TrimSpace(got)), []byte(want)) == 1
}

// amplifyServeStatic serves a request from the unpacked deployment: exact
// file → 200; directory → index.html; then the app's custom rules in order;
// default 404.
func amplifyServeStatic(w http.ResponseWriter, r *http.Request, rules []AmplifyCustomRule, content *amplifyHostedContent) {
	reqPath := r.URL.Path
	if key, ok := amplifyResolveFile(content.Files, reqPath); ok {
		amplifyWriteFile(w, key, content.Files[key], http.StatusOK)
		return
	}
	for _, rule := range rules {
		target, ok := amplifyMatchCustomRule(rule, reqPath)
		if !ok {
			continue
		}
		status := rule.Status
		if status == "" {
			// Documented default when a rule carries no status.
			status = "301"
		}
		switch status {
		case "301", "302":
			code := http.StatusMovedPermanently
			if status == "302" {
				code = http.StatusFound
			}
			http.Redirect(w, r, target, code)
			return
		case "200", "404-200":
			// Rewrite; for 404-200 the requested file is already known to
			// be missing (the SPA fallback case).
			if key, ok := amplifyResolveFile(content.Files, target); ok {
				amplifyWriteFile(w, key, content.Files[key], http.StatusOK)
				return
			}
		case "404":
			if key, ok := amplifyResolveFile(content.Files, target); ok {
				amplifyWriteFile(w, key, content.Files[key], http.StatusNotFound)
				return
			}
			http.NotFound(w, r)
			return
		}
	}
	http.NotFound(w, r)
}

// amplifyResolveFile maps a request path onto the deployment's file map:
// exact file, or the directory's index.html.
func amplifyResolveFile(files map[string][]byte, reqPath string) (string, bool) {
	cleaned := path.Clean("/" + reqPath)
	key := strings.TrimPrefix(cleaned, "/")
	if key == "" || key == "." {
		key = "index.html"
	}
	if _, ok := files[key]; ok {
		return key, true
	}
	if indexKey := key + "/index.html"; files[indexKey] != nil {
		return indexKey, true
	}
	return "", false
}

// amplifyMatchCustomRule matches one custom rule against a request path.
// Supported source forms: exact paths, and the documented wildcard forms
// `<*>` / `/<*>` / `/prefix/<*>` (the wildcard captures the path remainder
// and substitutes into the target's `<*>`). Rules with a condition (country
// code) never match: the sim has no geo context for the request, and
// silently applying a geo-restricted rule to everyone would be wrong.
func amplifyMatchCustomRule(rule AmplifyCustomRule, reqPath string) (string, bool) {
	if rule.Condition != "" {
		return "", false
	}
	source := rule.Source
	if idx := strings.Index(source, "<*>"); idx >= 0 {
		prefix := source[:idx]
		// The bare `<*>` form has no leading slash; request paths do.
		if prefix != "" && !strings.HasPrefix(reqPath, prefix) {
			return "", false
		}
		capture := strings.TrimPrefix(reqPath, prefix)
		return strings.ReplaceAll(rule.Target, "<*>", capture), true
	}
	if source == reqPath {
		return rule.Target, true
	}
	return "", false
}

func amplifyWriteFile(w http.ResponseWriter, key string, data []byte, status int) {
	contentType := mime.TypeByExtension(path.Ext(key))
	if contentType == "" {
		contentType = http.DetectContentType(data)
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Content-Length", strconv.Itoa(len(data)))
	w.WriteHeader(status)
	_, _ = w.Write(data)
}
