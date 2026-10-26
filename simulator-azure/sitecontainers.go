package main

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
	"github.com/e6qu/sockerless-cloud/sim/placement"
	"github.com/e6qu/sockerless-cloud/sim/workload"
	"github.com/e6qu/sockerless-cloud/sim/workloadhost"
	dockerclient "github.com/moby/moby/client"
)

// SiteContainer is Microsoft.Web/sites/{name}/sitecontainers/{containerName}.
// A multi-container site declares one main container (IsMain) and sidecars,
// all in one network namespace, so a sidecar binding a port is reachable from
// the main on localhost.
type SiteContainer struct {
	ID         string                  `json:"id,omitempty"`
	Name       string                  `json:"name,omitempty"`
	Type       string                  `json:"type,omitempty"`
	Kind       string                  `json:"kind,omitempty"`
	Properties SiteContainerProperties `json:"properties"`
}

// image and isMain are required.
type SiteContainerProperties struct {
	Image                                  string                  `json:"image"`
	TargetPort                             string                  `json:"targetPort,omitempty"`
	IsMain                                 bool                    `json:"isMain"`
	StartUpCommand                         string                  `json:"startUpCommand,omitempty"`
	AuthType                               string                  `json:"authType,omitempty"`
	UserName                               string                  `json:"userName,omitempty"`
	PasswordSecret                         string                  `json:"passwordSecret,omitempty"`
	UserManagedIdentityClientID            string                  `json:"userManagedIdentityClientId,omitempty"`
	InheritAppSettingsAndConnectionStrings *bool                   `json:"inheritAppSettingsAndConnectionStrings,omitempty"`
	EnvironmentVariables                   []SiteContainerEnvVar   `json:"environmentVariables,omitempty"`
	VolumeMounts                           []SiteContainerVolMount `json:"volumeMounts,omitempty"`
	CreatedTime                            string                  `json:"createdTime,omitempty"`
	LastModifiedTime                       string                  `json:"lastModifiedTime,omitempty"`
}

type SiteContainerEnvVar struct {
	Name  string `json:"name"`
	Value string `json:"value,omitempty"`
}

type SiteContainerVolMount struct {
	ContainerMountPath string `json:"containerMountPath,omitempty"`
	VolumeSubPath      string `json:"volumeSubPath,omitempty"`
	Data               string `json:"data,omitempty"`
	ReadOnly           bool   `json:"readOnly,omitempty"`
}

// Keyed by <siteID>/sitecontainers/<name>.
var azfSiteContainers sim.Store[SiteContainer]

// The front end asks for a site's containers on every request to its
// hostname.
var azfSiteContainersBySite sim.GenerationIndex[SiteContainer]

func siteContainersFor(siteID string) []SiteContainer {
	return azfSiteContainersBySite.LookupAll(azfSiteContainers, siteID, func(c SiteContainer) []string {
		i := strings.LastIndex(c.ID, "/sitecontainers/")
		if i < 0 {
			return nil
		}
		return []string{c.ID[:i]}
	})
}

// Nil when the site has no sitecontainers.
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
	// No member flagged main: fall back to the first, deterministic by name.
	main := containers[0]
	return &main
}

func sidecarSiteContainers(siteID string) []SiteContainer {
	var out []SiteContainer
	for _, c := range siteContainersFor(siteID) {
		if !c.Properties.IsMain {
			out = append(out, c)
		}
	}
	return out
}

// Quotes are honoured so an embedded script (`sh -c 'while ...; done'`) stays
// one argument. Azure runs startUpCommand as the container's command; it is
// passed as args, keeping the image entrypoint. Empty gives nil.
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

// Each entry names an app setting, and the container gets its value, a Key
// Vault reference resolved: "If the AppSetting is not found, the value will be
// set to an empty string in the container at runtime."
func siteContainerEnv(vars []SiteContainerEnvVar, appSettings map[string]string) map[string]string {
	if len(vars) == 0 {
		return nil
	}
	m := make(map[string]string, len(vars))
	for _, v := range vars {
		m[v.Name] = appSettings[v.Value]
	}
	return m
}

// All app settings and connection strings unless
// inheritAppSettingsAndConnectionStrings is false.
func (sc *SiteContainer) inheritedEnv(references webReferenceValues) map[string]string {
	if inherit := sc.Properties.InheritAppSettingsAndConnectionStrings; inherit != nil && !*inherit {
		return nil
	}
	return workloadhost.MergeEnv(references.appSettings, references.connectionStrings)
}

// Members of one site mounting the same VolumeSubPath share one named volume,
// standing in for the site-level Azure Files share.
func siteContainerVolumeBinds(siteName string, mounts []SiteContainerVolMount) []string {
	var binds []string
	for _, vm := range mounts {
		if vm.VolumeSubPath == "" || vm.ContainerMountPath == "" {
			continue
		}
		vol := fmt.Sprintf("skls-azf-%s-%s", siteStorageName(siteName), vm.VolumeSubPath)
		spec := vol + ":" + vm.ContainerMountPath
		if vm.ReadOnly {
			spec += ":ro"
		}
		binds = append(binds, spec)
	}
	return binds
}

// Each sidecar joins the main's network namespace, so localhost:<port>
// reaches it. A sidecar that does not start fails the site's start, as on App
// Service; the sidecars already started are stopped and the main is the
// caller's to stop. references are the resolved app settings and connection
// strings; metadataHost is the main network's sidecar, which they share.
func startSidecarContainers(ctx context.Context, site *Site, mainContainerID string, references webReferenceValues, metadataHost string, sink sim.LogSink) ([]placement.Handle, error) {
	sidecars := sidecarSiteContainers(site.ID)
	if len(sidecars) == 0 {
		return nil, nil
	}
	metadataEnv, err := hostMetadataEnv(site, metadataHost)
	if err != nil {
		return nil, fmt.Errorf("sidecars: resolve the metadata endpoint: %w", err)
	}
	members := make([]workload.Container, 0, len(sidecars))
	for _, sc := range sidecars {
		members = append(members, workload.Container{Name: sc.Name, Config: sim.ContainerConfig{
			CancelGracePeriod: siteStopGrace(site),
			Image:             sim.ResolveLocalImage(sc.Properties.Image),
			RegistryAuth:      acrWorkloadRegistryAuth(sc.Properties.Image, siteWorkloadRegistries(site, sc.Properties.Image)),
			Args:              splitStartUpCommand(sc.Properties.StartUpCommand),
			Env:               workloadhost.MergeEnv(sc.inheritedEnv(references), siteContainerEnv(sc.Properties.EnvironmentVariables, references.appSettings), metadataEnv),
			Binds:             siteContainerVolumeBinds(site.Name, sc.Properties.VolumeMounts),
			Name:              fmt.Sprintf("sockerless-sim-azure-func-sidecar-%s-%s-%d", siteStorageName(site.Name), sc.Name, time.Now().UnixNano()),
			Labels: map[string]string{
				"sockerless-sim-type":           "azure-function-sidecar",
				"sockerless-site":               site.Name,
				"sockerless-sitecontainer":      sc.Name,
				"sockerless-sitecontainer-main": mainContainerID,
			},
			Sandbox: SandboxAZF,
		}})
	}
	backend, err := azurePlacementBackend(placementWeb)
	if err != nil {
		return nil, err
	}
	var handles []placement.Handle
	for _, member := range members {
		handle, err := workload.PlaceMember(ctx, backend, mainContainerID, "sidecar", member, sink)
		if err != nil {
			for _, started := range handles {
				started.Cancel()
			}
			injectSiteTrace(site, fmt.Sprintf("sitecontainers: %v", err))
			return nil, err
		}
		handles = append(handles, handle)
	}
	return handles, nil
}

// The shared volumes persist across stages, so they go only when the site is
// deleted.
func cleanupSiteContainers(siteID, siteName string) {
	if azfSiteContainers == nil {
		return
	}
	vols := map[string]bool{}
	for _, sc := range siteContainersFor(siteID) {
		for _, vm := range sc.Properties.VolumeMounts {
			if vm.VolumeSubPath != "" {
				vols[fmt.Sprintf("skls-azf-%s-%s", siteStorageName(siteName), vm.VolumeSubPath)] = true
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

	// A slot resolves through its own record and keys its containers under its
	// own resource ID, so its sitecontainers are never the production site's.
	both := func(method, suffix string, h http.HandlerFunc) {
		srv.HandleFunc(method+" "+armBase+"/sites/{siteName}"+suffix, h)
		srv.HandleFunc(method+" "+armBase+"/sites/{siteName}/slots/{slot}"+suffix, h)
	}
	containerID := func(r *http.Request) string {
		return webResourceID(r) + "/sitecontainers/" + sim.PathParam(r, "containerName")
	}

	// Azure answers 200 with the persisted resource.
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
		if req.Properties.InheritAppSettingsAndConnectionStrings == nil {
			inherit := true
			req.Properties.InheritAppSettingsAndConnectionStrings = &inherit
		}
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

	both("GET", "/sitecontainers/{containerName}", func(w http.ResponseWriter, r *http.Request) {
		sc, ok := azfSiteContainers.Get(containerID(r))
		if !ok {
			AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
				"sitecontainer %q not found", sim.PathParam(r, "containerName"))
			return
		}
		sim.WriteJSON(w, http.StatusOK, sc)
	})

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

	both("DELETE", "/sitecontainers/{containerName}", func(w http.ResponseWriter, r *http.Request) {
		existed := azfSiteContainers.Delete(containerID(r))
		if !existed {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		w.WriteHeader(http.StatusOK)
	})
}
