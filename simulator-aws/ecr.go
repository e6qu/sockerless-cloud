package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
)

// RepositoryUri has the canonical `<account>.dkr.ecr.<region>.amazonaws.com/<repo>`
// shape; the simulator serves the registry API at its own endpoint, so clients
// must treat that endpoint as the registry host.
type ECRRepository struct {
	RepositoryArn  string `json:"repositoryArn"`
	RepositoryName string `json:"repositoryName"`
	RepositoryUri  string `json:"repositoryUri"` // external: the simulator serves the registry at its own endpoint
	RegistryId     string `json:"registryId"`
	CreatedAt      int64  `json:"createdAt"`
	// The aws_ecr_repository resource reads these back on refresh; echo the
	// request or the AWS defaults, or the plan drifts forever.
	ImageTagMutability         string                        `json:"imageTagMutability"`
	EncryptionConfiguration    ECREncryptionConfiguration    `json:"encryptionConfiguration"`
	ImageScanningConfiguration ECRImageScanningConfiguration `json:"imageScanningConfiguration"`
	// Omitted from DescribeRepositories when unset, as AWS does.
	ImageTagMutabilityExclusionFilters []ECRImageTagMutabilityExclusionFilter `json:"imageTagMutabilityExclusionFilters,omitempty"`
	Tags                               []SMTag                                `json:"-"`
}

type ECREncryptionConfiguration struct {
	EncryptionType string `json:"encryptionType"`
	KmsKey         string `json:"kmsKey,omitempty"`
}

type ECRImageScanningConfiguration struct {
	ScanOnPush bool `json:"scanOnPush"`
}

type ECRImageDetail struct {
	RegistryId     string   `json:"registryId"`
	RepositoryName string   `json:"repositoryName"`
	ImageDigest    string   `json:"imageDigest"`
	ImageTags      []string `json:"imageTags"`
	ImageManifest  string   `json:"imageManifest"`
	PushedAt       int64    `json:"pushedAt"`
}

type ECRLifecyclePolicy struct {
	RegistryId          string `json:"registryId"`
	RepositoryName      string `json:"repositoryName"`
	LifecyclePolicyText string `json:"lifecyclePolicyText"`
}

// A rule maps an Amazon ECR repository prefix onto an upstream registry; a pull
// under `<account>.dkr.ecr.<region>.amazonaws.com/<prefix>/…` fetches from it.
type ECRPullThroughCacheRule struct {
	EcrRepositoryPrefix string `json:"ecrRepositoryPrefix"`
	UpstreamRegistryUrl string `json:"upstreamRegistryUrl"` // external: the upstream registry the rule pulls from
	UpstreamRegistry    string `json:"upstreamRegistry,omitempty"`
	RegistryId          string `json:"registryId"`
	CreatedAt           int64  `json:"createdAt"`
	UpdatedAt           int64  `json:"updatedAt,omitempty"`
	// Set by UpdatePullThroughCacheRule when the upstream requires
	// authentication; ValidatePullThroughCacheRule echoes them.
	CredentialArn string `json:"credentialArn,omitempty"`
	CustomRoleArn string `json:"customRoleArn,omitempty"`
}

var (
	ecrRepositories          sim.Store[ECRRepository]
	ecrImages                sim.Store[ECRImageDetail]
	ecrLifecyclePolicies     sim.Store[ECRLifecyclePolicy]
	ecrPullThroughCacheRules sim.Store[ECRPullThroughCacheRule]
)

// Follows awsAccountID(), so an account override reaches every ARN, URI and
// token endpoint.
func ecrRegistryId() string { return awsAccountID() }

func ecrArn(resourceType, name string) string {
	return "arn:aws:ecr:" + awsRegion() + ":" + ecrRegistryId() + ":" + resourceType + "/" + name
}

func registerECR(r *AWSRouter, srv *sim.Server) {
	ecrRepositories = sim.MakeStore[ECRRepository](srv.DB(), "ecr_repositories")
	ecrImages = sim.MakeStore[ECRImageDetail](srv.DB(), "ecr_images")
	ecrLifecyclePolicies = sim.MakeStore[ECRLifecyclePolicy](srv.DB(), "ecr_lifecycle_policies")
	ecrPullThroughCacheRules = sim.MakeStore[ECRPullThroughCacheRule](srv.DB(), "ecr_pull_through_cache_rules")
	ecrAuthorizationTokens = sim.MakeStore[ECRAuthorizationToken](srv.DB(), "ecr_authorization_tokens")

	r.Register("AmazonEC2ContainerRegistry_V20150921.CreateRepository", handleECRCreateRepository)
	r.Register("AmazonEC2ContainerRegistry_V20150921.DescribeRepositories", handleECRDescribeRepositories)
	r.Register("AmazonEC2ContainerRegistry_V20150921.DeleteRepository", handleECRDeleteRepository)
	r.Register("AmazonEC2ContainerRegistry_V20150921.GetAuthorizationToken", handleECRGetAuthorizationToken)
	r.Register("AmazonEC2ContainerRegistry_V20150921.BatchGetImage", handleECRBatchGetImage)
	r.Register("AmazonEC2ContainerRegistry_V20150921.ListImages", handleECRListImages)
	r.Register("AmazonEC2ContainerRegistry_V20150921.DescribeImages", handleECRDescribeImages)
	r.Register("AmazonEC2ContainerRegistry_V20150921.PutImage", handleECRPutImage)
	r.Register("AmazonEC2ContainerRegistry_V20150921.BatchDeleteImage", handleECRBatchDeleteImage)
	r.Register("AmazonEC2ContainerRegistry_V20150921.BatchCheckLayerAvailability", handleECRBatchCheckLayerAvailability)
	r.Register("AmazonEC2ContainerRegistry_V20150921.PutLifecyclePolicy", handleECRPutLifecyclePolicy)
	r.Register("AmazonEC2ContainerRegistry_V20150921.GetLifecyclePolicy", handleECRGetLifecyclePolicy)
	r.Register("AmazonEC2ContainerRegistry_V20150921.DeleteLifecyclePolicy", handleECRDeleteLifecyclePolicy)
	r.Register("AmazonEC2ContainerRegistry_V20150921.ListTagsForResource", handleECRListTagsForResource)
	r.Register("AmazonEC2ContainerRegistry_V20150921.TagResource", handleECRTagResource)
	r.Register("AmazonEC2ContainerRegistry_V20150921.UntagResource", handleECRUntagResource)

	r.Register("AmazonEC2ContainerRegistry_V20150921.CreatePullThroughCacheRule", handleECRCreatePullThroughCacheRule)
	r.Register("AmazonEC2ContainerRegistry_V20150921.DescribePullThroughCacheRules", handleECRDescribePullThroughCacheRules)
	r.Register("AmazonEC2ContainerRegistry_V20150921.DeletePullThroughCacheRule", handleECRDeletePullThroughCacheRule)

	registerECRRegistry(r, srv)
	registerECRAdvanced(r, srv)
	registerECRLayers(r, srv)
	registerECROCI(srv)
}

func handleECRCreatePullThroughCacheRule(w http.ResponseWriter, r *http.Request) {
	var req struct {
		EcrRepositoryPrefix string `json:"ecrRepositoryPrefix"`
		UpstreamRegistryUrl string `json:"upstreamRegistryUrl"`
		UpstreamRegistry    string `json:"upstreamRegistry"`
		RegistryId          string `json:"registryId"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "InvalidParameterException", "Invalid request body", http.StatusBadRequest)
		return
	}
	if req.EcrRepositoryPrefix == "" || req.UpstreamRegistryUrl == "" {
		AWSError(w, "InvalidParameterException", "ecrRepositoryPrefix and upstreamRegistryUrl are required", http.StatusBadRequest)
		return
	}
	if _, exists := ecrPullThroughCacheRules.Get(req.EcrRepositoryPrefix); exists {
		AWSError(w, "PullThroughCacheRuleAlreadyExistsException",
			"A pull-through cache rule with the given prefix already exists",
			http.StatusBadRequest)
		return
	}
	now := time.Now().Unix()
	regID := req.RegistryId
	if regID == "" {
		regID = ecrRegistryId()
	}
	rule := ECRPullThroughCacheRule{
		EcrRepositoryPrefix: req.EcrRepositoryPrefix,
		UpstreamRegistryUrl: req.UpstreamRegistryUrl,
		UpstreamRegistry:    req.UpstreamRegistry,
		RegistryId:          regID,
		CreatedAt:           now,
		UpdatedAt:           now,
	}
	ecrPullThroughCacheRules.Put(req.EcrRepositoryPrefix, rule)

	// upstreamRegistry is a closed enum; omit it rather than send an empty string.
	created := map[string]any{
		"ecrRepositoryPrefix": rule.EcrRepositoryPrefix,
		"upstreamRegistryUrl": rule.UpstreamRegistryUrl,
		"registryId":          rule.RegistryId,
		"createdAt":           rule.CreatedAt,
	}
	if rule.UpstreamRegistry != "" {
		created["upstreamRegistry"] = rule.UpstreamRegistry
	}
	sim.WriteJSON(w, http.StatusOK, created)
}

// Answers without pagination.
func handleECRDescribePullThroughCacheRules(w http.ResponseWriter, r *http.Request) {
	var req struct {
		EcrRepositoryPrefixes []string `json:"ecrRepositoryPrefixes"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSErrorf(w, "InvalidParameterValue", http.StatusBadRequest, "invalid request body: %v", err)
		return
	}

	var rules []ECRPullThroughCacheRule
	if len(req.EcrRepositoryPrefixes) == 0 {
		rules = ecrPullThroughCacheRules.List()
	} else {
		for _, p := range req.EcrRepositoryPrefixes {
			if rule, ok := ecrPullThroughCacheRules.Get(p); ok {
				rules = append(rules, rule)
			}
		}
	}
	if rules == nil {
		rules = []ECRPullThroughCacheRule{}
	}

	sim.WriteJSON(w, http.StatusOK, map[string]any{
		"pullThroughCacheRules": rules,
	})
}

func handleECRDeletePullThroughCacheRule(w http.ResponseWriter, r *http.Request) {
	var req struct {
		EcrRepositoryPrefix string `json:"ecrRepositoryPrefix"`
		RegistryId          string `json:"registryId"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "InvalidParameterException", "Invalid request body", http.StatusBadRequest)
		return
	}
	if req.EcrRepositoryPrefix == "" {
		AWSError(w, "InvalidParameterException", "ecrRepositoryPrefix is required", http.StatusBadRequest)
		return
	}
	rule, ok := ecrPullThroughCacheRules.Get(req.EcrRepositoryPrefix)
	if !ok {
		AWSError(w, "PullThroughCacheRuleNotFoundException",
			"The pull-through cache rule does not exist",
			http.StatusNotFound)
		return
	}
	ecrPullThroughCacheRules.Delete(req.EcrRepositoryPrefix)

	// DeletePullThroughCacheRuleResponse has no upstreamRegistry member.
	sim.WriteJSON(w, http.StatusOK, map[string]any{
		"ecrRepositoryPrefix": rule.EcrRepositoryPrefix,
		"upstreamRegistryUrl": rule.UpstreamRegistryUrl,
		"registryId":          rule.RegistryId,
		"createdAt":           rule.CreatedAt,
	})
}

func handleECRCreateRepository(w http.ResponseWriter, r *http.Request) {
	var req struct {
		RepositoryName             string                         `json:"repositoryName"`
		ImageTagMutability         string                         `json:"imageTagMutability"`
		EncryptionConfiguration    *ECREncryptionConfiguration    `json:"encryptionConfiguration"`
		ImageScanningConfiguration *ECRImageScanningConfiguration `json:"imageScanningConfiguration"`
		Tags                       []SMTag                        `json:"tags"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "InvalidParameterException", "Invalid request body", http.StatusBadRequest)
		return
	}
	if req.RepositoryName == "" {
		AWSError(w, "InvalidParameterException", "repositoryName is required", http.StatusBadRequest)
		return
	}

	if _, exists := ecrRepositories.Get(req.RepositoryName); exists {
		AWSErrorf(w, "RepositoryAlreadyExistsException", http.StatusBadRequest,
			"The repository with name '%s' already exists", req.RepositoryName)
		return
	}

	repo := ECRRepository{
		RepositoryArn:              ecrArn("repository", req.RepositoryName),
		RepositoryName:             req.RepositoryName,
		RepositoryUri:              ecrRegistryId() + ".dkr.ecr." + awsRegion() + ".amazonaws.com/" + req.RepositoryName,
		RegistryId:                 ecrRegistryId(),
		CreatedAt:                  time.Now().Unix(),
		ImageTagMutability:         req.ImageTagMutability,
		ImageScanningConfiguration: ECRImageScanningConfiguration{},
		// Amazon ECR defaults to AES256 server-side encryption.
		EncryptionConfiguration: ECREncryptionConfiguration{EncryptionType: "AES256"},
	}
	if repo.ImageTagMutability == "" {
		repo.ImageTagMutability = "MUTABLE" // The AWS default.
	}
	if req.EncryptionConfiguration != nil && req.EncryptionConfiguration.EncryptionType != "" {
		repo.EncryptionConfiguration = *req.EncryptionConfiguration
	}
	if req.ImageScanningConfiguration != nil {
		repo.ImageScanningConfiguration = *req.ImageScanningConfiguration
	}
	// Store create-time tags; dropping them drifts every plan.
	repo.Tags = req.Tags
	ecrRepositories.Put(req.RepositoryName, repo)

	sim.WriteJSON(w, http.StatusOK, map[string]any{
		"repository": repo,
	})
}

func handleECRDescribeRepositories(w http.ResponseWriter, r *http.Request) {
	var req struct {
		RepositoryNames []string `json:"repositoryNames"`
		NextToken       string   `json:"nextToken"`
		MaxResults      int      `json:"maxResults"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "InvalidParameterException", "Invalid request body", http.StatusBadRequest)
		return
	}

	var repos []ECRRepository
	key := func(r ECRRepository) string { return r.RepositoryName }
	if len(req.RepositoryNames) == 0 {
		repos = ecrRepositories.List()
	} else {
		for _, name := range req.RepositoryNames {
			repo, ok := ecrRepositories.Get(name)
			if !ok {
				AWSErrorf(w, "RepositoryNotFoundException", http.StatusBadRequest,
					"The repository with name '%s' does not exist in the registry with id '%s'", name, awsAccountID())
				return
			}
			repos = append(repos, repo)
		}
	}
	if repos == nil {
		repos = []ECRRepository{}
	}

	page, next, pageOK := awsNamedOrKeyPage(w, ecrBadToken, repos, len(req.RepositoryNames) > 0, key, req.NextToken, req.MaxResults, 1000)
	if !pageOK {
		return
	}
	out := map[string]any{"repositories": page}
	if next != "" {
		out["nextToken"] = next
	}
	sim.WriteJSON(w, http.StatusOK, out)
}

func handleECRDeleteRepository(w http.ResponseWriter, r *http.Request) {
	var req struct {
		RepositoryName string `json:"repositoryName"`
		Force          bool   `json:"force"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "InvalidParameterException", "Invalid request body", http.StatusBadRequest)
		return
	}
	if req.RepositoryName == "" {
		AWSError(w, "InvalidParameterException", "repositoryName is required", http.StatusBadRequest)
		return
	}

	repo, ok := ecrRepositories.Get(req.RepositoryName)
	if !ok {
		AWSErrorf(w, "RepositoryNotFoundException", http.StatusBadRequest,
			"The repository with name '%s' does not exist", req.RepositoryName)
		return
	}

	if !req.Force && len(ecrImages.ListPrefix(req.RepositoryName+":")) > 0 {
		AWSErrorf(w, "RepositoryNotEmptyException", http.StatusBadRequest,
			"The repository with name '%s' in registry with id '%s' cannot be deleted because it still contains images",
			req.RepositoryName, ecrRegistryId())
		return
	}

	ecrRepositories.Delete(req.RepositoryName)
	ecrDeleteRepositoryContents(req.RepositoryName)

	sim.WriteJSON(w, http.StatusOK, map[string]any{
		"repository": repo,
	})
}

// A repository created again under the same name starts empty. Image and
// manifest keys are "<repository>:…", blob and layer keys "<repository>@…";
// a repository name contains neither separator.
func ecrDeleteRepositoryContents(repo string) {
	for _, row := range ecrImages.ListPrefix(repo + ":") {
		ecrImages.Delete(row.ID)
	}
	for _, row := range ecrOCIManifests.ListPrefix(repo + ":") {
		ecrOCIManifests.Delete(row.ID)
	}
	for _, row := range ecrOCIBlobs.ListPrefix(repo + "@") {
		ecrOCIBlobs.Delete(row.ID)
	}
	for _, row := range ecrLayers.ListPrefix(repo + "@") {
		ecrLayers.Delete(row.ID)
	}
	ecrLifecyclePolicies.Delete(repo)
	ecrRepoPolicies.Delete(repo)
}

func handleECRGetAuthorizationToken(w http.ResponseWriter, r *http.Request) {
	if err := sim.ReadJSON(r, &struct{}{}); err != nil {
		AWSErrorf(w, "InvalidParameterValue", http.StatusBadRequest, "invalid request body: %v", err)
		return
	}

	token, expires, err := ecrIssueAuthorizationToken()
	if err != nil {
		AWSErrorf(w, "ServerException", http.StatusInternalServerError,
			"failed to issue an authorization token: %v", err)
		return
	}
	expiresAt := expires.Unix()

	sim.WriteJSON(w, http.StatusOK, map[string]any{
		"authorizationData": []map[string]any{
			{
				"authorizationToken": token,
				"expiresAt":          expiresAt,
				"proxyEndpoint":      "https://" + ecrRegistryId() + ".dkr.ecr." + awsRegion() + ".amazonaws.com",
			},
		},
	})
}

// The store holds each image under its digest and each tag, so deduplicate by
// digest.
func ecrRepoImages(repo string) []ECRImageDetail {
	seen := map[string]bool{}
	var out []ECRImageDetail
	for _, row := range ecrImages.ListPrefix(repo + ":") {
		if seen[row.Item.ImageDigest] {
			continue
		}
		seen[row.Item.ImageDigest] = true
		out = append(out, row.Item)
	}
	return out
}

func handleECRListImages(w http.ResponseWriter, r *http.Request) {
	var req struct {
		RepositoryName string `json:"repositoryName"`
		NextToken      string `json:"nextToken"`
		MaxResults     int    `json:"maxResults"`
		Filter         struct {
			TagStatus string `json:"tagStatus"`
		} `json:"filter"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "InvalidParameterException", "Invalid request body", http.StatusBadRequest)
		return
	}
	if _, ok := ecrRepositories.Get(req.RepositoryName); !ok {
		AWSErrorf(w, "RepositoryNotFoundException", http.StatusBadRequest, "The repository with name '%s' does not exist", req.RepositoryName)
		return
	}
	// tagStatus is TAGGED, UNTAGGED or ANY (the default).
	tagStatus := strings.ToUpper(req.Filter.TagStatus)
	imageIds := []map[string]string{}
	for _, img := range ecrRepoImages(req.RepositoryName) {
		if len(img.ImageTags) == 0 {
			if tagStatus == "TAGGED" {
				continue
			}
			imageIds = append(imageIds, map[string]string{"imageDigest": img.ImageDigest})
			continue
		}
		if tagStatus == "UNTAGGED" {
			continue
		}
		for _, tag := range img.ImageTags {
			imageIds = append(imageIds, map[string]string{"imageDigest": img.ImageDigest, "imageTag": tag})
		}
	}
	page, next, pageOK := awsKeyPage(w, ecrBadToken, imageIds, func(it map[string]string) string { return it["imageDigest"] + "\x00" + it["imageTag"] }, req.NextToken, req.MaxResults, 0)
	if !pageOK {
		return
	}
	resp := map[string]any{"imageIds": page}
	if next != "" {
		resp["nextToken"] = next
	}
	sim.WriteJSON(w, http.StatusOK, resp)
}

func handleECRDescribeImages(w http.ResponseWriter, r *http.Request) {
	var req struct {
		RepositoryName string `json:"repositoryName"`
		ImageIds       []struct {
			ImageTag    string `json:"imageTag"`
			ImageDigest string `json:"imageDigest"`
		} `json:"imageIds"`
		NextToken  string `json:"nextToken"`
		MaxResults int    `json:"maxResults"`
		Filter     struct {
			TagStatus string `json:"tagStatus"`
		} `json:"filter"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "InvalidParameterException", "Invalid request body", http.StatusBadRequest)
		return
	}
	if _, ok := ecrRepositories.Get(req.RepositoryName); !ok {
		AWSErrorf(w, "RepositoryNotFoundException", http.StatusBadRequest, "The repository with name '%s' does not exist", req.RepositoryName)
		return
	}

	var images []ECRImageDetail
	if len(req.ImageIds) > 0 {
		for _, id := range req.ImageIds {
			key := req.RepositoryName + ":" + id.ImageTag
			if id.ImageDigest != "" {
				key = req.RepositoryName + ":" + id.ImageDigest
			}
			if img, ok := ecrImages.Get(key); ok {
				images = append(images, img)
			}
		}
	} else {
		images = ecrRepoImages(req.RepositoryName)
	}

	// tagStatus is TAGGED, UNTAGGED or ANY (the default).
	if tagStatus := strings.ToUpper(req.Filter.TagStatus); tagStatus == "TAGGED" || tagStatus == "UNTAGGED" {
		var f []ECRImageDetail
		for _, img := range images {
			if (len(img.ImageTags) > 0) == (tagStatus == "TAGGED") {
				f = append(f, img)
			}
		}
		images = f
	}
	page, next, pageOK := awsKeyPage(w, ecrBadToken, images, func(it ECRImageDetail) string { return it.ImageDigest }, req.NextToken, req.MaxResults, 0)
	if !pageOK {
		return
	}

	details := make([]map[string]any, 0, len(page))
	for _, img := range page {
		size, err := ecrImageSize(img)
		if err != nil {
			AWSError(w, "ServerException", err.Error(), http.StatusInternalServerError)
			return
		}
		tags := img.ImageTags
		if tags == nil {
			tags = []string{}
		}
		details = append(details, map[string]any{
			"registryId":       img.RegistryId,
			"repositoryName":   img.RepositoryName,
			"imageDigest":      img.ImageDigest,
			"imageTags":        tags,
			"imageSizeInBytes": size,
			"imagePushedAt":    img.PushedAt,
		})
	}
	resp := map[string]any{"imageDetails": details}
	if next != "" {
		resp["nextToken"] = next
	}
	sim.WriteJSON(w, http.StatusOK, resp)
}

func handleECRBatchGetImage(w http.ResponseWriter, r *http.Request) {
	var req struct {
		RepositoryName string `json:"repositoryName"`
		ImageIds       []struct {
			ImageTag    string `json:"imageTag"`
			ImageDigest string `json:"imageDigest"`
		} `json:"imageIds"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "InvalidParameterException", "Invalid request body", http.StatusBadRequest)
		return
	}

	var images []map[string]any
	var failures []map[string]any

	for _, imageId := range req.ImageIds {
		key := req.RepositoryName + ":" + imageId.ImageTag
		if imageId.ImageDigest != "" {
			key = req.RepositoryName + ":" + imageId.ImageDigest
		}

		img, ok := ecrImages.Get(key)
		if ok {
			images = append(images, map[string]any{
				"registryId":     img.RegistryId,
				"repositoryName": img.RepositoryName,
				"imageId": map[string]string{
					"imageDigest": img.ImageDigest,
					"imageTag":    imageId.ImageTag,
				},
				"imageManifest": img.ImageManifest,
			})
		} else {
			failures = append(failures, map[string]any{
				"imageId": map[string]string{
					"imageTag": imageId.ImageTag,
				},
				"failureCode":   "ImageNotFound",
				"failureReason": "Requested image not found",
			})
		}
	}
	if images == nil {
		images = []map[string]any{}
	}
	if failures == nil {
		failures = []map[string]any{}
	}

	sim.WriteJSON(w, http.StatusOK, map[string]any{
		"images":   images,
		"failures": failures,
	})
}

func handleECRPutImage(w http.ResponseWriter, r *http.Request) {
	var req struct {
		RepositoryName string `json:"repositoryName"`
		ImageManifest  string `json:"imageManifest"`
		ImageTag       string `json:"imageTag"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "InvalidParameterException", "Invalid request body", http.StatusBadRequest)
		return
	}
	if req.RepositoryName == "" || req.ImageManifest == "" {
		AWSError(w, "InvalidParameterException", "repositoryName and imageManifest are required", http.StatusBadRequest)
		return
	}

	if _, ok := ecrRepositories.Get(req.RepositoryName); !ok {
		AWSErrorf(w, "RepositoryNotFoundException", http.StatusBadRequest,
			"The repository with name '%s' does not exist", req.RepositoryName)
		return
	}

	sum := sha256.Sum256([]byte(req.ImageManifest))
	digest := "sha256:" + hex.EncodeToString(sum[:])
	img := ECRImageDetail{
		RegistryId:     ecrRegistryId(),
		RepositoryName: req.RepositoryName,
		ImageDigest:    digest,
		ImageTags:      []string{req.ImageTag},
		ImageManifest:  req.ImageManifest,
		PushedAt:       time.Now().Unix(),
	}

	key := req.RepositoryName + ":" + req.ImageTag
	ecrImages.Put(key, img)
	ecrImages.Put(req.RepositoryName+":"+digest, img)

	sim.WriteJSON(w, http.StatusOK, map[string]any{
		"image": map[string]any{
			"registryId":     img.RegistryId,
			"repositoryName": img.RepositoryName,
			"imageId": map[string]string{
				"imageDigest": img.ImageDigest,
				"imageTag":    req.ImageTag,
			},
			"imageManifest": img.ImageManifest,
		},
	})
}

func handleECRBatchDeleteImage(w http.ResponseWriter, r *http.Request) {
	var req struct {
		RepositoryName string `json:"repositoryName"`
		ImageIds       []struct {
			ImageTag    string `json:"imageTag"`
			ImageDigest string `json:"imageDigest"`
		} `json:"imageIds"`
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

	var deleted []map[string]any
	var failures []map[string]any

	for _, imageId := range req.ImageIds {
		key := req.RepositoryName + ":" + imageId.ImageTag
		if imageId.ImageDigest != "" {
			key = req.RepositoryName + ":" + imageId.ImageDigest
		}

		if img, ok := ecrImages.Get(key); ok {
			// Delete every alias of the content, or DescribeImages and ListImages would
			// still list the image.
			ecrImages.Delete(req.RepositoryName + ":" + img.ImageDigest)
			for _, tag := range img.ImageTags {
				ecrImages.Delete(req.RepositoryName + ":" + tag)
			}
			ecrImages.Delete(key)
			// Amazon ECR resolves the digest even when the request deleted by tag.
			imgId := map[string]any{"imageDigest": img.ImageDigest}
			if imageId.ImageTag != "" {
				imgId["imageTag"] = imageId.ImageTag
			}
			deleted = append(deleted, imgId)
		} else {
			imgId := map[string]string{}
			if imageId.ImageTag != "" {
				imgId["imageTag"] = imageId.ImageTag
			}
			if imageId.ImageDigest != "" {
				imgId["imageDigest"] = imageId.ImageDigest
			}
			failures = append(failures, map[string]any{
				"imageId":       imgId,
				"failureCode":   "ImageNotFound",
				"failureReason": "Requested image not found",
			})
		}
	}
	if deleted == nil {
		deleted = []map[string]any{}
	}
	if failures == nil {
		failures = []map[string]any{}
	}

	sim.WriteJSON(w, http.StatusOK, map[string]any{
		"imageIds": deleted,
		"failures": failures,
	})
}

func handleECRBatchCheckLayerAvailability(w http.ResponseWriter, r *http.Request) {
	var req struct {
		RepositoryName string   `json:"repositoryName"`
		LayerDigests   []string `json:"layerDigests"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "InvalidParameterException", "Invalid request body", http.StatusBadRequest)
		return
	}

	var layers []map[string]any
	for _, digest := range req.LayerDigests {
		availability := "UNAVAILABLE"
		if _, ok := ecrLayers.Get(ecrLayerKey(req.RepositoryName, digest)); ok {
			availability = "AVAILABLE"
		}
		layers = append(layers, map[string]any{
			"layerDigest":       digest,
			"layerAvailability": availability,
		})
	}
	if layers == nil {
		layers = []map[string]any{}
	}

	sim.WriteJSON(w, http.StatusOK, map[string]any{
		"layers":   layers,
		"failures": []any{},
	})
}

func handleECRPutLifecyclePolicy(w http.ResponseWriter, r *http.Request) {
	var req struct {
		RepositoryName      string `json:"repositoryName"`
		LifecyclePolicyText string `json:"lifecyclePolicyText"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "InvalidParameterException", "Invalid request body", http.StatusBadRequest)
		return
	}

	policy := ECRLifecyclePolicy{
		RegistryId:          ecrRegistryId(),
		RepositoryName:      req.RepositoryName,
		LifecyclePolicyText: req.LifecyclePolicyText,
	}
	ecrLifecyclePolicies.Put(req.RepositoryName, policy)

	sim.WriteJSON(w, http.StatusOK, map[string]any{
		"registryId":          ecrRegistryId(),
		"repositoryName":      req.RepositoryName,
		"lifecyclePolicyText": req.LifecyclePolicyText,
	})
}

func handleECRGetLifecyclePolicy(w http.ResponseWriter, r *http.Request) {
	var req struct {
		RepositoryName string `json:"repositoryName"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "InvalidParameterException", "Invalid request body", http.StatusBadRequest)
		return
	}

	policy, ok := ecrLifecyclePolicies.Get(req.RepositoryName)
	if !ok {
		AWSErrorf(w, "LifecyclePolicyNotFoundException", http.StatusBadRequest,
			"Lifecycle policy for repository '%s' does not exist", req.RepositoryName)
		return
	}

	sim.WriteJSON(w, http.StatusOK, map[string]any{
		"registryId":          policy.RegistryId,
		"repositoryName":      policy.RepositoryName,
		"lifecyclePolicyText": policy.LifecyclePolicyText,
	})
}

func handleECRDeleteLifecyclePolicy(w http.ResponseWriter, r *http.Request) {
	var req struct {
		RepositoryName string `json:"repositoryName"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "InvalidParameterException", "Invalid request body", http.StatusBadRequest)
		return
	}

	policy, ok := ecrLifecyclePolicies.Get(req.RepositoryName)
	if !ok {
		AWSErrorf(w, "LifecyclePolicyNotFoundException", http.StatusBadRequest,
			"Lifecycle policy for repository '%s' does not exist", req.RepositoryName)
		return
	}

	ecrLifecyclePolicies.Delete(req.RepositoryName)

	sim.WriteJSON(w, http.StatusOK, map[string]any{
		"registryId":          policy.RegistryId,
		"repositoryName":      policy.RepositoryName,
		"lifecyclePolicyText": policy.LifecyclePolicyText,
	})
}

func ecrRepoByArn(arn string) (string, bool) {
	const sep = ":repository/"
	idx := strings.Index(arn, sep)
	if idx < 0 {
		return "", false
	}
	name := arn[idx+len(sep):]
	_, ok := ecrRepositories.Get(name)
	return name, ok
}

func handleECRListTagsForResource(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ResourceArn string `json:"resourceArn"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSErrorf(w, "InvalidParameterValue", http.StatusBadRequest, "invalid request body: %v", err)
		return
	}
	tags := []SMTag{}
	if name, ok := ecrRepoByArn(req.ResourceArn); ok {
		if repo, ok := ecrRepositories.Get(name); ok {
			tags = append(tags, repo.Tags...)
		}
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{"tags": tags})
}

func handleECRTagResource(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ResourceArn string  `json:"resourceArn"`
		Tags        []SMTag `json:"tags"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSErrorf(w, "InvalidParameterValue", http.StatusBadRequest, "invalid request body: %v", err)
		return
	}
	name, ok := ecrRepoByArn(req.ResourceArn)
	if !ok {
		AWSErrorf(w, "RepositoryNotFoundException", http.StatusBadRequest, "repository not found: %s", req.ResourceArn)
		return
	}
	ecrRepositories.Update(name, func(repo *ECRRepository) {
		override := map[string]string{}
		for _, t := range req.Tags {
			override[t.Key] = t.Value
		}
		merged := make([]SMTag, 0, len(repo.Tags)+len(req.Tags))
		for _, t := range repo.Tags {
			if _, replaced := override[t.Key]; !replaced {
				merged = append(merged, t)
			}
		}
		repo.Tags = append(merged, req.Tags...)
	})
	sim.WriteJSON(w, http.StatusOK, map[string]any{})
}

func handleECRUntagResource(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ResourceArn string   `json:"resourceArn"`
		TagKeys     []string `json:"tagKeys"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSErrorf(w, "InvalidParameterValue", http.StatusBadRequest, "invalid request body: %v", err)
		return
	}
	name, ok := ecrRepoByArn(req.ResourceArn)
	if !ok {
		AWSErrorf(w, "RepositoryNotFoundException", http.StatusBadRequest, "repository not found: %s", req.ResourceArn)
		return
	}
	remove := map[string]struct{}{}
	for _, k := range req.TagKeys {
		remove[k] = struct{}{}
	}
	ecrRepositories.Update(name, func(repo *ECRRepository) {
		kept := make([]SMTag, 0, len(repo.Tags))
		for _, t := range repo.Tags {
			if _, drop := remove[t.Key]; !drop {
				kept = append(kept, t)
			}
		}
		repo.Tags = kept
	})
	sim.WriteJSON(w, http.StatusOK, map[string]any{})
}

// The compressed sizes of the blobs the manifest references (layers and
// config), and for a manifest list the largest listed manifest; the manifest
// itself does not count. The AWS CLI examples for cluster-autoscaler v1.13.6
// show 48318255 bytes for four layers of 48315478 and a 2777-byte config.
func ecrImageSize(img ECRImageDetail) (int64, error) {
	var manifest struct {
		Config *struct {
			Size int64 `json:"size"`
		} `json:"config"`
		Layers []struct {
			Size int64 `json:"size"`
		} `json:"layers"`
		Manifests []struct {
			Digest string `json:"digest"`
		} `json:"manifests"`
	}
	if err := json.Unmarshal([]byte(img.ImageManifest), &manifest); err != nil {
		return 0, fmt.Errorf("the stored manifest of %s@%s: %w", img.RepositoryName, img.ImageDigest, err)
	}
	var size int64
	if manifest.Config != nil {
		size += manifest.Config.Size
	}
	for _, layer := range manifest.Layers {
		size += layer.Size
	}
	for _, child := range manifest.Manifests {
		if listed, ok := ecrImages.Get(img.RepositoryName + ":" + child.Digest); ok {
			childSize, err := ecrImageSize(listed)
			if err != nil {
				return 0, err
			}
			size = max(size, childSize)
		}
	}
	return size, nil
}
