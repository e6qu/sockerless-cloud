package main

import (
	"crypto/sha256"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

	sim "github.com/e6qu/sockerless-cloud/simulator-aws/shared"
)

// ECR OCI Distribution data plane. The ECR control plane
// (CreateRepository / DescribeImages / layer ops) is awsJson; the actual
// `docker push` / `docker pull` go over the OCI Distribution `/v2/` API. This
// mounts the shared OCI data plane and registers each pushed manifest as an ECR
// image so the control plane sees it.
func registerECROCI(srv *sim.Server) {
	reg := &sim.OCIRegistry{
		Manifests: sim.MakeStore[sim.OCIManifest](srv.DB(), "ecr_oci_manifests"),
		Blobs:     sim.MakeStore[sim.OCIBlob](srv.DB(), "ecr_oci_blobs"),
		Uploads:   sim.MakeStore[sim.OCIUpload](srv.DB(), "ecr_oci_uploads"),
		// Amazon ECR is a private registry: every registry request carries the
		// authorization token GetAuthorizationToken issued (ecr_dataplane_auth.go).
		Authorize: ecrAuthorizeV2,
		// Amazon ECR repositories are explicit resources, so the data plane
		// only serves the ones the registry holds.
		AdmitRepository: ecrAdmitRepository,
		// A pull for a repository a pull-through cache rule covers fetches the
		// image from that rule's upstream registry and caches it here, which is
		// what the rule is for.
		HydrateManifest: ecrHydrateFromPullThroughCache,
		OnManifestPut: func(repo, ref, contentType string, data []byte) {
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
			ecrBumpImageGen()
		},
	}
	reg.Register(srv)
}

// Repository existence on the Amazon ECR data plane.
//
// A registry request naming a repository the registry does not hold is refused:
// "If you specify a Docker Hub repository that does not currently exist, Docker
// Hub creates it automatically. With Amazon ECR, new repositories must be
// explicitly created before they can be used. This prevents new repositories
// from being created accidentally (for example, due to typos), and it also
// ensures that an appropriate security access policy is explicitly assigned to
// any new repositories." (Amazon ECR User Guide, "Troubleshooting Amazon ECR
// error messages" § `HTTP 404: "Repository Does Not Exist" error`.)
//
// The refusal carries the Docker Registry HTTP API v2 error envelope with the
// NAME_UNKNOWN code, which is what a client reports back:
//
//	name unknown: The repository with name 'dev/app1' does not exist in the
//	registry with id 'registry host'
//
// from `docker push` (aws/containers-roadmap#1299), and, printing the code
// verbatim as go-containerregistry does,
//
//	GET https://…/v2/…/tags/list?n=1000: NAME_UNKNOWN: The repository with name
//	'…/eq-author' does not exist in the registry with id '…'
//
// (concourse/registry-image-resource#268) and the same for a manifest GET
// (GoogleContainerTools/kaniko#3443) and for the blob-upload probe a push
// permission check makes (GoogleContainerTools/kaniko#1120). The Docker client
// renders the envelope's code as its lower-cased, space-separated form, which
// is how one refusal produces both spellings.
//
// The one push Amazon ECR does not refuse is the one a repository creation
// template covers: "When you push an image to a repository that does not yet
// exist … When there isn't a repository creation template that matches the
// target repository for an image push, Amazon ECR will not create a repository
// with default settings." (Amazon ECR User Guide, "Templates to control
// repositories created during a pull through cache, create on push, or
// replication action".) So a push whose repository matches a template applied
// for CREATE_ON_PUSH creates the repository from that template and proceeds,
// and every other request for a repository that does not exist is refused.
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
		// The other repository Amazon ECR creates on demand: a pull through a
		// cache rule creates the repository the rule covers and caches the
		// upstream image in it. Refusing here would refuse the whole feature,
		// because the repository a rule names exists only once something has
		// been pulled through it.
		ecrCreateRepositoryForPullThroughCache(repo)
		return true
	}
	ecrRepositoryNameUnknown(w, repo)
	return false
}

// ecrRepositoryNameUnknown writes Amazon ECR's refusal of a registry request
// naming a repository that does not exist.
func ecrRepositoryNameUnknown(w http.ResponseWriter, repo string) {
	sim.WriteJSON(w, http.StatusNotFound, map[string]any{
		"errors": []map[string]any{{
			"code": "NAME_UNKNOWN",
			"message": fmt.Sprintf("The repository with name '%s' does not exist in the registry with id '%s'",
				repo, ecrRegistryId()),
		}},
	})
}

// ecrTemplateAppliedForCreateOnPush is the appliedFor value that puts a
// repository creation template in the path of an image push: "The supported
// scenarios are PULL_THROUGH_CACHE, REPLICATION, and CREATE_ON_PUSH"
// (Amazon ECR API Reference, CreateRepositoryCreationTemplate.appliedFor).
const ecrTemplateAppliedForCreateOnPush = "CREATE_ON_PUSH"

// ecrTemplateRootPrefix is the prefix that matches every repository without a
// more specific template: "To apply a template to all repositories in your
// registry that don't have an associated creation template, you can use ROOT as
// the prefix."
const ecrTemplateRootPrefix = "ROOT"

// ecrCreateOnPushTemplate returns the repository creation template Amazon ECR
// would create a pushed-to repository from.
//
// A prefix carries an assumed trailing slash and the most specific one wins:
// "There is always an assumed / applied to the end of the prefix. If you
// specify ecr-public as the prefix, Amazon ECR treats that as ecr-public/" and
// "In a registry containing two templates, if one template has the prefix
// "prod" and the other has the prefix "prod/team", the template with the prefix
// "prod/team" will be applied to all repositories whose names start with
// "prod/team/"". ROOT is the least specific of all — it applies to the
// repositories no other template matches.
func ecrCreateOnPushTemplate(repo string) (ECRRepositoryCreationTemplate, bool) {
	return ecrCreationTemplateFor(repo, ecrTemplateAppliedForCreateOnPush)
}

// ecrCreationTemplateFor is that lookup for any of the scenarios a template can
// be applied for — a pull through a cache rule creates its repository from one
// exactly as a push does.
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

// ecrCreateRepositoryFromTemplate creates the repository a push named, with the
// settings its repository creation template defines: "Use Amazon ECR repository
// creation templates to define the settings for repositories created by Amazon
// ECR on your behalf." The template's repository policy, lifecycle policy and
// resource tags are applied to the new repository alongside its tag mutability
// and encryption configuration, so GetRepositoryPolicy, GetLifecyclePolicy and
// ListTagsForResource answer for it exactly as they do for a repository created
// through CreateRepository.
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
		// used", and a repository is AES256-encrypted unless the template says
		// otherwise — the same defaults CreateRepository applies.
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
