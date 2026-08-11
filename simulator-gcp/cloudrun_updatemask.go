package main

import (
	"fmt"
	"strings"
)

// Validated updateMask application for the Cloud Run v2 PATCH handlers.
// Real Cloud Run v2 merges only the masked fields into the existing
// resource and rejects a mask naming an unknown or output-only field with
// 400 INVALID_ARGUMENT — a silent skip would hide caller bugs (a typo in
// updateMask would persist as a no-op the caller assumed applied). Mask
// paths arrive in proto (snake_case) or JSON (camelCase) form; both are
// accepted, matching protojson field-mask parsing.

// canonicalMaskPath converts each dot-separated segment of a field-mask
// path from snake_case to the camelCase JSON name.
func canonicalMaskPath(path string) string {
	segments := strings.Split(path, ".")
	for i, seg := range segments {
		parts := strings.Split(seg, "_")
		for j := 1; j < len(parts); j++ {
			if parts[j] != "" {
				parts[j] = strings.ToUpper(parts[j][:1]) + parts[j][1:]
			}
		}
		segments[i] = strings.Join(parts, "")
	}
	return strings.Join(segments, ".")
}

// maskPaths splits an updateMask into its canonical non-empty paths.
func maskPaths(mask string) []string {
	var out []string
	for _, raw := range strings.Split(mask, ",") {
		p := strings.TrimSpace(raw)
		if p != "" {
			out = append(out, canonicalMaskPath(p))
		}
	}
	return out
}

func invalidMaskPathError(path string) error {
	return fmt.Errorf("invalid updateMask: field %q cannot be updated", path)
}

// applyServiceUpdateMask merges the masked fields of update into existing.
func applyServiceUpdateMask(existing, update ServiceV2, mask string) (ServiceV2, error) {
	merged := existing
	for _, path := range maskPaths(mask) {
		top := path
		if i := strings.IndexByte(path, '.'); i >= 0 {
			top = path[:i]
		}
		switch top {
		case "labels":
			merged.Labels = update.Labels
		case "annotations":
			merged.Annotations = update.Annotations
		case "description":
			merged.Description = update.Description
		case "client":
			merged.Client = update.Client
		case "clientVersion":
			merged.ClientVersion = update.ClientVersion
		case "ingress":
			merged.Ingress = update.Ingress
		case "traffic":
			merged.Traffic = update.Traffic
		case "launchStage":
			merged.LaunchStage = update.LaunchStage
		case "defaultUriDisabled":
			merged.DefaultUriDisabled = update.DefaultUriDisabled
		case "invokerIamDisabled":
			merged.InvokerIamDisabled = update.InvokerIamDisabled
		case "iapEnabled":
			merged.IapEnabled = update.IapEnabled
		case "sshEnabled":
			merged.SshEnabled = update.SshEnabled
		case "customAudiences":
			merged.CustomAudiences = update.CustomAudiences
		case "binaryAuthorization":
			merged.BinaryAuthorization = update.BinaryAuthorization
		case "scaling":
			merged.Scaling = update.Scaling
		case "buildConfig":
			merged.BuildConfig = update.BuildConfig
		case "multiRegionSettings":
			merged.MultiRegionSettings = update.MultiRegionSettings
		case "template":
			if path == "template" {
				merged.Template = update.Template
				continue
			}
			tmpl, err := mergedRevisionTemplate(merged.Template, update.Template, strings.TrimPrefix(path, "template."))
			if err != nil {
				return ServiceV2{}, err
			}
			merged.Template = tmpl
		default:
			return ServiceV2{}, invalidMaskPathError(path)
		}
	}
	return merged, nil
}

// mergedRevisionTemplate applies one template sub-path of the mask,
// replacing only the named member of the existing template.
func mergedRevisionTemplate(existing, update *RevisionTemplate, sub string) (*RevisionTemplate, error) {
	leaf := sub
	if i := strings.IndexByte(sub, '.'); i >= 0 {
		leaf = sub[:i]
	}
	if existing == nil {
		existing = &RevisionTemplate{}
	}
	if update == nil {
		update = &RevisionTemplate{}
	}
	out := *existing
	switch leaf {
	case "labels":
		out.Labels = update.Labels
	case "annotations":
		out.Annotations = update.Annotations
	case "revision":
		out.Revision = update.Revision
	case "containers":
		out.Containers = update.Containers
	case "volumes":
		out.Volumes = update.Volumes
	case "scaling":
		out.Scaling = update.Scaling
	case "vpcAccess":
		out.VpcAccess = update.VpcAccess
	case "timeout":
		out.Timeout = update.Timeout
	case "serviceAccount":
		out.ServiceAccount = update.ServiceAccount
	case "executionEnvironment":
		out.ExecutionEnvironment = update.ExecutionEnvironment
	case "maxInstanceRequestConcurrency":
		out.MaxInstanceRequestConcurrency = update.MaxInstanceRequestConcurrency
	case "sessionAffinity":
		out.SessionAffinity = update.SessionAffinity
	case "healthCheckDisabled":
		out.HealthCheckDisabled = update.HealthCheckDisabled
	case "encryptionKey":
		out.EncryptionKey = update.EncryptionKey
	case "encryptionKeyRevocationAction":
		out.EncryptionKeyRevocationAction = update.EncryptionKeyRevocationAction
	case "encryptionKeyShutdownDuration":
		out.EncryptionKeyShutdownDuration = update.EncryptionKeyShutdownDuration
	case "gpuZonalRedundancyDisabled":
		out.GpuZonalRedundancyDisabled = update.GpuZonalRedundancyDisabled
	case "nodeSelector":
		out.NodeSelector = update.NodeSelector
	case "serviceMesh":
		out.ServiceMesh = update.ServiceMesh
	case "client":
		out.Client = update.Client
	case "clientVersion":
		out.ClientVersion = update.ClientVersion
	default:
		return nil, invalidMaskPathError("template." + sub)
	}
	return &out, nil
}

// applyWorkerPoolUpdateMask merges the masked fields of update into existing.
func applyWorkerPoolUpdateMask(existing, update WorkerPoolV2, mask string) (WorkerPoolV2, error) {
	merged := existing
	for _, path := range maskPaths(mask) {
		top := path
		if i := strings.IndexByte(path, '.'); i >= 0 {
			top = path[:i]
		}
		switch top {
		case "labels":
			merged.Labels = update.Labels
		case "annotations":
			merged.Annotations = update.Annotations
		case "description":
			merged.Description = update.Description
		case "client":
			merged.Client = update.Client
		case "clientVersion":
			merged.ClientVersion = update.ClientVersion
		case "launchStage":
			merged.LaunchStage = update.LaunchStage
		case "customAudiences":
			merged.CustomAudiences = update.CustomAudiences
		case "binaryAuthorization":
			merged.BinaryAuthorization = update.BinaryAuthorization
		case "scaling":
			merged.Scaling = update.Scaling
		case "instanceSplits":
			merged.InstanceSplits = update.InstanceSplits
		case "template":
			if path == "template" {
				merged.Template = update.Template
				continue
			}
			tmpl, err := mergedWorkerPoolTemplate(merged.Template, update.Template, strings.TrimPrefix(path, "template."))
			if err != nil {
				return WorkerPoolV2{}, err
			}
			merged.Template = tmpl
		default:
			return WorkerPoolV2{}, invalidMaskPathError(path)
		}
	}
	return merged, nil
}

// mergedWorkerPoolTemplate applies one template sub-path of the mask,
// replacing only the named member of the existing worker-pool template.
func mergedWorkerPoolTemplate(existing, update *WorkerPoolRevisionTemplate, sub string) (*WorkerPoolRevisionTemplate, error) {
	leaf := sub
	if i := strings.IndexByte(sub, '.'); i >= 0 {
		leaf = sub[:i]
	}
	if existing == nil {
		existing = &WorkerPoolRevisionTemplate{}
	}
	if update == nil {
		update = &WorkerPoolRevisionTemplate{}
	}
	out := *existing
	switch leaf {
	case "labels":
		out.Labels = update.Labels
	case "annotations":
		out.Annotations = update.Annotations
	case "revision":
		out.Revision = update.Revision
	case "containers":
		out.Containers = update.Containers
	case "volumes":
		out.Volumes = update.Volumes
	case "vpcAccess":
		out.VpcAccess = update.VpcAccess
	case "serviceAccount":
		out.ServiceAccount = update.ServiceAccount
	case "nodeSelector":
		out.NodeSelector = update.NodeSelector
	case "serviceMesh":
		out.ServiceMesh = update.ServiceMesh
	case "client":
		out.Client = update.Client
	case "clientVersion":
		out.ClientVersion = update.ClientVersion
	case "encryptionKey":
		out.EncryptionKey = update.EncryptionKey
	case "encryptionKeyRevocationAction":
		out.EncryptionKeyRevocationAction = update.EncryptionKeyRevocationAction
	case "encryptionKeyShutdownDuration":
		out.EncryptionKeyShutdownDuration = update.EncryptionKeyShutdownDuration
	case "gpuZonalRedundancyDisabled":
		out.GpuZonalRedundancyDisabled = update.GpuZonalRedundancyDisabled
	default:
		return nil, invalidMaskPathError("template." + sub)
	}
	return &out, nil
}

// applyInstanceUpdateMask merges the masked fields of update into existing.
func applyInstanceUpdateMask(existing, update InstanceV2, mask string) (InstanceV2, error) {
	merged := existing
	for _, path := range maskPaths(mask) {
		top := path
		if i := strings.IndexByte(path, '.'); i >= 0 {
			top = path[:i]
		}
		switch top {
		case "labels":
			merged.Labels = update.Labels
		case "annotations":
			merged.Annotations = update.Annotations
		case "description":
			merged.Description = update.Description
		case "client":
			merged.Client = update.Client
		case "clientVersion":
			merged.ClientVersion = update.ClientVersion
		case "launchStage":
			merged.LaunchStage = update.LaunchStage
		case "ingress":
			merged.Ingress = update.Ingress
		case "defaultUriDisabled":
			merged.DefaultUriDisabled = update.DefaultUriDisabled
		case "invokerIamDisabled":
			merged.InvokerIamDisabled = update.InvokerIamDisabled
		case "iapEnabled":
			merged.IapEnabled = update.IapEnabled
		case "containers":
			merged.Containers = update.Containers
		case "volumes":
			merged.Volumes = update.Volumes
		case "serviceAccount":
			merged.ServiceAccount = update.ServiceAccount
		case "vpcAccess":
			merged.VpcAccess = update.VpcAccess
		case "restartPolicy":
			merged.RestartPolicy = update.RestartPolicy
		case "nodeSelector":
			merged.NodeSelector = update.NodeSelector
		case "binaryAuthorization":
			merged.BinaryAuthorization = update.BinaryAuthorization
		case "encryptionKey":
			merged.EncryptionKey = update.EncryptionKey
		case "encryptionKeyRevocationAction":
			merged.EncryptionKeyRevocationAction = update.EncryptionKeyRevocationAction
		case "encryptionKeyShutdownDuration":
			merged.EncryptionKeyShutdownDuration = update.EncryptionKeyShutdownDuration
		case "gpuZonalRedundancyDisabled":
			merged.GpuZonalRedundancyDisabled = update.GpuZonalRedundancyDisabled
		default:
			return InstanceV2{}, invalidMaskPathError(path)
		}
	}
	return merged, nil
}
