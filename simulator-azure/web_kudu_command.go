package main

import (
	"context"
	"fmt"
	"net/http"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
	"github.com/e6qu/sockerless-cloud/sim/workload"
	"github.com/e6qu/sockerless-cloud/sim/workloadhost"
)

// web_kudu_command.go serves Kudu's command API: POST /api/command runs a
// shell command in the SCM site's environment — the site's image with its
// /home mounted and its app settings in the environment — from a directory
// relative to /home, and answers with what it wrote and its exit code. A
// command that writes nothing for SCM_COMMAND_IDLE_TIMEOUT seconds is
// killed. The command works on the site's persistent /home share, the one the
// app's containers mount.

// kuduSettingDefaults are the settings Kudu holds before any app setting or
// SCM settings write names them.
var kuduSettingDefaults = map[string]string{
	"deployment_branch":        "master",
	"SCM_TRACE_LEVEL":          "1",
	"SCM_COMMAND_IDLE_TIMEOUT": "60",
	"SCM_LOGSTREAM_TIMEOUT":    "7200",
	"SCM_BUILD_ARGS":           "",
}

// kuduSettingSeconds reads one of Kudu's timeout settings in seconds.
func kuduSettingSeconds(site *Site, name string) time.Duration {
	if v, err := strconv.Atoi(strings.TrimSpace(kuduSettings(site)[name])); err == nil && v > 0 {
		return time.Duration(v) * time.Second
	}
	v, _ := strconv.Atoi(kuduSettingDefaults[name])
	return time.Duration(v) * time.Second
}

// kuduCommandSink collects a command's output and reports each write.
type kuduCommandSink struct {
	mu       sync.Mutex
	stdout   strings.Builder
	stderr   strings.Builder
	activity chan struct{}
}

func (s *kuduCommandSink) WriteLog(line sim.LogLine) {
	s.mu.Lock()
	if line.Stream == "stderr" {
		s.stderr.WriteString(line.Text + "\n")
	} else {
		s.stdout.WriteString(line.Text + "\n")
	}
	s.mu.Unlock()
	select {
	case s.activity <- struct{}{}:
	default:
	}
}

func kuduCommand(w http.ResponseWriter, r *http.Request, site *Site) {
	var req struct {
		Command string `json:"command"`
		Dir     string `json:"dir"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		kuduWebAPIError(w, http.StatusBadRequest, err.Error())
		return
	}
	if strings.TrimSpace(req.Command) == "" {
		kuduWebAPIError(w, http.StatusBadRequest, "The request names no command.")
		return
	}
	dir := strings.TrimPrefix(path.Clean("/"+strings.ReplaceAll(req.Dir, `\`, "/")), "/")
	if rest, ok := strings.CutPrefix(dir, "home/"); ok && strings.HasPrefix(req.Dir, "/") {
		dir = rest
	}
	image := siteContainerImage(site)
	if platformImage, ok := sitePlatformImage(site); ok {
		image = platformImage
	}
	if image == "" {
		kuduWebAPIError(w, http.StatusConflict, siteImageMissing(site).Error())
		return
	}
	home, err := kuduOpenHome(site)
	if err != nil {
		kuduWebAPIError(w, http.StatusInternalServerError, err.Error())
		return
	}
	workdir := "/home"
	if dir != "" {
		workdir += "/" + dir
	}
	localImage := sim.ResolveLocalImage(image)
	idle := kuduSettingSeconds(site, "SCM_COMMAND_IDLE_TIMEOUT")
	registryAuth := acrWorkloadRegistryAuth(image, siteWorkloadRegistries(site, image))
	ctx, cancel := context.WithTimeout(r.Context(), siteStartTimeLimit(site))
	platform, err := workload.LocalImagePlatform(ctx, localImage, registryAuth)
	cancel()
	if err != nil {
		kuduWebAPIError(w, http.StatusInternalServerError, err.Error())
		return
	}
	metadataEnv, err := hostMetadataEnv(site)
	if err != nil {
		kuduWebAPIError(w, http.StatusInternalServerError, err.Error())
		return
	}
	env := workloadhost.MergeEnv(webResolvedAppSettings(site), siteConnectionStringEnv(site), appServicePlatformEnv(site), metadataEnv, map[string]string{"HOME": "/home"})
	binds := []string{home + ":/home"}
	if kuduWWWRootReadOnly(site) {
		wwwroot := filepath.Join(home, "site", "wwwroot")
		binds = append(binds, wwwroot+":/home/site/wwwroot:ro")
	}
	sink := &kuduCommandSink{activity: make(chan struct{}, 1)}
	handle, err := sim.StartContainerSyncContext(r.Context(), sim.ContainerConfig{
		Image:        localImage,
		Architecture: platform,
		RegistryAuth: registryAuth,
		Command:      []string{"/bin/sh", "-c", req.Command},
		Env:          env,
		WorkingDir:   workdir,
		Binds:        append(binds, siteAzureStorageBinds(site)...),
		Name:         fmt.Sprintf("sockerless-sim-azure-kudu-command-%s-%s", siteStorageName(site.Name), randomSuffix(6)),
		Labels: map[string]string{
			"sockerless-sim-type": "azure-kudu-command",
			"sockerless-site":     site.Name,
		},
		ExtraHosts: workloadhost.ExtraHosts(),
		Sandbox:    SandboxAZF,
	}, sink)
	if err != nil {
		kuduWebAPIError(w, http.StatusInternalServerError, err.Error())
		return
	}
	done := make(chan sim.ProcessResult, 1)
	go func() { done <- handle.Wait() }()
	timer := time.NewTimer(idle)
	defer timer.Stop()
	var result sim.ProcessResult
	timedOut := false
wait:
	for {
		select {
		case result = <-done:
			break wait
		case <-sink.activity:
			if !timer.Stop() {
				<-timer.C
			}
			timer.Reset(idle)
		case <-timer.C:
			timedOut = true
			handle.Cancel()
			result = <-done
			break wait
		case <-r.Context().Done():
			handle.Cancel()
			<-done
			return
		}
	}
	if err := webDiscoverWebJobs(site.ID); err != nil {
		kuduWebAPIError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if timedOut {
		kuduWebAPIError(w, http.StatusInternalServerError, fmt.Sprintf(
			"Command '%s' aborted due to no output and CPU activity for %d seconds. You can increase the SCM_COMMAND_IDLE_TIMEOUT app setting (or in the SCM settings) if needed.",
			req.Command, int(idle/time.Second)))
		return
	}
	if result.Error != nil && result.ExitCode == 0 {
		kuduWebAPIError(w, http.StatusInternalServerError, result.Error.Error())
		return
	}
	sink.mu.Lock()
	out := map[string]any{"Output": sink.stdout.String(), "Error": sink.stderr.String(), "ExitCode": result.ExitCode}
	sink.mu.Unlock()
	sim.WriteJSON(w, http.StatusOK, out)
}
