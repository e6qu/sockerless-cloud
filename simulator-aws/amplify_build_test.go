package main

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestAmplifyParseBuildSpec(t *testing.T) {
	spec, err := amplifyParseBuildSpec(`
version: 1
frontend:
  phases:
    preBuild:
      commands:
        - echo prebuild
    build:
      commands:
        - mkdir -p dist
        - cp index.html dist/
  artifacts:
    baseDirectory: dist
    files:
      - '**/*'
`)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if spec.Version != "1" {
		t.Fatalf("version: got %q", spec.Version)
	}
	if len(spec.FrontendCommands) != 3 || spec.FrontendCommands[0] != "echo prebuild" {
		t.Fatalf("frontend commands: %v", spec.FrontendCommands)
	}
	if spec.BaseDirectory != "dist" {
		t.Fatalf("baseDirectory: %q", spec.BaseDirectory)
	}
	if len(spec.Files) != 1 || spec.Files[0] != "**/*" {
		t.Fatalf("files: %v", spec.Files)
	}

	for name, text := range map[string]string{
		"invalid yaml":       "version: [unclosed",
		"no commands":        "version: 1\nfrontend:\n  artifacts:\n    baseDirectory: dist\n",
		"no baseDirectory":   "version: 1\nfrontend:\n  phases:\n    build:\n      commands: [echo hi]\n",
		"empty spec":         "",
		"unrelated document": "backend:\n  phases:\n    build:\n      commands: [echo hi]\n",
	} {
		if _, err := amplifyParseBuildSpec(text); err == nil {
			t.Fatalf("%s: expected error", name)
		}
	}
}

func TestAmplifyParseMonorepoBuildSpec(t *testing.T) {
	spec, err := amplifyParseBuildSpec(`
version: 1
applications:
  - appRoot: apps/web
    env:
      variables:
        FROM_SPEC: present
    backend:
      phases:
        build:
          commands: [echo backend]
    frontend:
      buildPath: /
      phases:
        preBuild:
          commands: [echo frontend]
        postBuild:
          commands: [echo post]
      artifacts:
        baseDirectory: apps/web/dist
        files: ['**/*']
      cache:
        paths: [apps/web/.cache/**/*]
    test:
      phases:
        test:
          commands: [echo test]
`, "apps/web")
	if err != nil {
		t.Fatalf("parse monorepo: %v", err)
	}
	if spec.AppRoot != "apps/web" || spec.BuildPath != "/" {
		t.Fatalf("monorepo paths: %#v", spec)
	}
	if spec.Environment["FROM_SPEC"] != "present" {
		t.Fatalf("environment: %#v", spec.Environment)
	}
	if len(spec.BackendCommands) != 1 || len(spec.FrontendCommands) != 2 || len(spec.TestCommands) != 1 {
		t.Fatalf("phase commands: %#v", spec)
	}
	if len(spec.CachePaths) != 1 {
		t.Fatalf("cache paths: %#v", spec.CachePaths)
	}
	if _, err := amplifyParseBuildSpec(`version: 1
applications:
  - appRoot: apps/other
    frontend:
      phases:
        build:
          commands: [echo no]
      artifacts:
        baseDirectory: dist
`, "apps/web"); err == nil {
		t.Fatal("a missing AMPLIFY_MONOREPO_APP_ROOT match must fail")
	}
}

func TestAmplifyRealBuildPlanBoundary(t *testing.T) {
	app := func(repo, spec string) AmplifyApp { return AmplifyApp{Repository: repo, BuildSpec: spec} }
	br := func(spec string) AmplifyBranch { return AmplifyBranch{BuildSpec: spec} }

	// No repository cannot start a build.
	if _, _, ok := amplifyRealBuildPlan(app("", "spec"), br("")); ok {
		t.Fatal("no repository must not build")
	}
	// Non-HTTP repository (SSH/CodeCommit URI) cannot use this transport.
	if _, _, ok := amplifyRealBuildPlan(app("git@github.com:acme/site.git", "spec"), br("")); ok {
		t.Fatal("non-HTTP repository must not build")
	}
	// A repository without a configured buildSpec resolves amplify.yml after
	// clone, so it remains a real build plan with an empty pre-clone spec.
	spec, repo, ok := amplifyRealBuildPlan(app("https://github.com/acme/site", ""), br(""))
	if !ok || spec != "" || repo != "https://github.com/acme/site" {
		t.Fatalf("checked-in build spec plan: %q %q %v", spec, repo, ok)
	}
	// App-level buildSpec builds.
	spec, repo, ok = amplifyRealBuildPlan(app("https://github.com/acme/site", "app-spec"), br(""))
	if !ok || spec != "app-spec" || repo != "https://github.com/acme/site" {
		t.Fatalf("app-level spec: %q %q %v", spec, repo, ok)
	}
	// Branch-level buildSpec wins (real precedence).
	spec, _, ok = amplifyRealBuildPlan(app("http://repos.local/site.git", "app-spec"), br("branch-spec"))
	if !ok || spec != "branch-spec" {
		t.Fatalf("branch-level spec must win: %q %v", spec, ok)
	}
}

func TestAmplifyArtifactMatch(t *testing.T) {
	cases := []struct {
		patterns []string
		rel      string
		want     bool
	}{
		{nil, "index.html", true},
		{[]string{"**/*"}, "deep/nested/file.js", true},
		{[]string{"**"}, "file.js", true},
		{[]string{"*.html"}, "index.html", true},
		{[]string{"*.html"}, "assets/app.js", false},
		{[]string{"*.html", "assets/*.js"}, "assets/app.js", true},
	}
	for _, tc := range cases {
		if got := amplifyArtifactMatch(tc.patterns, tc.rel); got != tc.want {
			t.Fatalf("match(%v, %q) = %v, want %v", tc.patterns, tc.rel, got, tc.want)
		}
	}
}

func TestAmplifyBuildEnvMerge(t *testing.T) {
	app := AmplifyApp{AppId: "dapp", EnvironmentVariables: map[string]string{"SHARED": "app", "APP_ONLY": "1"}}
	br := AmplifyBranch{BranchName: "main", EnvironmentVariables: map[string]string{"SHARED": "branch"}}
	env := amplifyBuildEnv(app, br, "j1")
	if env["SHARED"] != "branch" {
		t.Fatalf("branch env must win: %v", env)
	}
	if env["APP_ONLY"] != "1" || env["AWS_APP_ID"] != "dapp" || env["AWS_BRANCH"] != "main" || env["AWS_JOB_ID"] != "j1" {
		t.Fatalf("merged env incomplete: %v", env)
	}
}

func TestAmplifyBuildImageDefault(t *testing.T) {
	if img := amplifyBuildImage(); !strings.HasPrefix(img, "public.ecr.aws/docker/library/node:22-bookworm") {
		t.Fatalf("managed multi-language build image must come from Amazon ECR Public, got %q", img)
	}
}

func TestAmplifyBuildCacheBranchKeyCannotEscapeAppDirectory(t *testing.T) {
	appDirectory := filepath.Join(amplifyBuildCacheRoot(), "dapp")
	cacheDirectory := amplifyBuildCacheDirectory("dapp", "../../outside")
	if filepath.Dir(cacheDirectory) != appDirectory {
		t.Fatalf("branch cache escaped app directory: got %q, want child of %q", cacheDirectory, appDirectory)
	}
}

func TestAmplifyBuildOutputManifestError(t *testing.T) {
	amplifyResetHostingState()
	amplifySeedApp("buildman1", "main")
	amplifyApps.Update("buildman1", func(a *amplifyStoredApp) { a.App.Platform = "WEB_COMPUTE" })
	amplifySeedApp("buildman2", "main") // static WEB platform

	invalidZip := amplifyZipOf(t, map[string]string{"deploy-manifest.json": `{"version": 2}`})
	validZip := amplifyZipOf(t, map[string]string{
		"deploy-manifest.json": `{"version": 1, "framework": {"name": "custom", "version": "1.0.0"},
			"routes": [{"path": "/*", "target": {"kind": "Static"}}]}`,
	})
	plainZip := amplifyZipOf(t, map[string]string{"index.html": "<html></html>"})

	if err := amplifyBuildOutputManifestError("buildman1", invalidZip); err == nil {
		t.Error("invalid manifest on a WEB_COMPUTE app must fail validation")
	}
	if err := amplifyBuildOutputManifestError("buildman1", validZip); err != nil {
		t.Errorf("valid manifest: %v", err)
	}
	if err := amplifyBuildOutputManifestError("buildman1", plainZip); err != nil {
		t.Errorf("no manifest in output: %v", err)
	}
	if err := amplifyBuildOutputManifestError("buildman2", invalidZip); err != nil {
		t.Errorf("static WEB platform does not consume the manifest: %v", err)
	}
}
