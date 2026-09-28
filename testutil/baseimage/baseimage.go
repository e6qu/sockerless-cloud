// Package baseimage puts the public images a test runs containers from on the
// host's container engine.
package baseimage

import (
	"context"
	"fmt"
	"os/exec"
	"time"
)

// Ensure makes image present on the host, asking a registry only when the
// host lacks it. `docker pull` asks the registry even for an image the host
// holds, and the anonymous data cap refuses it though CI's cache loaded it.
func Ensure(image string) error {
	inspect, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	present := exec.CommandContext(inspect, "docker", "image", "inspect", image).Run() == nil
	cancel()
	if present {
		return nil
	}
	const attempts = 5
	var last error
	for attempt := 1; attempt <= attempts; attempt++ {
		pull, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		out, err := exec.CommandContext(pull, "docker", "pull", image).CombinedOutput()
		cancel()
		if err == nil {
			return nil
		}
		last = fmt.Errorf("docker pull %s (attempt %d of %d): %w\n%s", image, attempt, attempts, err, out)
		if attempt < attempts {
			time.Sleep(time.Duration(attempt*attempt) * time.Second)
		}
	}
	return last
}
