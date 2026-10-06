package main

import (
	"fmt"
	"io"
	"io/fs"
	"mime"
	"net/http"
	"os"
	"path"
	"sort"
	"strings"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
)

// web_kudu_vfs.go serves Kudu's virtual file system API on a site's SCM
// host: /api/vfs/{path} (and /vfs/{path}, the spelling the job log URLs use)
// reads, writes and deletes files and directories under the site's /home,
// the storage its containers mount. A directory is addressed with a trailing
// slash and reads as a JSON listing; a file reads as its content with an ETag
// a write or delete must match through If-Match. /home is the persistent
// share the site's containers mount, so the VFS and the running app see the
// same files.

// kuduVFSMaxUpload bounds one file written through the VFS.
const kuduVFSMaxUpload = webSiteContentLimit

// kuduVFSPath is the /home-relative path a VFS request addresses and whether
// the request named a directory (a trailing slash).
func kuduVFSPath(urlPath string) (rel string, dir bool, ok bool) {
	lower := strings.ToLower(urlPath)
	var rest string
	switch {
	case strings.HasPrefix(lower, "/api/vfs/") || lower == "/api/vfs":
		rest = urlPath[len("/api/vfs"):]
	case strings.HasPrefix(lower, "/vfs/") || lower == "/vfs":
		rest = urlPath[len("/vfs"):]
	default:
		return "", false, false
	}
	dir = rest == "" || strings.HasSuffix(rest, "/")
	rel = strings.TrimPrefix(path.Clean("/"+rest), "/")
	return rel, dir, true
}

// kuduVFSBaseURL is the URL of the VFS root, in the spelling the request used.
func kuduVFSBaseURL(r *http.Request) string {
	prefix := "/api/vfs/"
	if !strings.HasPrefix(strings.ToLower(r.URL.Path), "/api/") {
		prefix = "/vfs/"
	}
	return azureRequestScheme(r) + "://" + r.Host + prefix
}

// kuduWWWRootReadOnly reports whether a site's wwwroot is read-only: a
// run-from-package app's.
func kuduWWWRootReadOnly(site *Site) bool {
	return isPackageURL(strings.TrimSpace(siteAppSettings(site)["WEBSITE_RUN_FROM_PACKAGE"])) ||
		webSiteRunsFromDeployedPackage(site)
}

// kuduInWWWRoot reports whether a /home-relative path lies in site/wwwroot.
func kuduInWWWRoot(rel string) bool {
	return rel == "site/wwwroot" || strings.HasPrefix(rel, "site/wwwroot/")
}

// kuduOpenHome lays out the site's /home and returns its host directory.
func kuduOpenHome(site *Site) (string, error) {
	return ensureSiteHome(site.Name)
}

// kuduEtag is the ETag Kudu derives from a file's last write time: the .NET
// tick count in hex.
func kuduEtag(t time.Time) string {
	const unixEpochTicks = 621355968000000000
	ticks := unixEpochTicks + t.UTC().UnixNano()/100
	return fmt.Sprintf(`"%x"`, ticks)
}

// kuduVFSTime renders a time as Kudu serializes a DateTimeOffset.
func kuduVFSTime(t time.Time) string {
	return t.UTC().Format("2006-01-02T15:04:05.9999999-07:00")
}

// kuduMimeType is the media type Kudu reports for a file name.
func kuduMimeType(name string) string {
	if t := mime.TypeByExtension(path.Ext(name)); t != "" {
		mt, _, err := mime.ParseMediaType(t)
		if err == nil {
			return mt
		}
	}
	return "application/octet-stream"
}

func serveKuduVFS(w http.ResponseWriter, r *http.Request, site *Site, rel string, dir bool) {
	home, err := kuduOpenHome(site)
	if err != nil {
		kuduWebAPIError(w, http.StatusInternalServerError, err.Error())
		return
	}
	root, err := os.OpenRoot(home)
	if err != nil {
		kuduWebAPIError(w, http.StatusInternalServerError, err.Error())
		return
	}
	defer func() { _ = root.Close() }()
	name := rel
	if name == "" {
		name = "."
	}
	switch r.Method {
	case http.MethodGet, http.MethodHead:
		kuduVFSGet(w, r, root, name, rel, dir)
	case http.MethodPut:
		if kuduVFSRefuseReadOnly(w, site, rel) {
			return
		}
		commit := kuduVFSCommit(site, rel)
		if dir {
			kuduVFSPutDir(w, root, name, commit)
		} else {
			kuduVFSPutFile(w, r, root, name, commit)
		}
	case http.MethodDelete:
		if kuduVFSRefuseReadOnly(w, site, rel) {
			return
		}
		if rel == "" || rel == "site" || rel == "site/wwwroot" {
			kuduWebAPIError(w, http.StatusConflict, fmt.Sprintf("Cannot delete directory '/home/%s'.", rel))
			return
		}
		kuduVFSDelete(w, r, root, name, dir, kuduVFSCommit(site, rel))
	default:
		kuduMethod(w, r, http.MethodGet, http.MethodHead, http.MethodPut, http.MethodDelete)
	}
}

// kuduVFSRefuseReadOnly refuses a write to the wwwroot of a run-from-package
// app, which the platform mounts read-only.
func kuduVFSRefuseReadOnly(w http.ResponseWriter, site *Site, rel string) bool {
	if kuduWWWRootReadOnly(site) && kuduInWWWRoot(rel) {
		kuduWebAPIError(w, http.StatusConflict,
			"'/home/site/wwwroot' is read-only because the app runs from a package (WEBSITE_RUN_FROM_PACKAGE).")
		return true
	}
	return false
}

// kuduVFSCommit is what a change to the file system does once it is on disk:
// a change under site/wwwroot rediscovers the site's webjobs, as Kudu's
// watcher on App_Data/jobs does.
func kuduVFSCommit(site *Site, rel string) func() error {
	return func() error {
		if !kuduInWWWRoot(rel) {
			return nil
		}
		return webDiscoverWebJobs(site.ID)
	}
}

func kuduVFSGet(w http.ResponseWriter, r *http.Request, root *os.Root, name, rel string, dir bool) {
	info, err := root.Stat(name)
	if err != nil {
		kuduWebAPIError(w, http.StatusNotFound, fmt.Sprintf("'%s' not found.", "/home/"+rel))
		return
	}
	if info.IsDir() != dir {
		target := strings.TrimSuffix(r.URL.Path, "/")
		if info.IsDir() {
			target += "/"
		}
		u := *r.URL
		u.Path = target
		u.RawPath = ""
		w.Header().Set("Location", azureRequestScheme(r)+"://"+r.Host+u.RequestURI())
		w.WriteHeader(http.StatusTemporaryRedirect)
		return
	}
	if dir {
		kuduVFSList(w, r, root, name, rel)
		return
	}
	etag := kuduEtag(info.ModTime())
	w.Header().Set("ETag", etag)
	w.Header().Set("Last-Modified", info.ModTime().UTC().Format(http.TimeFormat))
	if match := r.Header.Get("If-None-Match"); match != "" && (match == etag || match == "*") {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	f, err := root.Open(name)
	if err != nil {
		kuduWebAPIError(w, http.StatusInternalServerError, err.Error())
		return
	}
	defer func() { _ = f.Close() }()
	w.Header().Set("Content-Type", kuduMimeType(name))
	w.Header().Set("Content-Length", fmt.Sprint(info.Size()))
	w.WriteHeader(http.StatusOK)
	if r.Method != http.MethodHead {
		_, _ = io.Copy(w, f)
	}
}

func kuduVFSList(w http.ResponseWriter, r *http.Request, root *os.Root, name, rel string) {
	entries, err := fs.ReadDir(root.FS(), name)
	if err != nil {
		kuduWebAPIError(w, http.StatusInternalServerError, err.Error())
		return
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	base := kuduVFSBaseURL(r)
	out := make([]map[string]any, 0, len(entries))
	for _, e := range entries {
		info, err := e.Info()
		if err != nil {
			continue
		}
		child := e.Name()
		if rel != "" {
			child = rel + "/" + e.Name()
		}
		entry := map[string]any{
			"name":   e.Name(),
			"size":   info.Size(),
			"mtime":  kuduVFSTime(info.ModTime()),
			"crtime": kuduVFSTime(info.ModTime()),
			"mime":   kuduMimeType(e.Name()),
			"href":   base + child,
			"path":   "/home/" + child,
		}
		if info.IsDir() {
			entry["size"] = 0
			entry["mime"] = "inode/directory"
			entry["href"] = base + child + "/"
		}
		out = append(out, entry)
	}
	sim.WriteJSON(w, http.StatusOK, out)
}

func kuduVFSPutDir(w http.ResponseWriter, root *os.Root, name string, commit func() error) {
	if info, err := root.Stat(name); err == nil {
		if info.IsDir() {
			kuduWebAPIError(w, http.StatusConflict, "Cannot update an existing directory.")
		} else {
			kuduWebAPIError(w, http.StatusConflict, "A file with the same name already exists.")
		}
		return
	}
	if err := root.MkdirAll(name, 0o777); err != nil {
		kuduWebAPIError(w, http.StatusConflict, err.Error())
		return
	}
	if err := commit(); err != nil {
		kuduWebAPIError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.WriteHeader(http.StatusCreated)
}

func kuduVFSPutFile(w http.ResponseWriter, r *http.Request, root *os.Root, name string, commit func() error) {
	info, err := root.Stat(name)
	exists := err == nil
	if exists && info.IsDir() {
		kuduWebAPIError(w, http.StatusConflict, "Cannot update a directory with a file.")
		return
	}
	if exists && !kuduIfMatch(w, r, info) {
		return
	}
	data, err := io.ReadAll(io.LimitReader(r.Body, kuduVFSMaxUpload+1))
	if err != nil {
		kuduWebAPIError(w, http.StatusBadRequest, err.Error())
		return
	}
	if len(data) > kuduVFSMaxUpload {
		kuduWebAPIError(w, http.StatusRequestEntityTooLarge, fmt.Sprintf("The file exceeds %d bytes.", kuduVFSMaxUpload))
		return
	}
	if parent := path.Dir(name); parent != "." {
		if err := root.MkdirAll(parent, 0o777); err != nil {
			kuduWebAPIError(w, http.StatusConflict, err.Error())
			return
		}
	}
	if err := root.WriteFile(name, data, 0o666); err != nil {
		kuduWebAPIError(w, http.StatusConflict, err.Error())
		return
	}
	if err := commit(); err != nil {
		kuduWebAPIError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if info, err := root.Stat(name); err == nil {
		w.Header().Set("ETag", kuduEtag(info.ModTime()))
	}
	if exists {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	w.WriteHeader(http.StatusCreated)
}

// kuduIfMatch admits a change to an existing file only when the request's
// If-Match names its current ETag or "*".
func kuduIfMatch(w http.ResponseWriter, r *http.Request, info os.FileInfo) bool {
	match := r.Header.Get("If-Match")
	if match == "" {
		kuduWebAPIError(w, http.StatusPreconditionFailed,
			"The file already exists: send If-Match with its ETag, or *, to change it.")
		return false
	}
	if match != "*" && match != kuduEtag(info.ModTime()) {
		kuduWebAPIError(w, http.StatusPreconditionFailed, "The file has changed since the ETag If-Match names.")
		return false
	}
	return true
}

func kuduVFSDelete(w http.ResponseWriter, r *http.Request, root *os.Root, name string, dir bool, commit func() error) {
	info, err := root.Stat(name)
	if err != nil || info.IsDir() != dir {
		kuduWebAPIError(w, http.StatusNotFound, fmt.Sprintf("'/home/%s' not found.", name))
		return
	}
	switch {
	case !dir:
		if !kuduIfMatch(w, r, info) {
			return
		}
		err = root.Remove(name)
	case strings.EqualFold(kuduQuery(r, "recursive"), "true"):
		err = root.RemoveAll(name)
	default:
		if entries, readErr := fs.ReadDir(root.FS(), name); readErr == nil && len(entries) > 0 {
			kuduWebAPIError(w, http.StatusConflict, "The directory is not empty: delete it with ?recursive=true.")
			return
		}
		err = root.Remove(name)
	}
	if err != nil {
		kuduWebAPIError(w, http.StatusConflict, err.Error())
		return
	}
	if err := commit(); err != nil {
		kuduWebAPIError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.WriteHeader(http.StatusOK)
}
