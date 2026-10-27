package main

import (
	"net/http"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
)

// The simulator runs no vulnerability scanner, so scans complete with no
// findings and zero severity counts.

type ECRImageTagMutabilityExclusionFilter struct {
	FilterType string `json:"filterType"`
	Filter     string `json:"filter"`
}

type ECRRegistryPolicy struct {
	RegistryId string `json:"registryId"`
	PolicyText string `json:"policyText"`
}

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

type ECRReplicationConfiguration struct {
	Rules []ECRReplicationRule `json:"rules"`
}

var (
	// One per registry, under the key "registry".
	ecrRegistryPolicy sim.Store[ECRRegistryPolicy]
	// One per registry, under the key "registry".
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

// PutImage stores repo:tag and repo:digest aliases.
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
	// No scanner runs, so the scan completes at once without findings.
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
	// No scanner runs: a completed scan with no findings and zero severity
	// counts.
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
	// Without policy text, preview the repository's stored lifecycle policy.
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
	// The simulator applies no lifecycle rules, so the preview expires nothing.
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
		// No replication configured means an empty rule set.
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
	// One status per configured destination; none without replication.
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
