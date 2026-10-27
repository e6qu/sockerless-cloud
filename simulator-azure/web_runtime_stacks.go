package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/e6qu/sockerless-cloud/sim"
	"github.com/e6qu/sockerless-cloud/sim/archive"
)

// appServiceLinuxStack is one built-in Linux runtime stack on its platform
// image. The image's entrypoint (/opt/startup/init_container.sh) runs Oryx,
// which starts the site's startup command, else the app it detects in
// /home/site/wwwroot, else the default page.
type appServiceLinuxStack struct {
	Stack        string
	StackDisplay string
	Major        string
	MajorDisplay string
	Minor        string
	MinorDisplay string
	Image        string
	// Port is the PORT the image's own configuration declares.
	Port int
}

func (s appServiceLinuxStack) LinuxFxVersion() string {
	return strings.ToUpper(s.Stack) + "|" + s.Minor
}

// Each stack runs on the image Microsoft publishes under
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

func appServiceLinuxStackNames() []string {
	out := make([]string, 0, len(appServiceLinuxStacks))
	for _, s := range appServiceLinuxStacks {
		out = append(out, s.LinuxFxVersion())
	}
	return out
}

// webDefaultSiteKind is a web app, on Linux when the site or its App Service
// plan is reserved.
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

func siteIsFunctionApp(site *Site) bool {
	return strings.Contains(strings.ToLower(site.Kind), "functionapp")
}

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

// appServiceSitesHomeRoot holds each built-in-stack site's /home, with the
// content in site/wwwroot and the platform's logs in LogFiles.
func appServiceSitesHomeRoot() string {
	return sim.ScopedDataDir("", "web-sites", "sockerless-sim-azure-web-sites")
}

func siteHomeDir(siteName string) string {
	return filepath.Join(appServiceSitesHomeRoot(), siteStorageName(siteName))
}

// App Service spells a slot's publishing user "<app>__<slot>".
func siteStorageName(siteName string) string {
	return strings.Replace(siteName, "/", "__", 1)
}

func removeSiteHome(siteName string) {
	_ = os.RemoveAll(siteHomeDir(siteName))
}

// /home is the site's one persistent share, so site/wwwroot holds whatever
// its deployments, Kudu and the app itself last wrote. A run-from-package app
// runs the package WEBSITE_RUN_FROM_PACKAGE names by URL, or with the value 1
// the last deployed package, mounted read-only as the platform mounts it.
func prepareSiteHome(site *Site) ([]string, error) {
	home, err := ensureSiteHome(site.Name)
	if err != nil {
		return nil, err
	}
	wwwroot := filepath.Join(home, "site", "wwwroot")
	binds := []string{home + ":/home"}
	if pkg := strings.TrimSpace(siteAppSettings(site)["WEBSITE_RUN_FROM_PACKAGE"]); isPackageURL(pkg) {
		if err := webClearDir(wwwroot); err != nil {
			return nil, fmt.Errorf("reset site content: %w", err)
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
	if webSiteRunsFromDeployedPackage(site) {
		return append(binds, wwwroot+":/home/site/wwwroot:ro"), nil
	}
	return binds, nil
}

func ensureSiteHome(siteName string) (string, error) {
	home := siteHomeDir(siteName)
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
