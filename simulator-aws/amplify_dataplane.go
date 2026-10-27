package main

import (
	"crypto/md5"
	"crypto/subtle"
	"encoding/hex"
	"fmt"
	"log"
	"mime"
	"net"
	"net/http"
	"path"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/e6qu/sockerless-cloud/sim"
	"github.com/e6qu/sockerless-cloud/sim/archive"
)

// The AWS Amplify Hosting data plane serves each branch's latest successful
// deployment on {branch}.{appId}.amplifyapp.com, on the app's
// {hash}.cloudfront.net host (Amplify's distribution is internal, so no
// CloudFront control-plane object backs it) and on verified custom domains.
//
// Register it as a WrapHandler after the spec validator so hosting traffic
// never reaches a control-plane operation.
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

// Derive the hostname deterministically so the advertised CNAME target and
// the host matcher agree.
func amplifyCloudFrontDomain(appID string) string {
	hash := md5.Sum([]byte("amplify-distribution/" + appID))
	return "d" + hex.EncodeToString(hash[:])[:13] + ".cloudfront.net"
}

func amplifyHostingTarget(host string) (appID, branch string, ok bool) {
	hostname := host
	if h, _, err := net.SplitHostPort(host); err == nil {
		hostname = h
	}
	hostname = strings.TrimSuffix(strings.ToLower(hostname), ".")

	if rest, found := strings.CutSuffix(hostname, ".amplifyapp.com"); found {
		// Branch names never contain dots, so the last label is the appId.
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
			// The distribution host names no branch: serve the production branch, or the
			// app's only branch.
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

	// Only verified subdomains of an AVAILABLE association serve. Index by
	// declared hostname so a request evaluates verification for one row: this is
	// the fall-through for every hostname the simulator serves.
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

// The hosting wrapper consults these on every request into the simulator.
var (
	amplifyAppsByDistribution sim.GenerationIndex[amplifyStoredApp]
	amplifyDomainsBySubdomain sim.GenerationIndex[amplifyStoredDomain]

	// Every hosted request resolves the branch's newest successful job, then that
	// job's files.
	amplifySucceededJobsByBranch sim.GenerationIndex[amplifyStoredJob]
	amplifyHostedArtifactsByJob  sim.GenerationIndex[amplifyStoredArtifact]
)

func amplifyBranchKey(appID, branch string) string { return appID + "\x00" + branch }

func amplifyJobKey(appID, branch, jobID string) string {
	return appID + "\x00" + branch + "\x00" + jobID
}

// amplifyDomainSubdomainHosts returns hosts before verification; the caller
// decides whether the matched one serves.
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

type amplifyHostedContent struct {
	JobID string
	// Files keys are slash-separated paths without a leading slash.
	Files map[string][]byte
	// Manifest is set for a bundle carrying deploy-manifest.json.
	Manifest *amplifyDeployManifest
}

var (
	amplifyHostingMu    sync.Mutex
	amplifyHostingCache = map[string]*amplifyHostedContent{}
)

// amplifyActiveContent returns nil when the branch has nothing to serve, which
// AWS Amplify answers with 404.
func amplifyActiveContent(appID, branch string) (*amplifyHostedContent, error) {
	job, ok := amplifyLatestSucceededJob(appID, branch)
	if !ok {
		amplifyInvalidateHostingCache(appID, branch)
		return nil, nil
	}
	cacheKey := appID + "/" + branch

	amplifyHostingMu.Lock()
	cached := amplifyHostingCache[cacheKey]
	amplifyHostingMu.Unlock()
	if cached != nil && cached.JobID == job.Job.Summary.JobId {
		return cached, nil
	}

	content, err := amplifyUnpackJobArtifacts(appID, branch, job.Job.Summary.JobId)
	if err != nil {
		amplifyInvalidateHostingCache(appID, branch)
		return nil, err
	}
	amplifyHostingMu.Lock()
	if content == nil {
		delete(amplifyHostingCache, cacheKey)
	} else {
		amplifyHostingCache[cacheKey] = content
	}
	amplifyHostingMu.Unlock()
	return content, nil
}

func amplifyInvalidateHostingCache(appID, branch string) {
	amplifyHostingMu.Lock()
	delete(amplifyHostingCache, appID+"/"+branch)
	amplifyHostingMu.Unlock()
}

func amplifyLatestSucceededJob(appID, branch string) (amplifyStoredJob, bool) {
	// Index only succeeded jobs; a status change writes the row, and the write
	// invalidates the index.
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

// amplifyDeploymentMaxBytes bounds a deployment the simulator fetches or unpacks.
const amplifyDeploymentMaxBytes = 1 << 30

// amplifyJobArtifactFiles expands a single zip artifact and uses a fileMap
// deployment directly. Test outputs are separate artifact roles and never
// site content.
func amplifyJobArtifactFiles(appID, branch, jobID string) (map[string][]byte, error) {
	// Index only hosted content.
	stored := amplifyHostedArtifactsByJob.LookupAll(amplifyArtifacts, amplifyJobKey(appID, branch, jobID),
		func(a amplifyStoredArtifact) []string {
			if !a.HostedContent {
				return nil
			}
			return []string{amplifyJobKey(a.AppId, a.BranchName, a.JobId)}
		})
	if len(stored) == 0 {
		return nil, nil
	}
	bucket := amplifyArtifactBucketName()
	read := func(a amplifyStoredArtifact) ([]byte, error) {
		obj, ok := s3Objects.Get(s3ObjectKey(bucket, a.Key))
		if !ok {
			return nil, fmt.Errorf("artifact %s of job %s: object %s is missing", a.Artifact.ArtifactFileName, jobID, a.Key)
		}
		data, err := s3ObjectData(obj)
		if err != nil {
			return nil, fmt.Errorf("artifact %s of job %s: %w", a.Artifact.ArtifactFileName, jobID, err)
		}
		return data, nil
	}
	files := map[string][]byte{}
	if len(stored) == 1 && strings.HasSuffix(stored[0].Artifact.ArtifactFileName, ".zip") {
		data, err := read(stored[0])
		if err != nil {
			return nil, err
		}
		if err := archive.ReadZip(data, amplifyDeploymentMaxBytes, func(f archive.File) error {
			files[f.Name] = f.Data
			return nil
		}); err != nil {
			return nil, fmt.Errorf("artifact %s of job %s: %w", stored[0].Artifact.ArtifactFileName, jobID, err)
		}
		return files, nil
	}
	for _, a := range stored {
		data, err := read(a)
		if err != nil {
			return nil, err
		}
		files[path.Clean(strings.TrimPrefix(a.Artifact.ArtifactFileName, "/"))] = data
	}
	return files, nil
}

func amplifyPlatformUsesManifest(platform string) bool {
	return platform == "WEB_COMPUTE" || platform == "WEB_DYNAMIC"
}

// A manifest-consuming platform validates its manifest when the job settles,
// so a stored manifest that no longer parses is corrupt state and serves
// nothing. On a static platform deploy-manifest.json is ordinary content.
func amplifyUnpackJobArtifacts(appID, branch, jobID string) (*amplifyHostedContent, error) {
	files, err := amplifyJobArtifactFiles(appID, branch, jobID)
	if err != nil || len(files) == 0 {
		return nil, err
	}
	content := &amplifyHostedContent{JobID: jobID, Files: files}
	if manifestData, ok := files["deploy-manifest.json"]; ok {
		manifest, err := amplifyParseDeployManifest(manifestData)
		if err != nil {
			if stored, ok := amplifyApps.Get(appID); ok && amplifyPlatformUsesManifest(stored.App.Platform) {
				return nil, nil
			}
		} else {
			content.Manifest = manifest
		}
	}
	return content, nil
}

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
	content, err := amplifyActiveContent(appID, branch)
	if err != nil {
		log.Printf("amplify: serve %s/%s: %v", appID, branch, err)
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}
	if content == nil {
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

// AWS Amplify stores basicAuthCredentials as base64("user:password"), the
// Basic payload verbatim. Branch settings override the app default.
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

// amplifyServeStatic tries the exact file, then the directory's index.html,
// then the custom rules in order, and otherwise answers 404.
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
			// AWS Amplify documents 301 as the default rule status.
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
			// For 404-200 the requested file is already known missing: the SPA fallback.
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

// amplifyMatchCustomRule supports exact sources and the `<*>`, `/<*>` and
// `/prefix/<*>` wildcards, whose capture substitutes into the target's `<*>`.
// A rule with a country condition never matches: the simulator has no geo
// context, and applying it to everyone would be wrong.
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
