package main

import (
	"context"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
	dockerclient "github.com/moby/moby/client"
)

// gceMetadataDefaultProject is the project a metadata read from outside every
// workload is answered for: the simulator's default project, which Cloud
// Resource Manager holds from the start.
const gceMetadataDefaultProject = "sockerless"

// gceMetadataWorkload is the resource a metadata request comes from: the
// project it runs in and the service account it runs as. An empty
// serviceAccount means the resource names none and runs as the project's
// Compute Engine default service account, as Compute Engine and Cloud Run do.
type gceMetadataWorkload struct {
	project        string
	serviceAccount string
}

// gceMetadataWorkloadFor places the caller of a metadata request: a Compute
// Engine instance by its private address, a Cloud Run container by the address
// of its network namespace. A caller that is neither reads the server as a
// workload of the default project.
func gceMetadataWorkloadFor(r *http.Request) gceMetadataWorkload {
	if inst, ok := gcpMetadataInstancesByIP.ForRequest(r); ok {
		w := gceMetadataWorkload{project: gcpProjectFromSelfLink(inst.SelfLink)}
		if len(inst.ServiceAccounts) > 0 {
			if email, _ := inst.ServiceAccounts[0]["email"].(string); email != "default" {
				w.serviceAccount = email
			}
		}
		if w.project != "" {
			return w
		}
	}
	if w, ok := cloudRunMetadataWorkload(r); ok {
		return w
	}
	return gceMetadataWorkload{project: gceMetadataDefaultProject}
}

// cloudRunMetadataWorkload finds the simulator container whose network
// namespace owns the request's source address and reads the Cloud Run
// resource its labels name. Sidecars join the first container's namespace, so
// every container of an instance resolves to the same resource.
func cloudRunMetadataWorkload(r *http.Request) (gceMetadataWorkload, bool) {
	docker := sim.DockerClient()
	if docker == nil {
		return gceMetadataWorkload{}, false
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	addr, err := netip.ParseAddr(host)
	if err != nil || addr.IsLoopback() {
		return gceMetadataWorkload{}, false
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	list, err := docker.ContainerList(ctx, dockerclient.ContainerListOptions{
		Filters: dockerclient.Filters{}.Add("label", "sockerless-sim=true"),
	})
	if err != nil {
		return gceMetadataWorkload{}, false
	}
	for _, c := range list.Items {
		if c.NetworkSettings == nil {
			continue
		}
		for _, ep := range c.NetworkSettings.Networks {
			if ep != nil && ep.IPAddress == addr.Unmap() {
				return cloudRunWorkloadFromLabels(c.Labels)
			}
		}
	}
	return gceMetadataWorkload{}, false
}

// cloudRunWorkloadFromLabels reads the service account of the Cloud Run
// service, instance, job execution or worker pool a container belongs to.
func cloudRunWorkloadFromLabels(labels map[string]string) (gceMetadataWorkload, bool) {
	if name := labels[cloudRunResourceLabel]; name != "" {
		w := gceMetadataWorkload{project: resourceProject(name)}
		switch {
		case strings.Contains(name, "/services/"):
			if svc, ok := crv2Services.Get(name); ok && svc.Template != nil {
				w.serviceAccount = svc.Template.ServiceAccount
			}
		case strings.Contains(name, "/instances/"):
			if inst, ok := crv2Instances.Get(name); ok {
				w.serviceAccount = inst.ServiceAccount
			}
		}
		return w, true
	}
	if name := labels[cloudRunTaskExecutionLabel]; name != "" {
		w := gceMetadataWorkload{project: resourceProject(name)}
		if exec, ok := crjExecutions.Get(name); ok && exec.Template != nil {
			w.serviceAccount = exec.Template.ServiceAccount
		}
		return w, true
	}
	if name := labels[cloudRunWorkerPoolLabel]; name != "" {
		w := gceMetadataWorkload{project: resourceProject(name)}
		if pool, ok := crv2WorkerPools.Get(name); ok && pool.Template != nil {
			w.serviceAccount = pool.Template.ServiceAccount
		}
		return w, true
	}
	return gceMetadataWorkload{}, false
}

// projectID is the workload's project ID, which Cloud Resource Manager
// resolves when the resource names its project by number.
func (w gceMetadataWorkload) projectID() string {
	if p, ok := crmResolveProject(w.project); ok {
		return p.ProjectId
	}
	return w.project
}

// projectNumber is the number Cloud Resource Manager assigned the workload's
// project, and false for a project it does not hold.
func (w gceMetadataWorkload) projectNumber() (string, bool) {
	return crmProjectNumber(w.project)
}

// defaultServiceAccount is the email the metadata server's `default` alias
// names, and false when the workload runs as no account: it names none and
// its project has no Compute Engine default service account.
func (w gceMetadataWorkload) defaultServiceAccount() (string, bool) {
	if w.serviceAccount != "" {
		return w.serviceAccount, true
	}
	number, ok := w.projectNumber()
	if !ok {
		return "", false
	}
	return computeDefaultServiceAccountEmail(number), true
}
