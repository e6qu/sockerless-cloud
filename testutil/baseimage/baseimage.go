// Package baseimage puts the public images a test runs containers from on the
// host's container engine.
package baseimage

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// Ensure makes image present on the host, asking a registry only when the
// host lacks it. `docker pull` asks the registry even for an image the host
// holds, and the anonymous data cap refuses it though CI's cache loaded it.
func Ensure(image string) error {
	_, err := EnsureRef(image)
	return err
}

// EnsureRef makes image present as Ensure does and returns the reference to
// run it by. `docker load` restores a digest-pinned image only under the local
// tag scripts/warm-base-images.sh saves it as, `repo:sha256-<hex>`, so a host
// that holds that tag runs the image by it instead of asking the registry.
func EnsureRef(image string) (string, error) {
	for _, ref := range []string{image, LocalRef(image)} {
		inspect, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		present := exec.CommandContext(inspect, "docker", "image", "inspect", ref).Run() == nil
		cancel()
		if present {
			return ref, nil
		}
	}
	const attempts = 5
	var last error
	for attempt := 1; attempt <= attempts; attempt++ {
		pull, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		out, err := exec.CommandContext(pull, "docker", "pull", image).CombinedOutput()
		cancel()
		if err == nil {
			return image, nil
		}
		last = fmt.Errorf("docker pull %s (attempt %d of %d): %w\n%s", image, attempt, attempts, err, out)
		if attempt < attempts {
			time.Sleep(time.Duration(attempt*attempt) * time.Second)
		}
	}
	return "", last
}

// LocalRef is the tag scripts/warm-base-images.sh saves image under: the image
// itself, or `repo:sha256-<hex>` for a digest-pinned `repo@sha256:<hex>`.
func LocalRef(image string) string {
	repo, digest, pinned := strings.Cut(image, "@sha256:")
	if !pinned {
		return image
	}
	return repo + ":sha256-" + digest
}
