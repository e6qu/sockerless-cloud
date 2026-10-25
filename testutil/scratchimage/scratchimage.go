// Package scratchimage builds a Go program into a scratch container image on
// the local Docker engine, for the workloads the simulator suites run.
package scratchimage

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// NativePlatform names the engine platform of the host's architecture.
func NativePlatform() string {
	return "linux/" + runtime.GOARCH
}

// Build compiles the main package in sourceDir as binaryName and loads it into
// the engine's image store as imageName, with binaryName as the entrypoint and
// cmd, when not empty, as the image's CMD in Dockerfile syntax.
func Build(imageName, sourceDir, binaryName, platform, cmd string) error {
	buildDir, err := os.MkdirTemp("", "sockerless-scratch-image-*")
	if err != nil {
		return fmt.Errorf("create image build dir: %w", err)
	}
	defer os.RemoveAll(buildDir)

	build := exec.Command("go", "build", "-o", filepath.Join(buildDir, binaryName), ".")
	build.Dir = sourceDir
	build.Env = append(os.Environ(),
		"CGO_ENABLED=0",
		"GOWORK=off",
		"GOOS=linux",
		"GOARCH="+strings.TrimPrefix(platform, "linux/"),
	)
	if out, err := build.CombinedOutput(); err != nil {
		return fmt.Errorf("build %s binary: %w\n%s", binaryName, err, out)
	}

	dockerfile := fmt.Sprintf(`FROM scratch
COPY %s /usr/local/bin/%s
ENTRYPOINT ["/usr/local/bin/%s"]
`, binaryName, binaryName, binaryName)
	if cmd != "" {
		dockerfile += "CMD " + cmd + "\n"
	}
	// A docker-container buildx driver leaves a plain `docker build -t` image
	// in its build cache, where the simulator's container start cannot find
	// it; `--load` puts it in the engine's store. The legacy builder writes to
	// the store already and rejects `--load`.
	var args []string
	if exec.Command("docker", "buildx", "version").Run() == nil {
		args = []string{"buildx", "build", "--load"}
	} else {
		args = []string{"build"}
	}
	args = append(args, "--platform", platform, "-t", imageName, "-f", "-", buildDir)
	dockerBuild := exec.Command("docker", args...)
	dockerBuild.Stdin = strings.NewReader(dockerfile)
	if out, err := dockerBuild.CombinedOutput(); err != nil {
		return fmt.Errorf("build %s image: %w\n%s", imageName, err, out)
	}
	return nil
}
