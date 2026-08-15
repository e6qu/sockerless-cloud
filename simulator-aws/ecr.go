package main

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	sim "github.com/e6qu/sockerless-cloud/simulator-aws/shared"
)

// ECR types

// ECRRepository represents an Elastic Container Registry repository.
//
// RepositoryUri is the canonical AWS registry URL
// (`<account>.dkr.ecr.<region>.amazonaws.com/<repo>`) that consumers
// follow with `docker pull` / `docker push`. The sim's actual ECR
// Docker-registry surface is served at the documented endpoint URL
// (the same listener that serves the AWS API), NOT at the
// repositoryUri. Operators using the sim must override the registry
// resolver (e.g., via `docker daemon --insecure-registry`) or
// configure their tooling to recognize the sim's endpoint as the
// registry host. Marked as external per the
// `sim-emitted-url-roundtrip` skill's "document external" branch —
// the URL points at real AWS by shape; the sim provides the same
// registry surface at a different host.
type ECRRepository struct {
	RepositoryArn  string `json:"repositoryArn"`
	RepositoryName string `json:"repositoryName"`
	RepositoryUri  string `json:"repositoryUri"` // external: canonical <account>.dkr.ecr.<region>.amazonaws.com/<repo>; sim serves the registry API at its own endpoint
	RegistryId     string `json:"registryId"`
	CreatedAt      int64  `json:"createdAt"`
	// Configuration the aws_ecr_repository Terraform provider reads back on
	// refresh; the sim must echo what CreateRepository received (or the AWS
	// defaults) or the provider shows perpetual drift.
	ImageTagMutability         string                        `json:"imageTagMutability"`
	EncryptionConfiguration    ECREncryptionConfiguration    `json:"encryptionConfiguration"`
	ImageScanningConfiguration ECRImageScanningConfiguration `json:"imageScanningConfiguration"`
	// imageTagMutabilityExclusionFilters round-trips through the
	// aws_ecr_repository Terraform provider and PutImageTagMutability;
	// omitted from DescribeRepositories when unset (AWS does the same).
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

// ECRPullThroughCacheRule models an ECR pull-through cache rule. The
// simulator stores these so callers (sockerless ECS backend, aws CLI,
// terraform) can register, list, and delete them just like real ECR.
// The rule is consulted when a container image URI's registry path
// starts with `<account>.dkr.ecr.<region>.amazonaws.com/<prefix>/…`
// and `<prefix>` matches a registered `EcrRepositoryPrefix`.
//
// UpstreamRegistryUrl is EXTERNAL by design — it's the third-party
// registry the pull-through cache pulls from (`registry-1.docker.io`,
// `public.ecr.aws`, `ghcr.io`, etc.). The sim doesn't service the
// upstream itself; real ECR fetches the upstream image when an
// authenticated pull arrives, and the sim's image-resolver does the
// same for backends pointed at the sim's registry surface.
type ECRPullThroughCacheRule struct {
	EcrRepositoryPrefix string `json:"ecrRepositoryPrefix"`
	UpstreamRegistryUrl string `json:"upstreamRegistryUrl"` // external: operator-supplied upstream registry (docker.io / ghcr.io / public.ecr.aws / etc.)
	UpstreamRegistry    string `json:"upstreamRegistry,omitempty"`
	RegistryId          string `json:"registryId"`
	CreatedAt           int64  `json:"createdAt"`
	UpdatedAt           int64  `json:"updatedAt,omitempty"`
	// CredentialArn / CustomRoleArn are set by UpdatePullThroughCacheRule when
	// the upstream registry requires authentication; echoed back by
	// ValidatePullThroughCacheRule.
	CredentialArn string `json:"credentialArn,omitempty"`
	CustomRoleArn string `json:"customRoleArn,omitempty"`
}

// State stores
var (
	ecrRepositories          sim.Store[ECRRepository]
	ecrImages                sim.Store[ECRImageDetail]
	ecrLifecyclePolicies     sim.Store[ECRLifecyclePolicy]
	ecrPullThroughCacheRules sim.Store[ECRPullThroughCacheRule]
)

// ecrRegistryId() returns the registry ID — same as the AWS account ID.
// Real ECR uses the caller's account; the sim defers to awsAccountID
// so a SOCKERLESS_AWS_ACCOUNT_ID override propagates through every ECR
// ARN, repository URI, and authorization-token endpoint.
func ecrRegistryId() string { return awsAccountID() }

func ecrArn(resourceType, name string) string {
	return "arn:aws:ecr:" + awsRegion() + ":" + ecrRegistryId() + ":" + resourceType + "/" + name
}

func registerECR(r *sim.AWSRouter, srv *sim.Server) {
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

	// Pull-through cache rules. Used by sockerless image resolvers
	// and by terraform's aws_ecr_pull_through_cache_rule resource.
	// Backend caller builds URIs like
	// `<account>.dkr.ecr.<region>.amazonaws.com/<prefix>/<repo>:<tag>`
	// which the simulator's ResolveLocalImage recognizes as a cache
	// hit and rewrites to the upstream registry on first pull.
	r.Register("AmazonEC2ContainerRegistry_V20150921.CreatePullThroughCacheRule", handleECRCreatePullThroughCacheRule)
	r.Register("AmazonEC2ContainerRegistry_V20150921.DescribePullThroughCacheRules", handleECRDescribePullThroughCacheRules)
	r.Register("AmazonEC2ContainerRegistry_V20150921.DeletePullThroughCacheRule", handleECRDeletePullThroughCacheRule)

	registerECRRegistry(r, srv)
	registerECRAdvanced(r, srv)
	registerECRLayers(r, srv)
	registerECROCI(srv)
}

// handleECRCreatePullThroughCacheRule registers a pull-through cache
// rule. Returns PullThroughCacheRuleAlreadyExistsException if the
// prefix is already in use — matches real AWS behaviour so sockerless
// and terraform's `aws_ecr_pull_through_cache_rule` see the same errors.
func handleECRCreatePullThroughCacheRule(w http.ResponseWriter, r *http.Request) {
	var req struct {
		EcrRepositoryPrefix string `json:"ecrRepositoryPrefix"`
		UpstreamRegistryUrl string `json:"upstreamRegistryUrl"`
		UpstreamRegistry    string `json:"upstreamRegistry"`
		RegistryId          string `json:"registryId"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "InvalidParameterException", "Invalid request body", http.StatusBadRequest)
		return
	}
	if req.EcrRepositoryPrefix == "" || req.UpstreamRegistryUrl == "" {
		sim.AWSError(w, "InvalidParameterException", "ecrRepositoryPrefix and upstreamRegistryUrl are required", http.StatusBadRequest)
		return
	}
	if _, exists := ecrPullThroughCacheRules.Get(req.EcrRepositoryPrefix); exists {
		sim.AWSError(w, "PullThroughCacheRuleAlreadyExistsException",
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

	sim.WriteJSON(w, http.StatusOK, map[string]any{
		"ecrRepositoryPrefix": rule.EcrRepositoryPrefix,
		"upstreamRegistryUrl": rule.UpstreamRegistryUrl,
		"upstreamRegistry":    rule.UpstreamRegistry,
		"registryId":          rule.RegistryId,
		"createdAt":           rule.CreatedAt,
	})
}

// handleECRDescribePullThroughCacheRules returns rules matching the
// requested prefixes, or all rules when the request is empty. Matches
// the real API's pagination-less response shape for the test-sized
// rule sets the simulator supports.
func handleECRDescribePullThroughCacheRules(w http.ResponseWriter, r *http.Request) {
	var req struct {
		EcrRepositoryPrefixes []string `json:"ecrRepositoryPrefixes"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSErrorf(w, "InvalidParameterValue", http.StatusBadRequest, "invalid request body: %v", err)
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

// handleECRDeletePullThroughCacheRule removes a rule. Returns
// PullThroughCacheRuleNotFoundException when the prefix isn't
// registered — matches real AWS.
func handleECRDeletePullThroughCacheRule(w http.ResponseWriter, r *http.Request) {
	var req struct {
		EcrRepositoryPrefix string `json:"ecrRepositoryPrefix"`
		RegistryId          string `json:"registryId"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "InvalidParameterException", "Invalid request body", http.StatusBadRequest)
		return
	}
	if req.EcrRepositoryPrefix == "" {
		sim.AWSError(w, "InvalidParameterException", "ecrRepositoryPrefix is required", http.StatusBadRequest)
		return
	}
	rule, ok := ecrPullThroughCacheRules.Get(req.EcrRepositoryPrefix)
	if !ok {
		sim.AWSError(w, "PullThroughCacheRuleNotFoundException",
			"The pull-through cache rule does not exist",
			http.StatusNotFound)
		return
	}
	ecrPullThroughCacheRules.Delete(req.EcrRepositoryPrefix)

	// Unlike the create/describe shapes, DeletePullThroughCacheRuleResponse
	// has no upstreamRegistry member.
	sim.WriteJSON(w, http.StatusOK, map[string]any{
		"ecrRepositoryPrefix": rule.EcrRepositoryPrefix,
		"upstreamRegistryUrl": rule.UpstreamRegistryUrl,
		"registryId":          rule.RegistryId,
		"createdAt":           rule.CreatedAt,
	})
}

// Pull-through-cache URI → local docker ref resolution is handled by
// `sim.ResolveLocalImage` in simulator-aws/shared/container.go, which
// already strips `docker-hub/` + `library/` prefixes from ECR URIs
// before the simulator pulls. The handlers above are what sockerless's
// image-resolver + terraform's aws_ecr_pull_through_cache_rule need;
// the launch-time URI mapping lives in the shared helper.

func handleECRCreateRepository(w http.ResponseWriter, r *http.Request) {
	var req struct {
		RepositoryName             string                         `json:"repositoryName"`
		ImageTagMutability         string                         `json:"imageTagMutability"`
		EncryptionConfiguration    *ECREncryptionConfiguration    `json:"encryptionConfiguration"`
		ImageScanningConfiguration *ECRImageScanningConfiguration `json:"imageScanningConfiguration"`
		Tags                       []SMTag                        `json:"tags"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "InvalidParameterException", "Invalid request body", http.StatusBadRequest)
		return
	}
	if req.RepositoryName == "" {
		sim.AWSError(w, "InvalidParameterException", "repositoryName is required", http.StatusBadRequest)
		return
	}

	if _, exists := ecrRepositories.Get(req.RepositoryName); exists {
		sim.AWSErrorf(w, "RepositoryAlreadyExistsException", http.StatusBadRequest,
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
		// Real ECR defaults to AES256 server-side encryption.
		EncryptionConfiguration: ECREncryptionConfiguration{EncryptionType: "AES256"},
	}
	if repo.ImageTagMutability == "" {
		repo.ImageTagMutability = "MUTABLE" // AWS default
	}
	if req.EncryptionConfiguration != nil && req.EncryptionConfiguration.EncryptionType != "" {
		repo.EncryptionConfiguration = *req.EncryptionConfiguration
	}
	if req.ImageScanningConfiguration != nil {
		repo.ImageScanningConfiguration = *req.ImageScanningConfiguration
	}
	// Tags set at create round-trip through ListTagsForResource; real ECR
	// accepts tags on CreateRepository and dropping them drifts every plan.
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
		sim.AWSError(w, "InvalidParameterException", "Invalid request body", http.StatusBadRequest)
		return
	}

	var repos []ECRRepository
	if len(req.RepositoryNames) == 0 {
		repos = ecrRepositories.List()
		sortBy(repos, func(r ECRRepository) string { return r.RepositoryName })
	} else {
		for _, name := range req.RepositoryNames {
			repo, ok := ecrRepositories.Get(name)
			if !ok {
				sim.AWSErrorf(w, "RepositoryNotFoundException", http.StatusBadRequest,
					"The repository with name '%s' does not exist in the registry with id '%s'", name, awsAccountID())
				return
			}
			repos = append(repos, repo)
		}
	}
	if repos == nil {
		repos = []ECRRepository{}
	}

	page, next := awsPage(repos, req.NextToken, req.MaxResults, 1000)
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
		sim.AWSError(w, "InvalidParameterException", "Invalid request body", http.StatusBadRequest)
		return
	}
	if req.RepositoryName == "" {
		sim.AWSError(w, "InvalidParameterException", "repositoryName is required", http.StatusBadRequest)
		return
	}

	repo, ok := ecrRepositories.Get(req.RepositoryName)
	if !ok {
		sim.AWSErrorf(w, "RepositoryNotFoundException", http.StatusBadRequest,
			"The repository with name '%s' does not exist", req.RepositoryName)
		return
	}

	ecrRepositories.Delete(req.RepositoryName)

	sim.WriteJSON(w, http.StatusOK, map[string]any{
		"repository": repo,
	})
}

func handleECRGetAuthorizationToken(w http.ResponseWriter, r *http.Request) {
	if err := sim.ReadJSON(r, &struct{}{}); err != nil {
		sim.AWSErrorf(w, "InvalidParameterValue", http.StatusBadRequest, "invalid request body: %v", err)
		return
	}

	token, expires, err := ecrIssueAuthorizationToken()
	if err != nil {
		sim.AWSErrorf(w, "ServerException", http.StatusInternalServerError,
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

// ecrImageIndex caches the registry's images grouped by repository name so a
// per-repo lookup doesn't scan every image in the whole registry (each image is
// stored under both its tag and its digest key, doubling the raw row count). It
// is rebuilt from ecrImages — the source of truth, which survives SQLite-backed
// restarts — only when a Put/Delete has bumped the generation since the last
// build, so a burst of ListImages/DescribeImages calls between pushes pays the
// scan once.
var ecrImageIndex struct {
	mu     sync.Mutex
	gen    uint64
	byRepo map[string][]ECRImageDetail
}

// ecrImageGen is bumped on every ecrImages Put/Delete to invalidate the index.
var ecrImageGen atomic.Uint64

// ecrBumpImageGen invalidates the per-repo image index. Call after any ecrImages
// Put/Delete (in ecr.go and ecr_oci.go).
func ecrBumpImageGen() { ecrImageGen.Add(1) }

// ecrRepoImages returns the distinct images in a repository (the ecrImages
// store holds each image under both its tag and its digest key, so dedup by
// digest). The deduped set is identical to a full-registry scan filtered to the
// repo; the per-repo index just avoids re-walking unrelated repositories.
func ecrRepoImages(repo string) []ECRImageDetail {
	gen := ecrImageGen.Load()
	ecrImageIndex.mu.Lock()
	defer ecrImageIndex.mu.Unlock()
	if ecrImageIndex.byRepo == nil || ecrImageIndex.gen != gen {
		byRepo := make(map[string][]ECRImageDetail)
		seenByRepo := make(map[string]map[string]bool)
		for _, img := range ecrImages.List() {
			seen := seenByRepo[img.RepositoryName]
			if seen == nil {
				seen = map[string]bool{}
				seenByRepo[img.RepositoryName] = seen
			}
			if seen[img.ImageDigest] {
				continue
			}
			seen[img.ImageDigest] = true
			byRepo[img.RepositoryName] = append(byRepo[img.RepositoryName], img)
		}
		ecrImageIndex.byRepo = byRepo
		ecrImageIndex.gen = gen
	}
	src := ecrImageIndex.byRepo[repo]
	// Return a copy so callers can't mutate the cached slice.
	out := make([]ECRImageDetail, len(src))
	copy(out, src)
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
		sim.AWSError(w, "InvalidParameterException", "Invalid request body", http.StatusBadRequest)
		return
	}
	if _, ok := ecrRepositories.Get(req.RepositoryName); !ok {
		sim.AWSErrorf(w, "RepositoryNotFoundException", http.StatusBadRequest, "The repository with name '%s' does not exist", req.RepositoryName)
		return
	}
	// filter.tagStatus ∈ TAGGED | UNTAGGED | ANY (default ANY). Real ECR
	// restricts the returned imageIds to the requested tag presence.
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
	sort.Slice(imageIds, func(i, j int) bool {
		if imageIds[i]["imageDigest"] != imageIds[j]["imageDigest"] {
			return imageIds[i]["imageDigest"] < imageIds[j]["imageDigest"]
		}
		return imageIds[i]["imageTag"] < imageIds[j]["imageTag"]
	})
	page, next := awsPageExplicit(imageIds, req.NextToken, req.MaxResults)
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
		sim.AWSError(w, "InvalidParameterException", "Invalid request body", http.StatusBadRequest)
		return
	}
	if _, ok := ecrRepositories.Get(req.RepositoryName); !ok {
		sim.AWSErrorf(w, "RepositoryNotFoundException", http.StatusBadRequest, "The repository with name '%s' does not exist", req.RepositoryName)
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

	// filter.tagStatus ∈ TAGGED | UNTAGGED | ANY (default ANY).
	if tagStatus := strings.ToUpper(req.Filter.TagStatus); tagStatus == "TAGGED" || tagStatus == "UNTAGGED" {
		var f []ECRImageDetail
		for _, img := range images {
			if (len(img.ImageTags) > 0) == (tagStatus == "TAGGED") {
				f = append(f, img)
			}
		}
		images = f
	}
	sort.Slice(images, func(i, j int) bool { return images[i].ImageDigest < images[j].ImageDigest })
	page, next := awsPageExplicit(images, req.NextToken, req.MaxResults)

	details := make([]map[string]any, 0, len(page))
	for _, img := range page {
		tags := img.ImageTags
		if tags == nil {
			tags = []string{}
		}
		details = append(details, map[string]any{
			"registryId":       img.RegistryId,
			"repositoryName":   img.RepositoryName,
			"imageDigest":      img.ImageDigest,
			"imageTags":        tags,
			"imageSizeInBytes": len(img.ImageManifest),
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
		sim.AWSError(w, "InvalidParameterException", "Invalid request body", http.StatusBadRequest)
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
		sim.AWSError(w, "InvalidParameterException", "Invalid request body", http.StatusBadRequest)
		return
	}
	if req.RepositoryName == "" || req.ImageManifest == "" {
		sim.AWSError(w, "InvalidParameterException", "repositoryName and imageManifest are required", http.StatusBadRequest)
		return
	}

	if _, ok := ecrRepositories.Get(req.RepositoryName); !ok {
		sim.AWSErrorf(w, "RepositoryNotFoundException", http.StatusBadRequest,
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
	// Also store by digest
	ecrImages.Put(req.RepositoryName+":"+digest, img)
	ecrBumpImageGen()

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
		sim.AWSError(w, "InvalidParameterException", "Invalid request body", http.StatusBadRequest)
		return
	}

	if _, ok := ecrRepositories.Get(req.RepositoryName); !ok {
		sim.AWSErrorf(w, "RepositoryNotFoundException", http.StatusBadRequest,
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
			// Each image is stored under its digest key and one key per tag —
			// delete every alias for the content, or DescribeImages/ListImages
			// (which dedup by digest) would still surface a "deleted" image.
			ecrImages.Delete(req.RepositoryName + ":" + img.ImageDigest)
			for _, tag := range img.ImageTags {
				ecrImages.Delete(req.RepositoryName + ":" + tag)
			}
			ecrImages.Delete(key)
			ecrBumpImageGen()
			// Deleted entries are bare ImageIdentifier objects. Real ECR
			// resolves the digest even when the request deleted by tag.
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
		sim.AWSError(w, "InvalidParameterException", "Invalid request body", http.StatusBadRequest)
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
		sim.AWSError(w, "InvalidParameterException", "Invalid request body", http.StatusBadRequest)
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
		sim.AWSError(w, "InvalidParameterException", "Invalid request body", http.StatusBadRequest)
		return
	}

	policy, ok := ecrLifecyclePolicies.Get(req.RepositoryName)
	if !ok {
		sim.AWSErrorf(w, "LifecyclePolicyNotFoundException", http.StatusBadRequest,
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
		sim.AWSError(w, "InvalidParameterException", "Invalid request body", http.StatusBadRequest)
		return
	}

	policy, ok := ecrLifecyclePolicies.Get(req.RepositoryName)
	if !ok {
		sim.AWSErrorf(w, "LifecyclePolicyNotFoundException", http.StatusBadRequest,
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

// ecrRepoByArn resolves a repository ARN (arn:aws:ecr:…:repository/<name>) to
// its stored name.
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
	// Terraform uses this to read tags for ECR repositories
	var req struct {
		ResourceArn string `json:"resourceArn"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSErrorf(w, "InvalidParameterValue", http.StatusBadRequest, "invalid request body: %v", err)
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
		sim.AWSErrorf(w, "InvalidParameterValue", http.StatusBadRequest, "invalid request body: %v", err)
		return
	}
	name, ok := ecrRepoByArn(req.ResourceArn)
	if !ok {
		sim.AWSErrorf(w, "RepositoryNotFoundException", http.StatusBadRequest, "repository not found: %s", req.ResourceArn)
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
		sim.AWSErrorf(w, "InvalidParameterValue", http.StatusBadRequest, "invalid request body: %v", err)
		return
	}
	name, ok := ecrRepoByArn(req.ResourceArn)
	if !ok {
		sim.AWSErrorf(w, "RepositoryNotFoundException", http.StatusBadRequest, "repository not found: %s", req.ResourceArn)
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
