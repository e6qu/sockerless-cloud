package main

import (
	"strings"
	"testing"
)

func TestWebOneDeployArtifactPlacement(t *testing.T) {
	linux := &Site{Kind: "app,linux", Properties: SiteProperties{Reserved: true}}
	windows := &Site{Kind: "app"}
	cases := []struct {
		name                 string
		site                 *Site
		typ, path, clean, rs string
		want                 webArtifact
		wantErr              string
	}{
		{name: "zip", site: linux, typ: "zip",
			want: webArtifact{Type: "zip", Target: "/home/site/wwwroot", Clean: true, Restart: true}},
		{name: "zip into a subdirectory", site: linux, typ: "zip", path: "tools", clean: "false",
			want: webArtifact{Type: "zip", Target: "/home/site/wwwroot/tools", Restart: true}},
		{name: "war", site: linux, typ: "WAR",
			want: webArtifact{Type: "war", Target: "/home/site/wwwroot/app.war", Clean: true, Restart: true}},
		{name: "jar without restart", site: linux, typ: "jar", rs: "False",
			want: webArtifact{Type: "jar", Target: "/home/site/wwwroot/app.jar", Clean: true}},
		{name: "static", site: linux, typ: "static", path: "staticfiles/test.txt",
			want: webArtifact{Type: "static", Target: "/home/site/wwwroot/staticfiles/test.txt", Restart: true}},
		{name: "static without a path", site: linux, typ: "static",
			wantErr: "Path must be defined for static file deployments"},
		{name: "startup on Linux", site: linux, typ: "startup",
			want: webArtifact{Type: "startup", Target: "/home/site/wwwroot/startup.sh", Restart: true}},
		{name: "startup on Windows", site: windows, typ: "startup",
			want: webArtifact{Type: "startup", Target: "/home/site/scripts/startup.cmd", Restart: true}},
		{name: "startup at an absolute path", site: linux, typ: "startup", path: "/home/site/scripts/startup-script.sh",
			want: webArtifact{Type: "startup", Target: "/home/site/scripts/startup-script.sh", Restart: true}},
		{name: "lib", site: linux, typ: "lib", path: "library.jar",
			want: webArtifact{Type: "lib", Target: "/home/site/libs/library.jar", Restart: true}},
		{name: "lib without a path", site: linux, typ: "lib",
			wantErr: "Path must be defined for library deployments"},
		{name: "a path leaving /home", site: linux, typ: "static", path: "../../../etc/passwd",
			wantErr: "is outside /home"},
		{name: "no type", site: linux, wantErr: "Artifact type is required"},
		{name: "an unknown type", site: linux, typ: "msi", wantErr: "not supported"},
		{name: "a malformed flag", site: linux, typ: "zip", clean: "maybe", wantErr: "invalid boolean"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := webOneDeployArtifact(tc.site, tc.typ, tc.path, tc.clean, tc.rs)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("error = %v, want one containing %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Fatalf("artifact = %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestSiteScmHostKeys(t *testing.T) {
	site := Site{Properties: SiteProperties{
		DefaultHostName:   "app-staging.azurewebsites.net",
		HostNameSslStates: siteHostNameSslStates("app-staging.azurewebsites.net", "App-Staging.scm.localhost:4568"),
	}}
	got := siteScmHostKeys(site)
	want := []string{"app-staging.scm.localhost", "app-staging.scm.azurewebsites.net"}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("keys = %q, want %q", got, want)
	}
}
