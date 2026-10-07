package main

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
	"github.com/e6qu/sockerless-cloud/sim/archive"
	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
)

// web_sourcecontrol.go serves a site's source control (sourcecontrols/web)
// and WebApps_SyncRepository. Configuring a repository and branch, and each
// sync, has the site's Kudu fetch the branch's head and deploy its tree into
// wwwroot, recording the deployment under the commit's ID with the commit's
// author and message, as Kudu records a repository deployment.

// webSourceControlFetchTimeout bounds one fetch of a site's repository.
const webSourceControlFetchTimeout = 10 * time.Minute

func registerWebSourceControl(both func(string, string, http.HandlerFunc)) {
	scID := func(r *http.Request) string { return webResourceID(r) + "/sourcecontrols/web" }
	get := func(w http.ResponseWriter, r *http.Request) {
		if webMissing(w, r) {
			return
		}
		sc, ok := webSourceControls.Get(scID(r))
		if !ok {
			AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
				"No source control configured for %q.", sim.PathParam(r, "siteName"))
			return
		}
		sim.WriteJSON(w, http.StatusOK, sc)
	}
	write := func(w http.ResponseWriter, r *http.Request) {
		if webMissing(w, r) {
			return
		}
		var req WebSourceControl
		if r.Method == http.MethodPatch {
			req, _ = webSourceControls.Get(scID(r))
		}
		if err := sim.ReadJSON(r, &req); err != nil {
			AzureError(w, "InvalidRequestContent", err.Error(), http.StatusBadRequest)
			return
		}
		if strings.TrimSpace(req.Properties.RepoURL) == "" {
			AzureError(w, "BadRequest", "The source control configuration must name a repoUrl.", http.StatusBadRequest)
			return
		}
		if req.Properties.Branch == "" {
			req.Properties.Branch = "master"
		}
		req.ID = scID(r)
		req.Name = "web"
		req.Type = "Microsoft.Web/sites/sourcecontrols"
		site, _ := webResource(r)
		if !kuduTryLock(site.ID) {
			kuduConflict(w)
			return
		}
		defer kuduUnlock(site.ID)
		webSourceControls.Put(req.ID, req)
		site = webSetScmType(r, webSourceControlScmType(req.Properties))
		if !boolValue(req.Properties.IsGitHubAction) {
			webDeployFromRepository(&site, req.Properties)
		}
		sim.WriteJSON(w, http.StatusOK, req)
	}
	both("GET", "/sourcecontrols/web", get)
	both("PUT", "/sourcecontrols/web", write)
	both("PATCH", "/sourcecontrols/web", write)
	both("DELETE", "/sourcecontrols/web", func(w http.ResponseWriter, r *http.Request) {
		if webMissing(w, r) {
			return
		}
		webSourceControls.Delete(scID(r))
		webSetScmType(r, "None")
		w.WriteHeader(http.StatusOK)
	})

	// WebApps_SyncRepository redeploys the configured branch's current head.
	both("POST", "/sync", func(w http.ResponseWriter, r *http.Request) {
		if webMissing(w, r) {
			return
		}
		sc, ok := webSourceControls.Get(scID(r))
		if !ok || boolValue(sc.Properties.IsGitHubAction) {
			AzureErrorf(w, "BadRequest", http.StatusBadRequest,
				"The site %q has no repository to sync: configure source control with a repoUrl first.", sim.PathParam(r, "siteName"))
			return
		}
		site, _ := webResource(r)
		if !kuduTryLock(site.ID) {
			kuduConflict(w)
			return
		}
		defer kuduUnlock(site.ID)
		webDeployFromRepository(&site, sc.Properties)
		w.WriteHeader(http.StatusOK)
	})
}

func kuduConflict(w http.ResponseWriter) {
	AzureError(w, "Conflict", "There is a deployment currently in progress. Please try again when it completes.", http.StatusConflict)
}

func boolValue(b *bool) bool { return b != nil && *b }

// webKeepScmType keeps a site's scmType across a configuration write that
// does not name one: a new site has none, and source control sets it.
func webKeepScmType(cfg *SiteConfig, prev *Site) {
	if cfg.ScmType != "" {
		return
	}
	cfg.ScmType = "None"
	if prev != nil && prev.Properties.SiteConfig != nil && prev.Properties.SiteConfig.ScmType != "" {
		cfg.ScmType = prev.Properties.SiteConfig.ScmType
	}
}

// webSetScmType records the site's source-control kind in its configuration
// and returns the updated site.
func webSetScmType(r *http.Request, scmType string) Site {
	store := webResourceStore(r)
	site, _ := store.Get(webResourceID(r))
	if site.Properties.SiteConfig == nil {
		site.Properties.SiteConfig = &SiteConfig{}
	}
	site.Properties.SiteConfig.ScmType = scmType
	store.Put(site.ID, site)
	return site
}

// webSourceControlScmType is the ScmType App Service records for a source
// control configuration: GitHub Actions, a continuously integrated GitHub or
// Bitbucket repository, or an external Git or Mercurial repository.
func webSourceControlScmType(p WebSourceControlProperties) string {
	if boolValue(p.IsGitHubAction) {
		return "GitHubAction"
	}
	hg := boolValue(p.IsMercurial)
	if !boolValue(p.IsManualIntegration) {
		switch webRepositoryHost(p.RepoURL) {
		case "github.com":
			return "GitHub"
		case "bitbucket.org":
			if hg {
				return "BitbucketHg"
			}
			return "BitbucketGit"
		}
	}
	if hg {
		return "ExternalHg"
	}
	return "ExternalGit"
}

func webRepositoryHost(repoURL string) string {
	u, err := url.Parse(strings.TrimSpace(repoURL))
	if err != nil {
		return ""
	}
	return strings.ToLower(u.Hostname())
}

// webRepositoryDeployer is the deployer Kudu records a repository deployment
// under.
func webRepositoryDeployer(repoURL string) string {
	switch webRepositoryHost(repoURL) {
	case "github.com":
		return "GitHub"
	case "bitbucket.org":
		return "Bitbucket"
	}
	return "External Git"
}

// webDeployFromRepository fetches the head of the configured branch and
// deploys its tree into the site's wwwroot with Kudu's sync semantics,
// recording the Kudu deployment. A fetch that fails is recorded as a failed
// deployment of the fetch. The caller holds the site's deployment lock.
func webDeployFromRepository(site *Site, p WebSourceControlProperties) {
	now := time.Now()
	deployer := webRepositoryDeployer(p.RepoURL)
	fetchLog := []WebKuduLogEntry{}
	logf := func(typ int, format string, args ...any) {
		fetchLog = append(fetchLog, WebKuduLogEntry{
			ID: sim.NewUUID(), LogTime: kuduTime(time.Now()), Type: typ, Message: fmt.Sprintf(format, args...),
		})
	}
	logf(kuduLogMessage, "Fetching changes.")
	commit, files, err := webFetchRepository(p, logf)
	rec := WebKuduDeployment{
		SiteID:       site.ID,
		Status:       kuduStatusBuilding,
		StatusText:   "Building and Deploying",
		Deployer:     deployer,
		ReceivedTime: kuduTime(now),
		StartTime:    kuduTime(now),
		Log:          fetchLog,
	}
	if err != nil {
		rec.DeploymentID = sim.NewUUID()
		rec.ID = site.ID + "/deployments/" + rec.DeploymentID
		rec.Author, rec.AuthorEmail = "N/A", "N/A"
		rec.Message = "Fetch from " + p.RepoURL
		webFailRepositoryDeployment(rec, err)
		return
	}
	rec.DeploymentID = commit.Hash.String()
	rec.ID = site.ID + "/deployments/" + rec.DeploymentID
	rec.Author = commit.Author.Name
	rec.AuthorEmail = commit.Author.Email
	rec.Message = strings.TrimSpace(commit.Message)
	rec.logf(kuduLogMessage, "Preparing deployment for commit id '%s'.", rec.DeploymentID[:10])
	kuduSaveDeployment(rec)

	written, err := webDeployFiles(site, files, webArtifact{
		Type: "zip", Target: webWWWRoot, Restart: true, SyncManifest: true,
	})
	if err != nil {
		webFailRepositoryDeployment(rec, err)
		return
	}
	for _, other := range kuduSiteDeployments(site.ID) {
		if other.Active && other.ID != rec.ID {
			kuduUpdateDeployment(other.ID, func(o *WebKuduDeployment) { o.Active = false })
		}
	}
	kuduUpdateDeployment(rec.ID, func(d *WebKuduDeployment) {
		d.Status = kuduStatusSuccess
		d.StatusText = ""
		d.Complete = true
		d.Active = true
		d.EndTime = kuduTime(time.Now())
		d.logf(kuduLogMessage, "Deployed %d file(s) of commit %s to %s.", written, d.DeploymentID, webWWWRoot)
		d.logf(kuduLogMessage, "Deployment successful.")
	})
}

func webFailRepositoryDeployment(rec WebKuduDeployment, err error) {
	rec.Status = kuduStatusFailed
	rec.StatusText = "Failed"
	rec.Complete = true
	rec.EndTime = kuduTime(time.Now())
	rec.logf(kuduLogError, "Deployment failed: %v", err)
	kuduSaveDeployment(rec)
}

// webFetchRepository clones the configured branch and returns its head
// commit and the files of its tree.
func webFetchRepository(p WebSourceControlProperties, logf func(int, string, ...any)) (*object.Commit, []archive.File, error) {
	if boolValue(p.IsMercurial) {
		return nil, nil, errors.New("fetching a Mercurial repository is not supported; configure a Git repository")
	}
	if u, err := url.Parse(p.RepoURL); err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		return nil, nil, fmt.Errorf("the repository URL %q is not an http or https URL", p.RepoURL)
	}
	dir, err := os.MkdirTemp("", "sockerless-azure-scm-*")
	if err != nil {
		return nil, nil, fmt.Errorf("create the fetch workspace: %w", err)
	}
	defer func() { _ = os.RemoveAll(dir) }()
	ctx, cancel := context.WithTimeout(context.Background(), webSourceControlFetchTimeout)
	defer cancel()
	logf(kuduLogMessage, "Updating branch '%s'.", p.Branch)
	repo, err := git.PlainCloneContext(ctx, dir, false, &git.CloneOptions{
		URL:               p.RepoURL,
		ReferenceName:     plumbing.NewBranchReferenceName(p.Branch),
		SingleBranch:      true,
		Depth:             1,
		RecurseSubmodules: git.DefaultSubmoduleRecursionDepth,
	})
	if err != nil {
		return nil, nil, fmt.Errorf("fetch branch '%s' of %s: %w", p.Branch, p.RepoURL, err)
	}
	logf(kuduLogMessage, "Updating submodules.")
	head, err := repo.Head()
	if err != nil {
		return nil, nil, fmt.Errorf("resolve the head of branch '%s': %w", p.Branch, err)
	}
	c, err := repo.CommitObject(head.Hash())
	if err != nil {
		return nil, nil, fmt.Errorf("read commit %s: %w", head.Hash(), err)
	}
	files, err := webRepositoryFiles(dir)
	if err != nil {
		return nil, nil, err
	}
	return c, files, nil
}

// webRepositoryFiles reads the regular files of a checked-out tree, leaving
// out the repository's own .git metadata as Kudu's sync does.
func webRepositoryFiles(dir string) ([]archive.File, error) {
	var files []archive.File
	var total int64
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && d.Name() == ".git" {
			return filepath.SkipDir
		}
		if !d.Type().IsRegular() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		total += info.Size()
		if total > webSiteContentLimit {
			return fmt.Errorf("the repository's tree exceeds %d bytes", int64(webSiteContentLimit))
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(dir, p)
		if err != nil {
			return err
		}
		files = append(files, archive.File{Name: filepath.ToSlash(rel), Mode: info.Mode().Perm(), Data: data})
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("read the repository's tree: %w", err)
	}
	return files, nil
}
