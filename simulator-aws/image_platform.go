package main

import (
	"context"
	"fmt"

	"github.com/e6qu/sockerless-cloud/sim"
)

func localImagePlatform(ctx context.Context, imageRef string) (string, error) {
	cli := sim.DockerClient()
	if cli == nil {
		return "", fmt.Errorf("docker client not initialized")
	}
	inspect, err := cli.ImageInspect(ctx, imageRef)
	if err != nil {
		if pullErr := sim.PullImage(ctx, imageRef, ""); pullErr != nil {
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
