package main

import (
	"bytes"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/e6qu/sockerless-cloud/sim"
	"github.com/e6qu/sockerless-cloud/sim/archive"
)

// appServiceLinuxStack is one built-in Linux runtime stack: the linuxFxVersion
// that selects it and the platform image App Service runs for it. The image's
// own entrypoint (/opt/startup/init_container.sh) runs Oryx, which writes the
// startup script for the content in /home/site/wwwroot: the site's startup
// command when it has one, else the app it detects, else the platform's
// default page when the directory is empty.
type appServiceLinuxStack struct {
	Stack        string // the stack's catalogue value, "node"
	StackDisplay string // "Node"
	Major        string // "20"
	MajorDisplay string // "Node 20"
	Minor        string // "20-lts"
	MinorDisplay string // "Node 20 LTS"
	Image        string
	// Port is the PORT the image's own configuration declares and its startup
	// script listens on.
	Port int
}

// LinuxFxVersion is the siteConfig value that selects the stack.
func (s appServiceLinuxStack) LinuxFxVersion() string {
	return strings.ToUpper(s.Stack) + "|" + s.Minor
}

// appServiceLinuxStacks are the built-in Linux stacks this App Service runs,
// each on the platform image Microsoft publishes for it under
// mcr.microsoft.com/appsvc, pinned to one build.
var appServiceLinuxStacks = []appServiceLinuxStack{
	{
		Stack: "node", StackDisplay: "Node", Major: "22", MajorDisplay: "Node 22",
		Minor: "22-lts", MinorDisplay: "Node 22 LTS",
		Image: "mcr.microsoft.com/appsvc/node:22-lts_20260904.5.tuxprod", Port: 8080,
	},
	{
		Stack: "node", StackDisplay: "Node", Major: "20", MajorDisplay: "Node 20",
		Minor: "20-lts", MinorDisplay: "Node 20 LTS",
		Image: "mcr.microsoft.com/appsvc/node:20-lts_20260904.5.tuxprod", Port: 8080,
	},
	{
		Stack: "python", StackDisplay: "Python", Major: "3", MajorDisplay: "Python 3",
		Minor: "3.12", MinorDisplay: "Python 3.12",
		Image: "mcr.microsoft.com/appsvc/python:3.12_20260910.5.tuxprod", Port: 8000,
	},
}

// appServiceLinuxStackNames lists the linuxFxVersion of every stack this App
// Service runs.
func appServiceLinuxStackNames() []string {
	out := make([]string, 0, len(appServiceLinuxStacks))
	for _, s := range appServiceLinuxStacks {
		out = append(out, s.LinuxFxVersion())
	}
	return out
}

// webDefaultSiteKind is the kind a site created without one reports: a web
// app, on Linux when the site or its App Service plan is reserved.
func webDefaultSiteKind(reserved bool, serverFarmID string) string {
	if !reserved && serverFarmID != "" {
		if plan, ok := webValidateLookupPlan(serverFarmID); ok {
			reserved = plan.Properties.Reserved
		}
	}
	if reserved {
		return "app,linux"
	}
	return "app"
}

// siteIsFunctionApp reports whether the site is an Azure Functions app, which
// the platform runs on the Functions host rather than on a web runtime stack.
func siteIsFunctionApp(site *Site) bool {
	return strings.Contains(strings.ToLower(site.Kind), "functionapp")
}

// siteBuiltInStack returns the built-in Linux stack a web app's linuxFxVersion
// selects, when it is one this App Service runs.
func siteBuiltInStack(site *Site) (appServiceLinuxStack, bool) {
	fx := siteRuntimeStack(site)
	if fx == "" || siteIsFunctionApp(site) {
		return appServiceLinuxStack{}, false
	}
	for _, s := range appServiceLinuxStacks {
		if strings.EqualFold(strings.TrimSpace(fx), s.LinuxFxVersion()) {
			return s, true
		}
	}
	return appServiceLinuxStack{}, false
}

// appServiceSitesHomeRoot holds each built-in-stack site's /home: the
// persistent storage App Service mounts into the site's container, with the
// deployed content in site/wwwroot and the platform's logs in LogFiles.
func appServiceSitesHomeRoot() string {
	return sim.ScopedDataDir("", "web-sites", "sockerless-sim-azure-web-sites")
}

func siteHomeDir(siteName string) string {
	return filepath.Join(appServiceSitesHomeRoot(), siteStorageName(siteName))
}

// siteStorageName is the name an app's or a slot's own storage and containers
// go by: "<app>" for an app and "<app>__<slot>" for a slot, as App Service
// spells a slot's publishing user.
func siteStorageName(siteName string) string {
	return strings.Replace(siteName, "/", "__", 1)
}

// removeSiteHome deletes a deleted site's /home storage.
func removeSiteHome(siteName string) {
	_ = os.RemoveAll(siteHomeDir(siteName))
}

// prepareSiteHome lays out the /home a built-in-stack site's container mounts
// and returns the binds for it. site/wwwroot holds what the site runs: the
// package WEBSITE_RUN_FROM_PACKAGE names by URL, mounted read-only as the
// platform mounts a run-from-package app, or else the content the site's
// deployments wrote, read-only too when WEBSITE_RUN_FROM_PACKAGE=1 makes the
// last deployed package the app.
func prepareSiteHome(site *Site) ([]string, error) {
	home, err := ensureSiteHome(site)
	if err != nil {
		return nil, err
	}
	wwwroot := filepath.Join(home, "site", "wwwroot")
	binds := []string{home + ":/home"}
	if pkg := strings.TrimSpace(siteAppSettings(site)["WEBSITE_RUN_FROM_PACKAGE"]); isPackageURL(pkg) {
		if err := os.RemoveAll(wwwroot); err != nil {
			return nil, fmt.Errorf("reset site content: %w", err)
		}
		if err := sim.EnsureWritableDir(wwwroot); err != nil {
			return nil, fmt.Errorf("create site storage: %w", err)
		}
		data, err := webFetchPackage(pkg)
		if err != nil {
			return nil, fmt.Errorf("WEBSITE_RUN_FROM_PACKAGE: %w", err)
		}
		if err := archive.ExtractZip(data, wwwroot, webSiteContentLimit); err != nil {
			return nil, fmt.Errorf("WEBSITE_RUN_FROM_PACKAGE: unpack package: %w", err)
		}
		return append(binds, wwwroot+":/home/site/wwwroot:ro"), nil
	}
	if err := syncDeployedContent(site.ID, wwwroot); err != nil {
		return nil, err
	}
	if webSiteRunsFromDeployedPackage(site) {
		return append(binds, wwwroot+":/home/site/wwwroot:ro"), nil
	}
	return binds, nil
}

// ensureSiteHome creates a site's /home storage with the directories the
// platform lays out in it, and returns its host directory.
func ensureSiteHome(site *Site) (string, error) {
	home := siteHomeDir(site.Name)
	for _, dir := range []string{home, filepath.Join(home, "site"), filepath.Join(home, "site", "wwwroot"),
		filepath.Join(home, "LogFiles"), filepath.Join(home, "data")} {
		if err := sim.EnsureWritableDir(dir); err != nil {
			return "", fmt.Errorf("create site storage: %w", err)
		}
	}
	return home, nil
}

func isPackageURL(v string) bool {
	lower := strings.ToLower(v)
	return strings.HasPrefix(lower, "http://") || strings.HasPrefix(lower, "https://")
}

// syncDeployedContent makes dir hold exactly the files the site's deployments
// persisted: it removes the files they do not hold and writes the ones that
// differ, keeping each file's modification time and every directory.
func syncDeployedContent(resID, dir string) error {
	root, err := os.OpenRoot(dir)
	if err != nil {
		return fmt.Errorf("open site content: %w", err)
	}
	defer func() { _ = root.Close() }()
	want := map[string]WebSiteContentFile{}
	for _, f := range webSiteContentFiles(resID) {
		want[f.Path] = f
	}
	var stale []string
	err = fs.WalkDir(root.FS(), ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if p == "." || d.IsDir() {
			return nil
		}
		if _, ok := want[p]; !ok {
			stale = append(stale, p)
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("read site content: %w", err)
	}
	for _, p := range stale {
		if err := root.Remove(p); err != nil {
			return fmt.Errorf("remove site content %q: %w", p, err)
		}
	}
	for p, f := range want {
		mode := fs.FileMode(f.Mode) & fs.ModePerm
		if mode == 0 {
			mode = 0o644
		}
		if info, err := root.Lstat(p); err == nil {
			if info.Mode().IsRegular() && info.Mode().Perm() == mode {
				if data, err := root.ReadFile(p); err == nil && bytes.Equal(data, f.Data) {
					continue
				}
			}
			if err := root.RemoveAll(p); err != nil {
				return fmt.Errorf("write site content %q: %w", p, err)
			}
		}
		if parent := path.Dir(p); parent != "." {
			if err := root.MkdirAll(parent, 0o777); err != nil {
				return fmt.Errorf("write site content %q: %w", p, err)
			}
		}
		if err := root.WriteFile(p, f.Data, mode); err != nil {
			return fmt.Errorf("write site content %q: %w", p, err)
		}
		if err := root.Chmod(p, mode); err != nil {
			return fmt.Errorf("write site content %q: %w", p, err)
		}
		if !f.Modified.IsZero() {
			if err := root.Chtimes(p, f.Modified, f.Modified); err != nil {
				return fmt.Errorf("write site content %q: %w", p, err)
			}
		}
	}
	return nil
}

// captureDeployedContent makes the site's persisted content exactly the
// regular files dir holds, as a change made through the SCM site's file
// system lands in the site's content, and rediscovers its webjobs.
func captureDeployedContent(resID, dir string) error {
	root, err := os.OpenRoot(dir)
	if err != nil {
		return fmt.Errorf("open site content: %w", err)
	}
	defer func() { _ = root.Close() }()
	have := map[string]WebSiteContentFile{}
	for _, f := range webSiteContentFiles(resID) {
		have[f.Path] = f
	}
	var total int64
	seen := map[string]bool{}
	err = fs.WalkDir(root.FS(), ".", func(p string, d fs.DirEntry, err error) error {
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
		if total += info.Size(); total > webSiteContentLimit {
			return fmt.Errorf("the site's content exceeds %d bytes", webSiteContentLimit)
		}
		data, err := root.ReadFile(p)
		if err != nil {
			return err
		}
		seen[p] = true
		mode := uint32(info.Mode().Perm())
		if cur, ok := have[p]; ok && cur.Mode == mode && bytes.Equal(cur.Data, data) {
			return nil
		}
		id := resID + "|" + p
		webSiteContent.Put(id, WebSiteContentFile{ID: id, Path: p, Mode: mode, Data: data, Modified: info.ModTime().UTC()})
		return nil
	})
	if err != nil {
		return fmt.Errorf("read site content: %w", err)
	}
	for p, f := range have {
		if !seen[p] {
			webSiteContent.Delete(f.ID)
		}
	}
	webDiscoverWebJobs(resID)
	return nil
}
