package tfsim

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// BuildGoScratchImage builds the Go program in sourceDir into a scratch image
// for the host's architecture.
func BuildGoScratchImage(t *testing.T, imageName, sourceDir, binaryName string) {
	t.Helper()
	buildDir := t.TempDir()
	build := exec.Command("go", "build", "-o", filepath.Join(buildDir, binaryName), ".")
	build.Dir = sourceDir
	build.Env = append(os.Environ(), "CGO_ENABLED=0", "GOOS=linux", "GOARCH="+runtime.GOARCH)
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build %s: %v\n%s", binaryName, err, out)
	}
	dockerfile := fmt.Sprintf("FROM scratch\nCOPY %s /usr/local/bin/%s\nENTRYPOINT [\"/usr/local/bin/%s\"]\n", binaryName, binaryName, binaryName)
	args := []string{"build"}
	if exec.Command("docker", "buildx", "version").Run() == nil {
		args = []string{"buildx", "build", "--load"}
	}
	args = append(args, "--platform", "linux/"+runtime.GOARCH, "-t", imageName, "-f", "-", buildDir)
	docker := exec.Command("docker", args...)
	docker.Stdin = strings.NewReader(dockerfile)
	if out, err := docker.CombinedOutput(); err != nil {
		t.Fatalf("build the %s image: %v\n%s", imageName, err, out)
	}
}
