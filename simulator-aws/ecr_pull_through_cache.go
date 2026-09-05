package main

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
)

// A pull through a cache rule fetches the image from the upstream registry.
//
// "When you use a pull through cache rule, Amazon ECR creates the repository
// the first time an image is pulled through it, and then pulls and caches the
// upstream image in that repository" — the rule maps an Amazon ECR repository
// prefix onto an external registry, and the pull is what populates it. Amazon
// ECR calls this the cache being *hydrated*, and until it happens the
// repository does not exist.
//
// The simulator registered rules and served them to the control plane —
// CreatePullThroughCacheRule, DescribePullThroughCacheRules,
// ValidatePullThroughCacheRule — and then refused every pull through one,
// because the repository the rule names had never been created. That is a
// registry that accepts the configuration for a feature and does not implement
// it, and the refusal was `NAME_UNKNOWN`: the caller was told the repository
// does not exist, when creating it is precisely what the rule asks for.
//
// The upstream image is fetched through the container engine the simulator
// already runs its workloads on. The bytes served are the upstream registry's
// own: the engine pulls the image the rule points at, the image is read back
// out of it, and its config and layers are stored as the repository's blobs.
// That is what makes this a cache rather than a stand-in — a pull through
// `docker-hub/library/alpine` serves the Docker Hub `library/alpine` a client
// would have got directly, and a repository the upstream does not hold fails
// the way a miss fails.

// ecrTemplateAppliedForPullThroughCache is the appliedFor value that puts a
// repository creation template in the path of a pull through a cache rule:
// "The supported scenarios are PULL_THROUGH_CACHE, REPLICATION, and
// CREATE_ON_PUSH" (Amazon ECR API Reference,
// CreateRepositoryCreationTemplate.appliedFor).
const ecrTemplateAppliedForPullThroughCache = "PULL_THROUGH_CACHE"

// ecrPullThroughCacheRuleFor returns the rule covering a repository, and the
// upstream image path the rule maps it to.
//
// A rule's `ecrRepositoryPrefix` is the first segment (or segments) of the
// Amazon ECR repository name, and what follows is the repository as the
// upstream registry names it: with a `docker-hub` rule for
// `registry-1.docker.io`, `docker-hub/library/alpine` is Docker Hub's
// `library/alpine`. The longest matching prefix wins, so a rule for a narrower
// prefix is not shadowed by a broader one.
func ecrPullThroughCacheRuleFor(repo string) (ECRPullThroughCacheRule, string, bool) {
	var (
		match     ECRPullThroughCacheRule
		remainder string
		matched   bool
	)
	for _, rule := range ecrPullThroughCacheRules.List() {
		prefix := strings.Trim(rule.EcrRepositoryPrefix, "/")
		if prefix == "" || !strings.HasPrefix(repo, prefix+"/") {
			continue
		}
		if matched && len(prefix) <= len(strings.Trim(match.EcrRepositoryPrefix, "/")) {
			continue
		}
		match, remainder, matched = rule, strings.TrimPrefix(repo, prefix+"/"), true
	}
	if !matched || remainder == "" {
		return ECRPullThroughCacheRule{}, "", false
	}
	return match, remainder, true
}

// ecrUpstreamImageRef is the image reference the engine pulls for a repository
// covered by a rule.
//
// Docker Hub is named `registry-1.docker.io` in a rule's upstreamRegistryUrl —
// that is the endpoint the API is served on — while an engine canonicalises
// Docker Hub to `docker.io`. They are the same registry under two names, and
// the engine only resolves credentials and mirrors correctly for the
// canonical one.
func ecrUpstreamImageRef(rule ECRPullThroughCacheRule, remainder, reference string) string {
	host := strings.TrimSuffix(strings.TrimPrefix(strings.TrimPrefix(
		rule.UpstreamRegistryUrl, "https://"), "http://"), "/")
	switch host {
	case "registry-1.docker.io", "index.docker.io", "":
		host = "docker.io"
	}
	// A registry reference is either a tag or a digest, and the two are
	// separated differently. A client pulling by digest asks for the same
	// upstream content by digest.
	if strings.HasPrefix(reference, "sha256:") {
		return host + "/" + remainder + "@" + reference
	}
	return host + "/" + remainder + ":" + reference
}

// ecrHydrateFromPullThroughCache fetches the upstream image a rule points at
// and stores it as the repository's content, which is what makes the following
// manifest read succeed. It reports whether the cache was hydrated.
func ecrHydrateFromPullThroughCache(reg *sim.OCIRegistry, scope, repo, reference string) bool {
	rule, remainder, ok := ecrPullThroughCacheRuleFor(repo)
	if !ok {
		return false
	}
	if err := ecrCacheUpstreamImage(reg, scope, rule, repo, remainder, reference); err != nil {
		fmt.Fprintf(os.Stderr, "[sim-aws-ecr] pull through cache miss for %s:%s via %s: %v\n",
			repo, reference, rule.UpstreamRegistryUrl, err)
		return false
	}
	return true
}

func ecrCacheUpstreamImage(reg *sim.OCIRegistry, scope string, rule ECRPullThroughCacheRule, repo, remainder, reference string) error {
	if err := sim.RequireContainerRuntime("hydrating an Amazon ECR pull through cache rule"); err != nil {
		return err
	}
	upstream := ecrUpstreamImageRef(rule, remainder, reference)
	ctx := context.Background()
	if err := sim.PullImage(ctx, upstream, ""); err != nil {
		return fmt.Errorf("fetch %s from the upstream registry: %w", upstream, err)
	}
	saved, err := sim.DockerClient().ImageSave(ctx, []string{upstream})
	if err != nil {
		return fmt.Errorf("read %s back from the engine: %w", upstream, err)
	}
	defer func() { _ = saved.Close() }()

	manifestData, files, err := ecrReadImageSave(saved)
	if err != nil {
		return err
	}
	var entries []struct {
		Config   string   `json:"Config"`
		RepoTags []string `json:"RepoTags"`
		Layers   []string `json:"Layers"`
	}
	if err := json.Unmarshal(manifestData, &entries); err != nil {
		return fmt.Errorf("decode the engine's image manifest: %w", err)
	}
	if len(entries) == 0 {
		return fmt.Errorf("the engine's image manifest is empty")
	}
	image := entries[0]
	configData, ok := files[image.Config]
	if !ok {
		return fmt.Errorf("image config %q is missing from the engine's export", image.Config)
	}

	// The image is served as an OCI image: the engine's config blob is
	// byte-compatible with the OCI image-config schema, so it carries the OCI
	// media type rather than the Docker v2s2 one. A manifest that mixes the two
	// is rejected by a `docker build` FROM ("invalid mixed OCI image with
	// Docker v2s2 config").
	configDigest := ecrDigestOf(configData)
	reg.PutBlob(scope, repo, configDigest, "application/vnd.oci.image.config.v1+json", configData)

	type descriptor struct {
		MediaType string `json:"mediaType"`
		Size      int64  `json:"size"`
		Digest    string `json:"digest"`
	}
	layers := make([]descriptor, 0, len(image.Layers))
	for _, path := range image.Layers {
		data, ok := files[path]
		if !ok {
			return fmt.Errorf("image layer %q is missing from the engine's export", path)
		}
		digest := ecrDigestOf(data)
		reg.PutBlob(scope, repo, digest, "application/vnd.oci.image.layer.v1.tar", data)
		layers = append(layers, descriptor{
			MediaType: "application/vnd.oci.image.layer.v1.tar",
			Size:      int64(len(data)),
			Digest:    digest,
		})
	}

	manifest, err := json.Marshal(map[string]any{
		"schemaVersion": 2,
		"mediaType":     "application/vnd.oci.image.manifest.v1+json",
		"config": descriptor{
			MediaType: "application/vnd.oci.image.config.v1+json",
			Size:      int64(len(configData)),
			Digest:    configDigest,
		},
		"layers": layers,
	})
	if err != nil {
		return fmt.Errorf("encode the cached image's manifest: %w", err)
	}
	reg.PutManifest(scope, repo, reference, "application/vnd.oci.image.manifest.v1+json", manifest)
	return nil
}

// ecrReadImageSave reads an engine image export, returning its manifest and
// every file in it by name.
func ecrReadImageSave(r io.Reader) ([]byte, map[string][]byte, error) {
	reader := tar.NewReader(r)
	files := map[string][]byte{}
	var manifest []byte
	for {
		header, err := reader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, nil, fmt.Errorf("read the engine's image export: %w", err)
		}
		if header.Typeflag == tar.TypeDir {
			continue
		}
		var buf bytes.Buffer
		if _, err := io.Copy(&buf, reader); err != nil {
			return nil, nil, fmt.Errorf("read %q from the engine's image export: %w", header.Name, err)
		}
		files[header.Name] = buf.Bytes()
		if header.Name == "manifest.json" {
			manifest = buf.Bytes()
		}
	}
	if len(manifest) == 0 {
		return nil, nil, fmt.Errorf("the engine's image export carries no manifest.json")
	}
	return manifest, files, nil
}

func ecrDigestOf(data []byte) string {
	return fmt.Sprintf("sha256:%x", sha256.Sum256(data))
}

// ecrCreateRepositoryForPullThroughCache creates the repository a rule covers,
// which Amazon ECR does on the first pull through it. A repository creation
// template applied for PULL_THROUGH_CACHE governs the settings when one matches
// — "Templates to control repositories created during a pull through cache,
// create on push, or replication action" — and the repository is created with
// the same defaults CreateRepository applies when none does.
func ecrCreateRepositoryForPullThroughCache(repo string) {
	if template, matched := ecrCreationTemplateFor(repo, ecrTemplateAppliedForPullThroughCache); matched {
		ecrCreateRepositoryFromTemplate(repo, template)
		return
	}
	ecrRepositories.Put(repo, ECRRepository{
		RepositoryArn:           ecrArn("repository", repo),
		RepositoryName:          repo,
		RepositoryUri:           ecrRegistryId() + ".dkr.ecr." + awsRegion() + ".amazonaws.com/" + repo,
		RegistryId:              ecrRegistryId(),
		CreatedAt:               time.Now().Unix(),
		ImageTagMutability:      "MUTABLE",
		EncryptionConfiguration: ECREncryptionConfiguration{EncryptionType: "AES256"},
	})
}
