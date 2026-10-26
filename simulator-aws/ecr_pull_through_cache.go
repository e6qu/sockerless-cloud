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

// "When you use a pull through cache rule, Amazon ECR creates the repository
// the first time an image is pulled through it, and then pulls and caches the
// upstream image in that repository."
//
// The container engine pulls the upstream image, and its config and layers
// become the repository's blobs, so a pull through
// `docker-hub/library/alpine` serves Docker Hub's own `library/alpine`, and an
// upstream miss fails as a miss.

// "The supported scenarios are PULL_THROUGH_CACHE, REPLICATION, and
// CREATE_ON_PUSH".
const ecrTemplateAppliedForPullThroughCache = "PULL_THROUGH_CACHE"

// After the rule's ecrRepositoryPrefix comes the upstream's own name: a
// `docker-hub` rule maps `docker-hub/library/alpine` to Docker Hub's
// `library/alpine`. The longest matching prefix wins.
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

// A rule names Docker Hub `registry-1.docker.io`, its API endpoint, but the
// engine resolves credentials and mirrors only for the canonical `docker.io`.
func ecrUpstreamImageRef(rule ECRPullThroughCacheRule, remainder, reference string) string {
	host := strings.TrimSuffix(strings.TrimPrefix(strings.TrimPrefix(
		rule.UpstreamRegistryUrl, "https://"), "http://"), "/")
	switch host {
	case "registry-1.docker.io", "index.docker.io", "":
		host = "docker.io"
	}
	// A digest reference asks the upstream for the same digest.
	if strings.HasPrefix(reference, "sha256:") {
		return host + "/" + remainder + "@" + reference
	}
	return host + "/" + remainder + ":" + reference
}

// Reports whether the cache was hydrated.
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

	// The engine's config blob is OCI-compatible, so carry the OCI media type; a
	// `docker build` FROM rejects a manifest mixing OCI and Docker v2s2 types.
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

// Returns the export's manifest and every file in it by name.
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

// A PULL_THROUGH_CACHE template governs the settings when one matches;
// otherwise CreateRepository's defaults apply.
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
