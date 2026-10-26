package main

import (
	"crypto/sha256"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
)

// `docker push` and `docker pull` speak the OCI Distribution `/v2/` API; each
// pushed manifest registers as an ECR image the control plane sees.
var (
	ecrOCIManifests sim.Store[sim.OCIManifest]
	ecrOCIBlobs     sim.Store[sim.OCIBlob]
)

func registerECROCI(srv *sim.Server) {
	ecrOCIManifests = sim.MakeStore[sim.OCIManifest](srv.DB(), "ecr_oci_manifests")
	ecrOCIBlobs = sim.MakeStore[sim.OCIBlob](srv.DB(), "ecr_oci_blobs")
	reg := &sim.OCIRegistry{
		Manifests: ecrOCIManifests,
		Blobs:     ecrOCIBlobs,
		Uploads:   sim.MakeStore[sim.OCIUpload](srv.DB(), "ecr_oci_uploads"),
		// Every registry request carries a GetAuthorizationToken token
		// (ecr_dataplane_auth.go).
		Authorize: ecrAuthorizeV2,
		// Repositories are explicit resources; serve only those the registry holds.
		AdmitRepository: ecrAdmitRepository,
		// A pull a cache rule covers fetches and caches the upstream image.
		HydrateManifest: ecrHydrateFromPullThroughCache,
		OnManifestPut: func(_, repo, ref, contentType string, data []byte) {
			digest := fmt.Sprintf("sha256:%x", sha256.Sum256(data))
			detail := ECRImageDetail{
				RegistryId:     ecrRegistryId(),
				RepositoryName: repo,
				ImageDigest:    digest,
				ImageTags:      []string{ref},
				ImageManifest:  string(data),
				PushedAt:       time.Now().Unix(),
			}
			ecrImages.Put(repo+":"+ref, detail)
			ecrImages.Put(repo+":"+digest, detail)
		},
	}
	reg.Register(srv)
}

// "With Amazon ECR, new repositories must be explicitly created before they
// can be used." A request for an absent repository gets the Registry v2
// NAME_UNKNOWN envelope, which clients print as
//
//	name unknown: The repository with name 'dev/app1' does not exist in the
//	registry with id 'registry host'
//
// (aws/containers-roadmap#1299) or with the code verbatim (go-containerregistry).
//
// The exception is a push a CREATE_ON_PUSH repository creation template
// covers: "When there isn't a repository creation template that matches the
// target repository for an image push, Amazon ECR will not create a
// repository", so a matching template creates the repository and the push
// proceeds.
func ecrAdmitRepository(w http.ResponseWriter, _ *http.Request, repo string, push bool) bool {
	if _, exists := ecrRepositories.Get(repo); exists {
		return true
	}
	if push {
		if template, matched := ecrCreateOnPushTemplate(repo); matched {
			ecrCreateRepositoryFromTemplate(repo, template)
			return true
		}
	} else if _, _, covered := ecrPullThroughCacheRuleFor(repo); covered {
		// A pull through a cache rule also creates its repository; refusing here
		// would disable the feature, since the repository exists only after the first
		// pull.
		ecrCreateRepositoryForPullThroughCache(repo)
		return true
	}
	ecrRepositoryNameUnknown(w, repo)
	return false
}

func ecrRepositoryNameUnknown(w http.ResponseWriter, repo string) {
	sim.WriteJSON(w, http.StatusNotFound, map[string]any{
		"errors": []map[string]any{{
			"code": "NAME_UNKNOWN",
			"message": fmt.Sprintf("The repository with name '%s' does not exist in the registry with id '%s'",
				repo, ecrRegistryId()),
		}},
	})
}

// "The supported scenarios are PULL_THROUGH_CACHE, REPLICATION, and
// CREATE_ON_PUSH".
const ecrTemplateAppliedForCreateOnPush = "CREATE_ON_PUSH"

// "To apply a template to all repositories in your registry that don't have
// an associated creation template, you can use ROOT as the prefix."
const ecrTemplateRootPrefix = "ROOT"

// A prefix carries an assumed trailing slash and the most specific wins:
// "prod/team" beats "prod" for "prod/team/…". ROOT applies only where nothing
// else matches.
func ecrCreateOnPushTemplate(repo string) (ECRRepositoryCreationTemplate, bool) {
	return ecrCreationTemplateFor(repo, ecrTemplateAppliedForCreateOnPush)
}

// A pull-through-cache repository is created from a template exactly as a push
// is.
func ecrCreationTemplateFor(repo, appliedFor string) (ECRRepositoryCreationTemplate, bool) {
	var (
		match   ECRRepositoryCreationTemplate
		matched bool
		root    ECRRepositoryCreationTemplate
		isRoot  bool
	)
	for _, template := range ecrRepoCreationTemplates.List() {
		if !slices.Contains(template.AppliedFor, appliedFor) {
			continue
		}
		if template.Prefix == ecrTemplateRootPrefix {
			root, isRoot = template, true
			continue
		}
		prefix := strings.TrimSuffix(template.Prefix, "/") + "/"
		if !strings.HasPrefix(repo, prefix) {
			continue
		}
		if !matched || len(prefix) > len(strings.TrimSuffix(match.Prefix, "/"))+1 {
			match, matched = template, true
		}
	}
	if matched {
		return match, true
	}
	return root, isRoot
}

// Apply the template's repository policy, lifecycle policy and tags with its
// tag mutability and encryption, so the Get operations answer as for a
// CreateRepository repository.
func ecrCreateRepositoryFromTemplate(repo string, template ECRRepositoryCreationTemplate) {
	created := ECRRepository{
		RepositoryArn:                      ecrArn("repository", repo),
		RepositoryName:                     repo,
		RepositoryUri:                      ecrRegistryId() + ".dkr.ecr." + awsRegion() + ".amazonaws.com/" + repo,
		RegistryId:                         ecrRegistryId(),
		CreatedAt:                          time.Now().Unix(),
		ImageTagMutability:                 template.ImageTagMutability,
		ImageTagMutabilityExclusionFilters: template.ImageTagMutabilityExclusionFilters,
		// "If this parameter is omitted, the default setting of MUTABLE will be
		// used"; encryption defaults to AES256, as CreateRepository's do.
		EncryptionConfiguration: ECREncryptionConfiguration{EncryptionType: "AES256"},
		Tags:                    template.ResourceTags,
	}
	if created.ImageTagMutability == "" {
		created.ImageTagMutability = "MUTABLE"
	}
	if template.EncryptionConfiguration != nil && template.EncryptionConfiguration.EncryptionType != "" {
		created.EncryptionConfiguration = *template.EncryptionConfiguration
	}
	ecrRepositories.Put(repo, created)
	if template.RepositoryPolicy != "" {
		ecrRepoPolicies.Put(repo, template.RepositoryPolicy)
	}
	if template.LifecyclePolicy != "" {
		ecrLifecyclePolicies.Put(repo, ECRLifecyclePolicy{
			RegistryId:          ecrRegistryId(),
			RepositoryName:      repo,
			LifecyclePolicyText: template.LifecyclePolicy,
		})
	}
}
