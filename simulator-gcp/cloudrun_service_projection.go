package main

import (
	"fmt"
	"strings"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
)

const cloudRunDefaultLocation = "us-central1"

var (
	crv1Services          sim.Store[CRService]
	crv1ReconcileChildren func(namespace string, service CRService, revision string)
	crv1DeleteChildren    func(namespace, service string)
	crv2Revisions         sim.Store[RevisionV2]
)

func parseCloudRunV2ServiceName(name string) (project, location, service string, ok bool) {
	parts := strings.Split(name, "/")
	if len(parts) != 6 || parts[0] != "projects" || parts[2] != "locations" || parts[4] != "services" {
		return "", "", "", false
	}
	return parts[1], parts[3], parts[5], true
}

// convertEach maps a container's list member through a converter. A container
// converts the same way in both directions — only the element types differ —
// so the mechanical part of both projections lives here once.
func convertEach[S, D any](src []S, convert func(S) D) []D {
	out := make([]D, 0, len(src))
	for _, item := range src {
		out = append(out, convert(item))
	}
	return out
}

// A secret-backed environment variable is the one member whose two spellings
// are not a type conversion: v2 names the secret and version, Knative names
// the secret and key.
func cloudRunV2EnvToV1(value EnvVar) CREnvVar {
	converted := CREnvVar{Name: value.Name, Value: value.Value}
	if value.ValueSource != nil && value.ValueSource.SecretKeyRef != nil {
		converted.Value = ""
		converted.ValueFrom = &CREnvVarSource{SecretKeyRef: &CRSecretKeySelector{
			Name: value.ValueSource.SecretKeyRef.Secret,
			Key:  value.ValueSource.SecretKeyRef.Version,
		}}
	}
	return converted
}

func cloudRunV1EnvToV2(value CREnvVar) EnvVar {
	converted := EnvVar{Name: value.Name, Value: value.Value}
	if value.ValueFrom != nil && value.ValueFrom.SecretKeyRef != nil {
		converted.Value = ""
		converted.ValueSource = &EnvVarSource{SecretKeyRef: &SecretKeySelector{
			Secret:  value.ValueFrom.SecretKeyRef.Name,
			Version: value.ValueFrom.SecretKeyRef.Key,
		}}
	}
	return converted
}

func cloudRunV2ContainerToV1(container Container) CRContainer {
	env := convertEach(container.Env, cloudRunV2EnvToV1)
	ports := convertEach(container.Ports, func(port ContainerPort) CRPort { return CRPort(port) })
	mounts := convertEach(container.VolumeMounts, func(m VolumeMount) CRVolumeMount { return CRVolumeMount(m) })
	converted := CRContainer{
		Name: container.Name, Image: container.Image, Command: container.Command,
		Args: container.Args, Env: env, Ports: ports, WorkingDir: container.WorkingDir,
	}
	if len(mounts) > 0 {
		converted.VolumeMounts = mounts
	}
	if container.Resources != nil {
		converted.Resources = &CRResourceRequirements{Limits: container.Resources.Limits}
	}
	return converted
}

func cloudRunV1ContainerToV2(container CRContainer) Container {
	env := convertEach(container.Env, cloudRunV1EnvToV2)
	ports := convertEach(container.Ports, func(port CRPort) ContainerPort { return ContainerPort(port) })
	mounts := convertEach(container.VolumeMounts, func(m CRVolumeMount) VolumeMount { return VolumeMount(m) })
	converted := Container{
		Name: container.Name, Image: container.Image, Command: container.Command,
		Args: container.Args, Env: env, Ports: ports, WorkingDir: container.WorkingDir,
	}
	if len(mounts) > 0 {
		converted.VolumeMounts = mounts
	}
	if container.Resources != nil {
		converted.Resources = &ResourceRequirements{Limits: container.Resources.Limits}
	}
	return converted
}

func cloudRunV2ToV1(service ServiceV2, project, serviceID string) CRService {
	location := cloudRunDefaultLocation
	if _, parsedLocation, _, ok := parseCloudRunV2ServiceName(service.Name); ok {
		location = parsedLocation
	}
	created := service.CreateTime
	if created == "" {
		created = nowTimestamp()
	}
	revision := serviceID + "-00001"
	if service.LatestReadyRevision != "" {
		revision = service.LatestReadyRevision[strings.LastIndex(service.LatestReadyRevision, "/")+1:]
	}

	var template *CRServiceTemplate
	if service.Template != nil {
		containers := make([]CRContainer, 0, len(service.Template.Containers))
		for _, container := range service.Template.Containers {
			containers = append(containers, cloudRunV2ContainerToV1(container))
		}
		var timeout int64
		if duration, err := time.ParseDuration(service.Template.Timeout); err == nil {
			timeout = int64(duration.Seconds())
		}
		template = &CRServiceTemplate{
			Metadata: &CRServiceMetadata{
				Namespace:   project,
				Labels:      service.Template.Labels,
				Annotations: service.Template.Annotations,
			},
			Spec: &CRTemplateSpec{Containers: containers, TimeoutSeconds: timeout},
		}
	}
	traffic := make([]CRTraffic, 0, len(service.Traffic))
	for _, target := range service.Traffic {
		traffic = append(traffic, CRTraffic{
			RevisionName: target.Revision,
			Percent:      target.Percent,
			Tag:          target.Tag,
		})
	}
	url := service.URI
	if url == "" {
		url = fmt.Sprintf("https://%s-%s-%s.a.run.app", serviceID, project, location)
	}
	return CRService{
		APIVersion: "serving.knative.dev/v1",
		Kind:       "Service",
		Metadata: CRServiceMetadata{
			Name: serviceID, Namespace: project, UID: service.UID,
			Generation: service.Generation, ResourceVersion: fmt.Sprintf("%d", service.Generation),
			Labels: service.Labels, Annotations: service.Annotations, CreationTimestamp: created,
		},
		Spec: CRServiceSpec{Template: template, Traffic: traffic},
		Status: &CRServiceStatus{
			ObservedGeneration: service.Generation, LatestReadyRevisionName: revision,
			LatestCreatedRevisionName: revision, URL: url, Address: &CRAddress{URL: url},
			Conditions: []CRCondition{{Type: "Ready", Status: "True", LastTransitionTime: service.UpdateTime}},
			Traffic:    crStatusTraffic(traffic, revision),
		},
	}
}

func cloudRunV1ToV2(service CRService, project, location string) ServiceV2 {
	serviceID := service.Metadata.Name
	name := fmt.Sprintf("projects/%s/locations/%s/services/%s", project, location, serviceID)
	created := service.Metadata.CreationTimestamp
	if created == "" {
		created = nowTimestamp()
	}
	revision := serviceID + "-00001"
	uri := fmt.Sprintf("https://%s-%s-%s.a.run.app", serviceID, project, location)
	if service.Status != nil {
		if service.Status.LatestReadyRevisionName != "" {
			revision = service.Status.LatestReadyRevisionName
		}
		if service.Status.URL != "" {
			uri = service.Status.URL
		}
	}

	var template *RevisionTemplate
	if service.Spec.Template != nil && service.Spec.Template.Spec != nil {
		spec := service.Spec.Template.Spec
		containers := make([]Container, 0, len(spec.Containers))
		for _, container := range spec.Containers {
			containers = append(containers, cloudRunV1ContainerToV2(container))
		}
		timeout := ""
		if spec.TimeoutSeconds > 0 {
			timeout = fmt.Sprintf("%ds", spec.TimeoutSeconds)
		}
		template = &RevisionTemplate{Containers: containers, Timeout: timeout}
		if service.Spec.Template.Metadata != nil {
			template.Labels = service.Spec.Template.Metadata.Labels
			template.Annotations = service.Spec.Template.Metadata.Annotations
		}
	}
	traffic := make([]TrafficTarget, 0, len(service.Spec.Traffic))
	for _, target := range service.Spec.Traffic {
		traffic = append(traffic, TrafficTarget{
			Revision: target.RevisionName, Percent: target.Percent, Tag: target.Tag,
		})
	}
	return ServiceV2{
		Name: name, UID: service.Metadata.UID, Generation: service.Metadata.Generation,
		Labels: service.Metadata.Labels, Annotations: service.Metadata.Annotations,
		CreateTime: created, UpdateTime: nowTimestamp(), Template: template, Traffic: traffic,
		TerminalCondition:     &Condition{Type: "Ready", State: "CONDITION_SUCCEEDED", LastTransitionTime: nowTimestamp()},
		Conditions:            []Condition{{Type: "Ready", State: "CONDITION_SUCCEEDED", LastTransitionTime: nowTimestamp()}},
		LatestReadyRevision:   name + "/revisions/" + revision,
		LatestCreatedRevision: name + "/revisions/" + revision,
		URI:                   uri,
	}
}

func projectCloudRunV2ToV1(service ServiceV2) {
	if crv1Services == nil {
		return
	}
	project, location, serviceID, ok := parseCloudRunV2ServiceName(service.Name)
	if !ok {
		return
	}
	projection := cloudRunV2ToV1(service, project, serviceID)
	revision := projection.Status.LatestReadyRevisionName
	for _, namespace := range []string{project, location} {
		namespaced := projection
		namespaced.Metadata.Namespace = namespace
		crv1Services.Put(namespace+"/"+serviceID, namespaced)
		if crv1ReconcileChildren != nil {
			crv1ReconcileChildren(namespace, namespaced, revision)
		}
	}
}

func projectCloudRunV1ToV2(service CRService, project, location string) {
	if crv2Services == nil {
		return
	}
	projection := cloudRunV1ToV2(service, project, location)
	// The Knative v1 surface and the v2 collection address one service record,
	// so a write through v1 mints the same fresh fingerprint a v2 write does.
	projection.Etag = generateUUID()
	crv2Services.Put(projection.Name, projection)
	if crv2Revisions != nil {
		revision := projection.LatestReadyRevision[strings.LastIndex(projection.LatestReadyRevision, "/")+1:]
		reconcileServiceRevision(crv2Revisions, projection.Name, revision, projection)
	}
	projectCloudRunV2ToV1(projection)
}

func deleteCloudRunServiceProjections(project, location, serviceID string) {
	if crv1Services != nil {
		for _, namespace := range []string{project, location} {
			crv1Services.Delete(namespace + "/" + serviceID)
			if crv1DeleteChildren != nil {
				crv1DeleteChildren(namespace, serviceID)
			}
		}
	}
	if crv2Services != nil {
		crv2Services.Delete(fmt.Sprintf("projects/%s/locations/%s/services/%s", project, location, serviceID))
	}
}
