package main

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/e6qu/sockerless-cloud/sim/archive"
)

// web_site_content.go reads and writes a site's /home/site/wwwroot. App
// Service keeps /home as one persistent share that the site's containers,
// its Kudu site and every instance mount, so the directory on that share is
// the site's content: deployments, restores, swaps and the Kudu file system
// write into it, and a file the running app writes there stays.

// WebSiteContentFile is one file of a site's wwwroot. Path is wwwroot-relative
// with forward slashes.
type WebSiteContentFile struct {
	Path     string
	Mode     uint32
	Data     []byte
	Modified time.Time
}

// webSiteNameOf is the site name, "<app>" or "<app>/<slot>", a resource ID
// names.
func webSiteNameOf(resID string) string {
	i := strings.LastIndex(resID, "/sites/")
	if i < 0 {
		return ""
	}
	return strings.Replace(resID[i+len("/sites/"):], "/slots/", "/", 1)
}

// webWWWRootDir is the host directory of a site's /home/site/wwwroot.
func webWWWRootDir(resID string) string {
	return filepath.Join(siteHomeDir(webSiteNameOf(resID)), "site", "wwwroot")
}

// webOpenWWWRoot creates the site's /home layout and opens its wwwroot.
func webOpenWWWRoot(resID string) (*os.Root, error) {
	if _, err := ensureSiteHome(webSiteNameOf(resID)); err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(webWWWRootDir(resID))
	if err != nil {
		return nil, fmt.Errorf("open site content: %w", err)
	}
	return root, nil
}

// webClearDir removes everything inside dir and keeps dir itself, which a
// running container may have bind-mounted.
func webClearDir(dir string) error {
	entries, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, e := range entries {
		if err := os.RemoveAll(filepath.Join(dir, e.Name())); err != nil {
			return err
		}
	}
	return nil
}

// webWalkSiteContent calls fn for each regular file under sub (wwwroot-relative,
// "" for all of it) in path order. A site that has no such directory has no
// files there.
func webWalkSiteContent(resID, sub string, fn func(root *os.Root, p string, info fs.FileInfo) error) error {
	root, err := os.OpenRoot(webWWWRootDir(resID))
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("open site content: %w", err)
	}
	defer func() { _ = root.Close() }()
	start := "."
	if sub != "" {
		start = path.Clean(sub)
		if _, err := root.Lstat(start); errors.Is(err, fs.ErrNotExist) {
			return nil
		}
	}
	err = fs.WalkDir(root.FS(), start, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.Type().IsRegular() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		return fn(root, p, info)
	})
	if err != nil {
		return fmt.Errorf("read site content: %w", err)
	}
	return nil
}

// webSiteContentPaths lists the wwwroot-relative paths of the files under sub.
func webSiteContentPaths(resID, sub string) ([]string, error) {
	var out []string
	err := webWalkSiteContent(resID, sub, func(_ *os.Root, p string, _ fs.FileInfo) error {
		out = append(out, p)
		return nil
	})
	return out, err
}

// webReadSiteContent reads the files under sub, sorted by path, so an archive
// built twice from the same content is byte-identical.
func webReadSiteContent(resID, sub string) ([]WebSiteContentFile, error) {
	var out []WebSiteContentFile
	var total int64
	err := webWalkSiteContent(resID, sub, func(root *os.Root, p string, info fs.FileInfo) error {
		if total += info.Size(); total > webSiteContentLimit {
			return fmt.Errorf("the site's content exceeds %d bytes", webSiteContentLimit)
		}
		data, err := root.ReadFile(p)
		if err != nil {
			return err
		}
		out = append(out, WebSiteContentFile{Path: p, Mode: uint32(info.Mode().Perm()), Data: data, Modified: info.ModTime().UTC()})
		return nil
	})
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out, err
}

// webSiteContentFiles reads every file of the site's wwwroot.
func webSiteContentFiles(resID string) ([]WebSiteContentFile, error) {
	return webReadSiteContent(resID, "")
}

// webSiteHasContent reports whether the site's wwwroot holds any file.
func webSiteHasContent(resID string) (bool, error) {
	errFound := errors.New("found")
	err := webWalkSiteContent(resID, "", func(*os.Root, string, fs.FileInfo) error { return errFound })
	if errors.Is(err, errFound) {
		return true, nil
	}
	return false, err
}

// webReadSiteFile reads one wwwroot-relative file; ok is false when the site
// has no such file.
func webReadSiteFile(resID, p string) (data []byte, ok bool, err error) {
	root, err := os.OpenRoot(webWWWRootDir(resID))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("open site content: %w", err)
	}
	defer func() { _ = root.Close() }()
	data, err = root.ReadFile(p)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("read site content %q: %w", p, err)
	}
	return data, true, nil
}

// webRemoveSiteContent removes sub (wwwroot-relative) and everything under it.
func webRemoveSiteContent(resID, sub string) error {
	root, err := os.OpenRoot(webWWWRootDir(resID))
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("open site content: %w", err)
	}
	defer func() { _ = root.Close() }()
	if err := root.RemoveAll(path.Clean(sub)); err != nil {
		return fmt.Errorf("remove site content %q: %w", sub, err)
	}
	return nil
}

// webWriteSiteFiles writes files into the open wwwroot, each replacing what
// its path held.
func webWriteSiteFiles(root *os.Root, files []WebSiteContentFile) error {
	for _, f := range files {
		mode := fs.FileMode(f.Mode) & fs.ModePerm
		if mode == 0 {
			mode = 0o644
		}
		if parent := path.Dir(f.Path); parent != "." {
			if err := root.MkdirAll(parent, 0o777); err != nil {
				return fmt.Errorf("write site content %q: %w", f.Path, err)
			}
		}
		if info, err := root.Lstat(f.Path); err == nil && !info.Mode().IsRegular() {
			if err := root.RemoveAll(f.Path); err != nil {
				return fmt.Errorf("write site content %q: %w", f.Path, err)
			}
		}
		if err := root.WriteFile(f.Path, f.Data, mode); err != nil {
			return fmt.Errorf("write site content %q: %w", f.Path, err)
		}
		if err := root.Chmod(f.Path, mode); err != nil {
			return fmt.Errorf("write site content %q: %w", f.Path, err)
		}
	}
	return nil
}

// webWriteSiteContent writes files under dir (wwwroot-relative) into the
// site's wwwroot. A clean artifact first empties dir; a zip deployment with
// KuduSync semantics removes the files the previous zip deployment wrote that
// this one lacks, and leaves every other file, the app's own among them.
func webWriteSiteContent(resID, dir string, files []archive.File, a webArtifact) (int, error) {
	join := func(name string) string {
		if dir == "" {
			return name
		}
		return dir + "/" + name
	}
	root, err := webOpenWWWRoot(resID)
	if err != nil {
		return 0, err
	}
	defer func() { _ = root.Close() }()
	incoming := make(map[string]bool, len(files))
	for _, f := range files {
		incoming[join(f.Name)] = true
	}
	switch {
	case a.Clean && dir == "":
		if err := webClearDir(webWWWRootDir(resID)); err != nil {
			return 0, fmt.Errorf("clean site content: %w", err)
		}
	case a.Clean:
		if err := root.RemoveAll(dir); err != nil {
			return 0, fmt.Errorf("clean site content %q: %w", dir, err)
		}
	case a.SyncManifest:
		if prev, ok := webDeployManifests.Get(resID); ok {
			for _, p := range prev.Paths {
				if incoming[p] {
					continue
				}
				if err := root.Remove(p); err != nil && !errors.Is(err, fs.ErrNotExist) {
					return 0, fmt.Errorf("remove site content %q: %w", p, err)
				}
			}
		}
	}
	out := make([]WebSiteContentFile, 0, len(files))
	paths := make([]string, 0, len(files))
	for _, f := range files {
		p := join(f.Name)
		out = append(out, WebSiteContentFile{Path: p, Mode: uint32(f.Mode), Data: f.Data})
		paths = append(paths, p)
	}
	if err := webWriteSiteFiles(root, out); err != nil {
		return 0, err
	}
	if a.SyncManifest {
		sort.Strings(paths)
		webDeployManifests.Put(resID, WebDeployManifest{ID: resID, Paths: paths})
	}
	return len(files), nil
}

// webReplaceSiteContent makes the site's wwwroot exactly the given set of
// files. Microsoft: "Without `_backup.filter`, restoring a backup deletes all
// existing files in the app and replaces them with the files in the backup" —
// so every file the archive does not carry is removed, which is the opposite
// of a deployment's merge. The restored app restarts on its new content.
func webReplaceSiteContent(resID string, files []WebSiteContentFile) error {
	root, err := webOpenWWWRoot(resID)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	if err := webClearDir(webWWWRootDir(resID)); err != nil {
		return fmt.Errorf("clear site content: %w", err)
	}
	if err := webWriteSiteFiles(root, files); err != nil {
		return err
	}
	if err := webDiscoverWebJobs(resID); err != nil {
		return err
	}
	if site, ok := webJobSite(resID); ok {
		restartAzureFunctionInstance(site)
	}
	return nil
}
