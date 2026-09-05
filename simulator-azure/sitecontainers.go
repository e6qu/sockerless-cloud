package main

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
	dockerclient "github.com/moby/moby/client"
)

// SiteContainer is the ARM resource
// Microsoft.Web/sites/{name}/sitecontainers/{containerName}. App Service
// multi-container ("sidecar containers") sites declare one main container
// (IsMain=true) plus N sidecars; all members of a site share one network
// namespace, so a sidecar that binds a port is reachable from the main on
// localhost:<port> — the faithful Azure analog of an ACA App / Cloud Run
// multi-container revision.
type SiteContainer struct {
	ID         string                  `json:"id,omitempty"`
	Name       string                  `json:"name,omitempty"`
	Type       string                  `json:"type,omitempty"`
	Kind       string                  `json:"kind,omitempty"`
	Properties SiteContainerProperties `json:"properties"`
}

// SiteContainerProperties mirrors armappservice.SiteContainerProperties.
// image + isMain are REQUIRED per the ARM schema. EnvironmentVariables on
// real Azure name AppSettings that are resolved at runtime; the simulator
// realizes them as literal container env so a workload can read them
// directly.
type SiteContainerProperties struct {
	Image                                  string                  `json:"image"`
	TargetPort                             string                  `json:"targetPort,omitempty"`
	IsMain                                 bool                    `json:"isMain"`
	StartUpCommand                         string                  `json:"startUpCommand,omitempty"`
	AuthType                               string                  `json:"authType,omitempty"`
	UserName                               string                  `json:"userName,omitempty"`
	PasswordSecret                         string                  `json:"passwordSecret,omitempty"`
	UserManagedIdentityClientID            string                  `json:"userManagedIdentityClientId,omitempty"`
	InheritAppSettingsAndConnectionStrings bool                    `json:"inheritAppSettingsAndConnectionStrings,omitempty"`
	EnvironmentVariables                   []SiteContainerEnvVar   `json:"environmentVariables,omitempty"`
	VolumeMounts                           []SiteContainerVolMount `json:"volumeMounts,omitempty"`
	CreatedTime                            string                  `json:"createdTime,omitempty"`
	LastModifiedTime                       string                  `json:"lastModifiedTime,omitempty"`
}

// SiteContainerEnvVar mirrors armappservice.EnvironmentVariable.
type SiteContainerEnvVar struct {
	Name  string `json:"name"`
	Value string `json:"value,omitempty"`
}

// SiteContainerVolMount mirrors armappservice.VolumeMount.
type SiteContainerVolMount struct {
	ContainerMountPath string `json:"containerMountPath,omitempty"`
	VolumeSubPath      string `json:"volumeSubPath,omitempty"`
	Data               string `json:"data,omitempty"`
	ReadOnly           bool   `json:"readOnly,omitempty"`
}

// Package-level store: sitecontainers keyed by their full ARM resource ID
// (<siteID>/sitecontainers/<name>).
var azfSiteContainers sim.Store[SiteContainer]

// siteContainersFor returns every sitecontainer belonging to a site,
// querying the cloud store (the source of truth) by resource-ID prefix.
func siteContainersFor(siteID string) []SiteContainer {
	prefix := siteID + "/sitecontainers/"
	return azfSiteContainers.Filter(func(c SiteContainer) bool {
		return strings.HasPrefix(c.ID, prefix)
	})
}

// mainSiteContainer returns the IsMain member of a site's sitecontainers,
// or nil if the site has no sitecontainers.
func mainSiteContainer(siteID string) *SiteContainer {
	containers := siteContainersFor(siteID)
	if len(containers) == 0 {
		return nil
	}
	for i := range containers {
		if containers[i].Properties.IsMain {
			return &containers[i]
		}
	}
	// No member flagged main: fall back to the first (deterministic by name).
	main := containers[0]
	return &main
}

// sidecarSiteContainers returns the non-main members of a site.
func sidecarSiteContainers(siteID string) []SiteContainer {
	var out []SiteContainer
	for _, c := range siteContainersFor(siteID) {
		if !c.Properties.IsMain {
			out = append(out, c)
		}
	}
	return out
}

// splitStartUpCommand turns a sitecontainer startUpCommand string into argv,
// honoring single/double quotes so an embedded shell script (e.g.
// `sh -c 'while ...; done'`) survives as one argument. Real Azure runs the
// startUpCommand as the container's command; the simulator supplies it as the
// container's command (args), preserving the image entrypoint. Empty → nil.
func splitStartUpCommand(s string) []string {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	var args []string
	var cur strings.Builder
	inWord := false
	var quote rune // 0, '\'' or '"'
	for _, r := range s {
		switch {
		case quote != 0:
			if r == quote {
				quote = 0
			} else {
				cur.WriteRune(r)
			}
			inWord = true
		case r == '\'' || r == '"':
			quote = r
			inWord = true
		case r == ' ' || r == '\t' || r == '\n':
			if inWord {
				args = append(args, cur.String())
				cur.Reset()
				inWord = false
			}
		default:
			cur.WriteRune(r)
			inWord = true
		}
	}
	if inWord {
		args = append(args, cur.String())
	}
	return args
}

// envVarsMap flattens a sitecontainer's EnvironmentVariables to a map for
// container env injection.
func envVarsMap(vars []SiteContainerEnvVar) map[string]string {
	if len(vars) == 0 {
		return nil
	}
	m := make(map[string]string, len(vars))
	for _, v := range vars {
		m[v.Name] = v.Value
	}
	return m
}

// siteContainerVolumeBinds realizes a sitecontainer's VolumeMounts as Docker
// bind specs against a per-(site, volume) named volume. Pod members of the
// same site that mount the same VolumeSubPath share one Docker volume — the
// shared workspace, the sim's realization of the site-level Azure Files share
// every sitecontainer references.
func siteContainerVolumeBinds(siteName string, mounts []SiteContainerVolMount) []string {
	var binds []string
	for _, vm := range mounts {
		if vm.VolumeSubPath == "" || vm.ContainerMountPath == "" {
			continue
		}
		vol := fmt.Sprintf("skls-azf-%s-%s", siteName, vm.VolumeSubPath)
		spec := vol + ":" + vm.ContainerMountPath
		if vm.ReadOnly {
			spec += ":ro"
		}
		binds = append(binds, spec)
	}
	return binds
}

// startSidecarContainers launches every non-main sitecontainer of a site so
// it shares the main container's network namespace (NetworkMode
// container:<mainID>). A sidecar that binds localhost:<port> is then
// reachable from the main on localhost:<port> — the App Service
// multi-container loopback contract. Handles are returned so the caller can
// tear them down with the main on invoke completion.
func startSidecarContainers(site *Site, mainContainerID string, sink sim.LogSink) []*sim.ContainerHandle {
	sidecars := sidecarSiteContainers(site.ID)
	if len(sidecars) == 0 {
		return nil
	}
	var handles []*sim.ContainerHandle
	for _, sc := range sidecars {
		localImage := sim.ResolveLocalImage(sc.Properties.Image)
		platform, err := localImagePlatform(context.Background(), localImage)
		if err != nil {
			injectAppTrace(site.Name, fmt.Sprintf("sidecar %q: resolve image platform failed: %v", sc.Name, err))
			continue
		}
		handle, err := sim.StartContainerSync(sim.ContainerConfig{
			CancelGracePeriod: 5 * time.Second,
			Image:             localImage,
			Architecture:      platform,
			Args:              splitStartUpCommand(sc.Properties.StartUpCommand),
			Env:               mergeEnv(envVarsMap(sc.Properties.EnvironmentVariables), hostMetadataEnv()),
			Binds:             siteContainerVolumeBinds(site.Name, sc.Properties.VolumeMounts),
			Name:              fmt.Sprintf("sockerless-sim-azure-func-sidecar-%s-%s-%d", site.Name, sc.Name, time.Now().UnixNano()),
			Labels: map[string]string{
				"sockerless-sim-type":           "azure-function-sidecar",
				"sockerless-site":               site.Name,
				"sockerless-sitecontainer":      sc.Name,
				"sockerless-sitecontainer-main": mainContainerID,
			},
			NetworkMode: "container:" + mainContainerID,
			Sandbox:     SandboxAZF,
		}, sink)
		if err != nil {
			injectAppTrace(site.Name, fmt.Sprintf("sidecar %q: start failed: %v", sc.Name, err))
			continue
		}
		handles = append(handles, handle)
	}
	return handles
}

// cleanupSiteContainers removes a deleted site's sitecontainers from the
// store and best-effort removes the shared Docker volumes their VolumeMounts
// realized (the pod workspace persists across stages, so it's torn down only
// when the site is deleted).
func cleanupSiteContainers(siteID, siteName string) {
	if azfSiteContainers == nil {
		return
	}
	vols := map[string]bool{}
	for _, sc := range siteContainersFor(siteID) {
		for _, vm := range sc.Properties.VolumeMounts {
			if vm.VolumeSubPath != "" {
				vols[fmt.Sprintf("skls-azf-%s-%s", siteName, vm.VolumeSubPath)] = true
			}
		}
		azfSiteContainers.Delete(sc.ID)
	}
	if cli := sim.DockerClient(); cli != nil {
		for v := range vols {
			_, _ = cli.VolumeRemove(context.Background(), v, dockerclient.VolumeRemoveOptions{Force: true})
		}
	}
}

func registerSiteContainerHandlers(srv *sim.Server, armBase string) {
	azfSiteContainers = sim.MakeStore[SiteContainer](srv.DB(), "azf_sitecontainers")

	// Every operation exists identically on a production site and on a
	// deployment slot; the addressed level resolves through its own resource
	// record (azfSites / webSlots) and its containers key under its own
	// resource ID, so a slot's sitecontainers are never the production
	// site's.
	both := func(method, suffix string, h http.HandlerFunc) {
		srv.HandleFunc(method+" "+armBase+"/sites/{siteName}"+suffix, h)
		srv.HandleFunc(method+" "+armBase+"/sites/{siteName}/slots/{slot}"+suffix, h)
	}
	containerID := func(r *http.Request) string {
		return webResourceID(r) + "/sitecontainers/" + sim.PathParam(r, "containerName")
	}

	// PUT — create or update a sitecontainer. Real Azure returns 200 with
	// the persisted resource.
	both("PUT", "/sitecontainers/{containerName}", func(w http.ResponseWriter, r *http.Request) {
		if webMissing(w, r) {
			return
		}
		name := sim.PathParam(r, "containerName")

		var req SiteContainer
		if err := sim.ReadJSON(r, &req); err != nil {
			AzureError(w, "InvalidRequestContent", "Failed to parse request body: "+err.Error(), http.StatusBadRequest)
			return
		}
		if req.Properties.Image == "" {
			AzureError(w, "InvalidRequestContent", "The 'properties.image' property is required.", http.StatusBadRequest)
			return
		}

		id := containerID(r)
		now := time.Now().UTC().Format(time.RFC3339)
		created := now
		if existing, ok := azfSiteContainers.Get(id); ok {
			created = existing.Properties.CreatedTime
		}
		req.Properties.CreatedTime = created
		req.Properties.LastModifiedTime = now

		sc := SiteContainer{
			ID:         id,
			Name:       name,
			Type:       webChildType(r, "sitecontainers"),
			Kind:       req.Kind,
			Properties: req.Properties,
		}
		azfSiteContainers.Put(id, sc)
		sim.WriteJSON(w, http.StatusOK, sc)
	})

	// GET — read a single sitecontainer.
	both("GET", "/sitecontainers/{containerName}", func(w http.ResponseWriter, r *http.Request) {
		sc, ok := azfSiteContainers.Get(containerID(r))
		if !ok {
			AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
				"sitecontainer %q not found", sim.PathParam(r, "containerName"))
			return
		}
		sim.WriteJSON(w, http.StatusOK, sc)
	})

	// GET (collection) — list all sitecontainers on a site or slot.
	both("GET", "/sitecontainers", func(w http.ResponseWriter, r *http.Request) {
		if webMissing(w, r) {
			return
		}
		list := siteContainersFor(webResourceID(r))
		if list == nil {
			list = []SiteContainer{}
		}
		sim.WriteJSON(w, http.StatusOK, map[string]any{"value": list})
	})

	// DELETE — remove a sitecontainer.
	both("DELETE", "/sitecontainers/{containerName}", func(w http.ResponseWriter, r *http.Request) {
		existed := azfSiteContainers.Delete(containerID(r))
		if !existed {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		w.WriteHeader(http.StatusOK)
	})
}
