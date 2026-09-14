package main

import (
	"context"
	"fmt"
	"strings"

	"github.com/e6qu/sockerless-cloud/sim"
)

func localImagePlatform(ctx context.Context, imageRef string) (string, error) {
	cli := sim.DockerClient()
	if cli == nil {
		return "", fmt.Errorf("docker client not initialized")
	}
	inspect, err := cli.ImageInspect(ctx, imageRef)
	if err != nil {
		if pullErr := sim.PullImageWithCredential(ctx, imageRef, "", ecrWorkloadRegistryAuth(imageRef)); pullErr != nil {
			return "", fmt.Errorf("inspect image %q platform: %w; pull image: %w", imageRef, err, pullErr)
		}
		inspect, err = cli.ImageInspect(ctx, imageRef)
		if err != nil {
			return "", fmt.Errorf("inspect pulled image %q platform: %w", imageRef, err)
		}
	}
	if inspect.Os == "" || inspect.Architecture == "" {
		return "", fmt.Errorf("inspect image %q platform: missing os/architecture", imageRef)
	}
	return inspect.Os + "/" + inspect.Architecture, nil
}

func lambdaDockerPlatform(architectures []string) (string, error) {
	if len(architectures) == 0 {
		return "linux/amd64", nil
	}
	switch architectures[0] {
	case "x86_64":
		return "linux/amd64", nil
	case "arm64":
		return "linux/arm64", nil
	default:
		return "", fmt.Errorf("unsupported Lambda architecture %q", architectures[0])
	}
}

// localImageDigest is the manifest digest the engine recorded when it pulled
// the image — what Amazon ECS reports as a container's imageDigest. A
// digest-pinned reference names it outright. Otherwise it is the RepoDigests
// entry for the reference's own repository, and empty when the engine holds
// the image under no repository digest (built locally, never pulled).
func localImageDigest(ctx context.Context, requested, local string) (string, error) {
	if i := strings.Index(requested, "@"); i >= 0 {
		return requested[i+1:], nil
	}
	cli := sim.DockerClient()
	if cli == nil {
		return "", fmt.Errorf("docker client not initialized")
	}
	inspect, err := cli.ImageInspect(ctx, local)
	if err != nil {
		return "", fmt.Errorf("inspect image %q digest: %w", local, err)
	}
	return repoDigestFor(local, inspect.RepoDigests), nil
}

// repoDigestFor picks the digest RepoDigests records for imageRef's repository.
func repoDigestFor(imageRef string, repoDigests []string) string {
	if i := strings.Index(imageRef, "@"); i >= 0 {
		return imageRef[i+1:]
	}
	want := imageRepository(imageRef)
	for _, rd := range repoDigests {
		repo, digest, ok := strings.Cut(rd, "@")
		if ok && imageRepository(repo) == want {
			return digest
		}
	}
	return ""
}

// imageRepository is a reference without its tag or digest, with Docker Hub's
// implicit prefixes removed, so "alpine:3.22" and "docker.io/library/alpine"
// compare equal and a registry port is not mistaken for a tag.
func imageRepository(ref string) string {
	if i := strings.Index(ref, "@"); i >= 0 {
		ref = ref[:i]
	}
	if i := strings.LastIndex(ref, ":"); i > strings.LastIndex(ref, "/") {
		ref = ref[:i]
	}
	ref = strings.TrimPrefix(ref, "docker.io/")
	ref = strings.TrimPrefix(ref, "index.docker.io/")
	return strings.TrimPrefix(ref, "library/")
}
