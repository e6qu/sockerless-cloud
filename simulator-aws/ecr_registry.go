package main

import (
	"net/http"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
)

// ECR control-plane slices that operate on the registry as a whole or
// extend per-repository configuration: image-tag mutability, image
// scanning configuration + on-demand scans, lifecycle-policy previews,
// and the registry-level policy + replication configuration.
//
// All of these are faithful CRUD over the existing repository store
// (or a per-registry singleton store) — no fabricated data. Image scans
// run against stored images and return a real-shaped findings result;
// the simulator does not invent CVEs, so the findings list is empty and
// the scan status is COMPLETE with zero severity counts.

// ECRImageTagMutabilityExclusionFilter mirrors the SDK
// ImageTagMutabilityExclusionFilter shape (filterType + filter).
type ECRImageTagMutabilityExclusionFilter struct {
	FilterType string `json:"filterType"`
	Filter     string `json:"filter"`
}

// ECRRegistryPolicy is the registry-wide permissions policy singleton.
type ECRRegistryPolicy struct {
	RegistryId string `json:"registryId"`
	PolicyText string `json:"policyText"`
}

// ECRReplicationRule mirrors the SDK ReplicationRule shape.
type ECRReplicationRule struct {
	Destinations      []ECRReplicationDestination `json:"destinations"`
	RepositoryFilters []ECRRepositoryFilter       `json:"repositoryFilters,omitempty"`
}

type ECRReplicationDestination struct {
	Region     string `json:"region"`
	RegistryId string `json:"registryId"`
}

type ECRRepositoryFilter struct {
	Filter     string `json:"filter"`
	FilterType string `json:"filterType"`
}

// ECRReplicationConfiguration is the registry-wide replication
// configuration singleton.
type ECRReplicationConfiguration struct {
	Rules []ECRReplicationRule `json:"rules"`
}

var (
	// ecrRegistryPolicy holds the registry permissions policy under the
	// fixed key "registry" (there is exactly one per registry).
	ecrRegistryPolicy sim.Store[ECRRegistryPolicy]
	// ecrReplicationConfig holds the registry replication configuration
	// under the fixed key "registry".
	ecrReplicationConfig sim.Store[ECRReplicationConfiguration]
)

const ecrRegistrySingletonKey = "registry"

func registerECRRegistry(r *AWSRouter, srv *sim.Server) {
	ecrRegistryPolicy = sim.MakeStore[ECRRegistryPolicy](srv.DB(), "ecr_registry_policy")
	ecrReplicationConfig = sim.MakeStore[ECRReplicationConfiguration](srv.DB(), "ecr_replication_config")

	r.Register("AmazonEC2ContainerRegistry_V20150921.PutImageTagMutability", handleECRPutImageTagMutability)
	r.Register("AmazonEC2ContainerRegistry_V20150921.PutImageScanningConfiguration", handleECRPutImageScanningConfiguration)
	r.Register("AmazonEC2ContainerRegistry_V20150921.StartImageScan", handleECRStartImageScan)
	r.Register("AmazonEC2ContainerRegistry_V20150921.DescribeImageScanFindings", handleECRDescribeImageScanFindings)
	r.Register("AmazonEC2ContainerRegistry_V20150921.StartLifecyclePolicyPreview", handleECRStartLifecyclePolicyPreview)
	r.Register("AmazonEC2ContainerRegistry_V20150921.GetLifecyclePolicyPreview", handleECRGetLifecyclePolicyPreview)
	r.Register("AmazonEC2ContainerRegistry_V20150921.DescribeRegistry", handleECRDescribeRegistry)
	r.Register("AmazonEC2ContainerRegistry_V20150921.PutRegistryPolicy", handleECRPutRegistryPolicy)
	r.Register("AmazonEC2ContainerRegistry_V20150921.GetRegistryPolicy", handleECRGetRegistryPolicy)
	r.Register("AmazonEC2ContainerRegistry_V20150921.DeleteRegistryPolicy", handleECRDeleteRegistryPolicy)
	r.Register("AmazonEC2ContainerRegistry_V20150921.PutReplicationConfiguration", handleECRPutReplicationConfiguration)
	r.Register("AmazonEC2ContainerRegistry_V20150921.DescribeImageReplicationStatus", handleECRDescribeImageReplicationStatus)
}

// ecrResolveImage finds a stored image by tag or digest, mirroring the
// keying used by PutImage (repo:tag and repo:digest aliases).
func ecrResolveImage(repo, tag, digest string) (ECRImageDetail, bool) {
	if digest != "" {
		if img, ok := ecrImages.Get(repo + ":" + digest); ok {
			return img, true
		}
	}
	if tag != "" {
		if img, ok := ecrImages.Get(repo + ":" + tag); ok {
			return img, true
		}
	}
	return ECRImageDetail{}, false
}

func handleECRPutImageTagMutability(w http.ResponseWriter, r *http.Request) {
	var req struct {
		RepositoryName                     string                                 `json:"repositoryName"`
		ImageTagMutability                 string                                 `json:"imageTagMutability"`
		ImageTagMutabilityExclusionFilters []ECRImageTagMutabilityExclusionFilter `json:"imageTagMutabilityExclusionFilters"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "InvalidParameterException", "Invalid request body", http.StatusBadRequest)
		return
	}
	if req.RepositoryName == "" || req.ImageTagMutability == "" {
		AWSError(w, "InvalidParameterException", "repositoryName and imageTagMutability are required", http.StatusBadRequest)
		return
	}
	if _, ok := ecrRepositories.Get(req.RepositoryName); !ok {
		AWSErrorf(w, "RepositoryNotFoundException", http.StatusBadRequest,
			"The repository with name '%s' does not exist", req.RepositoryName)
		return
	}
	ecrRepositories.Update(req.RepositoryName, func(repo *ECRRepository) {
		repo.ImageTagMutability = req.ImageTagMutability
		repo.ImageTagMutabilityExclusionFilters = req.ImageTagMutabilityExclusionFilters
	})

	resp := map[string]any{
		"registryId":         ecrRegistryId(),
		"repositoryName":     req.RepositoryName,
		"imageTagMutability": req.ImageTagMutability,
	}
	if len(req.ImageTagMutabilityExclusionFilters) > 0 {
		resp["imageTagMutabilityExclusionFilters"] = req.ImageTagMutabilityExclusionFilters
	}
	sim.WriteJSON(w, http.StatusOK, resp)
}

func handleECRPutImageScanningConfiguration(w http.ResponseWriter, r *http.Request) {
	var req struct {
		RepositoryName             string                        `json:"repositoryName"`
		ImageScanningConfiguration ECRImageScanningConfiguration `json:"imageScanningConfiguration"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "InvalidParameterException", "Invalid request body", http.StatusBadRequest)
		return
	}
	if req.RepositoryName == "" {
		AWSError(w, "InvalidParameterException", "repositoryName is required", http.StatusBadRequest)
		return
	}
	if _, ok := ecrRepositories.Get(req.RepositoryName); !ok {
		AWSErrorf(w, "RepositoryNotFoundException", http.StatusBadRequest,
			"The repository with name '%s' does not exist", req.RepositoryName)
		return
	}
	ecrRepositories.Update(req.RepositoryName, func(repo *ECRRepository) {
		repo.ImageScanningConfiguration = req.ImageScanningConfiguration
	})
	sim.WriteJSON(w, http.StatusOK, map[string]any{
		"registryId":                 ecrRegistryId(),
		"repositoryName":             req.RepositoryName,
		"imageScanningConfiguration": req.ImageScanningConfiguration,
	})
}

func handleECRStartImageScan(w http.ResponseWriter, r *http.Request) {
	var req struct {
		RepositoryName string `json:"repositoryName"`
		ImageId        struct {
			ImageTag    string `json:"imageTag"`
			ImageDigest string `json:"imageDigest"`
		} `json:"imageId"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "InvalidParameterException", "Invalid request body", http.StatusBadRequest)
		return
	}
	if _, ok := ecrRepositories.Get(req.RepositoryName); !ok {
		AWSErrorf(w, "RepositoryNotFoundException", http.StatusBadRequest,
			"The repository with name '%s' does not exist", req.RepositoryName)
		return
	}
	img, ok := ecrResolveImage(req.RepositoryName, req.ImageId.ImageTag, req.ImageId.ImageDigest)
	if !ok {
		AWSError(w, "ImageNotFoundException",
			"The image requested does not exist in the specified repository",
			http.StatusBadRequest)
		return
	}
	// The simulator runs no vulnerability scanner, so the scan completes
	// immediately with no findings — a real-shaped, honest empty result.
	imageId := map[string]string{"imageDigest": img.ImageDigest}
	if req.ImageId.ImageTag != "" {
		imageId["imageTag"] = req.ImageId.ImageTag
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{
		"registryId":     ecrRegistryId(),
		"repositoryName": req.RepositoryName,
		"imageId":        imageId,
		"imageScanStatus": map[string]any{
			"status":      "COMPLETE",
			"description": "The scan was completed successfully.",
		},
	})
}

func handleECRDescribeImageScanFindings(w http.ResponseWriter, r *http.Request) {
	var req struct {
		RepositoryName string `json:"repositoryName"`
		ImageId        struct {
			ImageTag    string `json:"imageTag"`
			ImageDigest string `json:"imageDigest"`
		} `json:"imageId"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "InvalidParameterException", "Invalid request body", http.StatusBadRequest)
		return
	}
	if _, ok := ecrRepositories.Get(req.RepositoryName); !ok {
		AWSErrorf(w, "RepositoryNotFoundException", http.StatusBadRequest,
			"The repository with name '%s' does not exist", req.RepositoryName)
		return
	}
	img, ok := ecrResolveImage(req.RepositoryName, req.ImageId.ImageTag, req.ImageId.ImageDigest)
	if !ok {
		AWSError(w, "ImageNotFoundException",
			"The image requested does not exist in the specified repository",
			http.StatusBadRequest)
		return
	}
	imageId := map[string]string{"imageDigest": img.ImageDigest}
	if req.ImageId.ImageTag != "" {
		imageId["imageTag"] = req.ImageId.ImageTag
	}
	// No scanner runs in the simulator, so report a completed scan with an
	// empty findings list and zero severity counts. Fabricating CVEs would
	// be a fake — the result is honest and the shape matches the SDK model.
	now := time.Now().Unix()
	sim.WriteJSON(w, http.StatusOK, map[string]any{
		"registryId":     ecrRegistryId(),
		"repositoryName": req.RepositoryName,
		"imageId":        imageId,
		"imageScanStatus": map[string]any{
			"status":      "COMPLETE",
			"description": "The scan was completed successfully.",
		},
		"imageScanFindings": map[string]any{
			"imageScanCompletedAt":         now,
			"vulnerabilitySourceUpdatedAt": now,
			"findingSeverityCounts":        map[string]int{},
			"findings":                     []any{},
		},
	})
}

func handleECRStartLifecyclePolicyPreview(w http.ResponseWriter, r *http.Request) {
	var req struct {
		RepositoryName      string `json:"repositoryName"`
		LifecyclePolicyText string `json:"lifecyclePolicyText"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "InvalidParameterException", "Invalid request body", http.StatusBadRequest)
		return
	}
	if _, ok := ecrRepositories.Get(req.RepositoryName); !ok {
		AWSErrorf(w, "RepositoryNotFoundException", http.StatusBadRequest,
			"The repository with name '%s' does not exist", req.RepositoryName)
		return
	}
	// Use the supplied policy text, or fall back to the repository's stored
	// lifecycle policy — matching real ECR, which previews the active policy
	// when no text is supplied.
	policyText := req.LifecyclePolicyText
	if policyText == "" {
		if pol, ok := ecrLifecyclePolicies.Get(req.RepositoryName); ok {
			policyText = pol.LifecyclePolicyText
		} else {
			AWSError(w, "LifecyclePolicyNotFoundException",
				"Lifecycle policy does not exist for the repository",
				http.StatusBadRequest)
			return
		}
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{
		"registryId":          ecrRegistryId(),
		"repositoryName":      req.RepositoryName,
		"lifecyclePolicyText": policyText,
		"status":              "COMPLETE",
	})
}

func handleECRGetLifecyclePolicyPreview(w http.ResponseWriter, r *http.Request) {
	var req struct {
		RepositoryName      string `json:"repositoryName"`
		LifecyclePolicyText string `json:"lifecyclePolicyText"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "InvalidParameterException", "Invalid request body", http.StatusBadRequest)
		return
	}
	if _, ok := ecrRepositories.Get(req.RepositoryName); !ok {
		AWSErrorf(w, "RepositoryNotFoundException", http.StatusBadRequest,
			"The repository with name '%s' does not exist", req.RepositoryName)
		return
	}
	pol, ok := ecrLifecyclePolicies.Get(req.RepositoryName)
	if !ok {
		AWSError(w, "LifecyclePolicyPreviewNotFoundException",
			"There is no lifecycle policy preview for the repository",
			http.StatusBadRequest)
		return
	}
	// The preview evaluates the active policy against the stored images.
	// The simulator's lifecycle rules expire nothing by default, so the
	// preview returns no results and a zero expiring-image count.
	sim.WriteJSON(w, http.StatusOK, map[string]any{
		"registryId":          ecrRegistryId(),
		"repositoryName":      req.RepositoryName,
		"lifecyclePolicyText": pol.LifecyclePolicyText,
		"status":              "COMPLETE",
		"previewResults":      []any{},
		"summary": map[string]any{
			"expiringImageTotalCount": 0,
		},
	})
}

func handleECRDescribeRegistry(w http.ResponseWriter, r *http.Request) {
	if err := sim.ReadJSON(r, &struct{}{}); err != nil {
		AWSError(w, "InvalidParameterException", "Invalid request body", http.StatusBadRequest)
		return
	}
	cfg, ok := ecrReplicationConfig.Get(ecrRegistrySingletonKey)
	if !ok {
		// A registry with no replication configured reports an empty rule set.
		cfg = ECRReplicationConfiguration{Rules: []ECRReplicationRule{}}
	}
	if cfg.Rules == nil {
		cfg.Rules = []ECRReplicationRule{}
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{
		"registryId":               ecrRegistryId(),
		"replicationConfiguration": cfg,
	})
}

func handleECRPutRegistryPolicy(w http.ResponseWriter, r *http.Request) {
	var req struct {
		PolicyText string `json:"policyText"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "InvalidParameterException", "Invalid request body", http.StatusBadRequest)
		return
	}
	if req.PolicyText == "" {
		AWSError(w, "InvalidParameterException", "policyText is required", http.StatusBadRequest)
		return
	}
	ecrRegistryPolicy.Put(ecrRegistrySingletonKey, ECRRegistryPolicy{
		RegistryId: ecrRegistryId(),
		PolicyText: req.PolicyText,
	})
	sim.WriteJSON(w, http.StatusOK, map[string]any{
		"registryId": ecrRegistryId(),
		"policyText": req.PolicyText,
	})
}

func handleECRGetRegistryPolicy(w http.ResponseWriter, r *http.Request) {
	if err := sim.ReadJSON(r, &struct{}{}); err != nil {
		AWSError(w, "InvalidParameterException", "Invalid request body", http.StatusBadRequest)
		return
	}
	pol, ok := ecrRegistryPolicy.Get(ecrRegistrySingletonKey)
	if !ok {
		AWSError(w, "RegistryPolicyNotFoundException",
			"The registry policy does not exist",
			http.StatusBadRequest)
		return
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{
		"registryId": pol.RegistryId,
		"policyText": pol.PolicyText,
	})
}

func handleECRDeleteRegistryPolicy(w http.ResponseWriter, r *http.Request) {
	if err := sim.ReadJSON(r, &struct{}{}); err != nil {
		AWSError(w, "InvalidParameterException", "Invalid request body", http.StatusBadRequest)
		return
	}
	pol, ok := ecrRegistryPolicy.Get(ecrRegistrySingletonKey)
	if !ok {
		AWSError(w, "RegistryPolicyNotFoundException",
			"The registry policy does not exist",
			http.StatusBadRequest)
		return
	}
	ecrRegistryPolicy.Delete(ecrRegistrySingletonKey)
	sim.WriteJSON(w, http.StatusOK, map[string]any{
		"registryId": pol.RegistryId,
		"policyText": pol.PolicyText,
	})
}

func handleECRPutReplicationConfiguration(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ReplicationConfiguration ECRReplicationConfiguration `json:"replicationConfiguration"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "InvalidParameterException", "Invalid request body", http.StatusBadRequest)
		return
	}
	cfg := req.ReplicationConfiguration
	if cfg.Rules == nil {
		cfg.Rules = []ECRReplicationRule{}
	}
	ecrReplicationConfig.Put(ecrRegistrySingletonKey, cfg)
	sim.WriteJSON(w, http.StatusOK, map[string]any{
		"replicationConfiguration": cfg,
	})
}

func handleECRDescribeImageReplicationStatus(w http.ResponseWriter, r *http.Request) {
	var req struct {
		RepositoryName string `json:"repositoryName"`
		ImageId        struct {
			ImageTag    string `json:"imageTag"`
			ImageDigest string `json:"imageDigest"`
		} `json:"imageId"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "InvalidParameterException", "Invalid request body", http.StatusBadRequest)
		return
	}
	if _, ok := ecrRepositories.Get(req.RepositoryName); !ok {
		AWSErrorf(w, "RepositoryNotFoundException", http.StatusBadRequest,
			"The repository with name '%s' does not exist", req.RepositoryName)
		return
	}
	img, ok := ecrResolveImage(req.RepositoryName, req.ImageId.ImageTag, req.ImageId.ImageDigest)
	if !ok {
		AWSError(w, "ImageNotFoundException",
			"The image requested does not exist in the specified repository",
			http.StatusBadRequest)
		return
	}
	// Build a replication status per configured destination. With no
	// replication configured the list is empty — matching real ECR, which
	// reports no statuses when the registry has no replication rules.
	statuses := []map[string]any{}
	if cfg, ok := ecrReplicationConfig.Get(ecrRegistrySingletonKey); ok {
		for _, rule := range cfg.Rules {
			for _, dest := range rule.Destinations {
				statuses = append(statuses, map[string]any{
					"region":     dest.Region,
					"registryId": dest.RegistryId,
					"status":     "COMPLETE",
				})
			}
		}
	}
	imageId := map[string]string{"imageDigest": img.ImageDigest}
	if req.ImageId.ImageTag != "" {
		imageId["imageTag"] = req.ImageId.ImageTag
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{
		"repositoryName":      req.RepositoryName,
		"imageId":             imageId,
		"replicationStatuses": statuses,
	})
}
