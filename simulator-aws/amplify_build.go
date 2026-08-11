package main

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"time"

	sim "github.com/e6qu/sockerless-cloud/simulator-aws/shared"
	git "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	githttp "github.com/go-git/go-git/v5/plumbing/transport/http"
	"gopkg.in/yaml.v3"
)

// Real Amplify build pipeline. A job runs a real build when the app has a
// clonable HTTP(S) git repository. A branch/app buildSpec wins when configured;
// otherwise the build host reads amplify.yml from the checked-out repository,
// matching Amplify Hosting's source-controlled build settings. The sim host clones the repository in-process
// (go-git — the host prepares the workspace the way Amplify's build host
// does), then executes the buildSpec's frontend phases inside a node
// container on the host Docker daemon, collects the artifact baseDirectory
// into a zip stored in the sim's S3, and reports SUCCEED/FAILED from the
// container exit code. A source that cannot be cloned is rejected by StartJob;
// it never produces a successful job without executing work.

// ---------- buildSpec ----------

// amplifyBuildSpec is one executable application from an Amplify build
// specification. It covers the backend, frontend, and test phase sequences,
// monorepo roots/build paths, build-spec environment variables, cache paths,
// and the frontend artifact collection rule.
type amplifyBuildSpec struct {
	Version            string
	AppRoot            string
	BuildPath          string
	Environment        map[string]string
	BackendCommands    []string
	FrontendCommands   []string
	TestCommands       []string
	TestBaseDirectory  string
	TestFiles          []string
	TestConfigFilePath string
	BaseDirectory      string
	Files              []string
	CachePaths         []string
}

type amplifyBuildPhaseYAML struct {
	Commands []string `yaml:"commands"`
}

type amplifyBuildSectionYAML struct {
	Phases map[string]amplifyBuildPhaseYAML `yaml:"phases"`
}

type amplifyTestYAML struct {
	amplifyBuildSectionYAML `yaml:",inline"`
	Artifacts               struct {
		BaseDirectory  string   `yaml:"baseDirectory"`
		Files          []string `yaml:"files"`
		ConfigFilePath string   `yaml:"configFilePath"`
	} `yaml:"artifacts"`
}

type amplifyFrontendYAML struct {
	amplifyBuildSectionYAML `yaml:",inline"`
	BuildPath               string `yaml:"buildPath"`
	Artifacts               struct {
		BaseDirectory string   `yaml:"baseDirectory"`
		Files         []string `yaml:"files"`
	} `yaml:"artifacts"`
	Cache struct {
		Paths []string `yaml:"paths"`
	} `yaml:"cache"`
}

type amplifyBuildApplicationYAML struct {
	AppRoot  string                  `yaml:"appRoot"`
	Env      amplifyBuildEnvYAML     `yaml:"env"`
	Backend  amplifyBuildSectionYAML `yaml:"backend"`
	Frontend amplifyFrontendYAML     `yaml:"frontend"`
	Test     amplifyTestYAML         `yaml:"test"`
}

type amplifyBuildEnvYAML struct {
	Variables map[string]string `yaml:"variables"`
}

type amplifyBuildSpecYAML struct {
	Version      any                           `yaml:"version"`
	Env          amplifyBuildEnvYAML           `yaml:"env"`
	Backend      amplifyBuildSectionYAML       `yaml:"backend"`
	Frontend     amplifyFrontendYAML           `yaml:"frontend"`
	Test         amplifyTestYAML               `yaml:"test"`
	Applications []amplifyBuildApplicationYAML `yaml:"applications"`
}

// amplifyParseBuildSpec parses an amplify.yml buildSpec. It requires at
// least one frontend build-phase command — a spec with nothing to execute
// is a configuration error the build surfaces as FAILED, not a silent
// success.
func amplifyParseBuildSpec(text string, monorepoRoot ...string) (amplifyBuildSpec, error) {
	var raw amplifyBuildSpecYAML
	if err := yaml.Unmarshal([]byte(text), &raw); err != nil {
		return amplifyBuildSpec{}, fmt.Errorf("invalid buildSpec YAML: %w", err)
	}
	application := amplifyBuildApplicationYAML{
		Env: raw.Env, Backend: raw.Backend, Frontend: raw.Frontend, Test: raw.Test,
	}
	if len(raw.Applications) > 0 {
		wanted := ""
		if len(monorepoRoot) > 0 {
			wanted = filepath.ToSlash(filepath.Clean(monorepoRoot[0]))
		}
		found := false
		for _, candidate := range raw.Applications {
			if filepath.ToSlash(filepath.Clean(candidate.AppRoot)) == wanted {
				application = candidate
				found = true
				break
			}
		}
		if !found {
			return amplifyBuildSpec{}, fmt.Errorf(
				"buildSpec applications has no appRoot matching AMPLIFY_MONOREPO_APP_ROOT %q", wanted,
			)
		}
	}
	spec := amplifyBuildSpec{
		Version:            fmt.Sprintf("%v", raw.Version),
		AppRoot:            application.AppRoot,
		BuildPath:          application.Frontend.BuildPath,
		Environment:        application.Env.Variables,
		BackendCommands:    amplifyBuildPhaseCommands(application.Backend.Phases, "preBuild", "build", "postBuild"),
		FrontendCommands:   amplifyBuildPhaseCommands(application.Frontend.Phases, "preBuild", "build", "postBuild"),
		TestCommands:       amplifyBuildPhaseCommands(application.Test.Phases, "preTest", "test", "postTest"),
		TestBaseDirectory:  application.Test.Artifacts.BaseDirectory,
		TestFiles:          application.Test.Artifacts.Files,
		TestConfigFilePath: application.Test.Artifacts.ConfigFilePath,
		BaseDirectory:      application.Frontend.Artifacts.BaseDirectory,
		Files:              application.Frontend.Artifacts.Files,
		CachePaths:         application.Frontend.Cache.Paths,
	}
	if len(spec.BackendCommands)+len(spec.FrontendCommands)+len(spec.TestCommands) == 0 {
		return amplifyBuildSpec{}, fmt.Errorf("buildSpec has no backend, frontend, or test commands")
	}
	if spec.BaseDirectory == "" {
		return amplifyBuildSpec{}, fmt.Errorf("buildSpec has no frontend.artifacts.baseDirectory")
	}
	return spec, nil
}

func amplifyBuildPhaseCommands(phases map[string]amplifyBuildPhaseYAML, names ...string) []string {
	var commands []string
	for _, name := range names {
		commands = append(commands, phases[name].Commands...)
	}
	return commands
}

// amplifyRealBuildPlan resolves the source and optional configured buildSpec.
// An empty spec is valid at this stage: the provision phase reads amplify.yml
// from the cloned repository. ok is false only when the source cannot be
// cloned through the supported HTTPS Git transport.
func amplifyRealBuildPlan(app AmplifyApp, br AmplifyBranch) (spec string, repo string, ok bool) {
	repo = app.Repository
	if !strings.HasPrefix(repo, "http://") && !strings.HasPrefix(repo, "https://") {
		return "", "", false
	}
	spec = br.BuildSpec
	if spec == "" {
		spec = app.BuildSpec
	}
	return spec, repo, true
}

// ---------- running builds (StopJob cancellation) ----------

var (
	amplifyBuildMu      sync.Mutex
	amplifyBuildCancels = map[string]func(){} // jobID → cancel running build container
)

func amplifyRegisterBuildCancel(jobID string, cancel func()) {
	amplifyBuildMu.Lock()
	defer amplifyBuildMu.Unlock()
	amplifyBuildCancels[jobID] = cancel
}

func amplifyUnregisterBuildCancel(jobID string) {
	amplifyBuildMu.Lock()
	defer amplifyBuildMu.Unlock()
	delete(amplifyBuildCancels, jobID)
}

// amplifyCancelRunningBuild stops the build container of an in-flight real
// build, if any. Called by StopJob after it marks the job CANCELLED.
func amplifyCancelRunningBuild(jobID string) {
	amplifyBuildMu.Lock()
	cancel := amplifyBuildCancels[jobID]
	amplifyBuildMu.Unlock()
	if cancel != nil {
		cancel()
	}
}

// ---------- build image ----------

// amplifyBuildImage is the managed build image used by this Amazon Amplify
// Hosting runtime generation. AWS selects its build image service-side; it is
// therefore fixed by the cloud implementation rather than caller-configurable
// simulator state.
func amplifyBuildImage() string {
	return "public.ecr.aws/docker/library/node:22-bookworm"
}

// ---------- step/log plumbing ----------

// amplifyStepLog accumulates one job step's log lines and lands them in the
// sim's S3 so the step's logUrl resolves (sim-emitted-url-roundtrip).
type amplifyStepLog struct {
	mu    sync.Mutex
	lines []string
}

func (l *amplifyStepLog) WriteLog(line sim.LogLine) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.lines = append(l.lines, line.Text)
}

func (l *amplifyStepLog) Printf(format string, args ...any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.lines = append(l.lines, fmt.Sprintf(format, args...))
}

func (l *amplifyStepLog) Text() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return strings.Join(l.lines, "\n") + "\n"
}

// amplifyStoreStepLog writes a step's log to the sim's S3 and returns the
// presigned URL that becomes the step's logUrl.
func amplifyStoreStepLog(urlBase, appID, branch, jobID, step string, log *amplifyStepLog) string {
	key := "logs/" + appID + "/" + branch + "/" + jobID + "/" + step + ".log"
	amplifyPutS3Object(key, "text/plain", []byte(log.Text()))
	return amplifyPresignedS3URLBase(urlBase, key, http.MethodGet)
}

// amplifyUpdateJobStep mutates one step of a stored job. Steps that already
// reached a terminal state are left alone — StopJob marks every step
// CANCELLED, and the build goroutine's late completion must not rewrite
// that to FAILED.
func amplifyUpdateJobStep(jobID, stepName string, mutate func(*AmplifyJobStep)) {
	amplifyJobs.Update(jobID, func(j *amplifyStoredJob) {
		for i := range j.Job.Steps {
			if j.Job.Steps[i].StepName == stepName && !j.Job.Steps[i].Status.Terminal() {
				mutate(&j.Job.Steps[i])
			}
		}
	})
}

func amplifyStartJobStep(jobID, stepName string) {
	amplifyUpdateJobStep(jobID, stepName, func(step *AmplifyJobStep) {
		step.Status = AmplifyJobStatusRunning
		step.StartTime = amplifyEpoch()
		if stepName == "BUILD" {
			step.Context = `{"buildImage":"` + amplifyBuildImage() + `"}`
		}
	})
}

func amplifyStartJobSummary(jobID string, recovering bool) bool {
	started := false
	amplifyJobs.Update(jobID, func(job *amplifyStoredJob) {
		if job.Job.Summary.Status == AmplifyJobStatusPending {
			job.Job.Summary.Status = AmplifyJobStatusRunning
			started = true
		} else if recovering && job.Job.Summary.Status == AmplifyJobStatusRunning {
			started = true
		}
	})
	return started
}

// amplifyFinishJob lands a real-build job in a terminal state, refusing to
// clobber a job that already left RUNNING (StopJob marked it CANCELLED).
// Remaining non-terminal steps land in the same state.
func amplifyFinishJob(jobID string, to AmplifyJobStatus) bool {
	finished := false
	amplifyJobs.Update(jobID, func(j *amplifyStoredJob) {
		if j.Job.Summary.Status != AmplifyJobStatusRunning {
			return
		}
		now := amplifyEpoch()
		j.Job.Summary.Status = to
		j.Job.Summary.EndTime = now
		for i := range j.Job.Steps {
			if !j.Job.Steps[i].Status.Terminal() {
				j.Job.Steps[i].Status = to
				j.Job.Steps[i].EndTime = now
			}
		}
		finished = true
	})
	return finished
}

// ---------- the build itself ----------

const amplifyBuildTimeout = 15 * time.Minute

// amplifyScheduleRealBuild runs a real build for a StartJob whose app has a
// clonable repository + buildSpec. Job state machine: PENDING → RUNNING at
// clone start → SUCCEED/FAILED honestly from the build container's exit.
func amplifyScheduleRealBuild(appID, branch, jobID, urlBase, repo, specText string, env map[string]string, commitID string) {
	amplifyScheduleRealBuildMode(appID, branch, jobID, urlBase, repo, specText, env, commitID, false)
}

func amplifyScheduleRealBuildMode(appID, branch, jobID, urlBase, repo, specText string, env map[string]string, commitID string, recovering bool) {
	go func() {
		if !amplifyStartJobSummary(jobID, recovering) {
			return // stopped before it started
		}
		status := amplifyRunRealBuild(appID, branch, jobID, urlBase, repo, specText, env, commitID)
		if amplifyFinishJob(jobID, status) && status == AmplifyJobStatusSucceed {
			amplifyMarkProductionDeploy(appID, branch, jobID)
		}
	}()
}

func amplifyRunRealBuild(appID, branch, jobID, urlBase, repo, specText string, env map[string]string, commitID string) AmplifyJobStatus {
	provisionLog := &amplifyStepLog{}
	amplifyStartJobStep(jobID, "PROVISION")
	finishStep := func(step string, log *amplifyStepLog, status AmplifyJobStatus) {
		logURL := amplifyStoreStepLog(urlBase, appID, branch, jobID, step, log)
		now := amplifyEpoch()
		amplifyUpdateJobStep(jobID, step, func(s *AmplifyJobStep) {
			s.Status = status
			s.EndTime = now
			s.LogUrl = logURL
		})
	}
	failProvision := func(format string, args ...any) AmplifyJobStatus {
		provisionLog.Printf(format, args...)
		finishStep("PROVISION", provisionLog, AmplifyJobStatusFailed)
		return AmplifyJobStatusFailed
	}

	// PROVISION: workspace + clone.
	workDir, err := os.MkdirTemp("", "sockerless-amplify-build-*")
	if err != nil {
		return failProvision("workspace: %v", err)
	}
	defer os.RemoveAll(workDir)
	provisionLog.Printf("# Cloning repository: %s (branch %s)", repo, branch)
	cloneOpts := &git.CloneOptions{
		URL:           repo,
		ReferenceName: plumbing.NewBranchReferenceName(branch),
		SingleBranch:  true,
		Depth:         1,
	}
	if connection, ok := amplifyRepositoryConnections.Get(appID); ok {
		_, token, decrypted := kmsDecryptBytes(connection.Ciphertext)
		if !decrypted {
			return failProvision("repository connection could not be decrypted")
		}
		cloneOpts.Auth = &githttp.BasicAuth{Username: connection.Username, Password: string(token)}
	}
	gitRepo, err := git.PlainClone(workDir, false, cloneOpts)
	if err != nil {
		return failProvision("git clone %s (branch %s): %v", repo, branch, err)
	}
	if head, err := gitRepo.Head(); err == nil {
		provisionLog.Printf("# HEAD %s", head.Hash())
		if commitID == "" || commitID == "HEAD" {
			amplifyJobs.Update(jobID, func(j *amplifyStoredJob) {
				j.Job.Summary.CommitId = head.Hash().String()
			})
		}
	}
	if strings.TrimSpace(specText) == "" {
		checkedIn, readErr := os.ReadFile(filepath.Join(workDir, "amplify.yml"))
		if readErr != nil {
			return failProvision("buildSpec error: repository has no readable amplify.yml: %v", readErr)
		}
		specText = string(checkedIn)
		provisionLog.Printf("# Build specification: amplify.yml")
	}
	spec, err := amplifyParseBuildSpec(specText, env["AMPLIFY_MONOREPO_APP_ROOT"])
	if err != nil {
		return failProvision("buildSpec error: %v", err)
	}
	projectDir, err := amplifyBuildProjectDirectory(workDir, spec.AppRoot, spec.BuildPath)
	if err != nil {
		return failProvision("buildSpec path error: %v", err)
	}
	if err := amplifyRestoreBuildCache(appID, branch, workDir); err != nil {
		return failProvision("restore build cache: %v", err)
	}
	for key, value := range spec.Environment {
		if _, configured := env[key]; !configured {
			env[key] = value
		}
	}
	provisionLog.Printf("# Build image: %s", amplifyBuildImage())
	finishStep("PROVISION", provisionLog, AmplifyJobStatusSucceed)

	// BUILD: preBuild + build commands in one shell (env exports persist
	// across phases, the way Amplify's build container runs them).
	buildLog := &amplifyStepLog{}
	amplifyStartJobStep(jobID, "BUILD")
	var script strings.Builder
	script.WriteString("set -e\n")
	for _, phase := range [][]string{spec.BackendCommands, spec.FrontendCommands, spec.TestCommands} {
		for _, command := range phase {
			script.WriteString(command + "\n")
		}
	}
	projectRelative, _ := filepath.Rel(workDir, projectDir)
	handle, err := sim.StartContainerSync(sim.ContainerConfig{
		Image:        amplifyBuildImage(),
		Architecture: "linux/amd64",
		Command:      []string{"/bin/sh", "-c", script.String()},
		WorkingDir:   path.Join("/workspace", filepath.ToSlash(projectRelative)),
		// The build writes real artifacts back into this workspace. A shared
		// SELinux relabel gives the confined build container that access on
		// enforcing hosts and is accepted as a no-op by Docker elsewhere.
		Binds:   []string{workDir + ":/workspace:z"},
		Env:     env,
		Timeout: amplifyBuildTimeout,
		Labels:  map[string]string{"sockerless-amplify-job": jobID},
		Sandbox: sim.SandboxFargate,
	}, buildLog)
	if err != nil {
		buildLog.Printf("# start build container: %v", err)
		finishStep("BUILD", buildLog, AmplifyJobStatusFailed)
		return AmplifyJobStatusFailed
	}
	amplifyRegisterBuildCancel(jobID, handle.Cancel)
	result := handle.Wait()
	amplifyUnregisterBuildCancel(jobID)
	if result.Error != nil {
		buildLog.Printf("# build container error: %v", result.Error)
		finishStep("BUILD", buildLog, AmplifyJobStatusFailed)
		return AmplifyJobStatusFailed
	}
	if result.ExitCode != 0 {
		buildLog.Printf("# build exited with status %d", result.ExitCode)
		finishStep("BUILD", buildLog, AmplifyJobStatusFailed)
		return AmplifyJobStatusFailed
	}
	if err := amplifySaveBuildCache(appID, branch, workDir, spec.CachePaths); err != nil {
		buildLog.Printf("# save build cache: %v", err)
		finishStep("BUILD", buildLog, AmplifyJobStatusFailed)
		return AmplifyJobStatusFailed
	}
	if err := amplifyCollectEndToEndTestArtifacts(
		urlBase, appID, branch, jobID, projectDir, spec, buildLog,
	); err != nil {
		buildLog.Printf("# collect end-to-end test artifacts: %v", err)
		finishStep("BUILD", buildLog, AmplifyJobStatusFailed)
		return AmplifyJobStatusFailed
	}
	finishStep("BUILD", buildLog, AmplifyJobStatusSucceed)

	// DEPLOY: collect baseDirectory into the job's artifact zip.
	deployLog := &amplifyStepLog{}
	amplifyStartJobStep(jobID, "DEPLOY")
	zipBytes, fileCount, err := amplifyZipArtifacts(filepath.Join(projectDir, spec.BaseDirectory), spec.Files, deployLog)
	if err != nil {
		deployLog.Printf("# artifact collection: %v", err)
		finishStep("DEPLOY", deployLog, AmplifyJobStatusFailed)
		return AmplifyJobStatusFailed
	}
	if manifestErr := amplifyBuildOutputManifestError(appID, zipBytes); manifestErr != nil {
		deployLog.Printf("!!! CustomerError: We failed to validate the deploy-manifest.json file found in your build output directory. %v", manifestErr)
		finishStep("DEPLOY", deployLog, AmplifyJobStatusFailed)
		return AmplifyJobStatusFailed
	}
	key := "artifacts/" + appID + "/" + branch + "/" + jobID + "/artifacts.zip"
	amplifyPutS3Object(key, "application/zip", zipBytes)
	amplifyRegisterJobArtifact(urlBase, appID, branch, jobID, amplifyArtifactID(jobID), "artifacts.zip", key)
	amplifySetJobStepArtifactsURL(jobID, "BUILD", amplifyPresignedS3URLBase(urlBase, key, http.MethodGet))
	deployLog.Printf("# deployed %d files (%d bytes) from %s", fileCount, len(zipBytes), spec.BaseDirectory)
	finishStep("DEPLOY", deployLog, AmplifyJobStatusSucceed)
	return AmplifyJobStatusSucceed
}

func amplifyCollectEndToEndTestArtifacts(
	urlBase, appID, branch, jobID, projectDir string,
	spec amplifyBuildSpec,
	log *amplifyStepLog,
) error {
	if spec.TestBaseDirectory == "" || len(spec.TestFiles) == 0 {
		return nil
	}
	baseDirectory := filepath.Join(projectDir, spec.TestBaseDirectory)
	testZip, count, err := amplifyZipArtifacts(baseDirectory, spec.TestFiles, log)
	if err != nil {
		return err
	}
	if count == 0 {
		return nil
	}
	aggregateKey := "test-artifacts/" + appID + "/" + branch + "/" + jobID + "/test-artifacts.zip"
	amplifyPutS3Object(aggregateKey, "application/zip", testZip)
	aggregateURL := amplifyPresignedS3URLBase(urlBase, aggregateKey, http.MethodGet)
	amplifyRegisterAuxiliaryArtifact(
		urlBase, appID, branch, jobID, amplifyArtifactID(jobID), "test-artifacts.zip", aggregateKey,
	)

	var configURL string
	err = filepath.WalkDir(baseDirectory, func(filePath string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		relative, err := filepath.Rel(baseDirectory, filePath)
		if err != nil {
			return err
		}
		relative = filepath.ToSlash(relative)
		data, err := os.ReadFile(filePath)
		if err != nil {
			return err
		}
		key := "test-artifacts/" + appID + "/" + branch + "/" + jobID + "/files/" + relative
		if amplifyArtifactMatch(spec.TestFiles, relative) {
			amplifyPutS3Object(key, "application/octet-stream", data)
			amplifyRegisterEndToEndTestArtifact(
				urlBase, appID, branch, jobID, amplifyArtifactID(jobID), relative, key,
			)
		}
		if configURL == "" &&
			spec.TestConfigFilePath != "" &&
			amplifyArtifactMatch([]string{spec.TestConfigFilePath}, relative) {
			configKey := "test-artifacts/" + appID + "/" + branch + "/" + jobID + "/config/" + relative
			amplifyPutS3Object(configKey, "application/json", data)
			configURL = amplifyPresignedS3URLBase(urlBase, configKey, http.MethodGet)
			amplifyRegisterAuxiliaryArtifact(
				urlBase, appID, branch, jobID, amplifyArtifactID(jobID), relative, configKey,
			)
		}
		return nil
	})
	if err != nil {
		return err
	}
	amplifySetJobStepTestURLs(jobID, "BUILD", aggregateURL, configURL)
	log.Printf("# collected %d end-to-end test artifacts", count)
	return nil
}

// amplifyZipArtifacts zips baseDir's contents filtered by the buildSpec's
// files patterns ('**/*' or empty = everything; otherwise path.Match against
// the slash-separated relative path).
// amplifyBuildOutputManifestError reports why the build output's
// deploy-manifest.json is invalid for a manifest-consuming platform; nil
// when the platform doesn't consume the manifest, the output carries none,
// or the manifest parses.
func amplifyBuildOutputManifestError(appID string, zipBytes []byte) error {
	stored, ok := amplifyApps.Get(appID)
	if !ok || !amplifyPlatformUsesManifest(stored.App.Platform) {
		return nil
	}
	zr, err := zip.NewReader(bytes.NewReader(zipBytes), int64(len(zipBytes)))
	if err != nil {
		return fmt.Errorf("read build output archive: %w", err)
	}
	for _, f := range zr.File {
		if path.Clean(strings.TrimPrefix(f.Name, "/")) != "deploy-manifest.json" {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return fmt.Errorf("read deploy-manifest.json: %w", err)
		}
		data, err := io.ReadAll(rc)
		_ = rc.Close()
		if err != nil {
			return fmt.Errorf("read deploy-manifest.json: %w", err)
		}
		_, parseErr := amplifyParseDeployManifest(data)
		return parseErr
	}
	return nil
}

func amplifyZipArtifacts(baseDir string, patterns []string, log *amplifyStepLog) ([]byte, int, error) {
	info, err := os.Stat(baseDir)
	if err != nil || !info.IsDir() {
		return nil, 0, fmt.Errorf("artifacts baseDirectory %s not found after build", filepath.Base(baseDir))
	}
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	count := 0
	err = filepath.WalkDir(baseDir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, err := filepath.Rel(baseDir, p)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if !amplifyArtifactMatch(patterns, rel) {
			return nil
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		f, err := zw.Create(rel)
		if err != nil {
			return err
		}
		if _, err := f.Write(data); err != nil {
			return err
		}
		log.Printf("# artifact: %s (%d bytes)", rel, len(data))
		count++
		return nil
	})
	if err != nil {
		return nil, 0, err
	}
	if count == 0 {
		return nil, 0, fmt.Errorf("no files matched artifacts.files in %s", filepath.Base(baseDir))
	}
	if err := zw.Close(); err != nil {
		return nil, 0, err
	}
	return buf.Bytes(), count, nil
}

func amplifyArtifactMatch(patterns []string, rel string) bool {
	if len(patterns) == 0 {
		return true
	}
	for _, pattern := range patterns {
		if pattern == "**/*" || pattern == "**" {
			return true
		}
		if ok, _ := path.Match(pattern, rel); ok {
			return true
		}
	}
	return false
}

func amplifyBuildProjectDirectory(workDir, appRoot, buildPath string) (string, error) {
	projectDir := workDir
	if appRoot != "" && appRoot != "." {
		projectDir = filepath.Join(workDir, filepath.FromSlash(appRoot))
	}
	switch buildPath {
	case "", ".":
	case "/":
		projectDir = workDir
	default:
		projectDir = filepath.Join(projectDir, filepath.FromSlash(buildPath))
	}
	clean := filepath.Clean(projectDir)
	relative, err := filepath.Rel(workDir, clean)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("appRoot/buildPath escapes the checked-out repository")
	}
	info, err := os.Stat(clean)
	if err != nil {
		return "", fmt.Errorf("build directory %s: %w", filepath.ToSlash(relative), err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("build directory %s is not a directory", filepath.ToSlash(relative))
	}
	return clean, nil
}

// amplifyBuildCacheRoot returns the on-disk root for AWS Amplify Hosting
// build caches. Real Amplify persists the build cache across builds, so the
// cache lives under <SIM_DATA_DIR>/amplify-cache when SIM_DATA_DIR — the
// persistence coordinate the shared server config reads — is set, and a temp
// directory otherwise.
func amplifyBuildCacheRoot() string {
	return simScopedDataDir("", "amplify-cache", "sockerless-amplify-cache")
}

func amplifyBuildCacheDirectory(appID, branch string) string {
	return filepath.Join(
		amplifyBuildCacheRoot(),
		appID,
		fmt.Sprintf("%x", sha256.Sum256([]byte(branch))),
	)
}

func amplifyRestoreBuildCache(appID, branch, workDir string) error {
	cacheDir := amplifyBuildCacheDirectory(appID, branch)
	if _, err := os.Stat(cacheDir); os.IsNotExist(err) {
		return nil
	} else if err != nil {
		return err
	}
	return amplifyCopyTree(cacheDir, workDir)
}

func amplifySaveBuildCache(appID, branch, workDir string, patterns []string) error {
	if len(patterns) == 0 {
		return nil
	}
	cacheDir := amplifyBuildCacheDirectory(appID, branch)
	if err := os.RemoveAll(cacheDir); err != nil {
		return err
	}
	if err := os.MkdirAll(cacheDir, 0o700); err != nil {
		return err
	}
	for _, pattern := range patterns {
		pattern = filepath.Clean(filepath.FromSlash(pattern))
		for _, suffix := range []string{
			string(filepath.Separator) + "**" + string(filepath.Separator) + "*",
			string(filepath.Separator) + "**",
			string(filepath.Separator) + "*",
		} {
			pattern = strings.TrimSuffix(pattern, suffix)
		}
		if pattern == "." || filepath.IsAbs(pattern) ||
			pattern == ".." || strings.HasPrefix(pattern, ".."+string(filepath.Separator)) {
			continue
		}
		matches, err := filepath.Glob(filepath.Join(workDir, pattern))
		if err != nil {
			return err
		}
		for _, source := range matches {
			relative, err := filepath.Rel(workDir, source)
			if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
				continue
			}
			if err := amplifyCopyTree(source, filepath.Join(cacheDir, relative)); err != nil {
				return err
			}
		}
	}
	return nil
}

func amplifyCopyTree(source, destination string) error {
	info, err := os.Lstat(source)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return amplifyCopyBuildFile(source, destination, info)
	}
	return filepath.WalkDir(source, func(current string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relative, err := filepath.Rel(source, current)
		if err != nil {
			return err
		}
		target := destination
		if relative != "." {
			target = filepath.Join(destination, relative)
		}
		entryInfo, err := entry.Info()
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return os.MkdirAll(target, entryInfo.Mode().Perm())
		}
		return amplifyCopyBuildFile(current, target, entryInfo)
	})
}

func amplifyCopyBuildFile(source, destination string, info fs.FileInfo) error {
	if err := os.MkdirAll(filepath.Dir(destination), 0o700); err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		target, err := os.Readlink(source)
		if err != nil {
			return err
		}
		_ = os.Remove(destination)
		return os.Symlink(target, destination)
	}
	if !info.Mode().IsRegular() {
		return nil
	}
	input, err := os.Open(source)
	if err != nil {
		return err
	}
	defer input.Close()
	output, err := os.OpenFile(destination, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, info.Mode().Perm())
	if err != nil {
		return err
	}
	if _, err := io.Copy(output, input); err != nil {
		_ = output.Close()
		return err
	}
	return output.Close()
}

func amplifyRemoveBuildCache(appID, branch string) {
	target := filepath.Join(amplifyBuildCacheRoot(), appID)
	if branch != "" {
		target = filepath.Join(target, fmt.Sprintf("%x", sha256.Sum256([]byte(branch))))
	}
	_ = os.RemoveAll(target)
}

// amplifyBuildEnv merges the app- and branch-level environment variables
// (branch wins) plus the standard variables real Amplify injects into every
// build.
func amplifyBuildEnv(app AmplifyApp, br AmplifyBranch, jobID string) map[string]string {
	env := map[string]string{}
	for k, v := range app.EnvironmentVariables {
		env[k] = v
	}
	for k, v := range br.EnvironmentVariables {
		env[k] = v
	}
	env["AWS_APP_ID"] = app.AppId
	env["AWS_BRANCH"] = br.BranchName
	env["AWS_JOB_ID"] = jobID
	return env
}
