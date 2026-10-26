package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
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
		StartupProbe: container.StartupProbe,
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
		StartupProbe: container.StartupProbe,
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
				Namespace: project,
				Labels:    service.Template.Labels,
				Annotations: cloudRunFoldVpcAccess(cloudRunFoldScaling(
					cloudRunFoldContainerDependencies(service.Template.Annotations, service.Template.Containers),
					service.Template.Scaling), service.Template.VpcAccess),
			},
			Spec: &CRTemplateSpec{Containers: containers, TimeoutSeconds: timeout,
				ContainerConcurrency: int64(service.Template.MaxInstanceRequestConcurrency),
				ServiceAccountName:   service.Template.ServiceAccount},
		}
		template.Metadata.Name = service.Template.Revision
	}
	traffic := make([]CRTraffic, 0, len(service.Traffic))
	for _, target := range service.Traffic {
		traffic = append(traffic, CRTraffic{
			RevisionName:   target.Revision,
			Percent:        target.Percent,
			Tag:            target.Tag,
			LatestRevision: target.Type == cloudRunTrafficLatest,
		})
	}
	url := service.URI
	if url == "" {
		url = cloudRunServiceURI(project, location, serviceID)
	}
	return CRService{
		APIVersion: "serving.knative.dev/v1",
		Kind:       "Service",
		Metadata: CRServiceMetadata{
			Name: serviceID, Namespace: project, UID: service.UID,
			Generation: service.Generation, ResourceVersion: fmt.Sprintf("%d", service.Generation),
			Labels: service.Labels, Annotations: cloudRunFoldServiceScaling(service.Annotations, service.Scaling), CreationTimestamp: created,
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
	uri := cloudRunServiceURI(project, location, serviceID)
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
		template = &RevisionTemplate{Containers: containers, Timeout: timeout,
			MaxInstanceRequestConcurrency: int32(spec.ContainerConcurrency), ServiceAccount: spec.ServiceAccountName}
		if service.Spec.Template.Metadata != nil {
			template.Revision = service.Spec.Template.Metadata.Name
			template.Labels = service.Spec.Template.Metadata.Labels
			annotations, err := cloudRunUnfoldContainerDependencies(service.Spec.Template.Metadata.Annotations, template.Containers)
			if err == nil {
				annotations, err = cloudRunUnfoldScaling(annotations, template)
			}
			if err == nil {
				annotations, template.VpcAccess, err = cloudRunUnfoldVpcAccess(annotations)
			}
			if err != nil {
				// Unreachable: cloudRunKnativeServiceValid refuses the service before it is stored.
				panic(err)
			}
			template.Annotations = annotations
		}
	}
	traffic := make([]TrafficTarget, 0, len(service.Spec.Traffic))
	for _, target := range service.Spec.Traffic {
		allocation := cloudRunTrafficRevision
		if target.LatestRevision {
			allocation = cloudRunTrafficLatest
		}
		traffic = append(traffic, TrafficTarget{
			Type: allocation, Revision: target.RevisionName, Percent: target.Percent, Tag: target.Tag,
		})
	}
	annotations, scaling, err := cloudRunUnfoldServiceScaling(service.Metadata.Annotations)
	if err != nil {
		// Unreachable: cloudRunKnativeServiceValid refuses the service before it is stored.
		panic(err)
	}
	return ServiceV2{
		Name: name, UID: service.Metadata.UID, Generation: service.Metadata.Generation,
		Labels: service.Metadata.Labels, Annotations: annotations, Scaling: scaling,
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

// writeKnativeService creates or replaces a service written through the
// Knative surface as the v2 service it projects, so it deploys a revision
// under the same rules a v2 write does, and answers the record the write
// leaves in namespace.
func writeKnativeService(w http.ResponseWriter, service CRService, project, location, namespace string, create bool) (CRService, bool) {
	written := cloudRunV1ToV2(service, project, location)
	update := ServiceV2{Labels: written.Labels, Annotations: written.Annotations, Scaling: written.Scaling, Template: written.Template, Traffic: written.Traffic}
	if create {
		if _, ok := createCloudRunService(w, update, project, location, service.Metadata.Name); !ok {
			return CRService{}, false
		}
	} else {
		existing, ok := crv2Services.Get(written.Name)
		if !ok {
			GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "service %q not found in namespace %q", service.Metadata.Name, namespace)
			return CRService{}, false
		}
		if _, ok := updateCloudRunService(w, existing, update); !ok {
			return CRService{}, false
		}
	}
	stored, _ := crv1Services.Get(namespace + "/" + service.Metadata.Name)
	return stored, true
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
	name := fmt.Sprintf("projects/%s/locations/%s/services/%s", project, location, serviceID)
	if crv2Services != nil {
		crv2Services.Delete(name)
	}
	// The service's IAM policy goes with it, so a service later created under
	// the same name starts with an empty policy.
	if store := gcpResourceIAMStore(); store != nil {
		store.Delete(name)
	}
}

// cloudRunKnativeServiceValid answers INVALID_ARGUMENT for a Knative service
// whose container-dependencies annotation is malformed, names a container the
// template does not define, or forms a cycle.
func cloudRunKnativeServiceValid(w http.ResponseWriter, service CRService) bool {
	if _, _, err := cloudRunUnfoldServiceScaling(service.Metadata.Annotations); err != nil {
		GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "%v", err)
		return false
	}
	template := service.Spec.Template
	if template == nil || template.Spec == nil || template.Metadata == nil {
		return true
	}
	containers := convertEach(template.Spec.Containers, cloudRunV1ContainerToV2)
	annotations, err := cloudRunUnfoldContainerDependencies(template.Metadata.Annotations, containers)
	if err == nil {
		annotations, err = cloudRunUnfoldScaling(annotations, &RevisionTemplate{})
	}
	if err == nil {
		_, _, err = cloudRunUnfoldVpcAccess(annotations)
	}
	if err != nil {
		GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "%v", err)
		return false
	}
	return cloudRunServiceTemplateValid(w, &RevisionTemplate{Containers: containers})
}

// cloudRunServiceTemplateValid answers INVALID_ARGUMENT for a revision
// template whose containers' dependsOn name no container of the template or
// form a cycle.
func cloudRunServiceTemplateValid(w http.ResponseWriter, template *RevisionTemplate) bool {
	if template == nil {
		return true
	}
	if _, err := cloudRunContainerStartOrder(template.Containers); err != nil {
		GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "%v", err)
		return false
	}
	return true
}

// The Knative surface carries a revision's instance bounds as template
// annotations; the v2 surface carries them in the template's scaling and
// refuses the autoscaling.knative.dev annotations.
const (
	cloudRunMinScaleAnnotation = "autoscaling.knative.dev/minScale"
	cloudRunMaxScaleAnnotation = "autoscaling.knative.dev/maxScale"
)

// cloudRunUnfoldScaling sets template's scaling from the minScale and
// maxScale annotations and returns the annotations without them.
func cloudRunUnfoldScaling(annotations map[string]string, template *RevisionTemplate) (map[string]string, error) {
	bound := func(key string) (int32, bool, error) {
		raw, ok := annotations[key]
		if !ok {
			return 0, false, nil
		}
		n, err := strconv.ParseInt(raw, 10, 32)
		if err != nil || n < 0 {
			return 0, false, fmt.Errorf("annotation %s must be a non-negative integer, got %q", key, raw)
		}
		return int32(n), true, nil
	}
	minimum, hasMin, err := bound(cloudRunMinScaleAnnotation)
	if err != nil {
		return nil, err
	}
	maximum, hasMax, err := bound(cloudRunMaxScaleAnnotation)
	if err != nil {
		return nil, err
	}
	if !hasMin && !hasMax {
		return annotations, nil
	}
	if hasMin && hasMax && maximum > 0 && minimum > maximum {
		return nil, fmt.Errorf("annotation %s (%d) must not exceed %s (%d)",
			cloudRunMinScaleAnnotation, minimum, cloudRunMaxScaleAnnotation, maximum)
	}
	template.Scaling = &RevisionScaling{MinInstanceCount: minimum, MaxInstanceCount: maximum}
	rest := make(map[string]string, len(annotations))
	for k, v := range annotations {
		if k != cloudRunMinScaleAnnotation && k != cloudRunMaxScaleAnnotation {
			rest[k] = v
		}
	}
	return rest, nil
}

// cloudRunFoldScaling returns annotations with scaling's bounds as the
// minScale and maxScale annotations.
func cloudRunFoldScaling(annotations map[string]string, scaling *RevisionScaling) map[string]string {
	if scaling == nil || (scaling.MinInstanceCount == 0 && scaling.MaxInstanceCount == 0) {
		return annotations
	}
	folded := make(map[string]string, len(annotations)+2)
	for k, v := range annotations {
		folded[k] = v
	}
	if scaling.MinInstanceCount > 0 {
		folded[cloudRunMinScaleAnnotation] = strconv.Itoa(int(scaling.MinInstanceCount))
	}
	if scaling.MaxInstanceCount > 0 {
		folded[cloudRunMaxScaleAnnotation] = strconv.Itoa(int(scaling.MaxInstanceCount))
	}
	return folded
}

// The Knative surface carries a service's own scaling as the Service-scoped
// annotations the v1 document lists; gcloud writes the mode in lower case and
// reads "manual" back.
const (
	cloudRunServiceMinScaleAnnotation    = "run.googleapis.com/minScale"
	cloudRunServiceMaxScaleAnnotation    = "run.googleapis.com/maxScale"
	cloudRunServiceManualCountAnnotation = "run.googleapis.com/manualInstanceCount"
	cloudRunServiceScalingModeAnnotation = "run.googleapis.com/scalingMode"
)

// cloudRunUnfoldServiceScaling reads a service's scaling out of its
// annotations and returns the annotations without them.
func cloudRunUnfoldServiceScaling(annotations map[string]string) (map[string]string, *ServiceScaling, error) {
	count := func(key string) (int32, bool, error) {
		raw, ok := annotations[key]
		if !ok {
			return 0, false, nil
		}
		n, err := strconv.ParseInt(raw, 10, 32)
		if err != nil || n < 0 {
			return 0, false, fmt.Errorf("annotation %s must be a non-negative integer, got %q", key, raw)
		}
		return int32(n), true, nil
	}
	scaling := &ServiceScaling{}
	var set bool
	for key, field := range map[string]*int32{
		cloudRunServiceMinScaleAnnotation:    &scaling.MinInstanceCount,
		cloudRunServiceMaxScaleAnnotation:    &scaling.MaxInstanceCount,
		cloudRunServiceManualCountAnnotation: &scaling.ManualInstanceCount,
	} {
		n, has, err := count(key)
		if err != nil {
			return nil, nil, err
		}
		*field = n
		set = set || has
	}
	if mode, has := annotations[cloudRunServiceScalingModeAnnotation]; has {
		switch strings.ToLower(mode) {
		case "manual":
			scaling.ScalingMode = "MANUAL"
		case "automatic":
			scaling.ScalingMode = "AUTOMATIC"
		default:
			return nil, nil, fmt.Errorf("annotation %s must be manual or automatic, got %q", cloudRunServiceScalingModeAnnotation, mode)
		}
		set = true
	}
	if !set {
		return annotations, nil, nil
	}
	rest := make(map[string]string, len(annotations))
	for k, v := range annotations {
		switch k {
		case cloudRunServiceMinScaleAnnotation, cloudRunServiceMaxScaleAnnotation,
			cloudRunServiceManualCountAnnotation, cloudRunServiceScalingModeAnnotation:
			continue
		}
		rest[k] = v
	}
	return rest, scaling, nil
}

// cloudRunFoldServiceScaling returns annotations with the service's scaling
// as its Service-scoped annotations.
func cloudRunFoldServiceScaling(annotations map[string]string, scaling *ServiceScaling) map[string]string {
	if scaling == nil {
		return annotations
	}
	folded := make(map[string]string, len(annotations)+4)
	for k, v := range annotations {
		folded[k] = v
	}
	for key, n := range map[string]int32{
		cloudRunServiceMinScaleAnnotation:    scaling.MinInstanceCount,
		cloudRunServiceMaxScaleAnnotation:    scaling.MaxInstanceCount,
		cloudRunServiceManualCountAnnotation: scaling.ManualInstanceCount,
	} {
		if n > 0 {
			folded[key] = strconv.Itoa(int(n))
		}
	}
	if mode := string(scaling.ScalingMode); mode == "MANUAL" || mode == "AUTOMATIC" {
		folded[cloudRunServiceScalingModeAnnotation] = strings.ToLower(mode)
	}
	return folded
}

// The Knative surface carries a template's VPC egress as annotations: the
// Serverless VPC Access connector, the egress setting, and direct VPC egress's
// network interfaces as a JSON list.
const (
	cloudRunVpcConnectorAnnotation     = "run.googleapis.com/vpc-access-connector"
	cloudRunVpcEgressAnnotation        = "run.googleapis.com/vpc-access-egress"
	cloudRunNetworkInterfaceAnnotation = "run.googleapis.com/network-interfaces"
)

// cloudRunUnfoldVpcAccess reads the VPC egress annotations into a v2
// VpcAccess and returns the annotations without them.
func cloudRunUnfoldVpcAccess(annotations map[string]string) (map[string]string, *VpcAccess, error) {
	connector, hasConnector := annotations[cloudRunVpcConnectorAnnotation]
	egress, hasEgress := annotations[cloudRunVpcEgressAnnotation]
	interfaces, hasInterfaces := annotations[cloudRunNetworkInterfaceAnnotation]
	if !hasConnector && !hasEgress && !hasInterfaces {
		return annotations, nil, nil
	}
	vpc := &VpcAccess{Connector: connector}
	switch egress {
	case "":
	case "all", "all-traffic":
		vpc.Egress = "ALL_TRAFFIC"
	case "private-ranges-only":
		vpc.Egress = "PRIVATE_RANGES_ONLY"
	default:
		return nil, nil, fmt.Errorf("annotation %s must be all-traffic or private-ranges-only, got %q", cloudRunVpcEgressAnnotation, egress)
	}
	if hasInterfaces && interfaces != "" {
		if err := json.Unmarshal([]byte(interfaces), &vpc.NetworkInterfaces); err != nil {
			return nil, nil, fmt.Errorf("annotation %s is not a JSON list of network interfaces: %w", cloudRunNetworkInterfaceAnnotation, err)
		}
	}
	rest := make(map[string]string, len(annotations))
	for k, v := range annotations {
		switch k {
		case cloudRunVpcConnectorAnnotation, cloudRunVpcEgressAnnotation, cloudRunNetworkInterfaceAnnotation:
		default:
			rest[k] = v
		}
	}
	return rest, vpc, nil
}

// cloudRunFoldVpcAccess returns annotations with vpc as the VPC egress
// annotations.
func cloudRunFoldVpcAccess(annotations map[string]string, vpc *VpcAccess) map[string]string {
	if vpc == nil || (vpc.Connector == "" && vpc.Egress == "" && len(vpc.NetworkInterfaces) == 0) {
		return annotations
	}
	folded := make(map[string]string, len(annotations)+3)
	for k, v := range annotations {
		folded[k] = v
	}
	if vpc.Connector != "" {
		folded[cloudRunVpcConnectorAnnotation] = vpc.Connector
	}
	switch vpc.Egress {
	case "ALL_TRAFFIC":
		folded[cloudRunVpcEgressAnnotation] = "all-traffic"
	case "PRIVATE_RANGES_ONLY":
		folded[cloudRunVpcEgressAnnotation] = "private-ranges-only"
	}
	if len(vpc.NetworkInterfaces) > 0 {
		encoded, err := json.Marshal(vpc.NetworkInterfaces)
		if err != nil {
			panic(err)
		}
		folded[cloudRunNetworkInterfaceAnnotation] = string(encoded)
	}
	return folded
}
